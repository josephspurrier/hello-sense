-- Insight cards, and the category display names they are rendered with.
--
-- The cards themselves live in DynamoDB in the old stack; the names live in a
-- THIRD Postgres database called `insights`, which the migrator had never
-- opened. Neither half is useful alone: a card carries category 19 and nothing
-- else, and the app shows "Wake Variance".

-- The insight's own identifier, a UUID generated when the card was written.
--
-- Separate from the table's bigserial because the API sends this one, and it
-- has to survive a re-migration: the app uses it to tell one card from another
-- across a refresh, and a renumbering would make every card look new.
ALTER TABLE insights ADD COLUMN uuid text UNIQUE;

-- DEFAULT for a generated insight, BASIC for the introduction cards a new
-- account gets. The app renders them differently.
ALTER TABLE insights ADD COLUMN insight_type text;

-- Category display names, keyed by the enum name rather than its ordinal.
--
-- The ordinal is what DynamoDB stores (19), and it is the wrong key to keep:
-- it means nothing when read directly, and the whole reason this table exists
-- is that somebody eventually reads these rows by hand. The migrator resolves
-- the ordinal once, on the way in.
CREATE TABLE insight_categories (
	category      text PRIMARY KEY,
	category_name text NOT NULL
);
