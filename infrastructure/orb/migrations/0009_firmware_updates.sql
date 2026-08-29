-- Over-the-air firmware updates for the Sense.
--
-- This table is the safety mechanism, not a convenience. orb offers an update
-- ONLY when a row here names a specific device and is explicitly armed. No row
-- means no OTA, which is how orb behaved before this existed and is what every
-- device gets by default.
--
-- The reference decides OTA from feature flags and device groups, which is a
-- reasonable design for a fleet and a bad one for a project with a single
-- irreplaceable unit: a flag flipped for a group is an update nobody
-- deliberately aimed at this device. Here the aim is the row.
--
-- from_version is the guard against re-flashing. An update is offered only to a
-- device reporting that exact version, so once the device comes back on
-- to_version it stops matching and the offer ends by itself.
CREATE TABLE firmware_updates (
    id              BIGSERIAL PRIMARY KEY,
    device_id       TEXT NOT NULL REFERENCES senses(device_id) ON DELETE CASCADE,

    -- Only offer to a device currently reporting this version. Null means any,
    -- which should be used with care and is not the normal case.
    from_version    INTEGER,
    -- What the device should be running afterwards. Used to recognise success.
    to_version      INTEGER NOT NULL,

    -- Where the device fetches the image. Split because the protocol splits it:
    -- host is the Host header, url is the path and query.
    host            TEXT NOT NULL,
    url             TEXT NOT NULL,

    -- Integrity. Both are required: the device checks them, and an image served
    -- without them is an image nothing verifies.
    sha1            BYTEA NOT NULL,
    file_size       INTEGER NOT NULL,

    -- Where it lands and what gets reset afterwards. Defaults match the
    -- reference's ota_file_settings for the main firmware image.
    copy_to_serial_flash        BOOLEAN NOT NULL DEFAULT true,
    reset_application_processor BOOLEAN NOT NULL DEFAULT true,
    reset_network_processor     BOOLEAN NOT NULL DEFAULT false,
    serial_flash_filename       TEXT,
    serial_flash_path           TEXT,
    sd_card_filename            TEXT,
    sd_card_path                TEXT,

    -- Nothing is offered until this is true. Defaults to false so that
    -- inserting a row is preparation, and arming it is the decision.
    armed           BOOLEAN NOT NULL DEFAULT false,

    -- Observability for a process that is otherwise invisible until it either
    -- works or bricks something.
    offer_count     INTEGER NOT NULL DEFAULT 0,
    first_offered_at TIMESTAMPTZ,
    last_offered_at TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One armed update per device at a time. Two rows racing to flash the same
-- device is not a situation worth having a policy for.
CREATE UNIQUE INDEX firmware_updates_one_armed_per_device
    ON firmware_updates (device_id) WHERE armed AND completed_at IS NULL;
