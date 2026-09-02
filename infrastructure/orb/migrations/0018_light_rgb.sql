-- The Sense 1.5's colour channels. Its TCS3400 reports red, green, blue and
-- clear; orb kept only clear and infrared. Light temperature (the app's
-- LIGHT_TEMP tile, in kelvin) is computed from all four, so the three colour
-- channels are stored too. Null on a Sense 1.0 and on rows from before this.
ALTER TABLE sensor_samples
    ADD COLUMN r INTEGER,
    ADD COLUMN g INTEGER,
    ADD COLUMN b INTEGER;
