-- Apple push device tokens.
--
-- The app posts one of these to /v1/notifications/registration every time it
-- launches and gets a token back from iOS. That is not a bug in the app: iOS
-- reissues the token after a reinstall, a restore onto a new phone, or an OS
-- upgrade, and the only way to learn the new one is to be told.
--
-- UNIQUE on token alone, not on (account_id, token), and that is the point. A
-- token identifies a physical installation, so if a phone re-registers under a
-- different account the row must MOVE rather than duplicate. Keyed the other
-- way, signing out and signing in as someone else would leave the old pairing
-- in place and that phone would keep receiving the first account's sleep
-- scores. There is one account here today, which is exactly the condition under
-- which a bug like that would go unnoticed until it mattered.
CREATE TABLE push_tokens (
    id           BIGSERIAL PRIMARY KEY,
    account_id   BIGINT      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- The APNS device token, lowercase hex as the app sends it.
    token        TEXT        NOT NULL UNIQUE,

    -- Reported by the app, kept for support rather than for logic. Nothing
    -- branches on these: a version check that silently stops sending is the
    -- kind of thing that is discovered months later.
    os           TEXT        NOT NULL DEFAULT '',
    os_version   TEXT        NOT NULL DEFAULT '',
    app_version  TEXT        NOT NULL DEFAULT '',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Set on a successful send. A token Apple has never accepted and a token
    -- that worked last week look identical without this, and telling them apart
    -- is the whole of diagnosing "push stopped working".
    last_sent_at TIMESTAMPTZ
);

CREATE INDEX push_tokens_account ON push_tokens (account_id);
