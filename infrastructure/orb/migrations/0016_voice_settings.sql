-- Per-account, per-Sense voice settings for the Sense with Voice, was a
-- DynamoDB table in the reference. The app reads and writes these through
-- GET/PATCH /v2/devices/sense/{id}/voice; the device learns its volume and
-- mute state from the sync response (that push is a later piece of work).
--
-- Defaults match the reference's: full volume, unmuted, and NOT the primary
-- user (the first account to pair claims primary explicitly).
CREATE TABLE voice_settings (
    account_id  BIGINT  NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id   TEXT    NOT NULL REFERENCES senses(device_id) ON DELETE CASCADE,
    volume      INTEGER NOT NULL DEFAULT 100,
    muted       BOOLEAN NOT NULL DEFAULT false,
    is_primary  BOOLEAN NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, device_id)
);
