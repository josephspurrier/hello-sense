-- Profile photos, in Postgres rather than S3.
--
-- The reference uploaded to a public S3 bucket and stored three density URLs
-- in DynamoDB. Neither store exists here, and a photo is a few hundred KB per
-- account on a deployment with a handful of accounts, so the bytes live in a
-- row. One photo per account: a new upload replaces the old.
--
-- token is the whole access control for the image bytes. The app fetches the
-- photo with a bare UIImageView request carrying no bearer token, exactly as
-- it fetched S3 URLs, so the URL must be servable unauthenticated; 128 bits
-- of crypto/rand in the path is the same protection the share pages use. A
-- new upload mints a new token, which also busts any URL cache.
CREATE TABLE profile_photos (
    account_id   BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    token        TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    data         BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
