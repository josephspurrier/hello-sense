package api

import "net/http"

// The Expansions screen (GET /v2/expansions and friends), ported from
// suripu-app's ExpansionsResource.
//
// Expansions were the smart-home integrations: Nest thermostats and Philips
// Hue lights, controlled by voice ("set the temperature to 68", "turn off the
// lights"). Each ran through that vendor's OWN cloud via OAuth, which is what
// makes reviving them more than a backend exercise:
//
//   - Nest: Google shut down the "Works with Nest" API in 2019. There is no
//     endpoint left to integrate with. It cannot be revived.
//   - Hue: still has a working API, but connecting one needs a registered
//     OAuth app and the owner's bridge, i.e. the user's own credentials and a
//     decision to build it.
//
// So this deployment offers no expansions today. The endpoint returns an empty
// catalog rather than a 404, which is the difference between the screen showing
// a clean "nothing connected" state and it erroring or spinning. If Hue support
// is ever built, its expansion is added to the catalog here.

func (h *Handler) getExpansions(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

// getExpansion / patchExpansion / configurations answer for a specific id.
// With an empty catalog none exist, so these are 404s; they are registered so
// the paths resolve to a clean not-found rather than the app's unimplemented
// fallback.

func (h *Handler) getExpansion(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (h *Handler) patchExpansion(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (h *Handler) getExpansionConfigurations(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// No expansion, so no configurations. An empty list keeps the app happy.
	writeJSON(w, http.StatusOK, []any{})
}

func (h *Handler) patchExpansionConfigurations(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}
