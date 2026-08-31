package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A pattern ending in a bare slash is a subtree in net/http, so "GET /v1/foo/"
// silently claims /v1/foo/anything as well. While orb fronts the app API and
// forwards what it does not implement, that is not a routing curiosity: a
// swallowed path is one orb answers *instead of* the old stack, with a plausible
// wrong body rather than an error.
//
// This bit for real. "GET /v1/questions/" captured /v1/questions/more, so the
// app's "show me another question" received today's list again. suripu also
// serves /skip and /{id}/skip under the same prefix.
//
// These tests assert against the mux's own matching rather than against a live
// server, so they fail on the registration mistake itself rather than on
// whatever the handler happened to return.
func TestUnimplementedSubpathsAreNotClaimed(t *testing.T) {
	h := &Handler{mux: http.NewServeMux()}
	h.routes()

	for _, c := range []struct {
		method, path string
	}{
		// Every sibling suripu serves under /v1/questions that orb does not.
		{"GET", "/v1/questions/more"},
		{"PUT", "/v1/questions/skip"},
		{"PUT", "/v1/questions/47/skip"},
		// Endpoints of the old stack that orb has no handler for at all. If any
		// of these starts matching, something has been registered as a subtree.
		{"GET", "/v1/devices"},
		{"GET", "/v1/insights"},
		{"GET", "/v1/alarms"},
		// The card art is registered as a subtree, which it has to be to serve
		// files. Its neighbours must not be swept up with it.
		{"GET", "/v1/insights/summary"},
		// /v2/insights/info/{category} takes exactly ONE more segment. These
		// are the siblings that must still reach the fallback, and the ones a
		// bare "/v2/insights/" registration would swallow.
		{"GET", "/v2/insights/summary"},
		{"GET", "/v2/insights/info"},
		{"GET", "/v2/insights/info/SOUND/extra"},
		// suripu serves other things under /v2/sharing that orb does not.
		{"POST", "/v2/sharing/timeline"},
		// The share page is a subtree; its neighbours are not ours.
		{"GET", "/share"},
		{"GET", "/share/timeline/abc"},
		// /v2/alarms/{ts} is a POST. The GET sibling we now serve is
		// /v2/alarms/sounds, and nothing else under it is ours.
		{"GET", "/v2/alarms/12345"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil)
			if _, pattern := h.mux.Handler(r); pattern != "" {
				t.Errorf("orb claims %s %s via %q; it must reach the fallback",
					c.method, c.path, pattern)
			}
		})
	}
}

// The paths orb does own must keep matching, in both the slashed and bare forms
// the app might send.
func TestImplementedPathsStillMatch(t *testing.T) {
	h := &Handler{mux: http.NewServeMux()}
	h.routes()

	for _, c := range []struct {
		method, path string
	}{
		{"GET", "/v1/questions/"},
		{"GET", "/v1/questions"},
		{"POST", "/v1/questions/save/"},
		{"GET", "/v1/insights/images/sound@3x.png"},
		// The bare form is ours too. Registering the subtree makes net/http add
		// an implicit redirect to the slashed path, which then 404s on the name
		// check like any other miss. suripu serves nothing here, so there is
		// nothing being taken from the fallback.
		{"GET", "/v1/insights/images"},
		{"POST", "/v1/questions/save"},
		{"GET", "/v2/sensors"},
		{"GET", "/v2/timeline/2026-08-15"},
		{"POST", "/v1/notifications/registration"},
		{"GET", "/v2/insights/info/SOUND"},
		{"GET", "/v2/insights/info/WAKE_VARIANCE"},
		{"POST", "/v2/sharing/insight"},
		{"GET", "/v2/alarms/sounds"},
		{"GET", "/v2/sleep_sounds/combined_state"},
		{"GET", "/v2/sleep_sounds/status"},
		{"GET", "/v1/sounds/previews/ST010.mp3"},
		{"GET", "/v1/sounds/previews/DIG002.mp3"},
		// The share page and its implicit bare-form redirect, same as the art.
		{"GET", "/share/insight/abc123"},
		{"GET", "/share/insight"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil)
			if _, pattern := h.mux.Handler(r); pattern == "" {
				t.Errorf("orb no longer serves %s %s", c.method, c.path)
			}
		})
	}
}
