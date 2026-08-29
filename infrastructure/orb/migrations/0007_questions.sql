-- The sleep questionnaire.
--
-- Five tables in the reference, four here. `account_question_ask_time` is
-- skipped: it is empty, and it belongs to the per-account ask-time scheduling
-- that this port deliberately does not reproduce. Adding an empty table to
-- imply a feature that is not there is worse than leaving it out.
--
-- The reference uses Postgres enum types for frequency, response type, ask time,
-- account info and category. These are TEXT with the same values, matching how
-- orb stores every other vendor enum: the values are on the wire and in the
-- app, so they are not ours to renumber, but a database enum buys nothing here
-- and makes adding a value a migration instead of an insert.

-- The question catalogue. Static reference data, the same for every account.
CREATE TABLE questions (
    id                  INTEGER PRIMARY KEY,
    parent_id           INTEGER NOT NULL DEFAULT 0,
    question_text       TEXT NOT NULL,
    lang                TEXT NOT NULL DEFAULT 'EN',
    frequency           TEXT,               -- ONE_TIME, DAILY, OCCASIONALLY, ...
    response_type       TEXT,               -- CHOICE, CHECKBOX, QUANTITY, ...
    responses           TEXT[] NOT NULL DEFAULT '{}',
    responses_ids       INTEGER[] NOT NULL DEFAULT '{}',
    dependency          INTEGER,
    dependency_response INTEGER[] NOT NULL DEFAULT '{}',
    ask_time            TEXT,               -- MORNING, AFTERNOON, EVENING, ANYTIME
    -- What this question tells us about the person: sleep_temperature, caffeine,
    -- snore and so on. This column is the whole reason the questionnaire exists,
    -- and in the reference it feeds exactly one thing. See the knowledgebase.
    account_info        TEXT,
    category            TEXT NOT NULL DEFAULT 'none',
    created             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The answers a question offers. Separate from questions.responses because the
-- app needs a stable id per choice to post back.
CREATE TABLE response_choices (
    id              INTEGER PRIMARY KEY,
    question_id     INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    response_text   TEXT NOT NULL,
    created         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX response_choices_question_idx ON response_choices (question_id);

-- A question put to an account. One row per asking, so a recurring question has
-- many. The app posts back account_question_id, not question_id, which is what
-- lets the same question be answered differently on different days.
CREATE TABLE account_questions (
    id                   BIGSERIAL PRIMARY KEY,
    account_id           BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    question_id          INTEGER NOT NULL REFERENCES questions(id),
    -- Local dates held as timestamps, matching the reference: the questionnaire
    -- is a calendar, and "asked on the 14th" must not move when the user flies.
    created_local_utc_ts TIMESTAMPTZ NOT NULL,
    expires_local_utc_ts TIMESTAMPTZ NOT NULL,
    created              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, question_id, created_local_utc_ts)
);
CREATE INDEX account_questions_account_idx ON account_questions (account_id);
CREATE INDEX account_questions_pending_idx
    ON account_questions (account_id, expires_local_utc_ts);

-- An answer, or a skip.
--
-- skip is a real answer, not an absence: a question the user declined is one
-- that should stop being asked, and treating it as unanswered would ask it
-- forever. That distinction is why this table has rows with response_id 0.
CREATE TABLE question_responses (
    id                  BIGSERIAL PRIMARY KEY,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    question_id         INTEGER NOT NULL REFERENCES questions(id),
    account_question_id BIGINT NOT NULL DEFAULT 0,
    response_id         INTEGER NOT NULL DEFAULT 0,
    skip                BOOLEAN NOT NULL DEFAULT false,
    question_freq       TEXT,
    created             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX question_responses_account_idx ON question_responses (account_id);
CREATE INDEX question_responses_aq_idx ON question_responses (account_question_id);
