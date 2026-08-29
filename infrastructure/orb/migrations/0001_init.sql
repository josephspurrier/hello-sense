-- orb: the consolidated schema. One database replacing Postgres x3 + DynamoDB x67.
--
-- Design notes, because the shape here is deliberately unlike the original:
--
--   * No monthly sharding. DynamoDB sharded sense_data by month to spread
--     provisioned throughput. Postgres has no such problem, and the real volume
--     is ~2,255 rows/day, so ~800k rows/year. A single table with a btree on
--     (device_id, ts) handles that without noticing.
--
--   * Column names are spelled out. The DynamoDB names were three-letter
--     abbreviations to save per-item bytes at scale ("apedb", "litevar",
--     "aqr"). That cost is meaningless here and the abbreviations are a
--     standing readability tax. Original names are kept in comments so a value
--     can always be traced back.
--
--   * Sensor values keep their original integer scaling. Temperature and
--     humidity arrive multiplied by 100, light is already scaled. Converting on
--     the way in would silently change what the algorithms see, and the Java
--     algo service still consumes these. Convert at the edge of the API, not in
--     storage.
--
--   * Timestamps are timestamptz, stored UTC. The original carried a local time
--     STRING (lutcts) alongside the UTC key plus an offset_millis, because
--     DynamoDB cannot do timezone-aware range queries. Postgres can, so local
--     time becomes derived. offset_millis is retained: it is what the device
--     reported at the time, and recomputing it later from a tz database would
--     silently rewrite history across DST changes.
--
--   * No feature flags table. 21 rows of flags gating code paths becomes
--     config in the Go binary. See knowledgebase/TIMELINE-ALGORITHMS.md for
--     what each one did.
--
--   * No KCL lease tables. They exist only to coordinate Kinesis consumers,
--     and Kinesis is being removed.

BEGIN;

-- ---------------------------------------------------------------- accounts --

CREATE TABLE accounts (
    id              BIGSERIAL PRIMARY KEY,
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    name            TEXT,
    gender          TEXT,
    height_cm       INTEGER,
    weight_grams    INTEGER,
    birthdate       DATE,
    tz_offset_ms    INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_modified   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The iOS app holds a bearer token it obtained before this migration. Keep the
-- token values byte-identical or the app is signed out and must re-pair.
CREATE TABLE oauth_applications (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    client_id       TEXT NOT NULL UNIQUE,
    client_secret   TEXT NOT NULL,
    redirect_uri    TEXT,
    scopes          INTEGER[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oauth_tokens (
    id              BIGSERIAL PRIMARY KEY,
    access_token    TEXT NOT NULL UNIQUE,
    refresh_token   TEXT NOT NULL,
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    app_id          BIGINT NOT NULL REFERENCES oauth_applications(id),
    scopes          INTEGER[] NOT NULL DEFAULT '{}',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX oauth_tokens_account_idx ON oauth_tokens (account_id);

-- ----------------------------------------------------------------- devices --

-- The Sense. device_id is the 16-hex-char external id burned into the unit
-- (e.g. 49F277D951568DF3) and is what every device request identifies itself
-- by, so it is the natural key rather than a surrogate.
CREATE TABLE senses (
    device_id       TEXT PRIMARY KEY,
    aes_key         BYTEA NOT NULL,             -- was DynamoDB key_store
    firmware_version INTEGER,
    hw_version      TEXT,
    last_seen_at    TIMESTAMPTZ,                -- was sense_last_seen
    wifi_ssid       TEXT,                       -- was wifi_info
    wifi_rssi       INTEGER,
    state           JSONB,                      -- was sense_state
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pills (
    pill_id         TEXT PRIMARY KEY,
    aes_key         BYTEA,                      -- was pill_key_store
    battery_level   INTEGER,                    -- was pill_heartbeat
    firmware_version INTEGER,
    uptime_secs     BIGINT,
    last_seen_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Pairings. Kept as rows rather than columns because a device can be re-paired,
-- and the history is what lets old data stay attributable.
CREATE TABLE account_senses (
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id       TEXT NOT NULL REFERENCES senses(device_id) ON DELETE CASCADE,
    active          BOOLEAN NOT NULL DEFAULT true,
    paired_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, device_id)
);

CREATE TABLE account_pills (
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    pill_id         TEXT NOT NULL REFERENCES pills(pill_id) ON DELETE CASCADE,
    active          BOOLEAN NOT NULL DEFAULT true,
    paired_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, pill_id)
);

-- ------------------------------------------------------------- timezone ----

-- Retained as history, not a single current value: a sample must be rendered
-- with the offset that applied when it was taken, not today's.
CREATE TABLE timezone_history (
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    effective_from  TIMESTAMPTZ NOT NULL,
    timezone_id     TEXT NOT NULL,              -- e.g. America/New_York
    offset_ms       INTEGER NOT NULL,
    PRIMARY KEY (account_id, effective_from)
);

-- -------------------------------------------------------------- telemetry --

-- was sense_data_YYYY_MM. Abbreviated DynamoDB names in comments.
CREATE TABLE sensor_samples (
    device_id           TEXT NOT NULL REFERENCES senses(device_id) ON DELETE CASCADE,
    ts                  TIMESTAMPTZ NOT NULL,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    offset_ms           INTEGER NOT NULL,       -- om
    temperature         INTEGER,                -- tmp,     centi-degrees C
    humidity            INTEGER,                -- hum,     centi-percent
    light               INTEGER,                -- lite
    light_variance      INTEGER,                -- litevar
    air_quality_raw     INTEGER,                -- aqr
    audio_peak_background_db INTEGER,           -- apbg
    audio_peak_energy_db     INTEGER,           -- apedb
    audio_peak_disturbances_db INTEGER,         -- apd
    audio_num_disturbances     INTEGER,         -- and
    wave_count          INTEGER,                -- wc
    hold_count          INTEGER,                -- hc
    -- Sense 1.5 sensors. Null on a Sense 1.0; kept so the schema does not need
    -- changing if a 1.5 is ever paired.
    pressure            INTEGER,                -- pa
    tvoc                INTEGER,                -- tvoc
    co2                 INTEGER,                -- co2
    ir                  INTEGER,                -- ir
    clear               INTEGER,                -- clear
    lux_count           INTEGER,                -- lux
    uv_count            INTEGER,                -- uv
    PRIMARY KEY (device_id, ts)
);

-- The two access patterns: "this device over a window" (covered by the PK) and
-- "this account over a window", which the timeline generator does per night.
CREATE INDEX sensor_samples_account_ts_idx ON sensor_samples (account_id, ts DESC);

-- was pill_data_YYYY_MM
CREATE TABLE pill_samples (
    pill_id             TEXT NOT NULL REFERENCES pills(pill_id) ON DELETE CASCADE,
    ts                  TIMESTAMPTZ NOT NULL,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    offset_ms           INTEGER NOT NULL,       -- om
    svm_no_gravity      BIGINT,                 -- val, motion amplitude
    motion_range        BIGINT,                 -- mr
    kickoff_counts      INTEGER,                -- kc
    on_duration_secs    INTEGER,                -- od
    cos_theta           INTEGER,                -- cosT
    motion_mask         BIGINT,                 -- mask
    PRIMARY KEY (pill_id, ts)
);
CREATE INDEX pill_samples_account_ts_idx ON pill_samples (account_id, ts DESC);

-- --------------------------------------------------------------- timeline --

-- A corrected event. This is the table that must not be lost: it is the only
-- human-supplied ground truth in the system, and it trains the HMM.
-- See knowledgebase/TIMELINE-ALGORITHMS.md: correcting one end of a pair
-- teaches a one-sided path, so both ends matter.
CREATE TABLE timeline_feedback (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date_of_night   DATE NOT NULL,
    event_type      INTEGER NOT NULL,           -- 11 IN_BED, 12 SLEEP, 13 OUT_OF_BED, 14 WAKE_UP
    old_time        TIME NOT NULL,
    new_time        TIME NOT NULL,
    sleep_period    INTEGER NOT NULL DEFAULT 2, -- 2 = NIGHT
    is_correct      BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX timeline_feedback_account_night_idx
    ON timeline_feedback (account_id, date_of_night);

-- The computed events for a night. was main_event_times.
CREATE TABLE timeline_events (
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date_of_night   DATE NOT NULL,
    sleep_period    INTEGER NOT NULL DEFAULT 2,
    in_bed_at       TIMESTAMPTZ,
    sleep_at        TIMESTAMPTZ,
    wake_up_at      TIMESTAMPTZ,
    out_of_bed_at   TIMESTAMPTZ,
    algorithm       TEXT,                       -- ONLINE_HMM | VOTING | HMM
    offset_ms       INTEGER,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, date_of_night, sleep_period)
);

-- was sleep_stats_v_0_2
CREATE TABLE sleep_stats (
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date_of_night       DATE NOT NULL,
    sleep_score         INTEGER,
    sleep_duration_mins INTEGER,
    sound_sleep_mins    INTEGER,
    light_sleep_mins    INTEGER,
    medium_sleep_mins   INTEGER,
    times_awake         INTEGER,
    sleep_onset_mins    INTEGER,
    stats               JSONB,                  -- the rest, rather than 20 more columns
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, date_of_night)
);

-- was agg_stats_v_0_1
CREATE TABLE agg_stats (
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date_of_night   DATE NOT NULL,
    stats           JSONB NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, date_of_night)
);

-- The learned per-user HMM. Blobs, because the algo service owns their format
-- (protobuf) and this schema should not pretend to understand it.
-- Deleting a row here reverts that account to the default ensemble.
CREATE TABLE hmm_models (
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date_of_night   DATE NOT NULL,
    model_params    BYTEA,
    scratchpad      BYTEA,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, date_of_night)
);

-- ------------------------------------------------------------- app-facing --

CREATE TABLE alarms (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id       TEXT REFERENCES senses(device_id) ON DELETE CASCADE,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    smart           BOOLEAN NOT NULL DEFAULT false,
    repeated        BOOLEAN NOT NULL DEFAULT false,
    hour            INTEGER NOT NULL,
    minute          INTEGER NOT NULL,
    day_of_week     INTEGER[] NOT NULL DEFAULT '{}',
    ring_at         TIMESTAMPTZ,
    sound_id        INTEGER,
    definition      JSONB,                      -- the app's full alarm object, echoed back verbatim
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX alarms_account_idx ON alarms (account_id);

CREATE TABLE insights (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    category        TEXT NOT NULL,
    title           TEXT,
    message         TEXT,
    timestamp       TIMESTAMPTZ NOT NULL,
    seen            BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX insights_account_ts_idx ON insights (account_id, timestamp DESC);

CREATE TABLE app_stats (
    account_id          BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    insights_last_viewed TIMESTAMPTZ,
    questions_last_viewed TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sleep sounds live on the Sense's SD card; these tables only track what is
-- available and what is selected.
CREATE TABLE sleep_sounds (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    preview_url     TEXT
);

CREATE TABLE sleep_sound_settings (
    device_id       TEXT PRIMARY KEY REFERENCES senses(device_id) ON DELETE CASCADE,
    sound_id        INTEGER,
    duration_id     INTEGER,
    volume_percent  INTEGER,
    playing         BOOLEAN NOT NULL DEFAULT false,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --------------------------------------------------------- device command --

-- Replaces messeji (Clojure + Redis). A queue of commands for the Sense, which
-- long-polls /receive. One device, so a table with a partial index is ample.
CREATE TABLE device_messages (
    id              BIGSERIAL PRIMARY KEY,
    device_id       TEXT NOT NULL REFERENCES senses(device_id) ON DELETE CASCADE,
    payload         BYTEA NOT NULL,             -- serialized protobuf
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ
);
-- Partial: the long-poll only ever asks for undelivered messages, and delivered
-- rows are the overwhelming majority over time.
CREATE INDEX device_messages_undelivered_idx
    ON device_messages (device_id, created_at)
    WHERE delivered_at IS NULL;

COMMIT;
