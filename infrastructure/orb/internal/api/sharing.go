package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"strings"
)

// The insight detail copy, and sharing a card.
//
// Both were suripu-app's until 2026-08-27 and were the last two app endpoints
// it served that mattered. See knowledgebase/GOING-PUBLIC.md.

// InsightInfo is one entry of the array returned by /v2/insights/info/{category}.
//
// An ARRAY of one, not an object. The reference has returned an array since
// 2015 and the app still reads it that way: SENAPIInsight takes `firstObject`
// and its own comment says "insight info will return an array of possibly
// greater than 1 info object, but we don't forsee this to be needed". Returning
// the bare object instead would parse as nil and the detail screen would be
// blank.
type InsightInfo struct {
	ID       int32   `json:"id"`
	Category string  `json:"category"`
	Title    string  `json:"title"`
	Text     string  `json:"text"`
	ImageURL *string `json:"image_url"`
}

func (h *Handler) getInsightInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	category := r.PathValue("category")
	row, found, err := h.store.InsightInfo(r.Context(), category)
	if err != nil {
		h.log.Error("insight info", "category", category, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		// An empty array, not a 404. The app renders "no detail" from an empty
		// result and shows an error from a 404, and a category nobody wrote
		// copy for is not an error.
		writeJSON(w, http.StatusOK, []InsightInfo{})
		return
	}

	writeJSON(w, http.StatusOK, []InsightInfo{{
		ID:       row.ID,
		Category: strings.ToUpper(row.Category),
		Title:    row.Title,
		Text:     row.Text,
		ImageURL: row.ImageURL,
	}})
}

// ShareRequest is the body of POST /v2/sharing/insight: the card's uuid.
type ShareRequest struct {
	ID string `json:"id"`
}

// ShareResponse is what the app turns into a share sheet.
type ShareResponse struct {
	URL string `json:"url"`
}

const sharePath = "/share/insight/"

// maxShareBody bounds the request. The body is one uuid.
const maxShareBody = 4 << 10

func (h *Handler) shareInsight(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var req ShareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxShareBody)).Decode(&req); err != nil || req.ID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Scoped to the account, so naming somebody else's card uuid returns 404
	// rather than sharing it.
	card, found, err := h.store.InsightByUUID(r.Context(), accountID, req.ID)
	if err != nil {
		h.log.Error("share insight lookup", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		h.log.Warn("share insight: card not found", "account", accountID, "uuid", req.ID)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	acct, err := h.store.Account(r.Context(), accountID)
	if err != nil {
		h.log.Error("share insight account", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	id, err := shareID()
	if err != nil {
		h.log.Error("share insight id", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// An account with no first name shares anonymously rather than as "".
	// The page has wording for both.
	var sharedBy string
	if acct.FirstName != nil {
		sharedBy = *acct.FirstName
	}

	at := card.Timestamp
	if err := h.store.CreateInsightShare(r.Context(), id, accountID,
		card.Category, card.Title, card.Message, sharedBy, &at); err != nil {
		h.log.Error("share insight store", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	h.log.Info("shared insight", "account", accountID, "category", card.Category, "share", id)
	writeJSON(w, http.StatusOK, ShareResponse{
		URL: insightImageOrigin(r) + sharePath + id,
	})
}

// shareID is an unguessable identifier.
//
// A share page is served to anyone holding the link and nothing else, so the id
// IS the access control. The reference used a sequential-ish id behind
// share.hello.is; here it is 128 bits of crypto/rand, base64url, which cannot
// be walked by incrementing.
func shareID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// shareTemplate renders the public page.
//
// Deliberately one self-contained page with no assets: no stylesheet, no
// script, no image. This is the only thing orb serves to people who are not the
// account holder, so it has the smallest surface that can still look like
// something. Every value is escaped by html/template.
var shareTemplate = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<meta name="robots" content="noindex, nofollow">
<style>
  :root { color-scheme: light dark; }
  body { margin:0; padding:2.5rem 1.25rem; font:16px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
         background:#f6f7f9; color:#1b2437; }
  @media (prefers-color-scheme: dark) { body { background:#12151c; color:#e7eaf0; } }
  main { max-width:34rem; margin:0 auto; }
  .card { background:#fff; border-radius:14px; padding:1.75rem; box-shadow:0 1px 3px rgba(0,0,0,.08); }
  @media (prefers-color-scheme: dark) { .card { background:#1c212b; box-shadow:none; } }
  .cat { font-size:.75rem; letter-spacing:.09em; text-transform:uppercase; opacity:.6; margin:0 0 .5rem; }
  h1 { font-size:1.5rem; line-height:1.3; margin:0 0 1rem; }
  .msg { white-space:pre-wrap; margin:0; }
  footer { max-width:34rem; margin:1.25rem auto 0; font-size:.8rem; opacity:.55; }
</style>
</head><body><main>
  <div class="card">
    <p class="cat">{{.Category}}</p>
    <h1>{{.Title}}</h1>
    <p class="msg">{{.Message}}</p>
  </div>
  <footer>{{if .SharedBy}}Shared by {{.SharedBy}}{{else}}Shared{{end}}{{if .When}} from a Sense sleep report, {{.When}}{{end}}.</footer>
</main></body></html>
`))

type sharePage struct {
	Category string
	Title    string
	Message  string
	SharedBy string
	When     string
}

// getSharePage serves a shared insight to anyone with the link.
//
// UNAUTHENTICATED, which is the whole point: a share nobody else can open is
// not a share. It is registered outside the authenticated mux for that reason,
// alongside the card art, and the id is the only secret.
func (h *Handler) getSharePage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, sharePath)
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	row, found, err := h.store.InsightShare(r.Context(), id)
	if err != nil {
		h.log.Error("share page", "share", id, "err", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	page := sharePage{
		Category: categoryLabel(row.Category),
		Title:    row.Title,
		Message:  row.Message,
		SharedBy: row.SharedBy,
	}
	if row.InsightAt != nil {
		page.When = row.InsightAt.UTC().Format("2 January 2006")
	}

	// No caching. A share can be revoked by deleting the row, and a cached copy
	// would outlive that.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The page loads nothing and runs nothing; say so, so that stays true.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")

	if err := shareTemplate.Execute(w, page); err != nil {
		h.log.Error("share render", "share", id, "err", err)
	}
}

// categoryLabel turns SLEEP_DURATION into "Sleep duration" for the page.
func categoryLabel(category string) string {
	s := strings.ToLower(strings.ReplaceAll(category, "_", " "))
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
