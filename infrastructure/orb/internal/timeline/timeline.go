// Package timeline defines the boundary between orb and the sleep algorithms.
//
// The algorithms stay in Java. That is a deliberate decision, not an unfinished
// port: ONLINE_HMM, VOTING and the feature extraction layer are subtle in ways
// that are invisible until they are wrong, and the only ground truth available
// is how a person felt that morning. On 2026-08-13 a single one-sided feedback
// correction silently collapsed the SLEEP model into an all-zero path, and the
// only symptom was MISSING_KEY_EVENTS on a night that otherwise looked normal.
// Reimplementing Baum-Welch and the binning in Go means recreating behaviour
// nobody can verify. See knowledgebase/TIMELINE-ALGORITHMS.md.
//
// So this package describes what orb sends and what it expects back, and
// nothing about how the answer is computed. The Java side becomes one stateless
// service: samples in, events out, models on a mounted volume. Eleven JVMs
// become one.
package timeline

import (
	"context"
	"time"
)

// Event types, matching Event.Type in suripu-core. The numbers are on the wire
// and in timeline_feedback.event_type, so they are not ours to renumber.
const (
	EventInBed    = 11
	EventSleep    = 12
	EventOutOfBed = 13
	EventWakeUp   = 14
)

// Algorithm computes a night's events.
//
// Implementations must be stateless with respect to a request: everything
// needed is in Request, and anything learned comes back in Result. That is what
// lets the Java side hold no database connection, which is the whole point of
// stripping it down.
type Algorithm interface {
	Timeline(ctx context.Context, req Request) (Result, error)
}

// Request is one night, ready to score.
type Request struct {
	AccountID int64     `json:"account_id"`
	Date      string    `json:"date"`      // YYYY-MM-DD, the night's date
	OffsetMS  int32     `json:"offset_ms"` // in force at the night's start
	Start     time.Time `json:"start"`     // window, UTC
	End       time.Time `json:"end"`

	Sensors  []Sensor   `json:"sensors"`
	Motion   []Motion   `json:"motion"`
	Feedback []Feedback `json:"feedback"`

	// PartnerAccountID and PartnerMotion are the bed partner's pill samples
	// over the same window, empty when the account has no partner. The far
	// side uses them for partner-motion events on the timeline; the
	// reference's partner filters, which rewrite Motion, are not applied.
	PartnerAccountID int64    `json:"partner_account_id,omitempty"`
	PartnerMotion    []Motion `json:"partner_motion"`

	// StoredEvents are the four main events of the night's last stored
	// timeline, nil when the night has never been scored. The far side falls
	// back to them only when every algorithm fails on the night, so a
	// correction can still be applied and drawn instead of being stored,
	// acknowledged and never shown. See Server.timeline in orb-algo.
	StoredEvents *StoredEvents `json:"stored_events,omitempty"`

	// Age in whole years at the night's date, for the sleep duration score.
	// Zero means unknown; the score reads it as an adult, which is what the
	// reference does for an account with no birthdate.
	Age int32 `json:"age,omitempty"`

	// DustOffset is the Sense's factory dust calibration, nil when the device
	// has never been calibrated.
	//
	// Sent because the algorithm service has no database and the timeline's air
	// quality condition is wrong without it. Omitting it made the timeline read
	// this device's dust about 213 counts high, which showed up as a
	// particulates condition of WARNING where the reference said IDEAL, and
	// through the environment score as a night one point lower. Invisible until
	// the `calibration` feature flag was turned on, because before that the
	// reference had no offset to apply either.
	//
	// A POINTER, not a zero default: an offset of zero derives a delta of +300
	// and is a completely different thing from no calibration at all.
	DustOffset *int32 `json:"dust_offset,omitempty"`

	// HardwareVersion of the Sense: 1 (or 0, unknown) for the original, 4 for
	// the 1.5. Selects the far side's conversion formulas.
	HardwareVersion int32 `json:"hardware_version,omitempty"`

	// PriorModel and Scratchpad carry the account's learned ONLINE_HMM state.
	// They are opaque protobuf blobs: orb stores them but never interprets
	// them, because their format belongs to the algorithm. Empty means "use the
	// default ensemble", which is the correct starting point for a new account
	// and the way to recover from a collapsed model.
	PriorModel []byte `json:"prior_model,omitempty"`
	Scratchpad []byte `json:"scratchpad,omitempty"`
}

// StoredEvents is a night's four main events as last stored.
type StoredEvents struct {
	InBed    time.Time `json:"in_bed"`
	Sleep    time.Time `json:"sleep"`
	WakeUp   time.Time `json:"wake_up"`
	OutOfBed time.Time `json:"out_of_bed"`
}

type Sensor struct {
	TS                     time.Time `json:"ts"`
	Temperature            *int32    `json:"temperature,omitempty"`
	Humidity               *int32    `json:"humidity,omitempty"`
	Light                  *int32    `json:"light,omitempty"`
	LightVariance          *int32    `json:"light_variance,omitempty"`
	AirQualityRaw          *int32    `json:"air_quality_raw,omitempty"`
	AudioPeakBackgroundDB  *int32    `json:"audio_peak_background_db,omitempty"`
	AudioPeakEnergyDB      *int32    `json:"audio_peak_energy_db,omitempty"`
	AudioPeakDisturbanceDB *int32    `json:"audio_peak_disturbances_db,omitempty"`
	AudioNumDisturbances   *int32    `json:"audio_num_disturbances,omitempty"`
	WaveCount              *int32    `json:"wave_count,omitempty"`
	HoldCount              *int32    `json:"hold_count,omitempty"`

	// Sense 1.5 extras, absent on a 1.0. See store.SensorSample.
	Pressure *int32 `json:"pressure,omitempty"`
	TVOC     *int32 `json:"tvoc,omitempty"`
	CO2      *int32 `json:"co2,omitempty"`
	IR       *int32 `json:"ir,omitempty"`
	Clear    *int32 `json:"clear,omitempty"`
	LuxCount *int32 `json:"lux_count,omitempty"`
	UVCount  *int32 `json:"uv_count,omitempty"`
}

type Motion struct {
	TS             time.Time `json:"ts"`
	SVMNoGravity   *int64    `json:"svm_no_gravity,omitempty"`
	MotionRange    *int64    `json:"motion_range,omitempty"`
	KickoffCounts  *int32    `json:"kickoff_counts,omitempty"`
	OnDurationSecs *int32    `json:"on_duration_secs,omitempty"`
}

// Feedback is a human correction. Both ends of a pair matter: correcting only
// SLEEP without WAKE_UP teaches the model a one-sided path and can collapse it.
type Feedback struct {
	EventType int32  `json:"event_type"`
	OldTime   string `json:"old_time"` // HH:MM local
	NewTime   string `json:"new_time"`

	// CreatedAt is when the correction was made, in UTC. The algorithm keeps
	// only feedback created inside the night's window, so omitting this makes
	// learning silently do nothing.
	CreatedAt time.Time `json:"created_at"`
}

// Result is what the algorithm decided.
type Result struct {
	// Algorithm names which one produced this: ONLINE_HMM, VOTING, HMM.
	// Recorded because the chain falls through, and knowing a night was scored
	// by the fallback is the difference between "the model works" and "the
	// model has been silently dead for a week".
	Algorithm string `json:"algorithm"`

	// Status is NO_ERROR, MISSING_KEY_EVENTS, or similar. A result can be
	// present and still not usable.
	Status string `json:"status"`

	InBed    *time.Time `json:"in_bed,omitempty"`
	Sleep    *time.Time `json:"sleep,omitempty"`
	WakeUp   *time.Time `json:"wake_up,omitempty"`
	OutOfBed *time.Time `json:"out_of_bed,omitempty"`

	SleepScore        *int32 `json:"sleep_score,omitempty"`
	SleepDurationMins *int32 `json:"sleep_duration_mins,omitempty"`
	SoundSleepMins    *int32 `json:"sound_sleep_mins,omitempty"`
	LightSleepMins    *int32 `json:"light_sleep_mins,omitempty"`
	MediumSleepMins   *int32 `json:"medium_sleep_mins,omitempty"`
	TimesAwake        *int32 `json:"times_awake,omitempty"`
	SleepOnsetMins    *int32 `json:"sleep_onset_mins,omitempty"`

	UninterruptedMins *int32 `json:"uninterrupted_mins,omitempty"`

	// Segments is the night minute by minute: sleep depth, and where the
	// notable events fall. Everything here is derived from the samples, which
	// is why it is computed on the Java side; turning it into what the app
	// renders (merged rows, English messages, condition bands) is this side's
	// job. See the DECISION section of knowledgebase/CONSOLIDATION-PLAN.md.
	Segments []Segment `json:"segments,omitempty"`

	// Conditions is one entry per sensor that had usable samples over the sleep
	// period. The app draws each as a coloured dot with no number, so only the
	// verdict crosses: the averages behind it stay on the Java side.
	Conditions []Condition `json:"conditions,omitempty"`

	// UpdatedModel and UpdatedScratchpad come back when the algorithm learned
	// from feedback. orb persists them verbatim for the next run.
	UpdatedModel      []byte `json:"updated_model,omitempty"`
	UpdatedScratchpad []byte `json:"updated_scratchpad,omitempty"`
}

// Segment is one row of the night as the algorithms produce it.
//
// Type is suripu's Event.Type name (SLEEPING, LIGHTS_OUT, MOTION, IN_BED...),
// not the app's event_type. The mapping between them is a presentation
// decision and lives with the endpoint.
type Segment struct {
	TS             time.Time `json:"ts"`
	DurationMillis int64     `json:"duration_millis"`
	Type           string    `json:"type"`
	SleepDepth     int32     `json:"sleep_depth"`
	OffsetMS       int32     `json:"offset_ms"`

	// NIGHT on a named event, NONE on a merged band. Carried rather than
	// derived because it is the segment's own field and the two do not follow
	// from each other.
	SleepPeriod string `json:"sleep_period"`
}

// Condition is one sensor's verdict over the sleep period.
//
// Sensor is suripu's own name (temperature, humidity, particulates, light,
// sound) and Condition is IDEAL, WARNING, ALERT or UNKNOWN. Both are the
// vendor's spelling; the app happens to use the same words, which is a
// coincidence worth not relying on.
type Condition struct {
	Sensor    string `json:"sensor"`
	Condition string `json:"condition"`
}

// Usable reports whether a result should be stored as a night's timeline.
//
// MISSING_KEY_EVENTS is the common not-usable case: ONLINE_HMM requires all
// four events and returns nothing rather than a partial answer, which makes it
// strictly more fragile than VOTING. Storing a partial timeline would show the
// user a night with a bedtime and no sleep.
func (r Result) Usable() bool {
	return r.Status == "NO_ERROR" &&
		r.InBed != nil && r.Sleep != nil && r.WakeUp != nil && r.OutOfBed != nil
}
