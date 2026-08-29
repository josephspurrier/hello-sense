package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// WithFallback makes this handler the front door for the app API, forwarding
// anything it does not implement to the old stack.
//
// This is the incremental cutover. Pointing the app straight at orb would be
// simpler, and would also be all-or-nothing: every screen served by orb the
// moment the app is rebuilt, and any endpoint orb has not implemented broken
// until it is. With a fallback, an endpoint moves the day its handler is
// registered, and `cmd/apidiff` still measures the two answers against each
// other beforehand.
//
// It is also what lets the phone register for push against orb without changing
// the app at all: the app keeps posting to the address it was built with, and
// /v1/notifications/registration is simply one of the paths orb now answers.
//
// The cost is real and worth stating: orb becomes the front door, so orb being
// down is the app being down, where before it was a shadow that could crash
// unnoticed. That is the argument for installing the launchd service before
// leaving this on.
func (h *Handler) WithFallback(upstream string) (*Handler, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	p := httputil.NewSingleHostReverseProxy(u)
	// A failed proxy must not look like a handled request. Without this, Go
	// writes a bare 502 with no log line and the app shows an empty screen for
	// a reason nothing records.
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.log.Error("app api fallback failed", "path", r.URL.Path, "err", err)
		w.WriteHeader(http.StatusBadGateway)
	}
	p.Transport = &http.Transport{
		// The app's own requests are short. A hung upstream must not pin a
		// connection here indefinitely.
		ResponseHeaderTimeout: 30 * time.Second,
	}
	h.fallback = p
	return h, nil
}

// serveFallback forwards one request to the old stack.
func (h *Handler) serveFallback(w http.ResponseWriter, r *http.Request) {
	// Logged at Debug, not Info: while most endpoints are still upstream this
	// is the common case, and logging it at Info would bury everything else.
	// The useful signal is the opposite one, and it shrinks over time.
	h.log.Debug("app api proxied upstream", "method", r.Method, "path", r.URL.Path)
	h.fallback.ServeHTTP(w, r)
}
