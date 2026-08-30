package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// POST /v1/oauth2/token: the only endpoint that turns a password into a token.
//
// Form-encoded, not JSON, because that is what OAuth2 specifies and what the
// app sends.
//
// Only the password grant is implemented. The reference also has
// authorization_code and refresh_token branches, and neither is reachable from
// this app: there is no web consent screen here, and the tokens last a year and
// are replaced by signing in again. Building them would mean writing code
// nothing calls, in the one place where a mistake hands out someone's account.

// tokenExpiry is 365 days, taken from the existing tokens rather than from the
// source: the configured value is threaded through constructor overloads, and
// every migrated token has exactly 31536000 seconds between created_at and
// expires_at.
const tokenExpiry = 365 * 24 * time.Hour

// authScope is the scope an application must hold to exchange a password.
// Scope 20 in the reference's enum.
const authScope int32 = 20

// bcryptPlaceholder is a valid bcrypt hash of a value nobody knows.
//
// Used to keep the work constant when the account does not exist. Without it,
// an unknown email returns immediately and a known one costs a bcrypt at cost
// 12, which is tens of milliseconds and trivially measurable over the network.
// That difference lets anyone enumerate which addresses have accounts.
var bcryptPlaceholder = []byte("$2a$12$C6UzMDM.H6dfI/f/IKcEe.7Ay6mVvJZ0m0zMLxSxNqTAPvHRrFNaC")

type tokenResponse struct {
	TokenType        string `json:"token_type"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	AccountID        string `json:"account_id"`
}

// newTokenUUID makes the random half of a credential.
//
// Wire format is "{appID}.{32 hex chars}", which is the same shape parseToken
// expects on the way back in. crypto/rand, and an error reading it is fatal to
// the request rather than something to fall back from: a predictable token is
// worse than a failed sign-in.
func newTokenUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// Version 4 and the RFC 4122 variant, so the value stored in the uuid
	// column is a well-formed UUID rather than 16 arbitrary bytes.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

func (h *Handler) postToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// Every failure here is 401 with no body, matching the reference. That is
	// deliberate and worth keeping: distinguishing "no such application" from
	// "wrong password" tells an attacker which half to keep trying.
	deny := func() { w.WriteHeader(http.StatusUnauthorized) }

	// Three states, three answers, and the reference distinguishes them:
	//
	//	absent            401, the explicit null check
	//	unrecognised      400, because the parameter fails to parse before any
	//	                  handler runs
	//	known but unbuilt 401 here, see the note below
	//
	// Found by diffing: client_credentials returned 400 from the reference and
	// 401 from orb, which is the parameter type rejecting a string that is not
	// in its enum rather than any deliberate decision.
	grant := r.PostForm.Get("grant_type")
	if grant == "" {
		deny()
		return
	}
	switch grant {
	case "password":
	case "authorization_code", "implicit", "refresh_token":
		// Recognised by the reference and not built here. 401 rather than 501
		// because this app never sends them, and an endpoint that hands out
		// credentials should refuse what it does not fully understand.
		deny()
		return
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	app, found, err := h.store.AppByClientID(r.Context(), r.PostForm.Get("client_id"))
	if err != nil {
		h.log.Error("token app lookup", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		deny()
		return
	}
	hasAuth := false
	for _, s := range app.Scopes {
		if s == authScope {
			hasAuth = true
			break
		}
	}
	if !hasAuth {
		deny()
		return
	}

	username := r.PostForm.Get("username")
	password := r.PostForm.Get("password")
	if username == "" || password == "" {
		deny()
		return
	}

	// Lowercased, matching the reference: addresses were stored lowercase and a
	// phone that capitalises the first letter must still sign in.
	accountID, externalID, hash, found, err := h.store.CredentialsByEmail(
		r.Context(), strings.ToLower(username))
	if err != nil {
		h.log.Error("token account lookup", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// The comparison runs even when the account does not exist, against a
	// placeholder hash, so the response time does not reveal which addresses are
	// registered. See bcryptPlaceholder.
	stored := []byte(hash)
	if !found {
		stored = bcryptPlaceholder
	}
	bcryptErr := bcrypt.CompareHashAndPassword(stored, []byte(password))
	if !found || bcryptErr != nil {
		deny()
		return
	}

	accessUUID, err := newTokenUUID()
	if err != nil {
		h.log.Error("token generate", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	refreshUUID, err := newTokenUUID()
	if err != nil {
		h.log.Error("token generate", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := h.store.InsertToken(r.Context(), accessUUID, refreshUUID,
		accountID, app.ID, app.Scopes, tokenExpiry); err != nil {
		h.log.Error("token insert", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	h.log.Info("issued token", "account", accountID, "app", app.ID)

	serialize := func(u string) string {
		return strconv.FormatInt(app.ID, 10) + "." + strings.ReplaceAll(u, "-", "")
	}
	writeJSON(w, http.StatusOK, tokenResponse{
		TokenType:    "Bearer",
		AccessToken:  serialize(accessUUID),
		RefreshToken: serialize(refreshUUID),
		ExpiresIn:    int64(tokenExpiry.Seconds()),
		// UNVERIFIED. The reference threads this through builder overloads that
		// never set it on this path, and the success path cannot be diffed
		// without a real password, so it is set equal to expires_in. If the app
		// misbehaves after a year, look here first.
		RefreshExpiresIn: int64(tokenExpiry.Seconds()),
		// The account's external UUID, not its row id. "0" when it has none,
		// which is the reference's DEFAULT_INTERNAL_ID rendered as a string.
		AccountID: defaultString(externalID, "0"),
	})
}

// DELETE /v1/oauth2/token: sign out.
//
// The app's Sign Out button clears its keychain only after this succeeds
// (SENAuthorizationService.deauthorize), so a missing route here does not
// merely lose telemetry, it pins the person to the account they are on.
//
// Runs behind auth like any other authenticated endpoint, then re-reads the
// header for the token value: the middleware proves the credential is live but
// deliberately hands handlers only the account, and this is the one endpoint
// whose subject is the credential itself. Only the presented token dies; the
// reference leaves the account's other sessions signed in, and so does this.
func (h *Handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	appID, uuid, ok := parseToken(bearerToken(r))
	if !ok {
		// Unreachable behind auth, which parsed the same header. Refusing is
		// still righter than guessing which token to disable.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if err := h.store.DisableToken(r.Context(), appID, uuid); err != nil {
		h.log.Error("disable token", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	h.log.Info("signed out", "account", accountID)
	w.WriteHeader(http.StatusNoContent)
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
