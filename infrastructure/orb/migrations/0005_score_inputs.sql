-- Two values the timeline endpoint needs that were computed but never stored.
--
-- uninterrupted_mins is what the summary sentence at the top of the app's sleep
-- screen actually reports. The sentence says "sleeping soundly for N hours" and
-- the obvious reading is that N is sound sleep; it is not, it is uninterrupted
-- sleep, and the two differ by around two hours on a normal night. Storing
-- sound sleep and rendering it as soundly is a sentence that is wrong every
-- night without ever looking wrong.
ALTER TABLE sleep_stats ADD COLUMN uninterrupted_mins integer;

-- The per-sensor conditions behind the coloured dots. JSONB for the same reason
-- as segments: written whole, read whole, never queried by field.
--
-- On timeline_events rather than sleep_stats because it is a property of the
-- night as rendered, and because it arrives in the same response as the
-- segments and is written in the same statement.
ALTER TABLE timeline_events ADD COLUMN conditions jsonb;
