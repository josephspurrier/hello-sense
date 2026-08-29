-- The columns the app API needs and 0001 did not carry.
--
-- Found by cmd/apidiff on the first comparison of GET /v1/account against the
-- running Java stack. None of them are cosmetic:
--
--   external_id  is what the app receives as `id`. The numeric primary key is
--                internal and the app never sees it, so an account rendered
--                with the integer is an account the app cannot address.
--   firstname    suripu stores the parts, it does not split `name`. Splitting
--                on the first space gave firstname "Orb" where the reference
--                returns "Orb Owner", because the whole name lives in
--                firstname and lastname is null.
--   lastname
--   gender_other suripu's `gender_name`, sent as "" rather than omitted.
--
-- external_id is nullable rather than defaulted, because a generated value
-- would be a different UUID from the one the app already knows, which is worse
-- than an absent one: it looks correct and addresses nothing.

ALTER TABLE accounts
    ADD COLUMN external_id  uuid,
    ADD COLUMN firstname    text,
    ADD COLUMN lastname     text,
    ADD COLUMN gender_other text;

CREATE UNIQUE INDEX accounts_external_id_key ON accounts (external_id)
    WHERE external_id IS NOT NULL;
