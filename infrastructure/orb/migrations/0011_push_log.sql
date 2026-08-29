-- What has already been pushed, so it is not pushed again.
--
-- The unique constraint IS the dedupe. There is no "have I sent this?" query
-- whose answer can be stale between the check and the send: claiming a slot and
-- deciding to send are the same statement.
--
-- The reference got this wrong in an instructive way. Its dedupe lived in a
-- DynamoDB table named per year, `push_notification_event_2026`, and when that
-- table was absent the processor retried, gave up, and concluded
-- `duplicate-push-notification`. A dedupe store that cannot be reached was
-- treated as proof the notification had already gone out, so nothing was ever
-- delivered and the log blamed a first send that never happened. Keeping this in
-- the same Postgres as everything else means it cannot be independently missing,
-- and a failure to reach it is a failure of the whole job, loudly, rather than a
-- silent decision not to send.
CREATE TABLE push_log (
    id         BIGSERIAL PRIMARY KEY,
    account_id BIGINT      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- 'sleep_score' or 'pill_battery'.
    kind       TEXT        NOT NULL,

    -- What makes this notification distinct: the night's date for a sleep
    -- score, the ISO week for a battery warning. Deliberately a string rather
    -- than a date, because the two kinds bucket differently and one column that
    -- holds either beats two columns that are each null half the time.
    dedupe_key TEXT        NOT NULL,

    sent_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (account_id, kind, dedupe_key)
);
