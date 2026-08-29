package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/josephspurrier/hello-orb/orb/internal/api/insightart"
)

// The categories the app has ever been sent, from the Android build's
// images.manifest.json, which is the only surviving record of the original set.
// If a category ever gains artwork it belongs here; if one is missing, the card
// silently falls back to generic and nobody notices until the feed looks wrong.
var insightCategories = []string{
	"AIR_QUALITY", "GENERIC", "HUMIDITY", "LIGHT", "PARTNER_MOTION",
	"SLEEP_DURATION", "SLEEP_HYGIENE", "SLEEP_QUALITY", "SOUND",
	"TEMPERATURE", "WAKE_VARIANCE",
}

func TestEveryCategoryHasArtAtEveryScale(t *testing.T) {
	for _, c := range insightCategories {
		name := strings.ToLower(c)
		for _, suffix := range []string{"", "@2x", "@3x"} {
			if !insightart.Has(name + suffix + ".png") {
				t.Errorf("missing art for %s%s", name, suffix)
			}
		}
	}
	// 11 categories at three densities. A stray file is as much a problem as a
	// missing one: it means the generator and this list have diverged.
	if got, want := len(insightart.Names()), len(insightCategories)*3; got != want {
		t.Errorf("embedded file count = %d, want %d", got, want)
	}
}

func TestCardImageIsAbsoluteAndPointsAtUs(t *testing.T) {
	r := httptest.NewRequest("GET", "http://192.168.0.17:9999/v2/insights", nil)
	img := cardImage(r, "WAKE_VARIANCE")

	want := "http://192.168.0.17:9999/v1/insights/images/wake_variance"
	if img.Phone1x != want+".png" {
		t.Errorf("1x = %q", img.Phone1x)
	}
	if img.Phone2x != want+"@2x.png" {
		t.Errorf("2x = %q", img.Phone2x)
	}
	if img.Phone3x != want+"@3x.png" {
		t.Errorf("3x = %q", img.Phone3x)
	}
}

// The app resolves these with no base URL, so anything relative loads nothing.
func TestCardImageNeverReturnsARelativeURL(t *testing.T) {
	r := httptest.NewRequest("GET", "http://host:9999/v2/insights", nil)
	for _, c := range insightCategories {
		img := cardImage(r, c)
		for _, u := range []string{img.Phone1x, img.Phone2x, img.Phone3x} {
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				t.Errorf("%s: %q is not absolute", c, u)
			}
		}
	}
}

func TestUnknownCategoryFallsBackToGeneric(t *testing.T) {
	r := httptest.NewRequest("GET", "http://host:9999/v2/insights", nil)
	img := cardImage(r, "SOMETHING_NOBODY_DREW")
	if !strings.HasSuffix(img.Phone3x, "/generic@3x.png") {
		t.Errorf("3x = %q, want the generic banner", img.Phone3x)
	}
}

// Behind a TLS terminator the scheme has to come from the header, or the app
// gets an http URL that App Transport Security will refuse.
func TestForwardedHeadersWin(t *testing.T) {
	r := httptest.NewRequest("GET", "http://internal:9999/v2/insights", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "sense.example.com")

	img := cardImage(r, "SOUND")
	if !strings.HasPrefix(img.Phone1x, "https://sense.example.com/") {
		t.Errorf("1x = %q", img.Phone1x)
	}
}

func TestHandlerServesArtAndRefusesAnythingElse(t *testing.T) {
	h := insightart.Handler("/v1/insights/images/")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/insights/images/sound@3x.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Body.Len() < 1024 {
		t.Errorf("body = %d bytes, too small to be a banner", rec.Body.Len())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control = %q; the app refetches every banner without it", cc)
	}

	for _, bad := range []string{
		"/v1/insights/images/",
		"/v1/insights/images/nope.png",
		"/v1/insights/images/../insights.go",
		"/v1/insights/images/sound@4x.png",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", bad, rec.Code)
		}
	}
}
