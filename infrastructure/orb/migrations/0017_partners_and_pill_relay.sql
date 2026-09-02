-- Partners, and where a pill sample came from.
--
-- account_partners links two accounts as bed partners. The reference had no
-- such table: it inferred a partner as "the other account paired to my Sense"
-- (DeviceReadDAO.getPartnerAccountId), which was a shortcut for its own setup
-- flow and ties the partner relation to the room device. Here the two are
-- separate: each account keeps its own Sense, and the link is explicit.
--
-- Stored symmetrically, one row per direction, so "who is my partner" is a
-- primary-key lookup either way and an account can have at most one partner.
-- Both rows are written and removed together (store.SetPartner/ClearPartner).
CREATE TABLE account_partners (
    account_id  BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    partner_id  BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id),
    UNIQUE (partner_id),
    CHECK (account_id <> partner_id)
);

-- Which Sense relayed a pill sample. Pills broadcast over ANT and every Sense
-- in range relays what it hears; orb routes by pill id, so until now the
-- relaying Sense was known at ingest and then discarded. Nullable: rows from
-- before this migration have no answer. With two Senses in one room this is
-- what says which one hears which pill.
ALTER TABLE pill_samples
    ADD COLUMN relayed_by TEXT REFERENCES senses(device_id) ON DELETE SET NULL;
