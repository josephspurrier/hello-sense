package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/josephspurrier/hello-orb/orb/internal/alarm"
	"github.com/josephspurrier/hello-orb/orb/internal/ota"
	"github.com/josephspurrier/hello-orb/orb/internal/timeline"
)

// ErrNoToken means the bearer token is unknown or has expired. The two are
// deliberately one error: distinguishing them tells a caller whether a token
// ever existed.
var ErrNoToken = errors.New("store: no such token")

// AccountByToken resolves a token to an account.
//
// appID is matched as well as the token. Both halves of the credential the
// client presented have to be checked, or the app id is decoration and a token
// issued to one application authenticates as another.
//
// Expiry is checked in the query rather than in Go so there is no window where
// a token is read as live and used a moment after it lapsed, and so the two
// clocks involved stay one clock: `expires_at` was written by Postgres and is
// compared against Postgres's now().
func (s *Store) AccountByToken(ctx context.Context, appID int64, token string) (int64, error) {
	var accountID int64
	err := s.pool.QueryRow(ctx, `
		SELECT account_id FROM oauth_tokens
		WHERE access_token = $1 AND app_id = $2 AND expires_at > now()`,
		token, appID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoToken
	}
	if err != nil {
		return 0, fmt.Errorf("store: account by token: %w", err)
	}
	return accountID, nil
}

// TimezoneAt returns the zone in force for an account at an instant.
//
// Separate from OffsetMSAt, which answers only the offset and reports whether
// it found a row. The app shows the zone name, and deriving one from an offset
// is not possible: -14400000 is America/New_York in summer and America/Halifax
// all year.
func (s *Store) TimezoneAt(ctx context.Context, accountID int64, at time.Time) (offsetMS int32, zone string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT offset_ms, timezone_id FROM timezone_history
		WHERE account_id = $1 AND effective_from <= $2
		ORDER BY effective_from DESC LIMIT 1`, accountID, at).Scan(&offsetMS, &zone)
	if errors.Is(err, pgx.ErrNoRows) {
		// Fall back to the account's stored offset. An account always has one,
		// and returning an error here would break a screen over a missing
		// history row rather than over missing data.
		err = s.pool.QueryRow(ctx,
			`SELECT tz_offset_ms FROM accounts WHERE id = $1`, accountID).Scan(&offsetMS)
		if err != nil {
			return 0, "", fmt.Errorf("store: timezone %d: %w", accountID, err)
		}
		return offsetMS, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("store: timezone %d: %w", accountID, err)
	}
	return offsetMS, zone, nil
}

// SenseRow and PillRow are the device rows the app renders.
type SenseRow struct {
	DeviceID        string
	FirmwareVersion int32
	HWVersion       *string
	LastSeenAt      time.Time
	WiFiSSID        *string
	WiFiRSSI        *int32
	WiFiUpdatedAt   *time.Time
}

type PillRow struct {
	PillID          string
	FirmwareVersion int32
	BatteryLevel    int32
	LastSeenAt      time.Time
}

// DevicesFor returns the account's paired devices.
//
// Only active pairings. A Sense that was unpaired still has rows in `senses`
// because the mirror is append-only, and returning it would show a stranger's
// former device on this account's settings screen.
func (s *Store) DevicesFor(ctx context.Context, accountID int64) ([]SenseRow, []PillRow, error) {
	senseRows, err := s.pool.Query(ctx, `
		SELECT s.device_id, COALESCE(s.firmware_version, 0), s.hw_version,
		       COALESCE(s.last_seen_at, s.created_at), s.wifi_ssid, s.wifi_rssi,
		       s.wifi_updated_at
		FROM senses s
		JOIN account_senses a ON a.device_id = s.device_id
		WHERE a.account_id = $1 AND a.active
		ORDER BY s.device_id`, accountID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: senses for %d: %w", accountID, err)
	}
	var senses []SenseRow
	for senseRows.Next() {
		var r SenseRow
		if err := senseRows.Scan(&r.DeviceID, &r.FirmwareVersion, &r.HWVersion,
			&r.LastSeenAt, &r.WiFiSSID, &r.WiFiRSSI, &r.WiFiUpdatedAt); err != nil {
			senseRows.Close()
			return nil, nil, err
		}
		senses = append(senses, r)
	}
	senseRows.Close()
	if err := senseRows.Err(); err != nil {
		return nil, nil, err
	}

	pillRows, err := s.pool.Query(ctx, `
		SELECT p.pill_id, COALESCE(p.firmware_version, 0), COALESCE(p.battery_level, 0),
		       COALESCE(p.last_seen_at, p.created_at)
		FROM pills p
		JOIN account_pills a ON a.pill_id = p.pill_id
		WHERE a.account_id = $1 AND a.active
		ORDER BY p.pill_id`, accountID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: pills for %d: %w", accountID, err)
	}
	defer pillRows.Close()
	var pills []PillRow
	for pillRows.Next() {
		var r PillRow
		if err := pillRows.Scan(&r.PillID, &r.FirmwareVersion, &r.BatteryLevel, &r.LastSeenAt); err != nil {
			return nil, nil, err
		}
		pills = append(pills, r)
	}
	return senses, pills, pillRows.Err()
}

// HasUnreadInsights reports whether any insight postdates the last time the
// account opened the insights screen.
func (s *Store) HasUnreadInsights(ctx context.Context, accountID int64) (bool, error) {
	// Against app_stats.insights_last_viewed, NOT against the `seen` column.
	//
	// An earlier version used `NOT seen`, which is the obvious reading and gave
	// the right answer only because the table was empty. The reference asks
	// whether the newest insight postdates the last time the screen was opened,
	// and crucially returns FALSE when it has never been opened: the flag drives
	// a badge, and badging an account that has never looked at the feature is
	// not what "unread" is for. `seen` is never written by anything here, so the
	// old query said true for every account the moment insights arrived.
	//
	// The INNER JOIN is what encodes "never opened means false": no app_stats
	// row, or a null column, yields no rows and EXISTS is false.
	var has bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM insights i
			JOIN app_stats a ON a.account_id = i.account_id
			WHERE i.account_id = $1
			  AND a.insights_last_viewed IS NOT NULL
			  AND i.timestamp > a.insights_last_viewed)`, accountID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("store: unread insights %d: %w", accountID, err)
	}
	return has, nil
}

// LatestSampleRow is the most recent sensor reading, in its stored form.
//
// Raw as the Sense sent it: the calibration into degrees, percent, lux and
// decibels belongs with the endpoint that renders them, because two endpoints
// calibrate the same columns differently.
type LatestSampleRow struct {
	TS          time.Time
	Temperature int32
	Humidity    int32
	Light       int32
	// Both audio columns, because the sensors endpoint prefers energy and falls
	// back to disturbances when energy is zero. Loading only the column it
	// usually wants would render a silent minute from nothing.
	AudioPeakEnergyDB       int32
	AudioPeakDisturbancesDB int32
	// AirQualityRaw is the dust sensor's raw count, and DustOffset is the
	// device's calibration delta, which is zero when uncalibrated.
	AirQualityRaw int32
	// DustOffset is nil when the device has never been calibrated, which is NOT
	// the same as an offset of zero: zero derives a delta of +300, while no
	// calibration means no delta at all.
	DustOffset *int32
}

// LatestSample returns the newest sample from any Sense paired to the account,
// or nil when there is none.
//
// Joined through the pairing rather than read by device id, so a Sense that was
// unpaired stops answering for this account immediately. Its rows stay in the
// table, which is the point of the mirror being append-only.
func (s *Store) LatestSample(ctx context.Context, accountID int64) (*LatestSampleRow, error) {
	var r LatestSampleRow
	err := s.pool.QueryRow(ctx, `
		SELECT ss.ts, COALESCE(ss.temperature,0), COALESCE(ss.humidity,0),
		       COALESCE(ss.light,0), COALESCE(ss.audio_peak_energy_db,0),
		       COALESCE(ss.audio_peak_disturbances_db,0),
		       COALESCE(ss.air_quality_raw,0), s.dust_offset
		FROM sensor_samples ss
		JOIN account_senses a ON a.device_id = ss.device_id
		JOIN senses s ON s.device_id = ss.device_id
		WHERE a.account_id = $1 AND a.active
		ORDER BY ss.ts DESC
		LIMIT 1`, accountID).Scan(&r.TS, &r.Temperature, &r.Humidity, &r.Light,
		&r.AudioPeakEnergyDB, &r.AudioPeakDisturbancesDB,
		&r.AirQualityRaw, &r.DustOffset)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest sample %d: %w", accountID, err)
	}
	return &r, nil
}

// InsightRow is one insight card, joined to its category's display name.
type InsightRow struct {
	UUID         string
	Category     string
	CategoryName string
	Title        string
	Message      string
	InsightType  string
	Timestamp    time.Time
}

// InsightsFor returns an account's insight cards, newest first.
//
// LEFT JOIN on the category name, and COALESCE to empty: a card whose category
// has no row still has to render. The reference does the same, defaulting to ""
// rather than dropping the card, because the name is a label on a card that
// otherwise carries a real message.
func (s *Store) InsightsFor(ctx context.Context, accountID int64, limit int) ([]InsightRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.uuid, i.category, COALESCE(c.category_name, ''),
		       COALESCE(i.title, ''), COALESCE(i.message, ''),
		       COALESCE(i.insight_type, ''), i.timestamp
		FROM insights i
		LEFT JOIN insight_categories c ON c.category = i.category
		WHERE i.account_id = $1
		ORDER BY i.timestamp DESC
		LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: insights %d: %w", accountID, err)
	}
	defer rows.Close()

	var out []InsightRow
	for rows.Next() {
		var r InsightRow
		if err := rows.Scan(&r.UUID, &r.Category, &r.CategoryName, &r.Title,
			&r.Message, &r.InsightType, &r.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NightTimeline is a scored night, ready to render.
type NightTimeline struct {
	Segments        []timeline.Segment
	Conditions      []timeline.Condition
	Score           *int32
	TotalSleepMins  *int64
	SoundSleepMins  *int64
	TimeToSleepMins *int64
	TimesAwake      *int64

	// What the summary sentence reports as "sleeping soundly", which is not
	// sound sleep. See migration 0005.
	UninterruptedMins *int64

	// Epoch millis, because the app's fell_asleep and woke_up metrics carry a
	// timestamp in the same `value` field the minute counts use.
	SleepAt  *int64
	WakeUpAt *int64
}

// TimelineFor returns a night's stored timeline, or nil when it has not been
// scored.
//
// nil rather than an error: an unscored night is a normal state, not a failure,
// and the endpoint renders it as an empty timeline.
func (s *Store) TimelineFor(ctx context.Context, accountID int64, date time.Time) (*NightTimeline, error) {
	var (
		out        NightTimeline
		segments   []byte
		conditions []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT e.segments, e.conditions, s.sleep_score, s.sleep_duration_mins,
		       s.sound_sleep_mins, s.sleep_onset_mins, s.times_awake,
		       s.uninterrupted_mins,
		       (EXTRACT(EPOCH FROM e.sleep_at) * 1000)::bigint,
		       (EXTRACT(EPOCH FROM e.wake_up_at) * 1000)::bigint
		FROM timeline_events e
		LEFT JOIN sleep_stats s
		       ON s.account_id = e.account_id AND s.date_of_night = e.date_of_night
		WHERE e.account_id = $1 AND e.date_of_night = $2`,
		accountID, date).Scan(&segments, &conditions, &out.Score, &out.TotalSleepMins,
		&out.SoundSleepMins, &out.TimeToSleepMins, &out.TimesAwake,
		&out.UninterruptedMins, &out.SleepAt, &out.WakeUpAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: timeline %d %s: %w", accountID, date.Format("2006-01-02"), err)
	}
	if len(segments) > 0 {
		if err := json.Unmarshal(segments, &out.Segments); err != nil {
			return nil, fmt.Errorf("store: timeline segments: %w", err)
		}
	}
	if len(conditions) > 0 {
		if err := json.Unmarshal(conditions, &out.Conditions); err != nil {
			return nil, fmt.Errorf("store: timeline conditions: %w", err)
		}
	}
	return &out, nil
}

// PreferencesFor returns only the account's explicit overrides. Defaults live
// in the api package; see the comment on defaultPreferences for why they are
// not rows.
func (s *Store) PreferencesFor(ctx context.Context, accountID int64) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, enabled FROM preferences WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: preferences %d: %w", accountID, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		var v bool
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// AlarmsFor returns the account's alarm definitions, newest first.
func (s *Store) AlarmsFor(ctx context.Context, accountID int64) ([][]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT definition FROM alarms
		WHERE account_id = $1 AND definition IS NOT NULL
		ORDER BY hour, minute, id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: alarms for %d: %w", accountID, err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var def []byte
		if err := rows.Scan(&def); err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, rows.Err()
}

// AccountRow is one row of the accounts table.
type AccountRow struct {
	ID           int64
	ExternalID   string
	Email        string
	Name         string
	FirstName    *string
	LastName     *string
	Gender       string
	GenderOther  string
	HeightCM     *int32
	WeightGrams  *int32
	Birthdate    *time.Time
	TZOffsetMS   int32
	CreatedAt    time.Time
	LastModified time.Time
}

// Account reads one account. password_hash is deliberately not selected: this
// row is rendered into a JSON response, and a field that is never loaded cannot
// be accidentally serialised.
func (s *Store) Account(ctx context.Context, accountID int64) (AccountRow, error) {
	var a AccountRow
	// COALESCE on the text columns because the app renders them and a null
	// arrives as the string "null" in some of its labels; the reference sends
	// "" for gender_other specifically.
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(external_id::text, ''), email, name,
		       firstname, lastname, COALESCE(gender, ''), COALESCE(gender_other, ''),
		       height_cm, weight_grams, birthdate,
		       tz_offset_ms, created_at, last_modified
		FROM accounts WHERE id = $1`, accountID).Scan(
		&a.ID, &a.ExternalID, &a.Email, &a.Name,
		&a.FirstName, &a.LastName, &a.Gender, &a.GenderOther,
		&a.HeightCM, &a.WeightGrams, &a.Birthdate,
		&a.TZOffsetMS, &a.CreatedAt, &a.LastModified)
	if err != nil {
		return a, fmt.Errorf("store: account %d: %w", accountID, err)
	}
	return a, nil
}

// TrendsStatRow is one night's aggregate, as the trends screen consumes it.
//
// Date is the night's local date, not an instant. Trends is entirely a
// calendar: which day of the week a night fell on decides where it is drawn and
// whether it counts as a weekday, so carrying a timestamp here would invite a
// zone conversion that moves a night into the wrong column.
type TrendsStatRow struct {
	Date         time.Time
	DurationMins int32
	LightMins    int32
	MediumMins   int32
	SoundMins    int32
	Score        int32
}

// TrendsStats returns the nights in a local date range, oldest first.
//
// Both bounds are inclusive, matching the reference's batch query over ymd
// strings. Nights with under a minute of sleep are dropped here rather than in
// the handler, because they must not count toward the averages or the min/max
// highlights either, and a filter applied in one of the three places and not
// the others is the kind of thing that shows up as one wrong bar.
func (s *Store) TrendsStats(ctx context.Context, accountID int64, from, to time.Time) ([]TrendsStatRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date_of_night,
		       COALESCE(sleep_duration_mins, 0), COALESCE(light_sleep_mins, 0),
		       COALESCE(medium_sleep_mins, 0), COALESCE(sound_sleep_mins, 0),
		       COALESCE(sleep_score, 0)
		FROM sleep_stats
		WHERE account_id = $1
		  AND date_of_night >= $2 AND date_of_night <= $3
		  AND COALESCE(sleep_duration_mins, 0) >= 1
		ORDER BY date_of_night`, accountID, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: trends stats %d: %w", accountID, err)
	}
	defer rows.Close()

	var out []TrendsStatRow
	for rows.Next() {
		var r TrendsStatRow
		if err := rows.Scan(&r.Date, &r.DurationMins, &r.LightMins,
			&r.MediumMins, &r.SoundMins, &r.Score); err != nil {
			return nil, fmt.Errorf("store: trends stats %d: %w", accountID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FeedbackRow is one stored correction for a night.
type FeedbackRow struct {
	EventType int32
	OldTime   string
	NewTime   string
	IsCorrect bool
}

// FeedbackForNight returns the corrections already stored for a night.
//
// Needed before storing another one: the ordering rule compares a proposed
// correction against the ones already there, so this is read inside the same
// request that writes.
func (s *Store) FeedbackForNight(ctx context.Context, accountID int64, date time.Time) ([]FeedbackRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_type, to_char(old_time, 'HH24:MI'), to_char(new_time, 'HH24:MI'),
		       is_correct
		FROM timeline_feedback
		WHERE account_id = $1 AND date_of_night = $2
		ORDER BY created_at`, accountID, date)
	if err != nil {
		return nil, fmt.Errorf("store: feedback for night: %w", err)
	}
	defer rows.Close()

	var out []FeedbackRow
	for rows.Next() {
		var f FeedbackRow
		if err := rows.Scan(&f.EventType, &f.OldTime, &f.NewTime, &f.IsCorrect); err != nil {
			return nil, fmt.Errorf("store: feedback for night: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertFeedback stores a correction.
//
// Append-only: a second correction to the same event does not replace the
// first. The algorithm wants the history, and overwriting would silently
// discard the evidence that somebody changed their mind, which is itself a
// signal. created_at defaults to now() in the database rather than being passed
// in, so the timestamp comes from the same clock the read path compares it
// against.
func (s *Store) InsertFeedback(ctx context.Context, accountID int64, date time.Time,
	eventType int32, oldTime, newTime string, isCorrect bool) error {

	_, err := s.pool.Exec(ctx, `
		INSERT INTO timeline_feedback
			(account_id, date_of_night, event_type, old_time, new_time, is_correct)
		VALUES ($1, $2, $3, $4::time, $5::time, $6)`,
		accountID, date, eventType, oldTime, newTime, isCorrect)
	if err != nil {
		return fmt.Errorf("store: insert feedback: %w", err)
	}
	return nil
}

// OffsetForNight returns the UTC offset a night was slept in.
//
// The night's own offset, not the account's current one. Corrections carry
// local wall-clock times, so reading them in the wrong zone moves every one of
// them by the difference: a bedtime corrected after flying east comes back an
// hour out, and the resulting feedback teaches the model the wrong time.
//
// Prefers the offset stored with the night's own stats, because that is what
// the timeline was rendered with and therefore what the app was showing when
// the user tapped. Falls back to the timezone history, then to the account's
// stored offset, which always exists.
func (s *Store) OffsetForNight(ctx context.Context, accountID int64, date time.Time) (int32, error) {
	var offset int32
	err := s.pool.QueryRow(ctx, `
		SELECT offset_ms FROM timeline_events
		WHERE account_id = $1 AND date_of_night = $2
		LIMIT 1`, accountID, date).Scan(&offset)
	if err == nil {
		return offset, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("store: offset for night: %w", err)
	}

	offset, _, err = s.OffsetMSAt(ctx, accountID, date)
	if err != nil {
		return 0, err
	}
	if offset != 0 {
		return offset, nil
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT tz_offset_ms FROM accounts WHERE id = $1`, accountID).Scan(&offset); err != nil {
		return 0, fmt.Errorf("store: offset for night: %w", err)
	}
	return offset, nil
}

// SamplesBetween returns the account's sensor samples in a time window, oldest
// first.
//
// Reuses LatestSampleRow because the graph endpoint wants exactly the columns
// the dial endpoint wants, just many of them. Both audio columns come along for
// the same reason as there: the caller picks between them per slot.
func (s *Store) SamplesBetween(ctx context.Context, accountID int64, start, end time.Time) ([]LatestSampleRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ss.ts, COALESCE(ss.temperature,0), COALESCE(ss.humidity,0),
		       COALESCE(ss.light,0), COALESCE(ss.audio_peak_energy_db,0),
		       COALESCE(ss.audio_peak_disturbances_db,0),
		       COALESCE(ss.air_quality_raw,0), s.dust_offset
		FROM sensor_samples ss
		JOIN account_senses a ON a.device_id = ss.device_id
		JOIN senses s ON s.device_id = ss.device_id
		WHERE a.account_id = $1 AND a.active AND ss.ts >= $2 AND ss.ts <= $3
		ORDER BY ss.ts`, accountID, start, end)
	if err != nil {
		return nil, fmt.Errorf("store: samples between: %w", err)
	}
	defer rows.Close()

	var out []LatestSampleRow
	for rows.Next() {
		var r LatestSampleRow
		if err := rows.Scan(&r.TS, &r.Temperature, &r.Humidity, &r.Light,
			&r.AudioPeakEnergyDB, &r.AudioPeakDisturbancesDB,
			&r.AirQualityRaw, &r.DustOffset); err != nil {
			return nil, fmt.Errorf("store: samples between: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutAppStatsViewed records that the account opened the insights or questions
// screen.
//
// Upsert, and each column is written only when a time was supplied: the app
// sends one field or the other, never both, and writing a null for the absent
// one would clear the other screen's badge as a side effect of opening this
// one. That is the whole bug this signature exists to prevent, which is why the
// arguments are pointers rather than zero times.
func (s *Store) PutAppStatsViewed(ctx context.Context, accountID int64, insights, questions *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_stats (account_id, insights_last_viewed, questions_last_viewed)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id) DO UPDATE SET
			insights_last_viewed = COALESCE($2, app_stats.insights_last_viewed),
			questions_last_viewed = COALESCE($3, app_stats.questions_last_viewed),
			updated_at = now()`, accountID, insights, questions)
	if err != nil {
		return fmt.Errorf("store: put app stats viewed: %w", err)
	}
	return nil
}

// HasActiveSense reports whether the account has a paired Sense.
//
// The timezone write refuses without one. That looks like a technicality and is
// not: the zone is what the Sense's alarms are scheduled in, so accepting a
// zone for an account with no device stores a preference nothing will ever read
// and quietly implies the alarm moved.
func (s *Store) HasActiveSense(ctx context.Context, accountID int64) (bool, error) {
	var has bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM account_senses WHERE account_id = $1 AND active)`,
		accountID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("store: has active sense: %w", err)
	}
	return has, nil
}

// PutTimezone records a zone change from now.
//
// timezone_history is append-only and keyed by the instant the zone took
// effect, because every sample and every night is rendered with the offset in
// force at the time. Updating a single current-zone column instead would move
// last month's nights when somebody flies.
func (s *Store) PutTimezone(ctx context.Context, accountID int64, zoneID string, offsetMS int32) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO timezone_history (account_id, effective_from, timezone_id, offset_ms)
		VALUES ($1, now(), $2, $3)
		ON CONFLICT (account_id, effective_from) DO UPDATE
			SET timezone_id = $2, offset_ms = $3`, accountID, zoneID, offsetMS)
	if err != nil {
		return fmt.Errorf("store: put timezone: %w", err)
	}
	// The account's own offset is the fallback when no history row applies, so
	// it moves too; leaving it stale makes the fallback answer with the zone the
	// user left.
	if _, err := s.pool.Exec(ctx,
		`UPDATE accounts SET tz_offset_ms = $2 WHERE id = $1`, accountID, offsetMS); err != nil {
		return fmt.Errorf("store: put timezone offset: %w", err)
	}
	return nil
}

// AlarmDef is one alarm as the app sent it, plus the fields the worker queries.
type AlarmDef struct {
	Enabled    bool
	Smart      bool
	Repeated   bool
	Hour       int32
	Minute     int32
	DayOfWeek  []int32
	SoundID    *int32
	Definition []byte
}

// ReplaceAlarms swaps the account's whole alarm set in one transaction.
//
// Replace, not merge: the app owns the set and PUTs it entire, so an alarm the
// app did not send is one the user deleted. Doing this as delete-then-insert
// inside a transaction is what stops a failed insert from leaving the account
// with no alarms at all, which would silently stop somebody's morning.
func (s *Store) ReplaceAlarms(ctx context.Context, accountID int64, deviceID string, alarms []AlarmDef) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: replace alarms: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM alarms WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("store: replace alarms: %w", err)
	}
	for _, a := range alarms {
		if _, err := tx.Exec(ctx, `
			INSERT INTO alarms (account_id, device_id, enabled, smart, repeated,
			                    hour, minute, day_of_week, sound_id, definition)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			accountID, deviceID, a.Enabled, a.Smart, a.Repeated,
			a.Hour, a.Minute, a.DayOfWeek, a.SoundID, a.Definition); err != nil {
			return fmt.Errorf("store: replace alarms: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ActiveSenseID returns the account's paired Sense, or "" when there is none.
func (s *Store) ActiveSenseID(ctx context.Context, accountID int64) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT device_id FROM account_senses
		WHERE account_id = $1 AND active
		ORDER BY device_id LIMIT 1`, accountID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: active sense: %w", err)
	}
	return id, nil
}

// ErrStaleAccount means the caller's last_modified did not match the row.
var ErrStaleAccount = errors.New("store: account modified since read")

// AccountUpdate is the mutable half of an account. Everything here is something
// the profile screen can change; email and password have their own endpoints.
type AccountUpdate struct {
	Name        string
	FirstName   *string
	LastName    *string
	Gender      string
	GenderOther string
	HeightCM    *int32
	WeightGrams *int32
	Birthdate   *time.Time
	TZOffsetMS  int32
}

// UpdateAccount applies a profile edit, guarded by the caller's last_modified.
//
// The guard is optimistic concurrency and it is the reason this returns an
// error rather than swallowing a no-op: the app sends the whole account object
// it last read, so two phones editing the same profile would otherwise have the
// slower one silently overwrite the faster one's changes with stale values.
// A mismatch is reported so the caller can answer 412 and the app can re-read.
//
// last_modified is compared to the MILLISECOND, matching what the app was given.
// Comparing the timestamptz directly would fail on sub-millisecond digits that
// never left the database.
func (s *Store) UpdateAccount(ctx context.Context, accountID int64, lastModifiedMS int64, u AccountUpdate) (AccountRow, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET
			name = $3, firstname = $4, lastname = $5,
			gender = $6, gender_other = $7,
			height_cm = $8, weight_grams = $9, birthdate = $10,
			tz_offset_ms = $11, last_modified = now()
		WHERE id = $1
		  AND (EXTRACT(EPOCH FROM last_modified) * 1000)::bigint = $2`,
		accountID, lastModifiedMS, u.Name, u.FirstName, u.LastName,
		u.Gender, u.GenderOther, u.HeightCM, u.WeightGrams, u.Birthdate, u.TZOffsetMS)
	if err != nil {
		return AccountRow{}, fmt.Errorf("store: update account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return AccountRow{}, ErrStaleAccount
	}
	return s.Account(ctx, accountID)
}

// OAuthApp is a registered client application.
type OAuthApp struct {
	ID     int64
	Scopes []int32
}

// AppByClientID looks up an application. Returns ok=false rather than an error
// for an unknown client, because an unknown client is a normal 401 and not a
// fault worth logging as one.
func (s *Store) AppByClientID(ctx context.Context, clientID string) (OAuthApp, bool, error) {
	var a OAuthApp
	err := s.pool.QueryRow(ctx,
		`SELECT id, scopes FROM oauth_applications WHERE client_id = $1`,
		clientID).Scan(&a.ID, &a.Scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthApp{}, false, nil
	}
	if err != nil {
		return OAuthApp{}, false, fmt.Errorf("store: app by client id: %w", err)
	}
	return a, true, nil
}

// CredentialsByEmail returns what is needed to check a password.
//
// The hash is returned rather than the comparison being done here, because the
// comparison has to happen even when the account does not exist: see the
// handler.
func (s *Store) CredentialsByEmail(ctx context.Context, email string) (accountID int64, externalID string, hash string, found bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(external_id::text, ''), password_hash
		FROM accounts WHERE lower(email) = $1`, email).Scan(&accountID, &externalID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", "", false, nil
	}
	if err != nil {
		return 0, "", "", false, fmt.Errorf("store: credentials by email: %w", err)
	}
	return accountID, externalID, hash, true, nil
}

// InsertToken stores a freshly minted token pair.
func (s *Store) InsertToken(ctx context.Context, accessToken, refreshToken string,
	accountID, appID int64, scopes []int32, expiresIn time.Duration) error {

	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_tokens
			(access_token, refresh_token, account_id, app_id, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + $6::interval)`,
		accessToken, refreshToken, accountID, appID, scopes,
		fmt.Sprintf("%d seconds", int64(expiresIn.Seconds())))
	if err != nil {
		return fmt.Errorf("store: insert token: %w", err)
	}
	return nil
}

// ErrDuplicateEmail means the address already belongs to an account. Its own
// error rather than a wrapped pg code because both registration and the email
// change answer it with a specific status, and neither should be parsing
// driver errors to find out.
var ErrDuplicateEmail = errors.New("store: email already registered")

// isUniqueViolation reports whether err is Postgres complaining about a
// duplicate key. 23505 is the SQLSTATE for unique_violation, and the accounts
// table's only unique constraints are email and external_id, the latter being
// freshly generated here.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// NewAccount is what registration stores. Name is already resolved: the app
// registers with firstname only, and the reference copies it into the
// not-null name column rather than storing a null.
type NewAccount struct {
	Email        string
	PasswordHash string
	Name         string
	FirstName    *string
	LastName     *string
	Gender       string
	GenderOther  string
	HeightCM     *int32
	WeightGrams  *int32
	Birthdate    *time.Time
	TZOffsetMS   int32
}

// InsertAccount creates an account.
//
// external_id is generated here, in the same statement. New accounts are the
// one case where minting a UUID is correct: the app learns whatever id this
// insert produces, unlike the migrated rows (see 0002_account_identity.sql)
// whose ids the app already held.
func (s *Store) InsertAccount(ctx context.Context, n NewAccount) (AccountRow, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts
			(external_id, email, password_hash, name, firstname, lastname,
			 gender, gender_other, height_cm, weight_grams, birthdate, tz_offset_ms)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		n.Email, n.PasswordHash, n.Name, n.FirstName, n.LastName,
		n.Gender, n.GenderOther, n.HeightCM, n.WeightGrams, n.Birthdate,
		n.TZOffsetMS).Scan(&id)
	if isUniqueViolation(err) {
		return AccountRow{}, ErrDuplicateEmail
	}
	if err != nil {
		return AccountRow{}, fmt.Errorf("store: insert account: %w", err)
	}
	return s.Account(ctx, id)
}

// UpdateEmail changes an account's address, guarded by last_modified exactly
// as UpdateAccount is: the app sends the whole account it last read, and the
// reference's UPDATE carries the same WHERE clause.
func (s *Store) UpdateEmail(ctx context.Context, accountID, lastModifiedMS int64, email string) (AccountRow, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET email = $3, last_modified = now()
		WHERE id = $1
		  AND (EXTRACT(EPOCH FROM last_modified) * 1000)::bigint = $2`,
		accountID, lastModifiedMS, email)
	if isUniqueViolation(err) {
		return AccountRow{}, ErrDuplicateEmail
	}
	if err != nil {
		return AccountRow{}, fmt.Errorf("store: update email: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return AccountRow{}, ErrStaleAccount
	}
	return s.Account(ctx, accountID)
}

// PasswordHash returns the stored bcrypt hash, for the change-password check.
// Separate from CredentialsByEmail because that lookup is by address and this
// caller already holds an authenticated account id.
func (s *Store) PasswordHash(ctx context.Context, accountID int64) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT password_hash FROM accounts WHERE id = $1`, accountID).Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("store: password hash %d: %w", accountID, err)
	}
	return hash, nil
}

// UpdatePassword swaps the hash, guarded by the old one.
//
// The guard mirrors the reference's UPDATE ... WHERE password_hash = :current:
// the caller has already bcrypt-verified the current password, and the WHERE
// clause keeps a concurrent change from being silently overwritten between
// that check and this write. false means the guard failed, not an error.
func (s *Store) UpdatePassword(ctx context.Context, accountID int64, newHash, oldHash string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET password_hash = $2
		WHERE id = $1 AND password_hash = $3`,
		accountID, newHash, oldHash)
	if err != nil {
		return false, fmt.Errorf("store: update password: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DisableToken expires the presented token immediately.
//
// An UPDATE of expires_at rather than a DELETE, matching the reference's
// disable (expires_in=0): the row remains as a record of the session, and
// AccountByToken already refuses anything past its expiry. Only the exact
// token is disabled; the reference leaves the account's other sessions alone.
func (s *Store) DisableToken(ctx context.Context, appID int64, token string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE oauth_tokens SET expires_at = now()
		WHERE access_token = $1 AND app_id = $2`, token, appID)
	if err != nil {
		return fmt.Errorf("store: disable token: %w", err)
	}
	return nil
}

// PendingQuestion is a question put to an account and not yet answered.
type PendingQuestion struct {
	ID                int32
	AccountQuestionID int64
	Text              string
	ResponseType      string
	AskTime           string
	AskLocalDate      time.Time
}

// QuestionChoice is one selectable answer.
type QuestionChoice struct {
	ID         int32
	QuestionID int32
	Text       string
}

// maxQuestionsServed caps how many are put in front of the user at once.
//
// The reference assembles its count from several rules and served five on the
// day this was written. This is a flat cap, which is the honest approximation:
// reproducing the count exactly would mean reproducing the selection logic.
const maxQuestionsServed = 5

// QuestionsFor returns the questions to put to an account, creating new
// askings when there is room.
//
// The generating half is not optional, and discovering that is what stopped
// this being a one-query endpoint. Every asking expires after a day, so an
// account that has been away has nothing live: serving only what is already
// pending returns either an empty list or, worse, dozens of expired rows.
// The reference creates fresh askings on each request, and so does this.
//
// Eligibility, by the question's own frequency:
//
//	one_time      never answered
//	daily         not asked in the last day
//	occasionally  not answered in the last 30 days
//	trigger       NEVER. These are the survey, goal and anomaly categories,
//	              fired by conditions this port does not reproduce. Serving
//	              them on a timer would ask somebody a sleep-therapy goal
//	              question with nothing behind it.
//
// Daily questions sort first, matching what the reference puts at the top.
func (s *Store) QuestionsFor(ctx context.Context, accountID int64, now time.Time) ([]PendingQuestion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: questions: %w", err)
	}
	defer tx.Rollback(ctx)

	// How many live, unanswered askings already exist.
	var live int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM account_questions aq
		WHERE aq.account_id = $1 AND aq.expires_local_utc_ts > $2
		  AND NOT EXISTS (SELECT 1 FROM question_responses r
		                  WHERE r.account_question_id = aq.id)`,
		accountID, now).Scan(&live); err != nil {
		return nil, fmt.Errorf("store: questions: %w", err)
	}

	if room := maxQuestionsServed - live; room > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO account_questions
				(account_id, question_id, created_local_utc_ts, expires_local_utc_ts)
			SELECT $1, q.id, $2::timestamptz, $2::timestamptz + interval '1 day'
			FROM questions q
			WHERE COALESCE(q.frequency, '') IN ('one_time', 'daily', 'occasionally')
			  AND NOT EXISTS (
			      SELECT 1 FROM account_questions aq
			      WHERE aq.account_id = $1 AND aq.question_id = q.id
			        AND aq.expires_local_utc_ts > $2)
			  AND (
			     (q.frequency = 'one_time' AND NOT EXISTS (
			         SELECT 1 FROM question_responses r
			         WHERE r.account_id = $1 AND r.question_id = q.id))
			  OR (q.frequency = 'daily' AND NOT EXISTS (
			         SELECT 1 FROM account_questions aq2
			         WHERE aq2.account_id = $1 AND aq2.question_id = q.id
			           AND aq2.created_local_utc_ts > $2::timestamptz - interval '1 day'))
			  OR (q.frequency = 'occasionally' AND NOT EXISTS (
			         SELECT 1 FROM question_responses r2
			         WHERE r2.account_id = $1 AND r2.question_id = q.id
			           AND r2.created > $2::timestamptz - interval '30 days'))
			  )
			ORDER BY (q.frequency = 'daily') DESC, q.id
			LIMIT $3
			ON CONFLICT (account_id, question_id, created_local_utc_ts) DO NOTHING`,
			accountID, now, room); err != nil {
			return nil, fmt.Errorf("store: questions: %w", err)
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT q.id, aq.id, q.question_text,
		       -- Upper-cased on the way out. The columns hold the reference's
		       -- Postgres enum labels, which are lowercase, and the app reads
		       -- CHOICE and MORNING. Sending "choice" is a wire format the app
		       -- does not parse, and it looks right in a database query.
		       upper(COALESCE(q.response_type, 'CHOICE')),
		       upper(COALESCE(q.ask_time, 'ANYTIME')),
		       aq.created_local_utc_ts
		FROM account_questions aq
		JOIN questions q ON q.id = aq.question_id
		WHERE aq.account_id = $1 AND aq.expires_local_utc_ts > $2
		  AND NOT EXISTS (SELECT 1 FROM question_responses r
		                  WHERE r.account_question_id = aq.id)
		ORDER BY (q.frequency = 'daily') DESC, aq.created_local_utc_ts, aq.id
		LIMIT $3`, accountID, now, maxQuestionsServed)
	if err != nil {
		return nil, fmt.Errorf("store: questions: %w", err)
	}
	var out []PendingQuestion
	for rows.Next() {
		var q PendingQuestion
		if err := rows.Scan(&q.ID, &q.AccountQuestionID, &q.Text,
			&q.ResponseType, &q.AskTime, &q.AskLocalDate); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: questions: %w", err)
		}
		out = append(out, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// ChoicesFor returns the answer choices for a set of questions, in one query.
// One query rather than one per question: the list is short but the N+1 shape
// is the kind that survives into a screen that loads slowly for no visible
// reason.
func (s *Store) ChoicesFor(ctx context.Context, questionIDs []int32) (map[int32][]QuestionChoice, error) {
	out := map[int32][]QuestionChoice{}
	if len(questionIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, question_id, response_text FROM response_choices
		WHERE question_id = ANY($1) ORDER BY id`, questionIDs)
	if err != nil {
		return nil, fmt.Errorf("store: choices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c QuestionChoice
		if err := rows.Scan(&c.ID, &c.QuestionID, &c.Text); err != nil {
			return nil, fmt.Errorf("store: choices: %w", err)
		}
		out[c.QuestionID] = append(out[c.QuestionID], c)
	}
	return out, rows.Err()
}

// SaveQuestionResponse records an answer or a skip.
//
// account_question_id is checked against the account before anything is
// written: it arrives from a browser, and an unchecked one lets a crafted post
// answer a question belonging to somebody else. Returns false when it does not
// belong, which the handler turns into a 400 rather than a silent success.
func (s *Store) SaveQuestionResponse(ctx context.Context, accountID, accountQuestionID int64,
	responseID *int32, skip bool) (bool, error) {

	var questionID int32
	err := s.pool.QueryRow(ctx, `
		SELECT question_id FROM account_questions WHERE id = $1 AND account_id = $2`,
		accountQuestionID, accountID).Scan(&questionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: save response: %w", err)
	}

	var rid int32
	if responseID != nil {
		rid = *responseID
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO question_responses
			(account_id, question_id, account_question_id, response_id, skip)
		VALUES ($1,$2,$3,$4,$5)`,
		accountID, questionID, accountQuestionID, rid, skip); err != nil {
		return false, fmt.Errorf("store: save response: %w", err)
	}
	return true, nil
}

// HasUnansweredQuestions backs the badge on the app's questions card.
func (s *Store) HasUnansweredQuestions(ctx context.Context, accountID int64) (bool, error) {
	var has bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM account_questions aq
			WHERE aq.account_id = $1 AND aq.expires_local_utc_ts > now()
			  AND NOT EXISTS (
			      SELECT 1 FROM question_responses r
			      WHERE r.account_question_id = aq.id))`, accountID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("store: unanswered questions: %w", err)
	}
	return has, nil
}

// SleepTempPreference returns whether the account said they sleep better hot or
// cold, or "" when they have not said.
//
// The response ids ARE the enum values in the reference: HOT(1), COLD(2),
// NONE(3), with a comment saying "values are response_id". So the mapping is
// positional and cannot be derived from the answer text, which is why this
// matches on id rather than on "Hot"/"Cold".
//
// Newest answer wins. The question is one_time, but a person can change, and
// taking the latest costs nothing.
func (s *Store) SleepTempPreference(ctx context.Context, accountID int64) (string, error) {
	var responseID int32
	err := s.pool.QueryRow(ctx, `
		SELECT r.response_id
		FROM question_responses r
		JOIN questions q ON q.id = r.question_id
		WHERE r.account_id = $1 AND q.account_info = 'sleep_temperature'
		  AND NOT r.skip
		ORDER BY r.created DESC LIMIT 1`, accountID).Scan(&responseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: sleep temp preference: %w", err)
	}
	switch responseID {
	case 1:
		return "HOT", nil
	case 2:
		return "COLD", nil
	default:
		return "", nil
	}
}

// WakeMinutes returns each scored night's local wake time in the window, as
// minutes past midnight, oldest first.
//
// Local, via the night's own stored offset, because "what time do you wake up"
// is a wall-clock question. Using UTC would make anyone who changed zones look
// wildly inconsistent, which is exactly what this measures.
func (s *Store) WakeMinutes(ctx context.Context, accountID int64, from, to time.Time) ([]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT (EXTRACT(EPOCH FROM (wake_up_at + (offset_ms || ' milliseconds')::interval))::bigint % 86400) / 60
		FROM timeline_events
		WHERE account_id = $1 AND wake_up_at IS NOT NULL
		  AND date_of_night > $2::date AND date_of_night <= $3::date
		ORDER BY date_of_night`, accountID, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: wake minutes: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var m int
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("store: wake minutes: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastInsightAt returns when a category last produced a card, or zero.
func (s *Store) LastInsightAt(ctx context.Context, accountID int64, category string) (time.Time, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(timestamp) FROM insights WHERE account_id = $1 AND category = $2`,
		accountID, category).Scan(&at)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: last insight: %w", err)
	}
	if at == nil {
		return time.Time{}, nil
	}
	return *at, nil
}

// InsertInsight stores a generated card.
//
// The uuid is generated here rather than taken from the caller, because it is
// the id the app uses to identify a card and nothing outside the database
// should be choosing it.
func (s *Store) InsertInsight(ctx context.Context, accountID int64,
	category, title, message string, at time.Time) error {

	_, err := s.pool.Exec(ctx, `
		INSERT INTO insights (account_id, uuid, category, insight_type, title, message, timestamp)
		VALUES ($1, gen_random_uuid()::text, $2, 'DEFAULT', $3, $4, $5)`,
		accountID, category, title, message, at)
	if err != nil {
		return fmt.Errorf("store: insert insight: %w", err)
	}
	return nil
}

// AccountsWithTimelines lists accounts that have at least one scored night,
// which is the set worth generating insights for.
func (s *Store) AccountsWithTimelines(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT account_id FROM timeline_events ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("store: accounts with timelines: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: accounts with timelines: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AlarmRow is an alarm template as the ring calculation needs it.
type AlarmRow struct {
	AccountID        int64
	Enabled          bool
	Repeated         bool
	Smart            bool
	Hour, Minute     int
	DayOfWeek        []int
	Year, Month, Day int
	SoundID          int
}

// AlarmsForSense returns the enabled alarms on a device.
//
// The date parts of a one-off alarm live in the `definition` blob rather than
// in columns, because the columns exist for the worker to query on and the blob
// is what the app sent. They are read out here rather than added as columns, so
// there is still one source of truth for what the alarm is.
func (s *Store) AlarmsForSense(ctx context.Context, deviceID string) ([]AlarmRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT account_id, enabled, repeated, smart, hour, minute, day_of_week,
		       COALESCE((definition->>'year')::int, 0),
		       COALESCE((definition->>'month')::int, 0),
		       COALESCE((definition->>'day_of_month')::int, 0),
		       COALESCE(sound_id, COALESCE((definition->'sound'->>'id')::int, 0))
		FROM alarms
		WHERE device_id = $1 AND enabled
		ORDER BY id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("store: alarms for sense: %w", err)
	}
	defer rows.Close()

	var out []AlarmRow
	for rows.Next() {
		var a AlarmRow
		var dow []int32
		if err := rows.Scan(&a.AccountID, &a.Enabled, &a.Repeated, &a.Smart,
			&a.Hour, &a.Minute, &dow,
			&a.Year, &a.Month, &a.Day, &a.SoundID); err != nil {
			return nil, fmt.Errorf("store: alarms for sense: %w", err)
		}
		for _, d := range dow {
			a.DayOfWeek = append(a.DayOfWeek, int(d))
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SenseZone returns the zone the device's alarms are set in.
//
// Prefers the named zone, falling back to a fixed offset. The name matters:
// an alarm is a wall-clock promise, and a fixed offset gets the morning after a
// daylight saving change wrong by an hour, which is the single most annoying
// bug this code could have.
func (s *Store) SenseZone(ctx context.Context, deviceID string) (*time.Location, error) {
	var zone string
	var offsetMS int32
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(t.timezone_id, ''), COALESCE(t.offset_ms, a.tz_offset_ms)
		FROM account_senses s
		JOIN accounts a ON a.id = s.account_id
		LEFT JOIN LATERAL (
			SELECT timezone_id, offset_ms FROM timezone_history
			WHERE account_id = s.account_id ORDER BY effective_from DESC LIMIT 1
		) t ON true
		WHERE s.device_id = $1 AND s.active
		LIMIT 1`, deviceID).Scan(&zone, &offsetMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.UTC, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: sense zone: %w", err)
	}
	if zone != "" {
		if loc, err := time.LoadLocation(zone); err == nil {
			return loc, nil
		}
	}
	return time.FixedZone("", int(offsetMS)/1000), nil
}

// RecentMotion returns the pill motion for an account in a window, oldest
// first, for deciding whether a smart alarm should ring early.
func (s *Store) RecentMotion(ctx context.Context, accountID int64, from, to time.Time) ([]alarm.Motion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(svm_no_gravity, 0), COALESCE(kickoff_counts, 0),
		       COALESCE(on_duration_secs, 0)
		FROM pill_samples
		WHERE account_id = $1 AND ts >= $2 AND ts <= $3
		ORDER BY ts`, accountID, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: recent motion: %w", err)
	}
	defer rows.Close()
	var out []alarm.Motion
	for rows.Next() {
		var m alarm.Motion
		if err := rows.Scan(&m.AmplitudeMilliG, &m.KickoffCounts, &m.OnDurationSecs); err != nil {
			return nil, fmt.Errorf("store: recent motion: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ArmedUpdateFor returns the prepared firmware update for a device, or nil.
//
// Returns the row whether or not it is armed, so the caller can log why an
// update was not offered. Deciding is internal/ota's job, not this query's:
// a filter here would silently hide a misconfigured row, and the point of this
// path is that nothing about it should be silent.
func (s *Store) ArmedUpdateFor(ctx context.Context, deviceID string) (*ota.Update, error) {
	var u ota.Update
	var completedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT device_id, from_version, to_version, host, url, sha1, file_size,
		       armed, completed_at,
		       copy_to_serial_flash, reset_application_processor,
		       reset_network_processor,
		       COALESCE(serial_flash_filename, ''), COALESCE(serial_flash_path, ''),
		       COALESCE(sd_card_filename, ''), COALESCE(sd_card_path, '')
		FROM firmware_updates
		WHERE device_id = $1 AND completed_at IS NULL
		ORDER BY armed DESC, created_at DESC
		LIMIT 1`, deviceID).Scan(
		&u.DeviceID, &u.FromVersion, &u.ToVersion, &u.Host, &u.URL, &u.SHA1,
		&u.FileSize, &u.Armed, &completedAt,
		&u.CopyToSerialFlash, &u.ResetApplicationProcessor, &u.ResetNetworkProcessor,
		&u.SerialFlashFilename, &u.SerialFlashPath, &u.SDCardFilename, &u.SDCardPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: armed update: %w", err)
	}
	u.Completed = completedAt != nil
	return &u, nil
}

// RecordUpdateOffered notes that an image was handed to a device.
//
// Best effort by design: the offer has already gone out by the time this runs,
// so failing the request afterwards would be a lie in the other direction. The
// counter is what makes a device stuck in a download loop visible.
func (s *Store) RecordUpdateOffered(ctx context.Context, deviceID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE firmware_updates
		SET offer_count = offer_count + 1,
		    first_offered_at = COALESCE(first_offered_at, now()),
		    last_offered_at = now()
		WHERE device_id = $1 AND armed AND completed_at IS NULL`, deviceID)
	if err != nil {
		return fmt.Errorf("store: record update offered: %w", err)
	}
	return nil
}

// CompleteUpdateIfReached closes out an update once the device reports the
// target version.
//
// This is the only success signal there is. The device never acknowledges an
// update directly; it simply comes back running something else, and that is
// what tells us the flash worked.
func (s *Store) CompleteUpdateIfReached(ctx context.Context, deviceID string, version int32) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE firmware_updates
		SET completed_at = now()
		WHERE device_id = $1 AND armed AND completed_at IS NULL AND to_version = $2`,
		deviceID, version)
	if err != nil {
		return false, fmt.Errorf("store: complete update: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// PushToken is one registered installation of the app.
type PushToken struct {
	Token      string
	AccountID  int64
	AppVersion string
}

// SavePushToken records a device token for an account.
//
// The conflict target is the token alone, so a phone that signs in as a
// different account moves rather than duplicates. See the migration for why
// that matters more than it looks.
func (s *Store) SavePushToken(ctx context.Context, accountID int64, token, os, osVersion, appVersion string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO push_tokens (account_id, token, os, os_version, app_version)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (token) DO UPDATE SET
			account_id  = EXCLUDED.account_id,
			os          = EXCLUDED.os,
			os_version  = EXCLUDED.os_version,
			app_version = EXCLUDED.app_version`,
		accountID, token, os, osVersion, appVersion)
	if err != nil {
		return fmt.Errorf("store: save push token: %w", err)
	}
	return nil
}

// DeletePushToken removes a registration.
//
// Scoped by account as well as token so that a request cannot unregister a
// device belonging to somebody else by naming its token.
func (s *Store) DeletePushToken(ctx context.Context, accountID int64, token string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM push_tokens WHERE account_id = $1 AND token = $2`, accountID, token)
	if err != nil {
		return fmt.Errorf("store: delete push token: %w", err)
	}
	return nil
}

// ForgetPushToken removes a token Apple has rejected as no longer valid.
//
// Unscoped by account on purpose: this is the response to APNS reporting
// Unregistered, which is a statement about the installation and not about who
// happens to be signed in to it.
func (s *Store) ForgetPushToken(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_tokens WHERE token = $1`, token)
	if err != nil {
		return fmt.Errorf("store: forget push token: %w", err)
	}
	return nil
}

// PushTokensFor lists the installations to notify for an account.
func (s *Store) PushTokensFor(ctx context.Context, accountID int64) ([]PushToken, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT token, account_id, app_version FROM push_tokens WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: push tokens %d: %w", accountID, err)
	}
	defer rows.Close()
	var out []PushToken
	for rows.Next() {
		var t PushToken
		if err := rows.Scan(&t.Token, &t.AccountID, &t.AppVersion); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkPushTokenSent records a successful delivery, so that a token which has
// never worked is distinguishable from one that stopped working.
func (s *Store) MarkPushTokenSent(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE push_tokens SET last_sent_at = now() WHERE token = $1`, token)
	if err != nil {
		return fmt.Errorf("store: mark push token sent: %w", err)
	}
	return nil
}

// AllPushTokens lists every registered installation, across accounts.
//
// Used by the manual send tool. Handlers should use PushTokensFor instead: this
// deliberately ignores account scoping and is not something a request should
// ever reach.
func (s *Store) AllPushTokens(ctx context.Context) ([]PushToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT token, account_id, app_version FROM push_tokens`)
	if err != nil {
		return nil, fmt.Errorf("store: all push tokens: %w", err)
	}
	defer rows.Close()
	var out []PushToken
	for rows.Next() {
		var t PushToken
		if err := rows.Scan(&t.Token, &t.AccountID, &t.AppVersion); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClaimPush reserves the right to send one notification, returning false if it
// has already been sent.
//
// Claim-then-send rather than send-then-record: a crash between the two then
// costs a missed notification rather than a duplicate, and a notification the
// merchant never sees is a smaller failure than one that arrives every fifteen
// minutes. ReleasePush undoes the claim when the send itself fails.
func (s *Store) ClaimPush(ctx context.Context, accountID int64, kind, dedupeKey string) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO push_log (account_id, kind, dedupe_key)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id, kind, dedupe_key) DO NOTHING
		RETURNING id`, accountID, kind, dedupeKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: claim push: %w", err)
	}
	return true, nil
}

// ReleasePush gives back a claim whose send failed, so the next run retries it.
func (s *Store) ReleasePush(ctx context.Context, accountID int64, kind, dedupeKey string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM push_log WHERE account_id = $1 AND kind = $2 AND dedupe_key = $3`,
		accountID, kind, dedupeKey)
	if err != nil {
		return fmt.Errorf("store: release push: %w", err)
	}
	return nil
}

// ScoredNight is a night with a sleep score, ready to notify about.
type ScoredNight struct {
	AccountID int64
	Date      time.Time
	Score     int32
}

// RecentScoredNights lists nights that are both recently scored and recently
// slept.
//
// Both bounds are needed and they catch different things. Without `updated_at`,
// the first run against a database of history would send one notification per
// night ever recorded. Without `date_of_night`, re-scoring an old night, which a
// timeline correction does routinely, would announce "you slept 76 last night"
// about a night three days ago. The message says "last night", so the query has
// to mean it.
func (s *Store) RecentScoredNights(ctx context.Context, within time.Duration) ([]ScoredNight, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT st.account_id, st.date_of_night, st.sleep_score
		FROM sleep_stats st
		JOIN timeline_events e
		  ON e.account_id = st.account_id AND e.date_of_night = st.date_of_night
		WHERE st.sleep_score IS NOT NULL
		  AND st.sleep_score > 0
		  AND st.date_of_night >= current_date - 1
		  AND st.updated_at > now() - $1::interval
		  -- Not before ScoreAnnounceHour local, the morning after the night.
		  --
		  -- A night is rescored every 15 minutes while its window is open, so
		  -- without this the notification fires on the FIRST usable score and
		  -- the dedupe key stops it ever being corrected. On 2026-08-17 that
		  -- meant a push at 05:08 saying "You slept 56 last night" while the
		  -- person was still asleep; the night went on to settle at 77. The app
		  -- recovers on refresh, a notification cannot.
		  --
		  -- The boundary is derived database-side from the night's own offset,
		  -- exactly as NightsNeedingTimeline derives its window close, because
		  -- the device clock and the server clock are not the same and
		  -- comparing them directly has caused bugs here before.
		  AND now() >= ((st.date_of_night::timestamp
		                  + make_interval(hours => $2)
		                  - make_interval(secs => e.offset_ms / 1000.0)) AT TIME ZONE 'UTC')
		ORDER BY st.date_of_night`, within.String(), 24+ScoreAnnounceHour)
	if err != nil {
		return nil, fmt.Errorf("store: recent scored nights: %w", err)
	}
	defer rows.Close()
	var out []ScoredNight
	for rows.Next() {
		var n ScoredNight
		if err := rows.Scan(&n.AccountID, &n.Date, &n.Score); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// LowBatteryPill is a paired pill reporting a battery below the threshold.
type LowBatteryPill struct {
	AccountID int64
	PillID    string
	Battery   int32
}

// PillsBelowBattery lists paired pills under a battery percentage.
func (s *Store) PillsBelowBattery(ctx context.Context, threshold int32) ([]LowBatteryPill, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ap.account_id, p.pill_id, p.battery_level
		FROM pills p
		JOIN account_pills ap ON ap.pill_id = p.pill_id AND ap.active
		WHERE p.battery_level IS NOT NULL AND p.battery_level < $1`, threshold)
	if err != nil {
		return nil, fmt.Errorf("store: pills below battery: %w", err)
	}
	defer rows.Close()
	var out []LowBatteryPill
	for rows.Next() {
		var p LowBatteryPill
		if err := rows.Scan(&p.AccountID, &p.PillID, &p.Battery); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NightEventTimes is the four main events of a stored timeline.
//
// Pointers because a night may have been scored without all four, and "absent"
// has to be distinguishable from midnight.
type NightEventTimes struct {
	InBed    *time.Time
	Sleep    *time.Time
	WakeUp   *time.Time
	OutOfBed *time.Time
}

// At returns the stored time for one of the four event types.
func (n NightEventTimes) At(eventType int32) *time.Time {
	switch eventType {
	case feedbackInBed:
		return n.InBed
	case feedbackSleep:
		return n.Sleep
	case feedbackWakeUp:
		return n.WakeUp
	case feedbackOutOfBed:
		return n.OutOfBed
	}
	return nil
}

// The four event type numbers, duplicated here rather than imported.
//
// internal/feedback imports nothing from this package and it stays that way;
// the numbers are on the wire and in timeline_feedback.event_type, so they are
// fixed rather than ours to choose. See internal/feedback for the same list.
const (
	feedbackInBed    int32 = 11
	feedbackSleep    int32 = 12
	feedbackOutOfBed int32 = 13
	feedbackWakeUp   int32 = 14
)

// EventTimesForNight returns the four main events of a night's stored timeline.
//
// Used to pair up a one-sided correction: correcting when you got into bed says
// nothing about when you got out, and the algorithm needs both ends to learn a
// path rather than a wall.
func (s *Store) EventTimesForNight(ctx context.Context, accountID int64, date time.Time) (NightEventTimes, error) {
	var out NightEventTimes
	err := s.pool.QueryRow(ctx, `
		SELECT in_bed_at, sleep_at, wake_up_at, out_of_bed_at
		FROM timeline_events
		WHERE account_id = $1 AND date_of_night = $2`,
		accountID, date).Scan(&out.InBed, &out.Sleep, &out.WakeUp, &out.OutOfBed)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("store: night event times %d %s: %w",
			accountID, date.Format("2006-01-02"), err)
	}
	return out, nil
}

// SleepMinutes returns the sleep duration of each scored night in the window,
// oldest first.
//
// The window convention matches WakeMinutes: `from` exclusive, `to` inclusive.
// The two are read together by generators that compare one against the other,
// and a half-open pair that disagreed would silently shift one of them by a
// night.
//
// Reads sleep_stats rather than deriving the duration from timeline_events,
// because sleep_duration_mins is what the algorithm actually scored and the
// difference between wake_up_at and sleep_at is not the same number: it counts
// the time awake in the middle of the night, which the algorithm subtracts.
func (s *Store) SleepMinutes(ctx context.Context, accountID int64, from, to time.Time) ([]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sleep_duration_mins
		FROM sleep_stats
		WHERE account_id = $1 AND sleep_duration_mins IS NOT NULL
		  AND date_of_night > $2::date AND date_of_night <= $3::date
		ORDER BY date_of_night`, accountID, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: sleep minutes: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var m int
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("store: sleep minutes: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AgeYears returns the account's age in whole years on a given date, or 0 when
// no birthdate is on file.
//
// The arithmetic is deliberately identical to LoadNight's, down to the
// divide-by-365 that makes a leap year move the birthday by a day. Two answers
// to "how old is this person" that disagree for a few days a year would be a
// genuinely miserable thing to debug, so they share one expression.
//
// Zero means unknown, and every caller must read it as such rather than as a
// newborn. The reference does the same and treats it as an adult.
func (s *Store) AgeYears(ctx context.Context, accountID int64, on time.Time) (int, error) {
	var age int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(((($2::date - birthdate::date))::int / 365), 0)
		FROM accounts WHERE id = $1`, accountID, on).Scan(&age); err != nil {
		return 0, fmt.Errorf("store: age %d: %w", accountID, err)
	}
	return age, nil
}

// InsightInfoRow is the editorial copy behind an insight category.
type InsightInfoRow struct {
	ID       int32
	Category string
	Title    string
	Text     string
	ImageURL *string
}

// InsightInfo returns the copy for a category, or false when there is none.
//
// Newest row wins, which is the reference's `ORDER BY id DESC LIMIT 1`. The
// category is matched lowercase because that is how the rows are stored and how
// suripu queries them; the app sends it uppercase.
func (s *Store) InsightInfo(ctx context.Context, category string) (InsightInfoRow, bool, error) {
	var r InsightInfoRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, category, COALESCE(title,''), COALESCE(text,''), image_url
		FROM insight_info
		WHERE category = lower($1)
		ORDER BY id DESC
		LIMIT 1`, category).Scan(&r.ID, &r.Category, &r.Title, &r.Text, &r.ImageURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("store: insight info %q: %w", category, err)
	}
	return r, true, nil
}

// InsightShareRow is a shared insight, as stored.
type InsightShareRow struct {
	ID        string
	Category  string
	Title     string
	Message   string
	SharedBy  string
	InsightAt *time.Time
	CreatedAt time.Time
}

// CreateInsightShare snapshots a card and returns the share id.
//
// A snapshot, not a reference: see the migration. The caller supplies the id so
// it can be generated where the URL is built, and so a retry of the same share
// is idempotent rather than producing a second link to the same card.
func (s *Store) CreateInsightShare(ctx context.Context, id string, accountID int64,
	category, title, message, sharedBy string, insightAt *time.Time) error {

	_, err := s.pool.Exec(ctx, `
		INSERT INTO insight_shares
		    (id, account_id, category, title, message, shared_by, insight_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO NOTHING`,
		id, accountID, category, title, message, sharedBy, insightAt)
	if err != nil {
		return fmt.Errorf("store: create share: %w", err)
	}
	return nil
}

// InsightShare returns a share by id, or false when it does not exist.
//
// Takes no account id on purpose: a share is public by construction, and the
// whole point of the link is that somebody who is not the sharer can open it.
func (s *Store) InsightShare(ctx context.Context, id string) (InsightShareRow, bool, error) {
	var r InsightShareRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, category, title, message, COALESCE(shared_by,''), insight_at, created_at
		FROM insight_shares WHERE id = $1`, id).
		Scan(&r.ID, &r.Category, &r.Title, &r.Message, &r.SharedBy, &r.InsightAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("store: share %q: %w", id, err)
	}
	return r, true, nil
}

// InsightByUUID returns one of an account's cards by its uuid.
//
// Scoped to the account, which is what stops a share request naming somebody
// else's card. The reference achieves the same by scanning that account's
// hundred most recent cards and matching the uuid in memory; a WHERE clause
// does it without the hundred-row window, and without the bug where sharing a
// card older than the window silently 404s.
func (s *Store) InsightByUUID(ctx context.Context, accountID int64, uuid string) (InsightRow, bool, error) {
	var r InsightRow
	err := s.pool.QueryRow(ctx, `
		SELECT i.uuid, i.category, COALESCE(c.category_name,''),
		       COALESCE(i.title,''), COALESCE(i.message,''),
		       COALESCE(i.insight_type,''), i.timestamp
		FROM insights i
		LEFT JOIN insight_categories c ON c.category = i.category
		WHERE i.account_id = $1 AND i.uuid = $2`, accountID, uuid).
		Scan(&r.UUID, &r.Category, &r.CategoryName, &r.Title, &r.Message, &r.InsightType, &r.Timestamp)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("store: insight by uuid: %w", err)
	}
	return r, true, nil
}
