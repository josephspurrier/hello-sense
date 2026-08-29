// Command migrate moves the Hello stack's data into the consolidated orb
// schema: three Postgres databases plus 67 DynamoDB tables down to one.
//
// It reads DynamoDB from JSON dumps produced by scripts/dump-dynamo.sh rather
// than from DynamoDB itself. That keeps the migration reading a fixed snapshot
// while the Orb is still writing, gives a backup of the source, and keeps the
// AWS SDK out of a module whose whole point is not needing it.
//
// Idempotent: every insert is ON CONFLICT DO UPDATE or DO NOTHING, so it can be
// run repeatedly. Non-destructive: it only writes to the target database.
//
// Usage:
//
//	go run ./cmd/migrate \
//	  -dump  ./migrations/dump \
//	  -src   'postgres://hello:hello@localhost:5432/common' \
//	  -dst   'postgres://hello:hello@localhost:5432/orb'
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// attr is one DynamoDB attribute value. Only the types this data actually uses
// are handled; anything else is a bug worth failing on rather than guessing.
type attr struct {
	S    *string `json:"S"`
	N    *string `json:"N"`
	B    *string `json:"B"`
	BOOL *bool   `json:"BOOL"`
}

type item map[string]attr

type dump struct {
	Items []item `json:"Items"`
}

func (i item) str(k string) string {
	if a, ok := i[k]; ok && a.S != nil {
		return *a.S
	}
	return ""
}

func (i item) num(k string) (float64, bool) {
	a, ok := i[k]
	if !ok || a.N == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(*a.N, 64)
	return f, err == nil
}

func (i item) intp(k string) *int64 {
	if f, ok := i.num(k); ok {
		v := int64(f)
		return &v
	}
	return nil
}

// millis returns a UTC time from an epoch-millis attribute, which may be stored
// as N or, inconsistently in sleep_stats, as S.
func (i item) millis(k string) *time.Time {
	var ms int64
	if f, ok := i.num(k); ok {
		ms = int64(f)
	} else if s := i.str(k); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil
		}
		ms = v
	} else {
		return nil
	}
	if ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

func (i item) bytes(k string) []byte {
	if a, ok := i[k]; ok && a.B != nil {
		b, err := base64.StdEncoding.DecodeString(*a.B)
		if err == nil {
			return b
		}
	}
	return nil
}

func loadDump(dir, table string) ([]item, error) {
	path := filepath.Join(dir, table+".json")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil // absent table is not an error; it was empty
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var d dump
	if err := json.NewDecoder(f).Decode(&d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d.Items, nil
}

// loadShards concatenates the month-sharded tables (sense_data_2026_07, _08,
// ...) that the consolidated schema collapses into one.
func loadShards(dir, prefix string) ([]item, error) {
	matches, err := filepath.Glob(filepath.Join(dir, prefix+"*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var all []item
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".json")
		items, err := loadDump(dir, name)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

// splitKey splits DynamoDB's composite range keys, e.g. "ts|dev" holding
// "2026-08-01 00:00|49F277D951568DF3".
func splitKey(v string) (string, string) {
	if i := strings.Index(v, "|"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func parseTS(s string) (time.Time, error) {
	// RFC3339 first: the insight cards write an ISO instant with milliseconds
	// and a Z, which none of the space-separated layouts below will take. The
	// existing layouts have no zone at all and are parsed as UTC, which is what
	// the columns they came from meant.
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", s)
}

type migrator struct {
	ctx  context.Context
	src  *pgx.Conn
	ins  *pgx.Conn // the `insights` database, the third one
	dst  *pgx.Conn
	dir  string
	rows map[string]int
	warn []string
}

// insightCategories is InsightCard.Category, by ordinal.
//
// DynamoDB stores the ordinal and nothing else, so this slice is the only thing
// that turns a 19 back into WAKE_VARIANCE. Order is the contract: appending is
// safe, reordering silently relabels every stored card. Anything out of range
// falls back to GENERIC, which is what the reference's fromInteger does.
var insightCategories = []string{
	"GENERIC", "SLEEP_HYGIENE", "LIGHT", "SOUND", "TEMPERATURE", "HUMIDITY",
	"AIR_QUALITY", "SLEEP_DURATION", "TIME_TO_SLEEP", "SLEEP_TIME", "WAKE_TIME",
	"WORKOUT", "CAFFEINE", "ALCOHOL", "DIET", "DAYTIME_SLEEPINESS",
	"DAYTIME_ACTIVITIES", "SLEEP_SCORE", "SLEEP_QUALITY", "WAKE_VARIANCE",
	"BED_LIGHT_DURATION", "BED_LIGHT_INTENSITY_RATIO", "PARTNER_MOTION",
	"DRIVE", "EAT", "LEARN", "LOVE", "PLAY", "RUN", "SWIM", "WORK",
	"GOAL_GO_OUTSIDE", "GOAL_COFFEE", "GOAL_SCHEDULE_THOUGHTS", "GOAL_SCREENS",
	"GOAL_WAKE_VARIANCE", "SLEEP_DEPRIVATION", "CORRELATION_TEMP",
}

func insightCategory(ordinal int) string {
	if ordinal < 0 || ordinal >= len(insightCategories) {
		return "GENERIC"
	}
	return insightCategories[ordinal]
}

func (m *migrator) note(table string, n int) { m.rows[table] = n }
func (m *migrator) warnf(f string, a ...any) {
	m.warn = append(m.warn, fmt.Sprintf(f, a...))
}

func main() {
	var (
		dumpDir = flag.String("dump", "./migrations/dump", "directory of DynamoDB JSON dumps")
		srcDSN  = flag.String("src", "postgres://hello:hello@localhost:5432/common", "source Postgres (common)")
		insDSN  = flag.String("insights", "postgres://hello:hello@localhost:5432/insights", "source Postgres (insights)")
		dstDSN  = flag.String("dst", "postgres://hello:hello@localhost:5432/orb", "target Postgres (orb)")
		only    = flag.String("only", "", "comma-separated step names to run; empty runs all")
	)
	flag.Parse()

	ctx := context.Background()
	src, err := pgx.Connect(ctx, *srcDSN)
	if err != nil {
		log.Fatalf("connect source: %v", err)
	}
	defer src.Close(ctx)

	ins, err := pgx.Connect(ctx, *insDSN)
	if err != nil {
		log.Fatalf("connect insights: %v", err)
	}
	defer ins.Close(ctx)

	dst, err := pgx.Connect(ctx, *dstDSN)
	if err != nil {
		log.Fatalf("connect target: %v", err)
	}
	defer dst.Close(ctx)

	m := &migrator{ctx: ctx, src: src, ins: ins, dst: dst, dir: *dumpDir, rows: map[string]int{}}

	// Order matters: foreign keys. Accounts and devices before anything that
	// references them, pairings before samples.
	steps := []struct {
		name string
		fn   func() error
	}{
		{"accounts", m.accounts},
		{"oauth", m.oauth},
		{"senses", m.senses},
		{"calibration", m.calibration},
		{"pills", m.pills},
		{"pairings", m.pairings},
		{"timezone_history", m.timezones},
		{"sensor_samples", m.sensorSamples},
		{"pill_samples", m.pillSamples},
		{"timeline_feedback", m.feedback},
		{"timeline_events", m.timelineEvents},
		{"sleep_stats", m.sleepStats},
		{"agg_stats", m.aggStats},
		{"hmm_models", m.hmmModels},
		{"wifi_info", m.wifiInfo},
		{"alarms", m.alarms},
		{"app_stats", m.appStats},
		{"insight_categories", m.insightCategories},
		{"insights", m.insights},
		{"questions", m.questions},
	}

	// -only exists because the whole-run default is the dangerous one.
	//
	// Re-running every step to pick up one new table is what overwrote three
	// nights of computed timelines on 2026-08-14. The steps are now written to
	// be safe to repeat, but "safe to repeat" is a property of sixteen separate
	// functions and it only has to lapse once.
	wanted := map[string]bool{}
	if *only != "" {
		for _, name := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(name)] = true
		}
		known := map[string]bool{}
		for _, s := range steps {
			known[s.name] = true
		}
		for name := range wanted {
			if !known[name] {
				log.Fatalf("-only: no step named %q", name)
			}
		}
	}

	for _, s := range steps {
		if len(wanted) > 0 && !wanted[s.name] {
			continue
		}
		if err := s.fn(); err != nil {
			log.Fatalf("%s: %v", s.name, err)
		}
	}

	fmt.Println("\nmigrated:")
	names := make([]string, 0, len(m.rows))
	for k := range m.rows {
		names = append(names, k)
	}
	sort.Strings(names)
	total := 0
	for _, n := range names {
		fmt.Printf("  %-22s %7d\n", n, m.rows[n])
		total += m.rows[n]
	}
	fmt.Printf("  %-22s %7d\n", "TOTAL", total)

	if len(m.warn) > 0 {
		fmt.Println("\nwarnings:")
		for _, w := range m.warn {
			fmt.Println("  " + w)
		}
	}
}

// --------------------------------------------------------------- wifi info --

// wifiInfo carries the Sense's WiFi reading and, importantly, WHEN it was
// taken.
//
// The app shows a "last updated" against the WiFi row and it is not the Sense's
// last_seen_at: the Sense reports every minute, the WiFi record only changes
// when the network does. Deriving one from the other made the WiFi reading look
// like it refreshed every minute, which is a different claim from the one the
// reference makes.
func (m *migrator) wifiInfo() error {
	items, err := loadDump(m.dir, "wifi_info")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		id := it.str("sense_id")
		if id == "" {
			continue
		}
		// "2026-08-13 15:36:26" as a string, not epoch millis. Every other
		// table in this dump uses millis for a timestamp, so millis() was the
		// obvious call and it silently returned nil for every row: the step
		// reported "wifi_info 0" and looked like an empty table rather than a
		// parse that never matched.
		at, err := parseTS(it.str("last_updated"))
		if err != nil {
			m.warnf("wifi_info: %s has unparseable last_updated %q", id, it.str("last_updated"))
			continue
		}
		tag, err := m.dst.Exec(m.ctx, `
			UPDATE senses SET
				wifi_ssid = COALESCE($2, wifi_ssid),
				wifi_rssi = COALESCE($3, wifi_rssi),
				wifi_updated_at = GREATEST($4, wifi_updated_at)
			WHERE device_id = $1`,
			id, nullIfEmpty(it.str("ssid")), it.intp("rssi"), at)
		if err != nil {
			return err
		}
		n += int(tag.RowsAffected())
	}
	m.note("wifi_info", n)
	return nil
}

// ---------------------------------------------------------------- accounts --

func (m *migrator) accounts() error {
	rows, err := m.src.Query(m.ctx, `
		SELECT id, email, password_hash, name, gender, height, weight, dob, tz_offset, created,
		       external_id, firstname, lastname, gender_name, last_modified
		FROM accounts`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var (
			id                    int64
			email, password       string
			name, gender          *string
			height, weight, tzoff *int64
			dob                   *time.Time
			created               time.Time
			extID                 *string
			first, last, genderOK *string
			// last_modified is a bigint of epoch millis in the source, not a
			// timestamp. Reading it as a time silently yields a date in 58,000
			// years, and the app renders it without complaint.
			lastModMS *int64
		)
		if err := rows.Scan(&id, &email, &password, &name, &gender, &height, &weight, &dob, &tzoff, &created,
			&extID, &first, &last, &genderOK, &lastModMS); err != nil {
			return err
		}
		_, err := m.dst.Exec(m.ctx, `
			INSERT INTO accounts (id, email, password_hash, name, gender, height_cm,
			                      weight_grams, birthdate, tz_offset_ms, created_at,
			                      external_id, firstname, lastname, gender_other,
			                      last_modified)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,
			        COALESCE(to_timestamp($15 / 1000.0), now()))
			ON CONFLICT (id) DO UPDATE SET
				email = EXCLUDED.email, password_hash = EXCLUDED.password_hash,
				name = EXCLUDED.name, external_id = EXCLUDED.external_id,
				firstname = EXCLUDED.firstname, lastname = EXCLUDED.lastname,
				gender_other = EXCLUDED.gender_other,
				-- Carried from the source rather than stamped with now(). It is
				-- a value the app displays, so overwriting it with the time of
				-- the migration is a visible lie about when the account changed.
				last_modified = EXCLUDED.last_modified`,
			id, email, password, name, gender, height, weight, dob, tzoff, created,
			extID, first, last, genderOK, lastModMS)
		if err != nil {
			return err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Keep the sequence ahead of the highest imported id, or the first new
	// account collides with an existing one.
	if _, err := m.dst.Exec(m.ctx,
		`SELECT setval('accounts_id_seq', GREATEST((SELECT COALESCE(MAX(id),1) FROM accounts), 1))`); err != nil {
		return err
	}
	m.note("accounts", n)
	return nil
}

func (m *migrator) oauth() error {
	appRows, err := m.src.Query(m.ctx,
		`SELECT id, name, client_id, client_secret, redirect_uri, scopes FROM oauth_applications`)
	if err != nil {
		return err
	}
	nApps := 0
	for appRows.Next() {
		var (
			id                           int64
			name, clientID, clientSecret string
			redirect                     *string
			scopes                       []int32
		)
		if err := appRows.Scan(&id, &name, &clientID, &clientSecret, &redirect, &scopes); err != nil {
			appRows.Close()
			return err
		}
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO oauth_applications (id, name, client_id, client_secret, redirect_uri, scopes)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`,
			id, name, clientID, clientSecret, redirect, scopes); err != nil {
			appRows.Close()
			return err
		}
		nApps++
	}
	appRows.Close()
	m.note("oauth_applications", nApps)

	// The app is holding one of these right now. Preserving access_token
	// verbatim is what keeps it signed in across the cutover.
	tokRows, err := m.src.Query(m.ctx, `
		SELECT access_token, refresh_token, account_id, app_id, scopes, created_at, expires_in
		FROM oauth_tokens`)
	if err != nil {
		return err
	}
	defer tokRows.Close()

	n := 0
	for tokRows.Next() {
		var (
			access, refresh string
			accountID       int64
			appID           int64
			scopes          []int32
			created         time.Time
			expiresIn       int64
		)
		if err := tokRows.Scan(&access, &refresh, &accountID, &appID, &scopes, &created, &expiresIn); err != nil {
			return err
		}
		_, err := m.dst.Exec(m.ctx, `
			INSERT INTO oauth_tokens (access_token, refresh_token, account_id, app_id, scopes, expires_at, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (access_token) DO NOTHING`,
			access, refresh, accountID, appID, scopes, created.Add(time.Duration(expiresIn)*time.Second), created)
		if err != nil {
			return err
		}
		n++
	}
	m.note("oauth_tokens", n)
	return tokRows.Err()
}

// ----------------------------------------------------------------- devices --

func (m *migrator) senses() error {
	keys, err := loadDump(m.dir, "key_store")
	if err != nil {
		return err
	}
	lastSeen, _ := loadDump(m.dir, "sense_last_seen")
	wifi, _ := loadDump(m.dir, "wifi_info")
	state, _ := loadDump(m.dir, "sense_state")

	// Index the satellite tables by device so each Sense becomes one row.
	seenAt := map[string]*time.Time{}
	fw := map[string]*int64{}
	for _, it := range lastSeen {
		id := it.str("sense_id")
		if ts, err := parseTS(it.str("updated_at_utc")); err == nil {
			seenAt[id] = &ts
		}
		fw[id] = it.intp("fw_version")
	}
	ssid := map[string]string{}
	rssi := map[string]*int64{}
	for _, it := range wifi {
		id := it.str("sense_id")
		ssid[id] = it.str("ssid")
		rssi[id] = it.intp("rssi")
	}
	stateJSON := map[string][]byte{}
	for _, it := range state {
		if id := it.str("sense_id"); id != "" {
			if b, err := json.Marshal(it); err == nil {
				stateJSON[id] = b
			}
		}
	}

	n := 0
	for _, it := range keys {
		id := it.str("device_id")
		if id == "" {
			continue
		}
		raw, err := hex.DecodeString(it.str("aes_key"))
		if err != nil {
			return fmt.Errorf("sense %s: aes_key not hex: %w", id, err)
		}
		var st any
		if b, ok := stateJSON[id]; ok {
			st = string(b)
		}
		_, err = m.dst.Exec(m.ctx, `
			INSERT INTO senses (device_id, aes_key, firmware_version, hw_version,
			                    last_seen_at, wifi_ssid, wifi_rssi, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (device_id) DO UPDATE SET
				aes_key = EXCLUDED.aes_key,
				firmware_version = COALESCE(EXCLUDED.firmware_version, senses.firmware_version),
				-- Monotonic; see the pills upsert for why this is not an
				-- assignment.
				last_seen_at = GREATEST(EXCLUDED.last_seen_at, senses.last_seen_at),
				wifi_ssid = COALESCE(EXCLUDED.wifi_ssid, senses.wifi_ssid),
				wifi_rssi = COALESCE(EXCLUDED.wifi_rssi, senses.wifi_rssi)`,
			id, raw, fw[id], it.str("hw_version"), seenAt[id],
			nullIfEmpty(ssid[id]), rssi[id], st)
		if err != nil {
			return err
		}
		n++
	}
	m.note("senses", n)
	return nil
}

func (m *migrator) pills() error {
	keys, err := loadDump(m.dir, "pill_key_store")
	if err != nil {
		return err
	}
	hb, _ := loadDump(m.dir, "pill_heartbeat")

	// Heartbeats are one row per beat; keep only the most recent per pill.
	type beat struct {
		battery, fw, uptime *int64
		at                  *time.Time
	}
	latest := map[string]beat{}
	for _, it := range hb {
		id := it.str("pill_id")
		if id == "" {
			id = it.str("device_id")
		}
		at := it.millis("created_at")
		if at == nil {
			if ts, err := parseTS(it.str("created_at")); err == nil {
				at = &ts
			}
		}
		cur, ok := latest[id]
		if !ok || (at != nil && cur.at != nil && at.After(*cur.at)) || cur.at == nil {
			latest[id] = beat{
				battery: it.intp("battery_level"),
				// "fw_version", not "firmware_version". The attribute is named
				// one way on the pill heartbeat and the other on the Sense, and
				// a missing DynamoDB attribute reads as absent rather than as
				// an error, so this silently left every pill's firmware null.
				// The app then displays no version at all on the pill's
				// settings row. Found by apidiff: java=3 orb=0.
				fw:     it.intp("fw_version"),
				uptime: it.intp("uptime"),
				at:     at,
			}
		}
	}

	n := 0
	for _, it := range keys {
		id := it.str("device_id")
		if id == "" {
			continue
		}
		raw, err := hex.DecodeString(it.str("aes_key"))
		if err != nil {
			return fmt.Errorf("pill %s: aes_key not hex: %w", id, err)
		}
		b := latest[id]
		_, err = m.dst.Exec(m.ctx, `
			INSERT INTO pills (pill_id, aes_key, battery_level, firmware_version, uptime_secs, last_seen_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (pill_id) DO UPDATE SET
				aes_key = EXCLUDED.aes_key,
				battery_level = EXCLUDED.battery_level,
				-- firmware_version was missing from this list, so even once the
				-- attribute name was corrected a re-run left the existing null
				-- in place. An insert-only column on an idempotent migrator is
				-- a column that can never be repaired.
				firmware_version = COALESCE(EXCLUDED.firmware_version, pills.firmware_version),
				uptime_secs = EXCLUDED.uptime_secs,
				-- GREATEST, not assignment. last_seen_at only moves forward,
				-- and the edge has been maintaining it live since the dump was
				-- taken. Overwriting sent the pill's "last seen" backwards by a
				-- day on every re-run, which reads on the app as a device that
				-- has stopped reporting.
				last_seen_at = GREATEST(EXCLUDED.last_seen_at, pills.last_seen_at)`,
			id, raw, b.battery, b.fw, b.uptime, b.at)
		if err != nil {
			return err
		}
		n++
	}
	m.note("pills", n)
	return nil
}

func (m *migrator) pairings() error {
	nS := 0
	rows, err := m.src.Query(m.ctx, `SELECT account_id, device_name, active FROM account_device_map`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID int64
		var deviceID string
		var active bool
		if err := rows.Scan(&accountID, &deviceID, &active); err != nil {
			rows.Close()
			return err
		}
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO account_senses (account_id, device_id, active) VALUES ($1,$2,$3)
			ON CONFLICT (account_id, device_id) DO UPDATE SET active = EXCLUDED.active`,
			accountID, deviceID, active); err != nil {
			rows.Close()
			return err
		}
		nS++
	}
	rows.Close()
	m.note("account_senses", nS)

	nP := 0
	rows, err = m.src.Query(m.ctx, `SELECT account_id, device_id, active FROM account_tracker_map`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID int64
		var pillID string
		var active bool
		if err := rows.Scan(&accountID, &pillID, &active); err != nil {
			return err
		}
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO account_pills (account_id, pill_id, active) VALUES ($1,$2,$3)
			ON CONFLICT (account_id, pill_id) DO UPDATE SET active = EXCLUDED.active`,
			accountID, pillID, active); err != nil {
			return err
		}
		nP++
	}
	m.note("account_pills", nP)
	return rows.Err()
}

func (m *migrator) timezones() error {
	items, err := loadDump(m.dir, "timezone_history")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		acct, ok := it.num("account_id")
		if !ok {
			continue
		}
		at := it.millis("updated_at_server_time_millis")
		if at == nil {
			continue
		}
		tzName := it.str("time_zone_name")
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			m.warnf("timezone_history: unknown zone %q, skipped", tzName)
			continue
		}
		_, offset := at.In(loc).Zone()
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO timezone_history (account_id, effective_from, timezone_id, offset_ms)
			VALUES ($1,$2,$3,$4) ON CONFLICT (account_id, effective_from) DO NOTHING`,
			int64(acct), *at, tzName, offset*1000); err != nil {
			return err
		}
		n++
	}
	m.note("timezone_history", n)
	return nil
}

// --------------------------------------------------------------- telemetry --

func (m *migrator) sensorSamples() error {
	items, err := loadShards(m.dir, "sense_data_")
	if err != nil {
		return err
	}

	batch := &pgx.Batch{}
	n, skipped := 0, 0
	for _, it := range items {
		tsStr, deviceID := splitKey(it.str("ts|dev"))
		if deviceID == "" {
			skipped++
			continue
		}
		ts, err := parseTS(tsStr)
		if err != nil {
			skipped++
			continue
		}
		acct, ok := it.num("aid")
		if !ok {
			skipped++
			continue
		}
		off, _ := it.num("om")

		batch.Queue(`
			INSERT INTO sensor_samples (device_id, ts, account_id, offset_ms,
				temperature, humidity, light, light_variance, air_quality_raw,
				audio_peak_background_db, audio_peak_energy_db, audio_peak_disturbances_db,
				audio_num_disturbances, wave_count, hold_count,
				pressure, tvoc, co2, ir, clear, lux_count, uv_count)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
			ON CONFLICT (device_id, ts) DO NOTHING`,
			deviceID, ts, int64(acct), int32(off),
			it.intp("tmp"), it.intp("hum"), it.intp("lite"), it.intp("litevar"), it.intp("aqr"),
			it.intp("apbg"), it.intp("apedb"), it.intp("apd"),
			it.intp("and"), it.intp("wc"), it.intp("hc"),
			it.intp("pa"), it.intp("tvoc"), it.intp("co2"),
			it.intp("ir"), it.intp("clear"), it.intp("lux"), it.intp("uv"))
		n++
	}

	if err := m.sendBatch(batch); err != nil {
		return err
	}
	if skipped > 0 {
		m.warnf("sensor_samples: skipped %d rows with unparseable key/account", skipped)
	}
	m.note("sensor_samples", n)
	return nil
}

func (m *migrator) pillSamples() error {
	items, err := loadShards(m.dir, "pill_data_")
	if err != nil {
		return err
	}

	batch := &pgx.Batch{}
	n, skipped := 0, 0
	for _, it := range items {
		tsStr, pillID := splitKey(it.str("ts|pil"))
		if pillID == "" {
			skipped++
			continue
		}
		ts, err := parseTS(tsStr)
		if err != nil {
			skipped++
			continue
		}
		acct, ok := it.num("aid")
		if !ok {
			skipped++
			continue
		}
		off, _ := it.num("om")

		batch.Queue(`
			INSERT INTO pill_samples (pill_id, ts, account_id, offset_ms,
				svm_no_gravity, motion_range, kickoff_counts, on_duration_secs,
				cos_theta, motion_mask)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (pill_id, ts) DO NOTHING`,
			pillID, ts, int64(acct), int32(off),
			it.intp("val"), it.intp("mr"), it.intp("kc"), it.intp("od"),
			it.intp("cosT"), it.intp("mask"))
		n++
	}

	if err := m.sendBatch(batch); err != nil {
		return err
	}
	if skipped > 0 {
		m.warnf("pill_samples: skipped %d rows with unparseable key/account", skipped)
	}
	m.note("pill_samples", n)
	return nil
}

func (m *migrator) sendBatch(b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	res := m.dst.SendBatch(m.ctx, b)
	defer res.Close()
	for i := 0; i < b.Len(); i++ {
		if _, err := res.Exec(); err != nil {
			return fmt.Errorf("batch item %d: %w", i, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- timeline --

func (m *migrator) feedback() error {
	rows, err := m.src.Query(m.ctx, `
		SELECT account_id, date_of_night, old_time, new_time, event_type,
		       COALESCE(is_correct, true), COALESCE(sleep_period, 2), created
		FROM timeline_feedback`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var (
			accountID         int64
			night             time.Time
			oldT, newT        string
			eventType, period int32
			isCorrect         bool
			created           time.Time
		)
		if err := rows.Scan(&accountID, &night, &oldT, &newT, &eventType, &isCorrect, &period, &created); err != nil {
			return err
		}
		// Deduplicated on the natural key, not the surrogate id.
		//
		// This table's primary key is a BIGSERIAL, so a plain INSERT duplicates
		// every row on a re-run, and this migrator is explicitly documented as
		// idempotent. Duplicated feedback is worse than duplicated anything else
		// here: LabelMaker builds HMM training labels from these rows, so a
		// doubled correction double-weights itself against the model, and the
		// symptom would be a model that drifts for no visible reason.
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO timeline_feedback (account_id, date_of_night, event_type,
			                               old_time, new_time, sleep_period, is_correct, created_at)
			SELECT $1,$2,$3,$4::time,$5::time,$6,$7,$8
			WHERE NOT EXISTS (
				SELECT 1 FROM timeline_feedback
				WHERE account_id = $1 AND date_of_night = $2 AND event_type = $3
				  AND old_time = $4::time AND new_time = $5::time
			)`,
			accountID, night, eventType, oldT, newT, period, isCorrect, created); err != nil {
			return err
		}
		n++
	}
	m.note("timeline_feedback", n)
	return rows.Err()
}

func (m *migrator) timelineEvents() error {
	items, err := loadDump(m.dir, "main_event_times")
	if err != nil {
		return err
	}
	algos := map[int]string{1: "NONE", 2: "VOTING", 3: "HMM", 4: "ONLINE_HMM", 5: "NEURAL_NET"}

	n := 0
	for _, it := range items {
		acct, ok := it.num("account")
		if !ok {
			continue
		}
		dateStr, period := splitKey(it.str("date|sleep_period"))
		night, err := parseTS(dateStr)
		if err != nil {
			continue
		}
		p := 2
		if strings.EqualFold(period, "morning") {
			p = 0
		} else if strings.EqualFold(period, "afternoon") {
			p = 1
		}
		algo := ""
		if a, ok := it.num("algorithm_type"); ok {
			algo = algos[int(a)]
		}
		off, _ := it.num("in_bed_offset")

		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO timeline_events (account_id, date_of_night, sleep_period,
				in_bed_at, sleep_at, wake_up_at, out_of_bed_at, algorithm, offset_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			-- DO NOTHING, not DO UPDATE. This is derived data that orb computes
			-- for itself once it is running, and the dumps are a snapshot from
			-- whenever they were taken. Overwriting means a re-run silently
			-- replaces nights orb has scored with older answers from the Java
			-- stack, which is exactly what happened on 2026-08-15: three
			-- nights reverted, including one that had been rescored after the
			-- timestamp and calibration fixes. Seeding an empty table is the
			-- job here; owning the table afterwards is not.
			ON CONFLICT (account_id, date_of_night, sleep_period) DO NOTHING`,
			int64(acct), night, p,
			it.millis("in_bed_time"), it.millis("sleep_time"),
			it.millis("wake_up_time"), it.millis("out_of_bed_time"),
			nullIfEmpty(algo), int32(off)); err != nil {
			return err
		}
		n++
	}
	m.note("timeline_events", n)
	return nil
}

func (m *migrator) sleepStats() error {
	items, err := loadDump(m.dir, "sleep_stats_v_0_2")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		acct, ok := it.num("account_id")
		if !ok {
			continue
		}
		night, err := parseTS(it.str("date"))
		if err != nil {
			continue
		}
		// Everything not promoted to a column is kept verbatim, so nothing is
		// silently dropped just because this schema did not anticipate it.
		extra, _ := json.Marshal(it)

		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO sleep_stats (account_id, date_of_night, sleep_score,
				sleep_duration_mins, sound_sleep_mins, light_sleep_mins, medium_sleep_mins,
				times_awake, sleep_onset_mins, stats)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			-- Derived data, same reasoning as timeline_events above.
			ON CONFLICT (account_id, date_of_night) DO NOTHING`,
			int64(acct), night,
			it.intp("score"), it.intp("sleep_duration"), it.intp("sound_sleep"),
			it.intp("light_sleep"), it.intp("medium_sleep"),
			it.intp("num_motions"), it.intp("sleep_onset_minutes"), string(extra)); err != nil {
			return err
		}
		n++
	}
	m.note("sleep_stats", n)
	return nil
}

func (m *migrator) aggStats() error {
	items, err := loadDump(m.dir, "agg_stats_v_0_1")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		// This table names its account "aid" and keys the night as
		// "date_local|sense_id", unlike sleep_stats next door which uses
		// "account_id" and "date". Same codebase, same era, different spelling.
		acct, ok := it.num("aid")
		if !ok {
			continue
		}
		dateStr, _ := splitKey(it.str("date_local|sense_id"))
		night, err := parseTS(dateStr)
		if err != nil {
			continue
		}
		blob, _ := json.Marshal(it)
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO agg_stats (account_id, date_of_night, stats) VALUES ($1,$2,$3)
			ON CONFLICT (account_id, date_of_night) DO UPDATE SET stats = EXCLUDED.stats, updated_at = now()`,
			int64(acct), night, string(blob)); err != nil {
			return err
		}
		n++
	}
	m.note("agg_stats", n)
	return nil
}

func (m *migrator) hmmModels() error {
	items, err := loadDump(m.dir, "online_hmm_models")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		// account_id is stored as a STRING here, unlike everywhere else.
		acctStr := it.str("account_id")
		if acctStr == "" {
			if f, ok := it.num("account_id"); ok {
				acctStr = strconv.FormatInt(int64(f), 10)
			}
		}
		acct, err := strconv.ParseInt(acctStr, 10, 64)
		if err != nil {
			continue
		}
		night, err := parseTS(it.str("date"))
		if err != nil {
			continue
		}
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO hmm_models (account_id, date_of_night, model_params, scratchpad)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (account_id, date_of_night) DO UPDATE SET
				model_params = EXCLUDED.model_params, scratchpad = EXCLUDED.scratchpad, updated_at = now()`,
			acct, night, it.bytes("model_params"), it.bytes("scratchpad")); err != nil {
			return err
		}
		n++
	}
	m.note("hmm_models", n)
	return nil
}

func (m *migrator) alarms() error {
	items, err := loadDump(m.dir, "alarm")
	if err != nil {
		return err
	}
	// alarm_templates is a JSON array of the app's own alarm objects. Keep each
	// verbatim in `definition`: the app expects its own shape echoed back, and
	// re-deriving it from columns is how subtle mismatches appear.
	type tmpl struct {
		Hour      int   `json:"hour"`
		Minute    int   `json:"minute"`
		DayOfWeek []int `json:"day_of_week"`
		Repeated  bool  `json:"repeated"`
		Enabled   bool  `json:"enabled"`
		Smart     bool  `json:"smart"`
		Sound     struct {
			ID int `json:"id"`
		} `json:"sound"`
	}

	// The DynamoDB `alarm` table is versioned: one item per account per edit,
	// each holding the WHOLE alarm list as it stood at that moment. Importing
	// every item unions every alarm that has ever existed, so a deleted alarm
	// comes back from the dead. suripu reads only the newest item, and this has
	// to as well.
	//
	// Keep the highest `updated_at` per account. It is stored as N (epoch
	// millis), so it must be read as a number: reading it as a string returns
	// "" for every item, every comparison is then false, and the map silently
	// keeps whichever item happened to come first. That produced a plausible
	// answer with the wrong alarm in it.
	newest := map[int64]item{}
	newestKey := map[int64]float64{}
	for _, it := range items {
		acct, ok := it.num("account_id")
		if !ok {
			continue
		}
		ts, ok := it.num("updated_at")
		if !ok {
			continue
		}
		id := int64(acct)
		if cur, seen := newestKey[id]; !seen || ts > cur {
			newestKey[id] = ts
			newest[id] = it
		}
	}

	n := 0
	for _, it := range newest {
		acct, ok := it.num("account_id")
		if !ok {
			continue
		}
		raw := it.str("alarm_templates")
		if raw == "" {
			continue
		}
		var list []tmpl
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			m.warnf("alarms: account %d templates unparseable, skipped", int64(acct))
			continue
		}
		var defs []json.RawMessage
		_ = json.Unmarshal([]byte(raw), &defs)

		for i, t := range list {
			var def any
			if i < len(defs) {
				def = string(defs[i])
			}
			dow := make([]int32, 0, len(t.DayOfWeek))
			for _, d := range t.DayOfWeek {
				dow = append(dow, int32(d))
			}
			// Keyed on the alarm's own id, which lives inside the definition
			// blob. Without this the insert has no conflict target at all and
			// every re-run appends the whole set again: eight runs had produced
			// twenty-four rows for three alarms, and the app would have shown
			// each alarm eight times.
			if _, err := m.dst.Exec(m.ctx, `
				INSERT INTO alarms (account_id, enabled, smart, repeated, hour, minute,
				                    day_of_week, sound_id, definition)
				SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb
				WHERE NOT EXISTS (
					SELECT 1 FROM alarms
					WHERE account_id = $1
					  AND definition->>'id' = ($9::jsonb)->>'id')`,
				int64(acct), t.Enabled, t.Smart, t.Repeated, t.Hour, t.Minute,
				dow, t.Sound.ID, def); err != nil {
				return err
			}
			n++
		}
	}
	m.note("alarms", n)
	return nil
}

func (m *migrator) appStats() error {
	items, err := loadDump(m.dir, "app_stats")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		acct, ok := it.num("account_id")
		if !ok {
			continue
		}
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO app_stats (account_id, insights_last_viewed, questions_last_viewed)
			VALUES ($1,$2,$3)
			ON CONFLICT (account_id) DO UPDATE SET
				insights_last_viewed = EXCLUDED.insights_last_viewed,
				questions_last_viewed = EXCLUDED.questions_last_viewed, updated_at = now()`,
			int64(acct), it.millis("insights_last_viewed"), it.millis("questions_last_viewed")); err != nil {
			return err
		}
		n++
	}
	m.note("app_stats", n)
	return nil
}

// insightCategories copies the category display names out of the `insights`
// database.
//
// `info_insight_cards` holds several rows per category, one per piece of
// detail copy, and every one of them repeats the same category_name. DISTINCT
// rather than a per-row insert, because the duplicates are not data.
func (m *migrator) insightCategories() error {
	rows, err := m.ins.Query(m.ctx, `
		SELECT DISTINCT category::text, category_name
		FROM info_insight_cards
		WHERE category_name IS NOT NULL AND category_name <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type pair struct{ category, name string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.category, &p.name); err != nil {
			return err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	n := 0
	for _, p := range pairs {
		// The source column is the enum name already, but in the database's own
		// case. Upper so the lookup on the way out has one spelling to match.
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO insight_categories (category, category_name)
			VALUES ($1,$2)
			ON CONFLICT (category) DO UPDATE SET category_name = EXCLUDED.category_name`,
			strings.ToUpper(p.category), p.name); err != nil {
			return err
		}
		n++
	}
	m.note("insight_categories", n)
	return nil
}

// insights copies the account's insight cards out of DynamoDB.
//
// ON CONFLICT DO NOTHING on the card's own UUID: these are generated, not
// authored, and re-running the migrator must not resurrect a card the account
// has since seen or reorder the feed. Same reasoning as timeline_events.
func (m *migrator) insights() error {
	items, err := loadDump(m.dir, "insights")
	if err != nil {
		return err
	}
	n := 0
	for _, it := range items {
		acct, ok := it.num("account_id")
		if !ok {
			continue
		}
		uuid := it.str("id")
		if uuid == "" {
			m.warnf("insight with no id for account %d, skipped", int64(acct))
			continue
		}
		// timestamp_utc is an ISO string, not millis. Two other tables in this
		// migrator store the same idea the other way round, and reading it as
		// millis yields the zero time rather than an error.
		ts, err := parseTS(it.str("timestamp_utc"))
		if err != nil {
			m.warnf("insight %s: %v", uuid, err)
			continue
		}
		ordinal := 0
		if v, ok := it.num("category"); ok {
			ordinal = int(v)
		}
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO insights (account_id, uuid, category, insight_type, title,
				message, timestamp, seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,false)
			ON CONFLICT (uuid) DO NOTHING`,
			int64(acct), uuid, insightCategory(ordinal), it.str("insight_type"),
			it.str("title"), it.str("message"), ts); err != nil {
			return err
		}
		n++
	}
	m.note("insights", n)
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// questions copies the questionnaire out of the `insights` database.
//
// Four tables in dependency order: the catalogue, its choices, what was asked
// of whom, and what came back. The catalogue and the choices carry their source
// ids rather than new ones, because the app posts those ids back and a
// renumbering would make every stored answer point at the wrong question.
//
// `account_question_ask_time` is not copied. It is empty, and it belongs to the
// per-account scheduling this port does not reproduce.
func (m *migrator) questions() error {
	// The catalogue. Enum columns are cast to text: orb stores them as TEXT,
	// see migration 0007.
	rows, err := m.ins.Query(m.ctx, `
		SELECT id, COALESCE(parent_id, 0), COALESCE(question_text, ''),
		       COALESCE(lang, 'EN'), COALESCE(frequency::text, ''),
		       COALESCE(response_type::text, ''),
		       COALESCE(responses, '{}'), COALESCE(responses_ids, '{}'),
		       dependency, COALESCE(dependency_response, '{}'),
		       COALESCE(ask_time::text, ''), COALESCE(account_info::text, ''),
		       COALESCE(category::text, 'none'), created
		FROM questions ORDER BY id`)
	if err != nil {
		return err
	}
	type q struct {
		id, parentID               int32
		text, lang, freq, respType string
		responses                  []string
		responseIDs                []int32
		dependency                 *int32
		dependencyResponse         []int32
		askTime, accountInfo, cat  string
		created                    time.Time
	}
	var qs []q
	for rows.Next() {
		var v q
		if err := rows.Scan(&v.id, &v.parentID, &v.text, &v.lang, &v.freq,
			&v.respType, &v.responses, &v.responseIDs, &v.dependency,
			&v.dependencyResponse, &v.askTime, &v.accountInfo, &v.cat, &v.created); err != nil {
			rows.Close()
			return err
		}
		qs = append(qs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, v := range qs {
		if _, err := m.dst.Exec(m.ctx, `
			INSERT INTO questions (id, parent_id, question_text, lang, frequency,
			                       response_type, responses, responses_ids,
			                       dependency, dependency_response, ask_time,
			                       account_info, category, created)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,
			        NULLIF($11,''),NULLIF($12,''),$13,$14)
			ON CONFLICT (id) DO NOTHING`,
			v.id, v.parentID, v.text, v.lang, v.freq, v.respType,
			v.responses, v.responseIDs, v.dependency, v.dependencyResponse,
			v.askTime, v.accountInfo, v.cat, v.created); err != nil {
			return fmt.Errorf("question %d: %w", v.id, err)
		}
	}
	log.Printf("  questions: %d", len(qs))

	n, err := m.copyRows(`SELECT id, question_id, response_text, created FROM response_choices ORDER BY id`,
		`INSERT INTO response_choices (id, question_id, response_text, created)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO NOTHING`, 4)
	if err != nil {
		return err
	}
	log.Printf("  response_choices: %d", n)

	n, err = m.copyRows(`
		SELECT id, account_id, question_id, created_local_utc_ts,
		       expires_local_utc_ts, created
		FROM account_questions ORDER BY id`,
		`INSERT INTO account_questions (id, account_id, question_id,
		     created_local_utc_ts, expires_local_utc_ts, created)
		 VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`, 6)
	if err != nil {
		return err
	}
	log.Printf("  account_questions: %d", n)

	n, err = m.copyRows(`
		SELECT id, account_id, question_id, COALESCE(account_question_id,0),
		       COALESCE(response_id,0), COALESCE(skip,false),
		       COALESCE(question_freq::text,''), created
		FROM responses ORDER BY id`,
		`INSERT INTO question_responses (id, account_id, question_id,
		     account_question_id, response_id, skip, question_freq, created)
		 VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8) ON CONFLICT (id) DO NOTHING`, 8)
	if err != nil {
		return err
	}
	log.Printf("  question_responses: %d", n)

	// The sequences own ids that were just inserted explicitly, so a later
	// insert would collide on the primary key. Resetting them is not optional
	// and the failure only appears when somebody answers a question.
	for _, s := range []string{"account_questions", "question_responses"} {
		if _, err := m.dst.Exec(m.ctx, fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','id'),
			                GREATEST((SELECT COALESCE(MAX(id),0) FROM %s), 1))`, s, s)); err != nil {
			return err
		}
	}
	return nil
}

// copyRows moves rows from the insights database to orb verbatim.
//
// Generic over column count because these four tables differ only in width;
// anything that needed a transform is written out longhand above instead.
func (m *migrator) copyRows(src, dst string, cols int) (int, error) {
	rows, err := m.ins.Query(m.ctx, src)
	if err != nil {
		return 0, err
	}
	var all [][]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			rows.Close()
			return 0, err
		}
		if len(vals) != cols {
			rows.Close()
			return 0, fmt.Errorf("expected %d columns, got %d", cols, len(vals))
		}
		all = append(all, vals)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, v := range all {
		if _, err := m.dst.Exec(m.ctx, dst, v...); err != nil {
			return 0, err
		}
	}
	return len(all), nil
}

// calibration copies the per-device dust calibration.
//
// Its own step rather than part of `senses`, because the calibration table is
// written by the factory test rig and can be updated long after a device row
// exists. Re-running this picks up a re-test without touching anything else.
//
// The STORED value is copied, not the derived delta. The reference keeps
// `dust_offset` and derives `round(300 - offset * 1.3)` at read time, so
// storing the delta here would leave a number in the database that matches
// nothing in the reference and drifts the moment the formula changes.
//
// Without this, the air quality dial reads roughly 40 units high: an
// uncalibrated device gets no delta, and this one's offset of 395 implies -213
// counts. That is how air quality came to be quietly wrong rather than quietly
// absent.
func (m *migrator) calibration() error {
	items, err := loadDump(m.dir, "calibration")
	if err != nil {
		return err
	}
	var n int
	for _, it := range items {
		senseID := it.str("sense_id")
		if senseID == "" {
			continue
		}
		offset, ok := it.num("dust_offset")
		if !ok {
			continue
		}
		tag, err := m.dst.Exec(m.ctx,
			`UPDATE senses SET dust_offset = $2 WHERE device_id = $1`, senseID, int32(offset))
		if err != nil {
			return fmt.Errorf("calibration %s: %w", senseID, err)
		}
		n += int(tag.RowsAffected())
	}
	log.Printf("  calibration: %d senses", n)
	return nil
}
