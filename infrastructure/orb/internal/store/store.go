// Package store is the Postgres access layer for orb.
//
// It replaces what used to be spread across DynamoDB (device keys, samples,
// state), Kinesis (transport), and Redis (message buffer). At this scale none
// of that indirection earns its keep: a sample arrives, it is written, done.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnknownDevice = errors.New("store: unknown device")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Sense is what the edge needs to authenticate and attribute an upload.
type Sense struct {
	DeviceID  string
	AESKey    []byte
	AccountID int64
	// DustOffset is the factory dust calibration, nil when the device has never
	// been calibrated. Read here rather than in a second query because the LED's
	// room condition needs it on every single upload, and it sits in the row the
	// edge already has to fetch to authenticate.
	DustOffset *int32

	// HWVersion is the reference's HardwareVersion id (1 original, 4 the Sense
	// 1.5), zero when the device never reported one. Decides how its readings
	// are converted.
	HWVersion int32
}

// SenseByID returns the device and the account it is paired to.
//
// The AES key is what every request signature is verified against, so an
// unknown device id must fail closed rather than fall back to the firmware
// default key: accepting a default-keyed device would let anything on the
// network write samples for any account.
func (s *Store) SenseByID(ctx context.Context, deviceID string) (Sense, error) {
	var out Sense
	err := s.pool.QueryRow(ctx, `
		SELECT s.device_id, s.aes_key, a.account_id, s.dust_offset,
		       COALESCE(NULLIF(s.hw_version, '')::int, 0)
		FROM senses s
		JOIN account_senses a ON a.device_id = s.device_id AND a.active
		WHERE s.device_id = $1`, deviceID).Scan(&out.DeviceID, &out.AESKey, &out.AccountID, &out.DustOffset, &out.HWVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("%w: %s", ErrUnknownDevice, deviceID)
	}
	if err != nil {
		return out, fmt.Errorf("store: sense %s: %w", deviceID, err)
	}
	return out, nil
}

// SensorSample mirrors one periodic_data reading. Values keep the device's own
// scaling (temperature and humidity are centi-units); converting here would
// change what the algorithms see. See migrations/0001_init.sql.
type SensorSample struct {
	DeviceID  string
	TS        time.Time
	AccountID int64
	OffsetMS  int32

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

	// The Sense 1.5 extras, nil on a Sense 1.0. Pressure is Q24.8 pascals,
	// TVOC ppb, CO2 ppm; the light-sensor group is the TCS3400's clear and IR
	// channels plus the firmware's own lux and UV counts. The reference reads
	// light for a 1.5 from lux_count, not from light, and converts
	// temperature and humidity differently too (SenseOneFiveDataConversion).
	Pressure *int32
	TVOC     *int32
	CO2      *int32
	IR       *int32
	Clear    *int32
	LuxCount *int32
	UVCount  *int32
	R, G, B  *int32
}

// InsertSensorSamples writes a batch.
//
// ON CONFLICT DO NOTHING because uploads are at-least-once: the device resends
// a batch it did not get an answer for, and the same minute arriving twice must
// not be an error. The primary key (device_id, ts) makes the retry a no-op.
func (s *Store) InsertSensorSamples(ctx context.Context, samples []SensorSample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, m := range samples {
		batch.Queue(`
			INSERT INTO sensor_samples (device_id, ts, account_id, offset_ms,
				temperature, humidity, light, light_variance, air_quality_raw,
				audio_peak_background_db, audio_peak_energy_db, audio_peak_disturbances_db,
				audio_num_disturbances, wave_count, hold_count,
				pressure, tvoc, co2, ir, clear, lux_count, uv_count, r, g, b)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
				$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
			ON CONFLICT (device_id, ts) DO NOTHING`,
			m.DeviceID, m.TS, m.AccountID, m.OffsetMS,
			m.Temperature, m.Humidity, m.Light, m.LightVariance, m.AirQualityRaw,
			m.AudioPeakBackgroundDB, m.AudioPeakEnergyDB, m.AudioPeakDisturbanceDB,
			m.AudioNumDisturbances, m.WaveCount, m.HoldCount,
			m.Pressure, m.TVOC, m.CO2, m.IR, m.Clear, m.LuxCount, m.UVCount,
			m.R, m.G, m.B)
	}
	res := s.pool.SendBatch(ctx, batch)
	defer res.Close()

	written := 0
	for range samples {
		tag, err := res.Exec()
		if err != nil {
			return written, fmt.Errorf("store: insert sensor samples: %w", err)
		}
		written += int(tag.RowsAffected())
	}
	return written, nil
}

// TouchSense records liveness and the current WiFi, replacing what
// sense_last_seen and wifi_info held in DynamoDB. Cheap enough to do on every
// upload, which is roughly once a minute.
// hwVersion is the raw value of the device's X-Hello-Sense-HW header (the TI
// HardwareVersion integer: 1 = original Sense, 4 = Sense with Voice). Stored
// verbatim and mapped to the app's wire string in the devices endpoint. Empty
// leaves the column untouched, so a firmware or endpoint that omits the header
// never blanks a value another request established.
func (s *Store) TouchSense(ctx context.Context, deviceID string, seenAt time.Time, fw *int32, ssid, hwVersion string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE senses SET last_seen_at = $2,
		                  firmware_version = COALESCE($3, firmware_version),
		                  wifi_ssid = COALESCE(NULLIF($4,''), wifi_ssid),
		                  hw_version = COALESCE(NULLIF($5,''), hw_version)
		WHERE device_id = $1`, deviceID, seenAt, fw, ssid, hwVersion)
	if err != nil {
		return fmt.Errorf("store: touch sense %s: %w", deviceID, err)
	}
	return nil
}

// Pill is what the edge needs to decrypt a relayed motion payload. The key is
// the PILL's, not the Sense's: the Sense relays a blob it cannot itself read.
type Pill struct {
	PillID    string
	AESKey    []byte
	AccountID int64
}

// PillByID returns the pill and the account it is paired to.
//
// AESKey may be nil: a pill can be paired and reporting before its key has been
// recovered (which requires SWD access to the hardware). Callers must treat a
// nil key as "cannot decrypt" rather than "use a default".
func (s *Store) PillByID(ctx context.Context, pillID string) (Pill, error) {
	var out Pill
	err := s.pool.QueryRow(ctx, `
		SELECT p.pill_id, p.aes_key, a.account_id
		FROM pills p
		JOIN account_pills a ON a.pill_id = p.pill_id AND a.active
		WHERE p.pill_id = $1`, pillID).Scan(&out.PillID, &out.AESKey, &out.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("%w: pill %s", ErrUnknownDevice, pillID)
	}
	if err != nil {
		return out, fmt.Errorf("store: pill %s: %w", pillID, err)
	}
	return out, nil
}

// PillSample is one minute of decoded motion.
type PillSample struct {
	PillID         string
	TS             time.Time
	AccountID      int64
	OffsetMS       int32
	SVMNoGravity   *int64
	MotionRange    *int64
	KickoffCounts  *int32
	OnDurationSecs *int32

	// CosTheta and MotionMask come only from the 1.5 pill's v4 payload and are
	// nil for older pills. The mask is one bit per second of the minute. It is
	// stored because the reference's motion-mask partner filter needs it from
	// both pills, and it cannot be recovered later.
	CosTheta   *int64
	MotionMask *int64

	// RelayedBy is the Sense that uploaded this sample. Empty when unknown.
	RelayedBy string
}

func (s *Store) InsertPillSamples(ctx context.Context, samples []PillSample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, m := range samples {
		batch.Queue(`
			INSERT INTO pill_samples (pill_id, ts, account_id, offset_ms,
				svm_no_gravity, motion_range, kickoff_counts, on_duration_secs,
				cos_theta, motion_mask, relayed_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''))
			ON CONFLICT (pill_id, ts) DO NOTHING`,
			m.PillID, m.TS, m.AccountID, m.OffsetMS,
			m.SVMNoGravity, m.MotionRange, m.KickoffCounts, m.OnDurationSecs,
			m.CosTheta, m.MotionMask, m.RelayedBy)
	}
	res := s.pool.SendBatch(ctx, batch)
	defer res.Close()

	written := 0
	for range samples {
		tag, err := res.Exec()
		if err != nil {
			return written, fmt.Errorf("store: insert pill samples: %w", err)
		}
		written += int(tag.RowsAffected())
	}
	return written, nil
}

// TouchPill records battery and liveness from a relayed heartbeat.
//
// A new battery reading shifts the current one into prev_battery_level, so the
// low-battery push can ask for two consecutive low heartbeats. Every SET
// expression reads the row as it was before the statement, so the order of the
// assignments does not matter.
func (s *Store) TouchPill(ctx context.Context, pillID string, seenAt time.Time, battery, uptime *int32) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE pills SET last_seen_at = $2,
		                 prev_battery_level = CASE WHEN $3::int IS NULL THEN prev_battery_level ELSE battery_level END,
		                 battery_level = COALESCE($3, battery_level),
		                 uptime_secs = COALESCE($4, uptime_secs)
		WHERE pill_id = $1`, pillID, seenAt, battery, uptime)
	if err != nil {
		return fmt.Errorf("store: touch pill %s: %w", pillID, err)
	}
	return nil
}

// OffsetMSAt returns the UTC offset that applied for an account at a given
// instant, from timezone_history.
//
// It deliberately does not fall back to the machine's local zone or to the
// account's current zone. A sample must be rendered with the offset in force
// when it was taken, or nights either side of a DST change shift by an hour.
// Zero with ok=false means "unknown", which the caller should treat as a reason
// to look, not a value to store.
func (s *Store) OffsetMSAt(ctx context.Context, accountID int64, at time.Time) (int32, bool, error) {
	var offset int32
	err := s.pool.QueryRow(ctx, `
		SELECT offset_ms FROM timezone_history
		WHERE account_id = $1 AND effective_from <= $2
		ORDER BY effective_from DESC LIMIT 1`, accountID, at).Scan(&offset)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: offset for %d: %w", accountID, err)
	}
	return offset, true, nil
}

// NextMessage pops the oldest undelivered command for a device, replacing
// messeji. Marking delivered in the same statement keeps it atomic without an
// explicit transaction, so two concurrent long-polls cannot both take it.
func (s *Store) NextMessage(ctx context.Context, deviceID string) ([]byte, bool, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE device_messages SET delivered_at = now()
		WHERE id = (
			SELECT id FROM device_messages
			WHERE device_id = $1 AND delivered_at IS NULL
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING payload`, deviceID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: next message %s: %w", deviceID, err)
	}
	return payload, true, nil
}

// QueueDeviceMessage inserts one command for the device's long-poll to
// collect, typically within half a second (the poll's tick). The payload is
// the complete signed response body: the long-poll writes it to the device
// verbatim, so signing happens at enqueue time, when the caller has the
// device key in hand.
func (s *Store) QueueDeviceMessage(ctx context.Context, deviceID string, payload []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_messages (device_id, payload) VALUES ($1, $2)`,
		deviceID, payload)
	if err != nil {
		return fmt.Errorf("store: queue message %s: %w", deviceID, err)
	}
	return nil
}

// SenseState returns the device's last self-reported state blob (protojson of
// SenseState), or ok=false when the device has never reported one.
func (s *Store) SenseState(ctx context.Context, deviceID string) ([]byte, bool, error) {
	var state *string
	err := s.pool.QueryRow(ctx,
		`SELECT state FROM senses WHERE device_id = $1`, deviceID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sense state %s: %w", deviceID, err)
	}
	if state == nil {
		return nil, false, nil
	}
	return []byte(*state), true, nil
}

// SetSenseState records the device's self-reported state as JSONB. Housekeeping
// that nothing joins on, and whose shape has changed across firmware versions,
// so a document beats columns here.
func (s *Store) SetSenseState(ctx context.Context, deviceID string, state []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE senses SET state = $2 WHERE device_id = $1`, deviceID, string(state))
	if err != nil {
		return fmt.Errorf("store: set sense state %s: %w", deviceID, err)
	}
	return nil
}
