// Package scoring turns a night's raw data into a scored timeline.
//
// It exists because two callers need the same thing and must not each build
// their own version of it. The worker scores nights on a timer; the timeline
// write endpoints score one night synchronously, because a correction has to
// come back as a re-rendered timeline in the same response that made it.
//
// Both go through ScoreNight. A second copy of the request builder would drift
// from this one, and the failure would be a night scored one way by the timer
// and another way by a correction, which is invisible until the two disagree
// about somebody's sleep.
package scoring

import (
	"context"
	"log/slog"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/feedback"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
	"github.com/josephspurrier/hello-orb/orb/internal/timeline"
)

// Scorer holds what scoring a night needs: the data, the algorithm, somewhere
// to say what happened.
type Scorer struct {
	store *store.Store
	algo  timeline.Algorithm
	log   *slog.Logger
}

func New(s *store.Store, algo timeline.Algorithm, log *slog.Logger) *Scorer {
	return &Scorer{store: s, algo: algo, log: log}
}

// Available reports whether an algorithm is configured.
//
// Running without one is a valid configuration while the Java service is not
// up: ingest still works and nights queue for later. Callers check this rather
// than discovering a nil client mid-request.
func (s *Scorer) Available() bool { return s != nil && s.algo != nil }

// ScoreNight scores one night and stores the result.
//
// Idempotent: scoring the same night twice produces the same timeline and
// overwrites it, which is what makes it safe to call from a restart and from a
// correction alike.
func (s *Scorer) ScoreNight(ctx context.Context, accountID int64, date time.Time) error {
	night, err := s.store.LoadNight(ctx, accountID, date)
	if err != nil {
		return err
	}

	// A night with no motion cannot be scored: the pill is what detects sleep.
	// This is a normal outcome (away from home, flat battery), not an error.
	if len(night.Motion) == 0 {
		s.log.Info("skipping night with no motion",
			"account", accountID, "date", date.Format(time.DateOnly))
		return nil
	}

	prior, scratch, err := s.store.LatestModel(ctx, accountID)
	if err != nil {
		return err
	}

	req := timeline.Request{
		AccountID:  accountID,
		Date:       date.Format(time.DateOnly),
		OffsetMS:   night.OffsetMS,
		Start:      night.Start,
		End:        night.End,
		PriorModel: prior,
		Scratchpad: scratch,
		Age:        night.Age,
		DustOffset: night.DustOffset,

		HardwareVersion: night.HardwareVersion,

		// Non-nil so the three lists marshal as [] rather than null. Jackson
		// leaves a null field null instead of keeping the field initialiser,
		// and the far side then dereferences it.
		Sensors:  []timeline.Sensor{},
		Motion:   []timeline.Motion{},
		Feedback: []timeline.Feedback{},

		PartnerAccountID: night.PartnerID,
		PartnerMotion:    []timeline.Motion{},
	}
	for _, s := range night.Sensors {
		req.Sensors = append(req.Sensors, timeline.Sensor{
			TS: s.TS, Temperature: s.Temperature, Humidity: s.Humidity,
			Light: s.Light, LightVariance: s.LightVariance,
			AirQualityRaw: s.AirQualityRaw, AudioPeakBackgroundDB: s.AudioPeakBackgroundDB,
			AudioPeakEnergyDB: s.AudioPeakEnergyDB, AudioPeakDisturbanceDB: s.AudioPeakDisturbanceDB,
			AudioNumDisturbances: s.AudioNumDisturbances, WaveCount: s.WaveCount,
			HoldCount: s.HoldCount,
			Pressure:  s.Pressure, TVOC: s.TVOC, CO2: s.CO2, IR: s.IR, Clear: s.Clear,
			LuxCount: s.LuxCount, UVCount: s.UVCount,
		})
	}
	for _, m := range night.Motion {
		req.Motion = append(req.Motion, timeline.Motion{
			TS: m.TS, SVMNoGravity: m.SVMNoGravity, MotionRange: m.MotionRange,
			KickoffCounts: m.KickoffCounts, OnDurationSecs: m.OnDurationSecs,
		})
	}
	for _, m := range night.PartnerMotion {
		req.PartnerMotion = append(req.PartnerMotion, timeline.Motion{
			TS: m.TS, SVMNoGravity: m.SVMNoGravity, MotionRange: m.MotionRange,
			KickoffCounts: m.KickoffCounts, OnDurationSecs: m.OnDurationSecs,
		})
	}
	for _, f := range night.Feedback {
		req.Feedback = append(req.Feedback, timeline.Feedback{
			EventType: f.EventType, OldTime: f.OldTime, NewTime: f.NewTime,
			CreatedAt: f.CreatedAt,
		})
	}

	// The last stored answer rides along as the fallback for a night every
	// algorithm now refuses. This happens: VOTING scores a night in the
	// morning, then the day's motion arrives and the same night comes back
	// EVENTS_OUT_OF_ORDER on every later pass. Without the fallback a
	// correction on such a night is stored, acknowledged, and never drawn.
	req.StoredEvents, err = s.store.StoredEvents(ctx, accountID, date)
	if err != nil {
		return err
	}

	res, err := s.algo.Timeline(ctx, req)
	if err != nil {
		return err
	}

	if !res.Usable() {
		// Worth logging at info, not debug. A run of these is how you notice a
		// collapsed model, and the algorithm name tells you whether the chain
		// fell through to the fallback.
		s.log.Info("night not scoreable",
			"account", accountID, "date", date.Format(time.DateOnly),
			"algorithm", res.Algorithm, "status", res.Status)
		return nil
	}

	if err := s.store.SaveTimeline(ctx, accountID, date, night.OffsetMS, res); err != nil {
		return err
	}
	s.log.Info("scored night",
		"account", accountID, "date", date.Format(time.DateOnly),
		"algorithm", res.Algorithm, "score", derefInt32(res.SleepScore),
		"sensors", len(night.Sensors), "motion", len(night.Motion),
		"partner_motion", len(night.PartnerMotion),
		"feedback", len(night.Feedback))

	s.checkFeedbackApplied(accountID, date, night.OffsetMS, night.Feedback, res)

	// Persist anything the algorithm learned. Stored verbatim: the format
	// belongs to the algorithm, and orb only carries it.
	if len(res.UpdatedModel) > 0 || len(res.UpdatedScratchpad) > 0 {
		if err := s.store.SaveModel(ctx, accountID, date, res.UpdatedModel, res.UpdatedScratchpad); err != nil {
			return err
		}
		s.log.Info("model updated", "account", accountID, "date", date.Format(time.DateOnly))
	}
	return nil
}

// derefInt32 keeps a nil score out of the log as "none" rather than as a
// pointer address.
func derefInt32(p *int32) any {
	if p == nil {
		return "none"
	}
	return *p
}

// checkFeedbackApplied verifies that a correction actually reached the answer.
//
// This exists because the opposite happened and nothing noticed. orb-algo took
// feedback as a LEARNING signal only: it trained the model and then returned
// the algorithm's original events, so a correction was stored, acknowledged
// with a 200, rescored, and silently discarded from the timeline the person was
// looking at. Every signal except the displayed time said it had worked. See
// knowledgebase/CONSOLIDATION-PLAN.md, "Corrections were stored, acknowledged,
// and then discarded".
//
// A warning rather than an error: a mismatch means the algorithm reconciled the
// correction against the other events, which reprocessEventsBasedOnFeedback is
// entitled to do when a correction would put the night out of order. Dragging
// a wake time past getting out of bed legitimately lands somewhere other than
// where it was dropped. What is NOT legitimate is every correction coming back
// unchanged, and a run of these says exactly that.
//
// The comparison is in LOCAL time because that is what feedback is recorded in.
// Comparing the UTC instants would need the same offset applied anyway, and
// getting that wrong is how the original bug survived review.
func (s *Scorer) checkFeedbackApplied(accountID int64, date time.Time, offsetMS int32,
	sent []store.Feedback, res timeline.Result) {

	for _, f := range sent {
		// Only the four ordered sleep events land in the result. A correction
		// to a noise or a light has nowhere to be checked against.
		got := resultTimeFor(f.EventType, res)
		if got == nil {
			continue
		}
		local := got.UTC().Add(time.Duration(offsetMS) * time.Millisecond)

		// Compare HH:MM only. The app sends "08:35" and the column is a SQL
		// TIME, which comes back as "08:35:00", so comparing the raw strings
		// reports every correction as unapplied. Feedback has minute
		// resolution: the seconds are always zero and carry no information.
		want := hhmm(f.NewTime)
		if have := local.Format("15:04"); have != want {
			s.log.Warn("correction did not land on the requested time",
				"account", accountID, "date", date.Format(time.DateOnly),
				"event_type", f.EventType, "requested", want, "got", have)
		}
	}
}

// resultTimeFor maps a feedback event type onto the matching event in the
// result, or nil for the ones that have no slot there.
func resultTimeFor(eventType int32, res timeline.Result) *time.Time {
	switch eventType {
	case feedback.InBed:
		return res.InBed
	case feedback.Sleep:
		return res.Sleep
	case feedback.WakeUp:
		return res.WakeUp
	case feedback.OutOfBed:
		return res.OutOfBed
	default:
		return nil
	}
}

// hhmm normalises a stored feedback time to HH:MM.
//
// The column is a SQL TIME and arrives as "06:50:00"; the app sends "06:50".
// Truncating rather than parsing because a malformed value should compare
// unequal and produce the warning, not silently become midnight.
func hhmm(s string) string {
	if len(s) >= 5 {
		return s[:5]
	}
	return s
}
