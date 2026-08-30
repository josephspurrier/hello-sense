// Package api serves the endpoints the iOS app calls.
//
// The endpoint list is not guesswork: it was taken from the running stack's own
// access log, which is a record of what the app actually asks for rather than
// what suripu happens to expose. suripu-app publishes far more than this, and
// building to the published surface would mean writing code nothing calls while
// missing something that is called constantly.
//
// Ordered by how often the app called them over two weeks:
//
//	852  GET   /v2/sensors            832  POST  /v2/sensors
//	691  GET   /v2/timeline/{date}    326  GET   /v1/account
//	323  GET   /v2/account/preferences 316 GET   /v2/devices
//	300  GET   /v1/app/stats/unread   218  GET   /v2/trends/{period}
//	169  GET   /v2/insights           168  GET   /v2/alerts
//	168  GET   /v1/questions/         140  GET   /v2/alarms
//	118  PATCH /v1/app/stats          116  GET   /v1/timezone
//	 72  GET   /v2/sleep_sounds/status 26  POST  /v1/oauth2/token
//
// plus the writes: PATCH /v2/timeline/{date}/events/{event}/{ts} (the
// correction that feeds learning), POST /v2/alarms/{ts}, POST /v1/timezone,
// PUT/POST /v1/account, POST /v1/questions/save/, and the credential set that
// never showed in that log because one phone stayed signed in for its whole
// span: POST /v1/account/{email,password} and DELETE /v1/oauth2/token
// (registration.go, oauth.go).
//
// Every response shape here is verified against the Java stack by cmd/apidiff
// rather than by reading suripu's resource classes. Reading them is how you
// learn what fields exist; only a diff tells you what the app receives.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/api/insightart"
	"github.com/josephspurrier/hello-orb/orb/internal/scoring"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// Handler serves the app API.
//
// It is a separate listener from the edge on purpose. The two have nothing in
// common: the edge speaks protobuf to one device over a signed envelope, this
// speaks JSON to a phone over a bearer token, and the edge's Host-based routing
// would only obscure that. They share a process and a database, nothing else.
type Handler struct {
	store *store.Store
	// scorer is shared with the worker. The timeline writes have to re-score a
	// night inside the request that corrected it, because the app expects the
	// corrected timeline back in the same response.
	scorer *scoring.Scorer
	log    *slog.Logger
	mux    *http.ServeMux
	// fallback forwards requests orb does not implement to the old stack. Nil
	// means orb answers alone and an unknown path is a 404.
	fallback http.Handler
}

func New(s *store.Store, scorer *scoring.Scorer, log *slog.Logger) *Handler {
	h := &Handler{store: s, scorer: scorer, log: log, mux: http.NewServeMux()}
	h.routes()
	return h
}

func (h *Handler) routes() {
	// Unauthenticated.
	h.mux.HandleFunc("GET /v2/ping", h.ping)
	h.mux.HandleFunc("GET /ping", h.ping)
	h.mux.HandleFunc("POST /v1/oauth2/token", h.postToken)
	// Registration is unauthenticated by nature: it creates the credentials
	// everything else authenticates with.
	h.mux.HandleFunc("POST /v1/account", h.postAccount)

	// Authenticated.
	h.mux.Handle("DELETE /v1/oauth2/token", h.auth(h.deleteToken))
	h.mux.Handle("GET /v1/account", h.auth(h.getAccount))
	h.mux.Handle("PUT /v1/account", h.auth(h.putAccount))
	h.mux.Handle("POST /v1/account/email", h.auth(h.postAccountEmail))
	h.mux.Handle("POST /v1/account/password", h.auth(h.postAccountPassword))
	h.mux.Handle("GET /v1/timezone", h.auth(h.getTimezone))
	h.mux.Handle("POST /v1/timezone", h.auth(h.postTimezone))
	h.mux.Handle("GET /v2/devices", h.auth(h.getDevices))
	h.mux.Handle("GET /v1/app/stats/unread", h.auth(h.getAppStatsUnread))
	h.mux.Handle("PATCH /v1/app/stats", h.auth(h.patchAppStats))
	h.mux.Handle("GET /v2/alarms", h.auth(h.getAlarms))
	h.mux.Handle("POST /v2/alarms/{ts}", h.auth(h.postAlarms))
	h.mux.Handle("GET /v2/alerts", h.auth(h.getAlerts))
	h.mux.Handle("GET /v2/account/preferences", h.auth(h.getPreferences))
	h.mux.Handle("PUT /v2/account/preferences", h.auth(h.putPreferences))
	h.mux.Handle("POST /v1/photo/profile", h.auth(h.postProfilePhoto))
	h.mux.Handle("DELETE /v1/photo/profile", h.auth(h.deleteProfilePhoto))
	// The image bytes. Unauthenticated: a plain image fetch with a random
	// token as the path, the same scheme as the share pages (see photo.go).
	h.mux.HandleFunc("GET "+photoPath+"{token}", h.getProfilePhotoImage)
	h.mux.Handle("GET /v2/sleep_sounds/status", h.auth(h.getSleepSoundsStatus))
	h.mux.Handle("GET /v2/sleep_sounds/combined_state", h.auth(h.getCombinedSleepSoundState))
	h.mux.Handle("GET /v2/alarms/sounds", h.auth(h.getAlarmSounds))
	h.mux.Handle("GET /v2/timeline/{date}", h.auth(h.getTimeline))
	h.mux.Handle("GET /v2/insights", h.auth(h.getInsights))
	// A wildcard segment, NOT a subtree: "/v2/insights/info/{category}" matches
	// exactly one more segment, so /v2/insights/summary and any other sibling
	// still reach the fallback. Registering "/v2/insights/" instead would
	// swallow all of them. routing_test.go pins the siblings.
	h.mux.Handle("GET /v2/insights/info/{category}", h.auth(h.getInsightInfo))
	h.mux.Handle("POST /v2/sharing/insight", h.auth(h.shareInsight))
	// The public share page. Unauthenticated by design: a share only anybody
	// with the account's token could open would not be a share. The id is 128
	// bits of crypto/rand and is the only thing protecting it. Subtree, because
	// the id is the rest of the path.
	h.mux.Handle("GET "+sharePath, http.HandlerFunc(h.getSharePage))
	// The insight card banners, embedded in the binary. Unauthenticated on
	// purpose: it is a plain image fetch by UIImageView, which carries no
	// token, and this is exactly how the app fetched them from S3 before.
	// Subtree, so the {$} rule above does not apply.
	h.mux.Handle("GET "+insightImagePath, insightart.Handler(insightImagePath))
	h.mux.Handle("GET /v2/sensors", h.auth(h.getSensors))
	h.mux.Handle("POST /v2/sensors", h.auth(h.postSensors))
	h.mux.Handle("GET /v2/trends/{scale}", h.auth(h.getTrends))
	// Anchored with {$}, and that is not decoration. A pattern ending in a bare
	// slash is a SUBTREE in net/http: "GET /v1/questions/" matches
	// /v1/questions/more as well, so orb answered it with today's question list
	// where suripu answers with the next ones. The app's "show me another" then
	// returns the same questions. Caught by diffing orb against suripu on a path
	// nothing was thought to route.
	//
	// The trailing-slash and bare forms are registered separately because the
	// app calls "/v1/questions/" and net/http's implicit redirect would
	// otherwise send the bare form somewhere neither of us chose.
	//
	// Anything else under /v1/questions/ (more, skip, {id}/skip) is left
	// unregistered on purpose so it reaches the fallback.
	h.mux.Handle("GET /v1/questions/{$}", h.auth(h.getQuestions))
	h.mux.Handle("GET /v1/questions", h.auth(h.getQuestions))
	h.mux.Handle("POST /v1/questions/save/{$}", h.auth(h.postQuestionResponse))
	h.mux.Handle("POST /v1/questions/save", h.auth(h.postQuestionResponse))

	// Apple push registration. The app posts here on every launch that yields a
	// device token from iOS, which is more often than it sounds: a reinstall or
	// a restore issues a new one.
	h.mux.Handle("POST /v1/notifications/registration", h.auth(h.postNotificationRegistration))
	h.mux.Handle("DELETE /v1/notifications/registration", h.auth(h.deleteNotificationRegistration))
	// The notification settings screen's list and its Save button.
	h.mux.Handle("GET /v1/notifications", h.auth(h.getNotificationSettings))
	h.mux.Handle("PUT /v1/notifications", h.auth(h.putNotificationSettings))

	// The corrections. One path, three verbs, three meanings: move the event,
	// confirm it, reject it.
	h.mux.Handle("PATCH /v2/timeline/{date}/events/{type}/{ts}", h.auth(h.amendEvent))
	h.mux.Handle("PUT /v2/timeline/{date}/events/{type}/{ts}", h.auth(h.confirmEvent))
	h.mux.Handle("DELETE /v2/timeline/{date}/events/{type}/{ts}", h.auth(h.rejectEvent))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// With a fallback configured, "no route here" means "the old stack's
	// problem" rather than 404. mux.Handler reports the matched pattern, and an
	// empty pattern is the only reliable way to tell a real route from the
	// built-in NotFoundHandler: ServeMux answers every request, so checking the
	// handler itself would not distinguish them.
	if h.fallback != nil {
		if _, pattern := h.mux.Handler(r); pattern == "" {
			h.serveFallback(w, r)
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, "ok")
}

// ctxKey is unexported so nothing outside this package can fabricate an
// identity on the context. The account on a request comes from the token and
// from nowhere else; taking it from a query parameter or a body field is how
// one phone reads another account's nights.
type ctxKey struct{}

// AccountFrom returns the authenticated account.
//
// It returns ok=false rather than a zero id, because a zero id is a valid-
// looking value that would silently query account 0.
func AccountFrom(r *http.Request) (int64, bool) {
	v, ok := r.Context().Value(ctxKey{}).(int64)
	return v, ok
}

func withAccount(ctx context.Context, accountID int64) context.Context {
	return context.WithValue(ctx, ctxKey{}, accountID)
}

// auth resolves the bearer token to an account.
//
// An unknown or expired token is 401 with no body: saying which of the two it
// was tells an attacker whether a token ever existed.
func (h *Handler) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appID, uuid, ok := parseToken(bearerToken(r))
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		accountID, err := h.store.AccountByToken(r.Context(), appID, uuid)
		if err != nil {
			if errors.Is(err, store.ErrNoToken) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h.log.Error("token lookup", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		next(w, r.WithContext(withAccount(r.Context(), accountID)))
	})
}

// bearerToken pulls the credential out of the Authorization header. suripu
// accepts it either as "Bearer <token>" or bare, and the iOS app has sent both
// over the years, so both are accepted here.
func bearerToken(r *http.Request) string {
	tok := strings.TrimSpace(r.Header.Get("Authorization"))
	if i := strings.IndexByte(tok, ' '); i >= 0 && strings.EqualFold(tok[:i], "bearer") {
		tok = strings.TrimSpace(tok[i+1:])
	}
	return tok
}

// parseToken splits the credential the app sends into its two halves.
//
// The wire format is NOT the stored value, which is the trap here.
// AccessToken.serializeAccessToken renders "{appId}.{uuid without dashes}"
// while the column holds the plain UUID. Comparing the header against the
// column is self-consistent if you also mint tokens that way, and produces a
// service that authenticates its own tokens happily and rejects every real one
// from the app. The first apidiff run caught exactly that: orb 200, suripu 401.
func parseToken(tok string) (appID int64, uuid string, ok bool) {
	dot := strings.IndexByte(tok, '.')
	if dot <= 0 || dot == len(tok)-1 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(tok[:dot], 10, 64)
	if err != nil {
		return 0, "", false
	}
	hex := tok[dot+1:]
	if len(hex) != 32 {
		return 0, "", false
	}
	for i := 0; i < len(hex); i++ {
		c := hex[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return 0, "", false
		}
	}
	// Back to the canonical 8-4-4-4-12 form the column stores.
	return id, strings.ToLower(
		hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]), true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// millis renders a time the way suripu does on the wire: epoch milliseconds.
// A nil time is null rather than 0, because 1970 is a plausible-looking date
// and null is not.
func millis(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}
