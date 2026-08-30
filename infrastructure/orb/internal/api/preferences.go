package api

import (
	"encoding/json"
	"net/http"
)

// defaultPreferences is what the app receives for an account that has never
// changed anything.
//
// Confirmed against the running stack with an EMPTY DynamoDB `preferences`
// table: suripu returned all seven of these, so they are defaults in code
// rather than stored rows. Only the two push settings default on.
//
// Order does not matter on the wire (it is a JSON object) but the set does: a
// missing key is not the same as false to the app, which uses presence to
// decide whether a toggle exists at all.
var defaultPreferences = map[string]bool{
	"ENHANCED_AUDIO":        false,
	"HEIGHT_METRIC":         false,
	"PUSH_ALERT_CONDITIONS": true,
	"PUSH_SCORE":            true,
	"TEMP_CELSIUS":          false,
	"TIME_TWENTY_FOUR_HOUR": false,
	"WEIGHT_METRIC":         false,
}

// mergedPreferences is defaults first, overrides on top, shared by the read
// and the write so a toggle can never save as one value and read back as
// another. Copied rather than mutated: the defaults map is package level and
// writing to it would leak one account's choice into every other request in
// the process.
func mergedPreferences(stored map[string]bool) map[string]bool {
	out := make(map[string]bool, len(defaultPreferences))
	for k, v := range defaultPreferences {
		out[k] = v
	}
	for k, v := range stored {
		// Only keys the app knows about. A stale row for a removed preference
		// would otherwise appear as a toggle with nothing behind it.
		if _, known := defaultPreferences[k]; known {
			out[k] = v
		}
	}
	return out
}

func (h *Handler) getPreferences(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	stored, err := h.store.PreferencesFor(r.Context(), accountID)
	if err != nil {
		h.log.Error("preferences", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, mergedPreferences(stored))
}

// putPreferences saves the toggles the app sends and answers with the full
// merged set, the same shape as the GET.
//
// An unknown key is 400, matching the reference, where the enum-keyed map
// fails to parse before the handler runs. That strictness is worth keeping on
// a write: silently storing a misspelled key would look like success while
// the toggle it was meant for never changes.
func (h *Handler) putPreferences(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var prefs map[string]bool
	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid preferences."})
		return
	}
	for k := range prefs {
		if _, known := defaultPreferences[k]; !known {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code: http.StatusBadRequest, Message: "Unknown preference."})
			return
		}
	}

	if err := h.store.PutPreferences(r.Context(), accountID, prefs); err != nil {
		h.log.Error("put preferences", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	stored, err := h.store.PreferencesFor(r.Context(), accountID)
	if err != nil {
		h.log.Error("preferences reread", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, mergedPreferences(stored))
}

// SleepSoundsStatus is the shape of GET /v2/sleep_sounds/status.
//
// All four fields null or false: no sleep sound has ever been played on this
// deployment and the Sense's speaker is not driven by anything orb does. The
// app polls this constantly and expects the shape, not the feature.
type SleepSoundsStatus struct {
	Playing       bool `json:"playing"`
	Sound         *any `json:"sound"`
	Duration      *any `json:"duration"`
	VolumePercent *int `json:"volume_percent"`
}

// sleepSoundsStatus is the single source for this payload. GET
// /v2/sleep_sounds/status and the `status` block of
// GET /v2/sleep_sounds/combined_state must never be able to disagree.
func sleepSoundsStatus() SleepSoundsStatus {
	return SleepSoundsStatus{Playing: false}
}

func (h *Handler) getSleepSoundsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sleepSoundsStatus())
}
