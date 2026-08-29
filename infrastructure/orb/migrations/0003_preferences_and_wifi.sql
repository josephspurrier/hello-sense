-- Account preferences, and the timestamp on a Sense's WiFi reading.
--
-- The DynamoDB `preferences` table is EMPTY and the API still returns seven
-- values, so what the app receives is a set of defaults with any stored
-- overrides merged on top. Storing the defaults as rows would be wrong: it
-- makes "never set" indistinguishable from "set to the default value", and the
-- next change to a default would silently not apply to this account.
--
-- Only overrides are stored. The default set lives in code, beside the reason
-- for each one.
CREATE TABLE preferences (
    account_id bigint  NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name       text    NOT NULL,
    enabled    boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, name)
);

-- The app shows "last updated" against the WiFi reading, and it is NOT the same
-- as the Sense's last_seen_at: the Sense reports every minute, while the WiFi
-- record only changes when the network does. Sending last_seen_at here made the
-- WiFi row appear to update every minute, which is a different claim.
ALTER TABLE senses ADD COLUMN wifi_updated_at timestamptz;
