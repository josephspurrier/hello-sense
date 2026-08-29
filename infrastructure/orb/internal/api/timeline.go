package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/timeline"
)

// Everything in this file is presentation. It turns the per-minute segments
// orb-algo computed into what the iOS app renders: merged rows, event names,
// English sentences, and IDEAL/WARNING bands.
//
// None of it touches the night's samples. That is the seam: if it needs the
// samples it is computed in Java, if it is a wording or a shape it is done
// here, where a change is a recompile and an apidiff rather than a container
// rebuild. See the DECISION section of knowledgebase/CONSOLIDATION-PLAN.md.

// TimelineResponse is GET /v2/timeline/{date}.
type TimelineResponse struct {
	Date           string          `json:"date"`
	Score          int32           `json:"score"`
	ScoreCondition string          `json:"score_condition"`
	Message        string          `json:"message"`
	Events         []TimelineEvent `json:"events"`
	Metrics        []Metric        `json:"metrics"`
	SleepPeriods   []string        `json:"sleep_periods"`
	LockedDown     bool            `json:"locked_down"`
}

type TimelineEvent struct {
	Timestamp      int64    `json:"timestamp"`
	TimezoneOffset int32    `json:"timezone_offset"`
	DurationMillis int64    `json:"duration_millis"`
	EventType      string   `json:"event_type"`
	Message        string   `json:"message"`
	SleepDepth     int32    `json:"sleep_depth"`
	SleepState     string   `json:"sleep_state"`
	SleepPeriod    string   `json:"sleep_period"`
	ValidActions   []string `json:"valid_actions"`
}

type Metric struct {
	Name      string `json:"name"`
	Value     *int64 `json:"value"`
	Unit      string `json:"unit"`
	Condition string `json:"condition"`
}

// eventNames maps suripu's Event.Type onto the app's event_type.
//
// The two vocabularies are not the same and the difference is not cosmetic:
// suripu's IN_BED is the moment of getting in, while the app's IN_BED is a
// stretch of time spent there. Sending suripu's name through unchanged would
// put a "you went to bed" marker on every row of the night.
var eventNames = map[string]string{
	"IN_BED":     "GOT_IN_BED",
	"SLEEP":      "FELL_ASLEEP",
	"WAKE_UP":    "WOKE_UP",
	"OUT_OF_BED": "GOT_OUT_OF_BED",
	"SLEEPING":   "IN_BED",
	"":           "IN_BED",
	// A greyed-out stretch outside the in-bed period. Same row as a sleeping
	// stretch to the app; only its sleep_state gives it away.
	"NONE":         "IN_BED",
	"MOTION":       "GENERIC_MOTION",
	"SLEEP_MOTION": "GENERIC_MOTION",
	"LIGHTS_OUT":   "LIGHTS_OUT",
	"LIGHT":        "LIGHT",
	"NOISE":        "GENERIC_SOUND",
	"SOUND":        "GENERIC_SOUND",
}

// eventMessages is the sentence shown against each event.
//
// A stretch of sleep carries no message: the app draws those as a band rather
// than a line item, and a sentence on each would repeat nineteen times.
var eventMessages = map[string]string{
	"GOT_IN_BED":     "You went to bed.",
	"FELL_ASLEEP":    "You fell asleep.",
	"GOT_OUT_OF_BED": "You got out of bed.",
	"LIGHTS_OUT":     "The lights went out in your room.",
	"LIGHT":          "There was a light disturbance.",
	"GENERIC_MOTION": "You were moving around quite a bit.",
	"PARTNER_MOTION": "You and your partner were both moving around.",
	"GENERIC_SOUND":  "There was a noise disturbance.",
	"IN_BED":         "",
	// WOKE_UP is absent on purpose: its wording depends on the hour. See
	// wakeMessage.
}

// wakeMessage greets by local hour rather than saying "You woke up."
//
// The obvious sentence is the wrong one: "You woke up." exists in the reference
// as WAKE_UP_DISTURBANCE_MESSAGE, for stirring mid-night, while the event that
// ends the night greets the time of day. Using the disturbance wording for both
// is a plausible-looking mistake that reads fine on every night.
func wakeMessage(ts time.Time, offsetMS int32) string {
	switch hour := ts.Add(time.Duration(offsetMS) * time.Millisecond).UTC().Hour(); {
	case hour < 12:
		return "Good morning."
	case hour < 18:
		return "Good afternoon."
	default:
		return "Good evening."
	}
}

// validActions is what the app may offer on each row.
//
// Only the four events a person can meaningfully correct accept ADJUST_TIME,
// and that list is what makes the correction PATCH reachable at all. A derived
// IN_BED stretch offers nothing: there is no single moment to move.
func validActions(eventType string) []string {
	switch eventType {
	case "GOT_IN_BED", "FELL_ASLEEP", "WOKE_UP", "GOT_OUT_OF_BED":
		return []string{"ADJUST_TIME", "VERIFY", "INCORRECT"}
	case "LIGHTS_OUT", "GENERIC_SOUND", "GENERIC_MOTION":
		return []string{"VERIFY", "INCORRECT"}
	default:
		return []string{}
	}
}

// sleepState buckets a depth the way the app colours it.
//
// Depth alone decides this: there is deliberately no special case for the named
// events. An earlier version forced AWAKE on GOT_IN_BED, WOKE_UP and the rest,
// which is right on almost every night because those events usually land on a
// depth of 0, and wrong on the night one of them does not.
//
// The thresholds are suripu's SleepState.from, not a guess. The reference sends
// SOUND at depth 77 and MEDIUM at 31, which no round-number banding produces.
func sleepState(depth int32) string {
	switch {
	case depth < 5:
		return "AWAKE"
	case depth < 10:
		return "LIGHT"
	case depth < 70:
		return "MEDIUM"
	default:
		return "SOUND"
	}
}

// renderSegments turns the algorithm's segments into the app's rows, one for
// one.
//
// There is deliberately no merging here. The bands the app draws are already
// bands when they arrive: orb-algo runs suripu's own mergeEvents, which buffers
// 21 consecutive sleeping minutes into one event and computes that band's
// depth. An earlier version of this function re-banded per-minute segments on a
// 21-minute clock and averaged the depths, which produced bands of about the
// right shape carrying the wrong numbers, and cost 149 minutes of misclassified
// sound sleep. Banding is arithmetic on the samples, so it belongs on the far
// side of the seam.
func renderSegments(in []timeline.Segment) []TimelineEvent {
	out := []TimelineEvent{}
	for _, s := range in {
		name, known := eventNames[s.Type]
		if !known {
			name = "IN_BED"
		}

		// A greyed-out stretch reads AWAKE whatever depth it carries. These are
		// the minutes outside the in-bed period, where a depth was computed but
		// means nothing: the room was empty.
		state := sleepState(s.SleepDepth)
		if s.Type == "NONE" {
			state = "AWAKE"
		}

		// A merged band carries no period of its own: mergeEvents builds it as
		// a bare null event, keeping only the start, end, offset and depth.
		// NONE is what the reference then renders for it.
		period := s.SleepPeriod
		if period == "" {
			period = "NONE"
		}

		message := eventMessages[name]
		if name == "WOKE_UP" {
			message = wakeMessage(s.TS, s.OffsetMS)
		}

		out = append(out, TimelineEvent{
			Timestamp:      s.TS.UnixMilli(),
			TimezoneOffset: s.OffsetMS,
			DurationMillis: s.DurationMillis,
			EventType:      name,
			SleepDepth:     s.SleepDepth,
			SleepState:     state,
			SleepPeriod:    period,
			Message:        message,
			ValidActions:   validActions(name),
		})
	}
	return out
}

// metric builds one of the six measured metrics.
//
// Every one of them carries condition IDEAL, unconditionally. This looks like a
// bug and is not: SleepMetrics.fromV1 passes Condition.IDEAL for all six, and
// the app does its own colouring from the value. An earlier version invented
// thresholds here (under six hours is ALERT, and so on) which were plausible,
// consistent, and disagreed with the reference on most nights.
//
// A zero minute count or timestamp becomes null rather than 0. The reference
// does this in create() because the underlying stats default to 0 rather than
// being optional, so 0 means "not measured" and the app must not draw it as a
// measured zero.
func metric(name string, v *int64, unit string) Metric {
	if unit != "QUANTITY" && v != nil && *v == 0 {
		v = nil
	}
	return Metric{Name: name, Value: v, Unit: unit, Condition: "IDEAL"}
}

func (h *Handler) getTimeline(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	date, err := time.Parse("2006-01-02", r.PathValue("date"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	h.writeTimeline(w, r, accountID, date, http.StatusOK)
}

// writeTimeline renders a night and sends it.
//
// Shared with the correction endpoints, which return the same document at a
// different status: PATCH answers 200 and DELETE answers 202. Rendering it in
// one place is what keeps a corrected timeline identical to a fetched one; two
// renderers would disagree the first time either changed, and the app would
// show one thing on save and another on refresh.
func (h *Handler) writeTimeline(w http.ResponseWriter, r *http.Request,
	accountID int64, date time.Time, status int) {

	night, err := h.store.TimelineFor(r.Context(), accountID, date)
	if err != nil {
		h.log.Error("timeline", "account", accountID, "date", date, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	out := TimelineResponse{
		Date:         date.Format("2006-01-02"),
		Events:       []TimelineEvent{},
		Metrics:      []Metric{},
		SleepPeriods: []string{"NIGHT"},
	}
	if night == nil {
		// A night with no timeline is an empty one, not a 404. The app shows
		// "no data" for this and treats a 404 as an error worth retrying.
		writeJSON(w, status, out)
		return
	}

	out.Events = renderSegments(night.Segments)
	if night.Score != nil {
		out.Score = *night.Score
	}
	out.ScoreCondition = scoreCondition(out.Score)
	// Six measured metrics then one per sensor, in the reference's order. The
	// app reads them positionally in places, so the order is part of the
	// contract rather than a nicety.
	out.Metrics = []Metric{
		metric("total_sleep", night.TotalSleepMins, "MINUTES"),
		metric("sound_sleep", night.SoundSleepMins, "MINUTES"),
		metric("time_to_sleep", night.TimeToSleepMins, "MINUTES"),
		metric("times_awake", night.TimesAwake, "QUANTITY"),
		metric("fell_asleep", night.SleepAt, "TIMESTAMP"),
		metric("woke_up", night.WakeUpAt, "TIMESTAMP"),
	}
	// The sensor conditions carry a null value and only a condition: the app
	// draws them as a coloured dot, not a number. However many arrive is how
	// many there are; a sensor with no usable samples over the sleep period is
	// omitted by the algorithm rather than sent as UNKNOWN.
	for _, c := range night.Conditions {
		out.Metrics = append(out.Metrics, Metric{
			Name: c.Sensor, Unit: "CONDITION", Condition: c.Condition,
		})
	}
	out.Message = summary(night.TotalSleepMins, night.UninterruptedMins)

	writeJSON(w, status, out)
}

func scoreCondition(score int32) string {
	switch {
	case score >= 80:
		return "IDEAL"
	case score >= 60:
		return "WARNING"
	default:
		return "ALERT"
	}
}

// summary is the sentence at the top of the screen. The markdown asterisks are
// the app's own emphasis syntax and it renders them; they are not a mistake.
//
// The second figure is UNINTERRUPTED sleep, despite the sentence saying
// "soundly" and despite sound sleep being right there in the same stats. The
// reference reads uninterruptedSleepDurationInMinutes; on 2026-08-13 that is
// 363 minutes against 253 of sound sleep, so the wrong one renders as 4.2 hours
// where the app shows 6.1.
func summary(total, uninterrupted *int64) string {
	if total == nil {
		return ""
	}
	if uninterrupted == nil || *uninterrupted <= 0 {
		return fmt.Sprintf("You were asleep for **%s hours**.", hours(*total))
	}
	return fmt.Sprintf("You were asleep for **%s hours**, and sleeping soundly for %s hours.",
		hours(*total), hours(*uninterrupted))
}

// hours renders a minute count as hours to one decimal, rounding halves up.
//
// Not %.1f. Go rounds a printed float to even and works from the exact binary
// value, while Java's Formatter rounds the shortest decimal representation half
// up, and 363 minutes lands exactly on the boundary: 6.05 hours prints as 6.0
// here and 6.1 there. Doing the rounding in integer arithmetic sidesteps the
// float entirely, so there is no half-way case left to disagree about.
func hours(mins int64) string {
	tenths := (mins*20 + 60) / 120
	return fmt.Sprintf("%d.%d", tenths/10, tenths%10)
}
