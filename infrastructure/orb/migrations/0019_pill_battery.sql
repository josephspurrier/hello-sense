-- Two facts about a pill that the reference had and orb lacked.
--
-- prev_battery_level is the heartbeat before the current one. The pill's
-- battery estimate is noisy near the end of a coin cell (a single reading
-- dipped to 10 and bounced back to 33 within the hour), so the low-battery
-- push now waits for two consecutive low heartbeats rather than one.
--
-- battery_type is REMOVABLE (the original Sleep Pill, coin cell on a spring)
-- or SEALED (the 1.5). The reference derived it from the pill's serial number,
-- which orb never sees, so it is set by hand and null means unknown. The app
-- shows its "Replace battery" walkthrough only when it is REMOVABLE.
ALTER TABLE pills
    ADD COLUMN prev_battery_level INTEGER,
    ADD COLUMN battery_type TEXT CHECK (battery_type IN ('REMOVABLE', 'SEALED'));
