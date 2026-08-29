package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/feedback"
)

// The three timeline corrections, all on one path:
//
//	PATCH   move an event to a new time, returns the re-rendered timeline
//	PUT     the event was right, returns 202 with no body
//	DELETE  the event was wrong, returns 202 with the timeline
//
// All three store a row in timeline_feedback, and that row is training data:
// the algorithm reads a night's corrections back and learns from them. So the
// gate in internal/feedback runs first, and a correction that fails it is
// refused with 412 and never stored. Storing a nonsense correction is worse
// than refusing a real one, because the damage is not to one night but to every
// night scored afterwards.
//
// The order is deliberate and matches the reference: validate, then score, then
// store. Scoring before storing is what lets a correction that produces an
// unusable timeline be rejected without leaving the bad row behind.

// timeAmendment is the PATCH body. Only the new time; everything else about the
// correction is in the URL.
type timeAmendment struct {
	NewEventTime string `json:"new_event_time"`
}

// correction is what the three handlers have in common: the same path parsing,
// the same lookups, the same gate.
type correction struct {
	accountID int64
	date      time.Time
	offsetMS  int32
	record    feedback.Record
}

// prepare parses and validates a correction request, writing the error response
// itself when something is wrong.
//
// Returns ok=false when the caller should simply return: every failure path has
// already been answered.
func (h *Handler) prepare(w http.ResponseWriter, r *http.Request, newTime string) (correction, bool) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return correction{}, false
	}

	date, err := time.Parse(time.DateOnly, r.PathValue("date"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid date."})
		return correction{}, false
	}

	eventType, ok := feedback.EventTypeFromName(r.PathValue("type"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid event type."})
		return correction{}, false
	}

	ts, err := strconv.ParseInt(r.PathValue("ts"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid timestamp."})
		return correction{}, false
	}

	// The offset is the night's, not the account's current one. A correction
	// made after travelling must be read in the zone the night was slept in, or
	// the times move by the difference.
	offsetMS, err := h.store.OffsetForNight(r.Context(), accountID, date)
	if err != nil {
		h.log.Error("correction offset", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return correction{}, false
	}

	// The old time is what the app was showing, derived from the event's own
	// timestamp rather than trusted from the request.
	oldTime := feedback.LocalHourMinute(time.UnixMilli(ts), offsetMS)
	if newTime == "" {
		// PUT and DELETE do not move the event: old and new are the same, and
		// only is_correct distinguishes them.
		newTime = oldTime
	}

	rec := feedback.Record{
		EventType: eventType,
		DateNight: date,
		OldTime:   oldTime,
		NewTime:   newTime,
	}

	// The window offset differs by verb in the reference, and reproducing that
	// is the whole reason WindowOffsetForVerb exists. See its comment.
	windowOffset := feedback.WindowOffsetForVerb(r.Method)(offsetMS)

	existing, err := h.store.FeedbackForNight(r.Context(), accountID, date)
	if err != nil {
		h.log.Error("correction feedback", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return correction{}, false
	}
	prior := make([]feedback.Record, 0, len(existing))
	for _, e := range existing {
		prior = append(prior, feedback.Record{
			EventType: e.EventType, DateNight: date,
			OldTime: e.OldTime, NewTime: e.NewTime, IsCorrect: e.IsCorrect,
		})
	}

	if err := feedback.Validate(prior, rec, windowOffset); err != nil {
		// The reference's own wording. The app shows these to the user, so they
		// are part of the contract rather than log lines.
		msg := "This adjustment could not be made because it is too early or too late."
		if errors.Is(err, feedback.ErrOutOfOrder) {
			msg = "This adjustment could not be made because it is inconsistent with your other adjustments."
		}
		writeJSON(w, http.StatusPreconditionFailed, errorBody{
			Code: http.StatusPreconditionFailed, Message: msg})
		return correction{}, false
	}

	return correction{accountID: accountID, date: date, offsetMS: offsetMS, record: rec}, true
}

// store writes the correction and re-scores the night.
//
// Re-scoring synchronously is what makes the corrected timeline available to
// the response. The worker would get to it anyway, since a night whose feedback
// is newer than its timeline is picked up on the next pass, but the app expects
// the corrected timeline in this response rather than in a minute.
func (h *Handler) applyCorrection(r *http.Request, c correction, isCorrect bool) error {
	if err := h.store.InsertFeedback(r.Context(), c.accountID, c.date,
		c.record.EventType, c.record.OldTime, c.record.NewTime, isCorrect); err != nil {
		return err
	}

	h.pairCorrection(r, c)
	if !h.scorer.Available() {
		// No algorithm configured: the correction is stored and the night will
		// be scored when one appears. Not an error, and not silent.
		h.log.Warn("correction stored but not scored; no algorithm configured",
			"account", c.accountID, "date", c.date.Format(time.DateOnly))
		return nil
	}
	return h.scorer.ScoreNight(r.Context(), c.accountID, c.date)
}

// amendEvent handles PATCH: the user dragged an event to a new time.
//
// This is the correction that matters most. The other two say the algorithm was
// right or wrong; this one says what the right answer was, which is the only
// one that can teach the model a time it did not already have.
func (h *Handler) amendEvent(w http.ResponseWriter, r *http.Request) {
	var body timeAmendment
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NewEventTime == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Missing new_event_time."})
		return
	}

	c, ok := h.prepare(w, r, body.NewEventTime)
	if !ok {
		return
	}
	if err := h.applyCorrection(r, c, true); err != nil {
		h.log.Error("amend event", "account", c.accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.writeTimeline(w, r, c.accountID, c.date, http.StatusOK)
}

// confirmEvent handles PUT: the user tapped the tick.
//
// Feedback equals prediction, so old and new times are the same and is_correct
// is true. Returns 202 with no body: the timeline cannot have changed, because
// the correction agreed with it.
func (h *Handler) confirmEvent(w http.ResponseWriter, r *http.Request) {
	c, ok := h.prepare(w, r, "")
	if !ok {
		return
	}
	if err := h.applyCorrection(r, c, true); err != nil {
		h.log.Error("confirm event", "account", c.accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// rejectEvent handles DELETE: the user said the event did not happen.
//
// Despite the verb, nothing is deleted. A correction is inserted with
// is_correct false, and the event stays on the timeline: the reference's own
// comment notes that removing an intermediate event would need an API change it
// never made. So this records disagreement rather than acting on it.
func (h *Handler) rejectEvent(w http.ResponseWriter, r *http.Request) {
	c, ok := h.prepare(w, r, "")
	if !ok {
		return
	}
	if err := h.applyCorrection(r, c, false); err != nil {
		h.log.Error("reject event", "account", c.accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.writeTimeline(w, r, c.accountID, c.date, http.StatusAccepted)
}

// appStatsPatch is the body of PATCH /v1/app/stats. Both fields optional, and
// the app sends exactly one at a time: it marks a screen read as the screen is
// opened.
type appStatsPatch struct {
	InsightsLastViewed  *int64 `json:"insights_last_viewed"`
	QuestionsLastViewed *int64 `json:"questions_last_viewed"`
}

// patchAppStats records that a screen was opened, which is what clears its
// badge.
//
// 202 when something was written and **304 when nothing was**, which is unusual
// enough to be worth stating: a body with neither field is not an error, it is a
// no-op, and the reference says so with Not Modified rather than with a 400.
func (h *Handler) patchAppStats(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body appStatsPatch
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid body."})
		return
	}

	at := func(ms *int64) *time.Time {
		if ms == nil {
			return nil
		}
		t := time.UnixMilli(*ms).UTC()
		return &t
	}
	insights, questions := at(body.InsightsLastViewed), at(body.QuestionsLastViewed)

	if insights == nil && questions == nil {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if err := h.store.PutAppStatsViewed(r.Context(), accountID, insights, questions); err != nil {
		h.log.Error("app stats", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// postTimezone records the zone the phone is now in.
//
// The offset is recomputed from the zone rather than trusted from the body. The
// app sends both, and the two disagree across a DST boundary: a phone that
// cached its offset before the clocks changed sends yesterday's number with
// today's zone, and storing that shifts every subsequent night by an hour.
// The zone name is the durable fact; the offset is derived from it.
func (h *Handler) postTimezone(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body Timezone
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ZoneID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid timezone."})
		return
	}

	// A DELIBERATE divergence: the reference lets the zone lookup throw and
	// answers 500. This answers 400, because an unparseable zone is a bad
	// request and not a server fault. Safe to differ here in a way it would not
	// be elsewhere: the app sends the phone's own zone, so it cannot reach this
	// branch, and nothing is built against the 500. Recorded rather than left as
	// a silent difference, because "orb and the reference disagree" is a claim
	// that has to stay auditable.
	loc, err := time.LoadLocation(body.ZoneID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid timezone."})
		return
	}
	_, offsetSecs := time.Now().In(loc).Zone()
	offsetMS := int32(offsetSecs * 1000)

	// Refused without a paired Sense, matching the reference: the zone is what
	// the device's alarms are scheduled in.
	has, err := h.store.HasActiveSense(r.Context(), accountID)
	if err != nil {
		h.log.Error("timezone sense", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !has {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if err := h.store.PutTimezone(r.Context(), accountID, body.ZoneID, offsetMS); err != nil {
		h.log.Error("timezone", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, Timezone{OffsetMillis: offsetMS, ZoneID: body.ZoneID})
}

// pairCorrection records the other end of a corrected event as confirmed, when
// the person did not correct it themselves.
//
// This exists to stop a one-sided correction collapsing an ONLINE_HMM model,
// which has now happened twice: SLEEP on 2026-08-13 and BED on 2026-08-15. The
// second went unnoticed for two days, because the only symptom is that the
// algorithm silently stops winning and timelines quietly get worse. See
// feedback.PartnerOf and knowledgebase/TIMELINE-ALGORITHMS.md.
//
// The reasoning for treating the partner as CONFIRMED rather than leaving it
// out: a person who drags "got in bed" and leaves "got out of bed" alone is
// saying the second one was already right. That is the same statement the tick
// button makes, so recording it is reading their intent rather than inventing
// one. It is still a synthesised row, which is why it is only ever written when
// they have not touched that event themselves.
//
// Best effort. A correction that is stored and applied but not paired is worth
// less than one that is paired, and far more than a 500: the person's actual
// edit has already been written by the time this runs.
func (h *Handler) pairCorrection(r *http.Request, c correction) {
	partner, ok := feedback.PartnerOf(c.record.EventType)
	if !ok {
		// Not one of the four ordered sleep events, so nothing to pair. A
		// correction to a noise or a light trains no model.
		return
	}

	ctx := r.Context()
	existing, err := h.store.FeedbackForNight(ctx, c.accountID, c.date)
	if err != nil {
		h.log.Warn("pairing: could not read feedback",
			"account", c.accountID, "date", c.date.Format(time.DateOnly), "err", err)
		return
	}
	for _, e := range existing {
		if e.EventType == partner {
			// They have already said something about the other end. Their words
			// beat ours.
			return
		}
	}

	times, err := h.store.EventTimesForNight(ctx, c.accountID, c.date)
	if err != nil {
		h.log.Warn("pairing: could not read event times",
			"account", c.accountID, "date", c.date.Format(time.DateOnly), "err", err)
		return
	}
	at := times.At(partner)
	if at == nil {
		// No stored time for the partner, so there is nothing to confirm. A
		// night that never produced all four events is exactly the case this is
		// trying to prevent, but it cannot be fixed retroactively here.
		return
	}

	local := feedback.LocalHourMinute(*at, c.offsetMS)
	if err := h.store.InsertFeedback(ctx, c.accountID, c.date,
		partner, local, local, true); err != nil {
		h.log.Warn("pairing: could not write partner feedback",
			"account", c.accountID, "date", c.date.Format(time.DateOnly), "err", err)
		return
	}
	h.log.Info("paired one-sided correction",
		"account", c.accountID, "date", c.date.Format(time.DateOnly),
		"corrected", c.record.EventType, "confirmed", partner, "at", local)
}
