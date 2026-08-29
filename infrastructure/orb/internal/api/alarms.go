package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// Alarms is the shape of GET /v2/alarms.
//
// Three lists. `voice` and `expansions` are for hardware integrations that
// never shipped here, and both are always empty, but they are always present:
// the app iterates them and a null is not an empty list.
type Alarms struct {
	Classic    []json.RawMessage `json:"classic"`
	Voice      []json.RawMessage `json:"voice"`
	Expansions []json.RawMessage `json:"expansions"`
}

// getAlarms returns the account's alarm set.
//
// The stored `definition` blob is passed through verbatim rather than rebuilt
// from the columns. It is what the app sent when it created the alarm, so
// echoing it cannot disagree with the app about a field this code does not know
// about, and there are several: `editable`, `source`, `year`, `month`,
// `day_of_month`, and a `sound` object with a url. Reconstructing it would mean
// inventing values for every one of them.
//
// The columns exist for the worker to query on, not for rendering.
func (h *Handler) getAlarms(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	defs, err := h.store.AlarmsFor(r.Context(), accountID)
	if err != nil {
		h.log.Error("alarms", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// The zone matters: whether a one-off alarm has already rung is a question
	// about the sleeper's wall clock, not UTC.
	_, zone, err := h.store.TimezoneAt(r.Context(), accountID, time.Now().UTC())
	if err != nil {
		h.log.Error("alarms timezone", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	loc, lerr := time.LoadLocation(zone)
	if lerr != nil {
		loc = time.UTC
	}

	out := Alarms{
		Classic:    []json.RawMessage{},
		Voice:      []json.RawMessage{},
		Expansions: []json.RawMessage{},
	}
	for _, d := range defs {
		out.Classic = append(out.Classic, json.RawMessage(disableIfExpired(d, time.Now(), loc)))
	}
	writeJSON(w, http.StatusOK, out)
}

// disableIfExpired applies Alarm.Utils.disableExpiredNoneRepeatedAlarms.
//
// A one-off alarm whose ring time has passed is reported as disabled even
// though the stored record still says enabled. This is computed, not stored:
// live DynamoDB says `enabled: true` for an alarm the API returns as false, so
// echoing the stored value shows the app a stale alarm it thinks is still set.
//
// Decoded into a map and re-encoded rather than parsed into a struct, so every
// field this code does not know about survives untouched.
func disableIfExpired(def []byte, now time.Time, loc *time.Location) []byte {
	var m map[string]any
	if err := json.Unmarshal(def, &m); err != nil {
		return def
	}
	if repeated, _ := m["repeated"].(bool); repeated {
		return def
	}
	year, ok1 := numField(m, "year")
	month, ok2 := numField(m, "month")
	day, ok3 := numField(m, "day_of_month")
	hour, ok4 := numField(m, "hour")
	minute, ok5 := numField(m, "minute")
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return def
	}

	ring := time.Date(year, time.Month(month), day, hour, minute, 0, 0, loc)
	if !ring.Before(now.Truncate(time.Minute)) {
		return def
	}

	m["enabled"] = false
	out, err := json.Marshal(m)
	if err != nil {
		return def
	}
	return out
}

func numField(m map[string]any, key string) (int, bool) {
	f, ok := m[key].(float64)
	return int(f), ok
}

// errorBody is suripu's error shape, which the app parses.
type errorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// getAlerts answers the alerts endpoint the way the running stack does.
//
// It has returned 403 to this app on all 170 calls in the log, every time,
// because the feature is not enabled for this deployment. Building something
// that returns data here would be inventing a behaviour the app has never seen
// and cannot have been tested against.
func (h *Handler) getAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusForbidden, errorBody{Code: http.StatusForbidden, Message: "Forbidden"})
}

// alarmFields are the parts of an alarm this code needs to understand. The rest
// of the object is echoed back untouched, which is why the blob is kept whole
// alongside these.
type alarmFields struct {
	Enabled   bool    `json:"enabled"`
	Smart     bool    `json:"smart"`
	Repeated  bool    `json:"repeated"`
	Hour      int32   `json:"hour"`
	Minute    int32   `json:"minute"`
	DayOfWeek []int32 `json:"day_of_week"`
	Sound     *struct {
		ID int32 `json:"id"`
	} `json:"sound"`
}

// clockSkewTolerance is how far the phone's clock may be from ours.
//
// One minute, plus the reference's own 50 second allowance on this endpoint.
// The client's idea of "now" is in the URL, and an alarm set by a phone whose
// clock is wrong rings at the wrong time, so this is refused rather than
// corrected.
const clockSkewTolerance = 110 * time.Second

// postAlarms replaces the account's alarms.
//
// The whole set arrives at once and replaces what was there: an alarm the app
// did not send is one the user deleted. The path carries the phone's clock so
// the skew check below can run.
func (h *Handler) postAlarms(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	clientMS, err := strconv.ParseInt(r.PathValue("ts"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid client time."})
		return
	}
	if skew := time.Since(time.UnixMilli(clientMS)); skew > clockSkewTolerance || skew < -clockSkewTolerance {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest,
			// The reference's exact wording. This string is shown to the user
			// verbatim, so paraphrasing it is a visible product change, not a
			// detail: the original tells them which setting to open.
			Message: "Your device's time is significantly different from our reference time. From your device's Settings app, please enable automatic Date & Time, or enter the correct time manually."})
		return
	}

	var body Alarms
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid alarms."})
		return
	}

	// Refused without a paired Sense: an alarm with no device to ring on is a
	// silent failure the user would only discover in the morning.
	senseID, err := h.store.ActiveSenseID(r.Context(), accountID)
	if err != nil {
		h.log.Error("alarms sense", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if senseID == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var defs []store.AlarmDef
	smartDays := map[int32]bool{}
	for _, group := range [][]json.RawMessage{body.Classic, body.Voice, body.Expansions} {
		for _, raw := range group {
			var f alarmFields
			if err := json.Unmarshal(raw, &f); err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody{
					Code: http.StatusBadRequest, Message: "Invalid alarms."})
				return
			}
			// Two smart alarms on the same day cannot both be honoured: the
			// smart one moves to meet your sleep, and two competing targets have
			// no answer. The reference refuses the save rather than picking one.
			if f.Smart && f.Enabled {
				days := f.DayOfWeek
				if len(days) == 0 {
					days = []int32{0} // a one-off still occupies its day
				}
				for _, d := range days {
					if smartDays[d] {
						writeJSON(w, http.StatusBadRequest, errorBody{
							Code:    http.StatusBadRequest,
							Message: "Cannot have two smart alarms on the same day."})
						return
					}
					smartDays[d] = true
				}
			}
			def := store.AlarmDef{
				Enabled: f.Enabled, Smart: f.Smart, Repeated: f.Repeated,
				Hour: f.Hour, Minute: f.Minute, DayOfWeek: f.DayOfWeek,
				Definition: raw,
			}
			if f.Sound != nil {
				id := f.Sound.ID
				def.SoundID = &id
			}
			if def.DayOfWeek == nil {
				def.DayOfWeek = []int32{}
			}
			defs = append(defs, def)
		}
	}

	if err := h.store.ReplaceAlarms(r.Context(), accountID, senseID, defs); err != nil {
		h.log.Error("alarms save", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// The request echoed back, which is what the reference returns: the app
	// treats the response as confirmation of what it sent.
	writeJSON(w, http.StatusOK, body)
}
