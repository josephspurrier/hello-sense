package api

import (
	"github.com/josephspurrier/hello-orb/orb/internal/api/insightart"
	"net/http"
	"strings"
)

// InsightCard is one row of GET /v2/insights.
//
// The app renders these as a scrolling feed of cards, newest first.
type InsightCard struct {
	AccountID    int64     `json:"account_id"`
	ID           string    `json:"id"`
	Category     string    `json:"category"`
	CategoryName string    `json:"category_name"`
	Title        string    `json:"title"`
	Message      string    `json:"message"`
	Timestamp    int64     `json:"timestamp"`
	InsightType  string    `json:"insight_type"`
	Image        CardImage `json:"image"`
	InfoPreview  *string   `json:"info_preview"`
}

// CardImage is the same picture at three screen densities.
type CardImage struct {
	Phone1x string `json:"phone_1x"`
	Phone2x string `json:"phone_2x"`
	Phone3x string `json:"phone_3x"`
}

// insightImagePath is where orb serves the card art, and it is deliberately
// the same shape the reference used: <base>/<lowercased category>[@2x|@3x].png.
//
// It used to be `https://s3.amazonaws.com/hello-data/insights_images/`, and the
// note here used to say the bucket was still serving and so the URL was not
// ours to move. Both halves turned out to be wrong. Path-style S3 addressing
// was retired, the bucket behind the modern name is private, and every card in
// the app showed an empty grey box. The artwork does not survive in anything we
// hold; see knowledgebase/CONSOLIDATION-PLAN.md, "The insight card art is gone".
//
// So orb serves its own, embedded in the binary. The convention is kept because
// the app derives nothing: it reads the three URLs we send it.
const insightImagePath = "/v1/insights/images/"

// maxInsights matches the reference's page size. The app does not paginate, so
// this is the whole feed as far as it is concerned.
const maxInsights = 20

// cardImage builds the three URLs from the category name.
//
// Absolute, and built from the request rather than from configuration. The app
// hands the string to `[NSURL URLWithString:]` with no base, so a relative path
// silently yields no image; and orb has no idea what address the phone reached
// it on. The Host header does, which also means this keeps working when the LAN
// address changes without anyone editing a constant.
//
// A category with no artwork falls back to `generic` rather than to a URL that
// 404s. The reference let it 404, which was invisible while every card 404'd
// anyway; now that the rest load, one broken card would look like a bug.
func cardImage(r *http.Request, category string) CardImage {
	name := strings.ToLower(category)
	if !insightart.Has(name + ".png") {
		name = "generic"
	}
	base := insightImageOrigin(r) + insightImagePath
	return CardImage{
		Phone1x: base + name + ".png",
		Phone2x: base + name + "@2x.png",
		Phone3x: base + name + "@3x.png",
	}
}

// insightImageOrigin derives scheme://host for URLs the phone has to resolve.
//
// r.URL is path-only on a server request, so the origin has to be rebuilt.
// X-Forwarded-Proto is honoured because orb sits behind a TLS terminator on the
// device path and could end up behind one here too; guessing http in that case
// would hand the app a URL that App Transport Security refuses to load.
func insightImageOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func (h *Handler) getInsights(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	rows, err := h.store.InsightsFor(r.Context(), accountID, maxInsights)
	if err != nil {
		h.log.Error("insights", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Non-nil so an account with no insights marshals as [] rather than null.
	// The app treats null as a failure and an empty list as "nothing yet".
	out := []InsightCard{}
	for _, c := range rows {
		out = append(out, InsightCard{
			AccountID:    accountID,
			ID:           c.UUID,
			Category:     c.Category,
			CategoryName: c.CategoryName,
			Title:        c.Title,
			Message:      c.Message,
			Timestamp:    c.Timestamp.UnixMilli(),
			InsightType:  c.InsightType,
			Image:        cardImage(r, c.Category),
			// Always null on this endpoint, despite the reference's method
			// being called insightCardsWithInfoPreview: that function only
			// backfills images and category names, and nothing on this path
			// ever sets a preview. The field is here because the app expects
			// the key, not because it will ever carry a value.
			InfoPreview: nil,
		})
	}

	writeJSON(w, http.StatusOK, out)
}
