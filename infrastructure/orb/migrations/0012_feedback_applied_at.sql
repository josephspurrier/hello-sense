-- Which corrections a stored timeline already incorporates.
--
-- `timeline_events.updated_at` was doing two jobs and they came apart.
--
-- NightsNeedingTimeline decides a night needs rescoring when its feedback is
-- newer than the timeline, which it tested as `updated_at < MAX(created_at)`.
-- That reads as "the timeline predates the correction", and it is only a proxy
-- for the thing actually meant: "there is a correction this timeline has not
-- taken account of". The two agree right up until something touches updated_at
-- for a different reason.
--
-- Two ways that bit, both real:
--
--   1. A correction lands, the synchronous rescore stamps updated_at from
--      now(), and now() can fall a few hundred microseconds AFTER the feedback
--      row that caused it. Observed: feedback at 15:30:55.295, timeline at
--      15:30:55.413. Harmless while the night's window is open, because the
--      window clause keeps it eligible anyway. Not harmless after.
--
--   2. Setting updated_at back by hand to force a rescore, which is the obvious
--      thing to reach for and has no guard on it. On 2026-08-17 that was done
--      to a settled night and made it worse: the model had moved on since the
--      night was first scored, ONLINE_HMM could no longer produce four events,
--      and the rescore replaced a good stored answer with the VOTING fallback.
--      A stored timeline is a record of what the algorithm knew THEN, and
--      recomputing it replays today's model against an old night. There is no
--      undo; the raw data survives but the derived timeline is overwritten.
--
-- So the question gets its own column. `feedback_applied_at` is the newest
-- feedback timestamp the stored timeline was computed from, and the recompute
-- test compares against that instead. updated_at goes back to meaning only
-- "when this row was last written", which is all anyone should read it as.
--
-- Null means "scored before this column existed, or scored with no feedback at
-- all". Both want the same treatment: if feedback exists and this is null, the
-- night has feedback it has not accounted for, so rescore it once and the
-- column fills in. That is why the comparison below uses IS DISTINCT FROM
-- rather than `<`, which would be false for null and skip those nights forever.
ALTER TABLE timeline_events ADD COLUMN feedback_applied_at TIMESTAMPTZ;

-- Backfill so the deploy does not rescore every night that already has
-- feedback. These timelines DO incorporate their corrections; the column simply
-- did not exist to say so. Without this the first worker pass after deploy
-- would rescore every corrected night against today's model, which is exactly
-- the damage described in (2) above, applied in bulk.
UPDATE timeline_events e
   SET feedback_applied_at = (
        SELECT MAX(f.created_at) FROM timeline_feedback f
         WHERE f.account_id = e.account_id AND f.date_of_night = e.date_of_night)
 WHERE EXISTS (
        SELECT 1 FROM timeline_feedback f
         WHERE f.account_id = e.account_id AND f.date_of_night = e.date_of_night);
