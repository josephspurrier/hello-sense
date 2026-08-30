-- Per-account notification setting toggles, was DynamoDB in the reference
-- (NotificationSettingsDynamoDB). Three known types; rows exist only for
-- settings someone has saved, defaults (everything enabled, no schedule)
-- live in code exactly as the reference kept them.
CREATE TABLE notification_settings (
    account_id    BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    type          TEXT NOT NULL,
    enabled       BOOLEAN NOT NULL,
    schedule_hour INTEGER,
    schedule_min  INTEGER,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, type)
);
