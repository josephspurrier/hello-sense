package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/josephspurrier/hello-orb/orb/internal/timeline"
)

// Night boundaries, matching DateTimeUtil.DAY_STARTS_AT_HOUR and
// DAY_ENDS_AT_HOUR in suripu-core.
//
// A "night of D" runs from 20:00 local on D to 12:00 local on D+1. It is a
// 16-hour window, not a calendar day, and it is asymmetric: the evening side
// starts early enough to catch getting into bed, the morning side runs late
// enough to catch a lie-in. The HMM's 5-minute bins are indexed from the 20:00
// origin, so a timeline event at bin 60 is 01:00 local.
//
// Getting these wrong shifts every computed event, silently, by whole hours.
const (
	DayStartsAtHour = 20
	DayEndsAtHour   = 12

	// ScoreAnnounceHour is the earliest local hour, the morning after a night,
	// at which its sleep score may be pushed to a phone.
	//
	// Eleven rather than DayEndsAtHour, so the notification is late enough for
	// the score to have stopped moving but still lands in the morning. It is a
	// judgement, not a derived value: the night keeps rescoring until noon, so
	// 11:00 can in principle still announce a score that changes once more.
	//
	// Whatever it is set to it must not be early. A sleep score is announced
	// ONCE per night, deduplicated by date, so the first send is the only send
	// and an early one is permanently wrong rather than briefly wrong. See
	// RecentScoredNights.
	ScoreAnnounceHour = 11
)

// NightWindow returns the UTC instants bounding the night of date, given the
// account's offset in force at the time.
//
// The offset is passed in rather than looked up here because it must be the one
// that applied on that night, not today's. See OffsetMSAt.
func NightWindow(date time.Time, offsetMS int32) (start, end time.Time) {
	// Work in the account's local wall clock, then convert to UTC once.
	loc := time.FixedZone("acct", int(offsetMS/1000))
	y, m, d := date.Date()
	startLocal := time.Date(y, m, d, DayStartsAtHour, 0, 0, 0, loc)
	endLocal := time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, 1).
		Add(DayEndsAtHour * time.Hour)
	return startLocal.UTC(), endLocal.UTC()
}

// SensorReading is one minute of room data for the algorithms.
type SensorReading struct {
	TS                     time.Time
	OffsetMS               int32
	Temperature            *int32
	Humidity               *int32
	Light                  *int32
	LightVariance          *int32
	AirQualityRaw          *int32
	AudioPeakBackgroundDB  *int32
	AudioPeakEnergyDB      *int32
	AudioPeakDisturbanceDB *int32
	AudioNumDisturbances   *int32
	WaveCount              *int32
	HoldCount              *int32
}

// MotionReading is one minute of pill movement.
type MotionReading struct {
	TS             time.Time
	OffsetMS       int32
	SVMNoGravity   *int64
	MotionRange    *int64
	KickoffCounts  *int32
	OnDurationSecs *int32
}

// Feedback is a human correction to a night's events. event_type follows
// Event.Type: 11 IN_BED, 12 SLEEP, 13 OUT_OF_BED, 14 WAKE_UP.
type Feedback struct {
	EventType int32
	OldTime   string
	NewTime   string
	IsCorrect bool

	// CreatedAt is when the correction was made. The algorithm discards
	// feedback created outside the night's own window, so this travels with the
	// correction rather than being reinvented on the far side.
	CreatedAt time.Time
}

// NightData is everything the timeline algorithm needs for one night.
type NightData struct {
	AccountID  int64
	Date       time.Time
	OffsetMS   int32
	Start, End time.Time
	Sensors    []SensorReading
	Motion     []MotionReading
	Feedback   []Feedback

	// PartnerID and PartnerMotion are the bed partner's account and pill
	// samples over the same window, when the account has a partner
	// (account_partners). Zero and empty otherwise. The algorithms use them to
	// mark minutes where the partner moved and this account did not, and the
	// reference's partner filters need them as input.
	PartnerID     int64
	PartnerMotion []MotionReading

	// Age in whole years on the night, for the sleep duration score. Zero when
	// the account has no birthdate, which the score reads as an adult.
	Age int32

	// DustOffset is the paired Sense's factory dust calibration, nil when the
	// device has never been calibrated. The timeline's air quality condition is
	// computed from it, so a night scored without it reads dust high.
	DustOffset *int32
}

// LoadNight assembles one night.
//
// Returns the window even when empty, so callers can distinguish "no data" from
// "wrong window", which is the failure that looks like a broken algorithm.
func (s *Store) LoadNight(ctx context.Context, accountID int64, date time.Time) (NightData, error) {
	out := NightData{AccountID: accountID, Date: date}

	// The offset in force at the START of the night. A DST change mid-night is
	// rare enough, and using one offset for the window matches what suripu does
	// via its timezone offset map.
	probe := time.Date(date.Year(), date.Month(), date.Day(), DayStartsAtHour, 0, 0, 0, time.UTC)
	offset, _, err := s.OffsetMSAt(ctx, accountID, probe)
	if err != nil {
		return out, err
	}
	out.OffsetMS = offset
	out.Start, out.End = NightWindow(date, offset)

	// Age at the night, not today. Postgres does the subtraction so the two
	// dates are compared by one clock; a birthdate is a plain date and doing
	// this in Go would drag the process timezone into the answer. The reference
	// takes whole years by integer-dividing days by 365, so a leap year moves
	// the birthday by a day: reproduced deliberately, since the alternative
	// disagrees with the running stack for a few days a year.
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(((($2::date - birthdate::date))::int / 365), 0)
		FROM accounts WHERE id = $1`, accountID, date).Scan(&out.Age); err != nil {
		return out, fmt.Errorf("store: age %d: %w", accountID, err)
	}

	// The dust calibration of whichever Sense is paired now, not of the one
	// that took the readings. They are the same device in every case that
	// matters, and joining per sample to find out would cost a row lookup per
	// minute to answer a question about the account's one Sense.
	//
	// A missing row leaves it nil, which is correct: no calibration is not an
	// offset of zero, and an offset of zero derives a delta of +300.
	if err := s.pool.QueryRow(ctx, `
		SELECT s.dust_offset
		FROM senses s
		JOIN account_senses a ON a.device_id = s.device_id AND a.active
		WHERE a.account_id = $1`, accountID).Scan(&out.DustOffset); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("store: night dust offset %d: %w", accountID, err)
	}

	sensorRows, err := s.pool.Query(ctx, `
		SELECT ts, offset_ms, temperature, humidity, light, light_variance,
		       air_quality_raw, audio_peak_background_db, audio_peak_energy_db,
		       audio_peak_disturbances_db, audio_num_disturbances, wave_count,
		       hold_count
		FROM sensor_samples
		WHERE account_id = $1 AND ts >= $2 AND ts < $3
		ORDER BY ts`, accountID, out.Start, out.End)
	if err != nil {
		return out, fmt.Errorf("store: night sensors: %w", err)
	}
	for sensorRows.Next() {
		var r SensorReading
		if err := sensorRows.Scan(&r.TS, &r.OffsetMS, &r.Temperature, &r.Humidity,
			&r.Light, &r.LightVariance, &r.AirQualityRaw, &r.AudioPeakBackgroundDB,
			&r.AudioPeakEnergyDB, &r.AudioPeakDisturbanceDB, &r.AudioNumDisturbances,
			&r.WaveCount, &r.HoldCount); err != nil {
			sensorRows.Close()
			return out, err
		}
		out.Sensors = append(out.Sensors, r)
	}
	sensorRows.Close()
	if err := sensorRows.Err(); err != nil {
		return out, err
	}

	out.Motion, err = s.motionInWindow(ctx, accountID, out.Start, out.End)
	if err != nil {
		return out, err
	}

	// The partner's motion over the SAME window, in this account's offset. The
	// reference reads the partner's pill by the account's own local window
	// (getPartnerTrackerMotion), which is what makes the two series line up
	// minute for minute.
	if partner, ok, err := s.PartnerOf(ctx, accountID); err != nil {
		return out, err
	} else if ok {
		out.PartnerID = partner.AccountID
		out.PartnerMotion, err = s.motionInWindow(ctx, partner.AccountID, out.Start, out.End)
		if err != nil {
			return out, err
		}
	}

	// Feedback is keyed by date_of_night, not by timestamp, so it is fetched by
	// date rather than by the window.
	fbRows, err := s.pool.Query(ctx, `
		SELECT event_type, old_time::text, new_time::text, is_correct, created_at
		FROM timeline_feedback
		WHERE account_id = $1 AND date_of_night = $2 AND is_correct
		ORDER BY created_at`, accountID, date)
	if err != nil {
		return out, fmt.Errorf("store: night feedback: %w", err)
	}
	defer fbRows.Close()
	for fbRows.Next() {
		var f Feedback
		if err := fbRows.Scan(&f.EventType, &f.OldTime, &f.NewTime, &f.IsCorrect, &f.CreatedAt); err != nil {
			return out, err
		}
		out.Feedback = append(out.Feedback, f)
	}
	return out, fbRows.Err()
}

// motionInWindow is one account's pill samples over a window, oldest first.
func (s *Store) motionInWindow(ctx context.Context, accountID int64, start, end time.Time) ([]MotionReading, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ts, offset_ms, svm_no_gravity, motion_range, kickoff_counts, on_duration_secs
		FROM pill_samples
		WHERE account_id = $1 AND ts >= $2 AND ts < $3
		ORDER BY ts`, accountID, start, end)
	if err != nil {
		return nil, fmt.Errorf("store: night motion: %w", err)
	}
	defer rows.Close()
	var out []MotionReading
	for rows.Next() {
		var r MotionReading
		if err := rows.Scan(&r.TS, &r.OffsetMS, &r.SVMNoGravity, &r.MotionRange,
			&r.KickoffCounts, &r.OnDurationSecs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NightsNeedingTimeline lists account/date pairs that have motion data but no
// computed timeline, or whose feedback is newer than the stored timeline.
//
// Feedback newness matters because a correction must trigger a recompute; that
// is the whole mechanism by which a corrected night gets redrawn.
func (s *Store) NightsNeedingTimeline(ctx context.Context, limit int) ([]struct {
	AccountID int64
	Date      time.Time
}, error) {
	rows, err := s.pool.Query(ctx, `
		WITH samples AS (
			-- The account's local wall clock as a plain timestamp. The offset is
			-- added first, then the zone is dropped, so every date and hour taken
			-- from it below is the sleeper's, not the session's. Without the
			-- AT TIME ZONE this depends on the connection's TimeZone setting,
			-- which is UTC today and is not something this query should rely on.
			SELECT account_id, offset_ms,
			       (ts + make_interval(secs => offset_ms / 1000.0))
			           AT TIME ZONE 'UTC' AS local_ts
			FROM pill_samples
		), nights AS (
			SELECT account_id,
			       (local_ts - make_interval(hours => $1))::date AS date_of_night,
			       MAX(offset_ms) AS offset_ms
			FROM samples
			-- Only samples that fall inside some night's window.
			--
			-- A night runs 20:00 local to 12:00 local the next day, so the eight
			-- hours from 12:00 to 20:00 belong to no night at all. Subtracting 20
			-- hours and taking the date does not know that: it maps an afternoon
			-- reading onto the PREVIOUS night, which that night's window then
			-- excludes. The two disagreed, and NightWindow is the one that counts,
			-- because it is what LoadNight selects on.
			--
			-- The symptom is a night that can never be satisfied. It appears here
			-- because it has samples, loads zero motion because none are in the
			-- window, gets skipped without writing a timeline, and so appears
			-- again on the next pass, forever. Observed on 2026-08-09, whose only
			-- eight samples were afternoon movement on 08-10.
			WHERE EXTRACT(hour FROM local_ts) >= $1
			   OR EXTRACT(hour FROM local_ts) <  $4
			GROUP BY 1, 2
		)
		SELECT n.account_id, n.date_of_night
		FROM nights n
		LEFT JOIN timeline_events e
		       ON e.account_id = n.account_id AND e.date_of_night = n.date_of_night
		WHERE e.account_id IS NULL
		   -- Feedback this timeline has not accounted for.
		   --
		   -- Compared against feedback_applied_at, NOT updated_at. The two say
		   -- different things and only this one is the question being asked:
		   -- updated_at moves whenever the row is written, so anything that
		   -- rewrites a settled night makes it look as though it predates its
		   -- own corrections. See migration 0012.
		   --
		   -- IS DISTINCT FROM, not <, so a null (scored before the column
		   -- existed) counts as "not accounted for" and is picked up once,
		   -- rather than being skipped forever the way a null comparison would.
		   OR e.feedback_applied_at IS DISTINCT FROM (
		        SELECT MAX(f.created_at)
		        FROM timeline_feedback f
		        WHERE f.account_id = n.account_id AND f.date_of_night = n.date_of_night
		      )
		   -- A night whose window has not closed is recomputed every pass.
		   -- Without this a night is scored once, from however much of it had
		   -- arrived at that moment, and never revisited: a run scored at 05:44
		   -- saw 19 of the night's 39 motion samples and froze there while the
		   -- sleeper was still asleep. suripu never hit this because it computes
		   -- a timeline on demand for every app request, so it always sees
		   -- everything so far.
		   --
		   -- The boundary is derived entirely database-side rather than by
		   -- comparing a device timestamp against now(): the two clocks are not
		   -- the same and that comparison has already caused two bugs here.
		   -- Once the window closes no further pass matches, so a settled night
		   -- stops being touched without needing a separate guard.
		   OR now() < ((n.date_of_night::timestamp
		                 + make_interval(hours => $3)
		                 - make_interval(secs => n.offset_ms / 1000.0)) AT TIME ZONE 'UTC')
		ORDER BY n.date_of_night DESC
		LIMIT $2`, DayStartsAtHour, limit, 24+DayEndsAtHour, DayEndsAtHour)
	if err != nil {
		return nil, fmt.Errorf("store: nights needing timeline: %w", err)
	}
	defer rows.Close()

	var out []struct {
		AccountID int64
		Date      time.Time
	}
	for rows.Next() {
		var r struct {
			AccountID int64
			Date      time.Time
		}
		if err := rows.Scan(&r.AccountID, &r.Date); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestModel returns the account's most recent learned ONLINE_HMM model and
// scratchpad, or nils if it has none.
//
// Nil is meaningful: it tells the algorithm to start from the default ensemble.
// That is both the correct state for a new account and the way to recover from
// a collapsed model, which is why deleting rows from hmm_models is a supported
// repair rather than data loss.
func (s *Store) LatestModel(ctx context.Context, accountID int64) (model, scratchpad []byte, err error) {
	// The two are selected independently because they do not travel together.
	// A night that learns something writes a scratchpad and no model, because
	// promotion is deferred by a day; the night that promotes writes a model
	// and zeroes the scratchpad. Taking "the newest row" therefore returns a
	// null model most mornings, which reads as "this account has never learned
	// anything" and silently drops the algorithm back to the default ensemble.
	// Nothing errors, and the only symptom is that corrections stop mattering.
	err = s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT model_params FROM hmm_models
		    WHERE account_id = $1 AND model_params IS NOT NULL
		    ORDER BY date_of_night DESC LIMIT 1),
		  (SELECT scratchpad FROM hmm_models
		    WHERE account_id = $1 AND scratchpad IS NOT NULL
		    ORDER BY date_of_night DESC LIMIT 1)`,
		accountID).Scan(&model, &scratchpad)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: latest model %d: %w", accountID, err)
	}
	return model, scratchpad, nil
}

// SaveModel stores what the algorithm learned, keyed by the night it learned
// from so the history is inspectable.
func (s *Store) SaveModel(ctx context.Context, accountID int64, date time.Time, model, scratchpad []byte) error {
	// model_params is never nulled out. Most runs return a scratchpad and no
	// model, and writing that straight in destroys whatever the account had
	// learned: it turned a 16,805 byte model into NULL here, and because the
	// blob is opaque there is nothing to notice. An absent model means "nothing
	// new was promoted", not "the model is gone", so the previous one is
	// carried forward.
	//
	// The scratchpad IS overwritten, including with null, because clearing it
	// is a real instruction: updateModelPriorsAndZeroOutScratchpad zeroes it
	// once learning has been promoted, and keeping it would apply the same
	// correction a second time.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hmm_models (account_id, date_of_night, model_params, scratchpad)
		VALUES ($1, $2,
		        COALESCE($3, (SELECT h.model_params FROM hmm_models h
		                       WHERE h.account_id = $1
		                         AND h.date_of_night <= $2
		                         AND h.model_params IS NOT NULL
		                       ORDER BY h.date_of_night DESC LIMIT 1)),
		        $4)
		ON CONFLICT (account_id, date_of_night) DO UPDATE SET
			model_params = COALESCE(EXCLUDED.model_params, hmm_models.model_params),
			scratchpad = EXCLUDED.scratchpad,
			updated_at = now()`, accountID, date, model, scratchpad)
	if err != nil {
		return fmt.Errorf("store: save model %d: %w", accountID, err)
	}
	return nil
}

// PruneDeliveredMessages removes device commands that were delivered longer ago
// than keep. Undelivered rows are never touched, however old: a command that
// was never collected is a bug to investigate, not garbage to sweep up.
func (s *Store) PruneDeliveredMessages(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM device_messages
		WHERE delivered_at IS NOT NULL AND delivered_at < now() - $1::interval`,
		keep.String())
	if err != nil {
		return 0, fmt.Errorf("store: prune messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SaveTimeline stores a night's computed events and stats.
//
// Written in one transaction: a timeline and its stats are shown together, and
// a crash between them would leave the app displaying events with no score, or
// a score for events that were never stored.
func (s *Store) SaveTimeline(ctx context.Context, accountID int64, date time.Time, offsetMS int32, r timeline.Result) error {
	// nil rather than a JSONB "null" when there is nothing to store: a JSONB
	// null and a SQL NULL read differently on the way out, and only one of them
	// means "this night was never scored".
	var segments []byte
	if len(r.Segments) > 0 {
		var mErr error
		segments, mErr = json.Marshal(r.Segments)
		if mErr != nil {
			return fmt.Errorf("store: marshal segments: %w", mErr)
		}
	}
	var conditions []byte
	if len(r.Conditions) > 0 {
		var mErr error
		conditions, mErr = json.Marshal(r.Conditions)
		if mErr != nil {
			return fmt.Errorf("store: marshal conditions: %w", mErr)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO timeline_events (account_id, date_of_night, sleep_period,
			in_bed_at, sleep_at, wake_up_at, out_of_bed_at, algorithm, offset_ms,
			segments, conditions, feedback_applied_at)
		VALUES ($1,$2,2,$3,$4,$5,$6,$7,$8,$9,$10,
			-- Stamped from the FEEDBACK, not from now(). This records which
			-- corrections the stored answer accounts for, which is the question
			-- NightsNeedingTimeline actually asks; now() would only record when
			-- the row happened to be written. See migration 0012.
			--
			-- Read inside the same statement that writes the result, so a
			-- correction landing between the score and the write is not marked
			-- as applied when it was not: it will be newer than this value and
			-- the night stays eligible.
			(SELECT MAX(f.created_at) FROM timeline_feedback f
			  WHERE f.account_id = $1 AND f.date_of_night = $2))
		ON CONFLICT (account_id, date_of_night, sleep_period) DO UPDATE SET
			in_bed_at = EXCLUDED.in_bed_at, sleep_at = EXCLUDED.sleep_at,
			wake_up_at = EXCLUDED.wake_up_at, out_of_bed_at = EXCLUDED.out_of_bed_at,
			algorithm = EXCLUDED.algorithm, offset_ms = EXCLUDED.offset_ms,
			segments = EXCLUDED.segments, conditions = EXCLUDED.conditions,
			feedback_applied_at = EXCLUDED.feedback_applied_at,
			updated_at = now()`,
		accountID, date, r.InBed, r.Sleep, r.WakeUp, r.OutOfBed, r.Algorithm, offsetMS,
		segments, conditions); err != nil {
		return fmt.Errorf("store: save timeline events: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO sleep_stats (account_id, date_of_night, sleep_score,
			sleep_duration_mins, sound_sleep_mins, light_sleep_mins,
			medium_sleep_mins, times_awake, sleep_onset_mins, uninterrupted_mins)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (account_id, date_of_night) DO UPDATE SET
			sleep_score = EXCLUDED.sleep_score,
			sleep_duration_mins = EXCLUDED.sleep_duration_mins,
			sound_sleep_mins = EXCLUDED.sound_sleep_mins,
			light_sleep_mins = EXCLUDED.light_sleep_mins,
			medium_sleep_mins = EXCLUDED.medium_sleep_mins,
			times_awake = EXCLUDED.times_awake,
			sleep_onset_mins = EXCLUDED.sleep_onset_mins,
			uninterrupted_mins = EXCLUDED.uninterrupted_mins,
			updated_at = now()`,
		accountID, date, r.SleepScore, r.SleepDurationMins, r.SoundSleepMins,
		r.LightSleepMins, r.MediumSleepMins, r.TimesAwake, r.SleepOnsetMins,
		r.UninterruptedMins); err != nil {
		return fmt.Errorf("store: save sleep stats: %w", err)
	}

	return tx.Commit(ctx)
}
