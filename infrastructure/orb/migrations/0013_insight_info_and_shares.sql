-- The last four endpoints suripu-app was still serving.
--
-- Two tables: the editorial text behind `GET /v2/insights/info/{category}`, and
-- the share records behind `POST /v2/sharing/insight`. Both are the app's, not
-- the device's, and neither has anything to do with the Sense.

-- The "what is this insight about" copy, shown when a card is tapped.
--
-- Migrated from the `insights` database's `info_insight_cards`, which is the
-- only copy of this text anywhere. It is genuine Hello editorial content, not
-- generated, so it is carried across verbatim rather than rewritten. 38 rows.
--
-- `category` is TEXT here where the source used a Postgres ENUM
-- (`insight_category`). The enum bought nothing except a migration whenever a
-- category was added, and orb already treats the category as a string
-- everywhere else: in `insights.category`, in `insight_categories`, and in the
-- art lookup in `internal/api/insightart`. One representation, everywhere.
--
-- Stored lowercase, because that is how the source stores it and how suripu
-- queries it (`getGenericInsightCardsByCategory(category.toString().toLowerCase())`).
-- The app sends the category uppercase, so the handler lowercases on the way in.
CREATE TABLE insight_info (
    id            INTEGER PRIMARY KEY,
    category      TEXT NOT NULL,
    title         TEXT,
    text          TEXT,
    image_url     TEXT,
    category_name TEXT
);

-- The lookup is "newest row for this category", which is suripu's
-- `WHERE category = ? ORDER BY id DESC LIMIT 1`. In this data every category
-- has exactly one row (38 rows, 38 categories), so the ORDER BY never actually
-- chooses. It is kept anyway because the reference's query is the contract and
-- a second row for a category must not change which text is served.
CREATE INDEX insight_info_category_idx ON insight_info (category, id DESC);

-- `image_url` is carried across EXACTLY, including the distinction between NULL
-- (21 rows), empty string (16 rows) and one surviving URL that points at
-- `s3.amazonaws.com/hello-data/insights_images/`, a bucket that is gone.
--
-- The first load of this table nulled the empties and the dead URL, on the
-- reasoning that a dead link is worse than no link. apidiff caught it
-- immediately: suripu returns `""` where the source is empty and `null` where
-- it is null, and the app is sent whichever it stores. Collapsing the two is a
-- change to the wire format to fix a field that **this app version never
-- reads** anyway, since the detail screen reuses the feed card's image (see
-- suripu-ios CHANGELOG, "Insight image cached shared between feed and detail").
--
-- So: no cleverness. Keeping the data as it is costs nothing, keeps the
-- regression diff clean, and leaves the option of pointing these at orb's own
-- category art later, which nulling them would have thrown away.

-- A shared insight.
--
-- SNAPSHOT, NOT A REFERENCE. The row carries the card's own title, message and
-- timestamp rather than pointing at `insights.id`, and that is deliberate for
-- the reason recorded in internal/insights: an insight card is a statement
-- about data as it was, and it cannot be regenerated from today's database. A
-- share that joined live would quietly rewrite itself, so a link sent last week
-- would show a different number this week. The reference does the same thing,
-- serialising the whole InsightCard into a payload column.
--
-- `shared_by` is the sharer's first name, also snapshotted, because the account
-- can be renamed and an old link should not change who it says sent it.
--
-- No foreign key to `insights`: deleting a card must not delete the record of
-- having shared it, or a link already sent to somebody breaks retroactively.
-- The account FK stays, because a deleted account should take its shares.
CREATE TABLE insight_shares (
    id         TEXT PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    category   TEXT NOT NULL,
    title      TEXT NOT NULL,
    message    TEXT NOT NULL,
    shared_by  TEXT,
    insight_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX insight_shares_account_idx ON insight_shares (account_id, created_at DESC);
