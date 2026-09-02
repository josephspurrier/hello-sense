import com.google.common.collect.ImmutableList;
import com.hello.suripu.core.algorithmintegration.OneDaysSensorData;
import com.hello.suripu.core.algorithmintegration.OneDaysTrackerMotion;
import com.google.common.base.Optional;
import com.hello.suripu.core.models.AllSensorSampleList;
import com.hello.suripu.core.models.CalibratedDeviceData;
import com.hello.suripu.core.models.Calibration;
import com.hello.suripu.core.models.Device;
import com.hello.suripu.core.models.DeviceData;
import com.hello.suripu.core.models.Sample;
import com.hello.suripu.core.models.Sensor;
import com.hello.suripu.core.models.TimelineFeedback;
import com.hello.suripu.core.models.TrackerMotion;
import com.hello.suripu.core.models.UserBioInfo;
import org.joda.time.DateTime;
import com.hello.suripu.core.models.TimeZoneHistory;
import com.hello.suripu.core.util.TimeZoneOffsetMap;
import org.joda.time.DateTimeZone;

import java.util.ArrayList;
import java.util.EnumMap;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Turns orb's JSON into the objects suripu's algorithms expect.
 *
 * This is the layer where quiet mistakes live, because every value has to land
 * in the right unit and the right clock and nothing complains if it does not.
 * Three rules govern everything here, and each one cost a wrong answer to find:
 *
 *  1. Sensor values keep the scaling they were stored with, and are converted
 *     by CalibratedDeviceData rather than by hand. suripu's own reader
 *     (Bucketing.populateMapAll) calls exactly these methods; calling them too
 *     means this file cannot drift from it.
 *
 *  2. SAMPLE TIMESTAMPS ARE REAL UTC, NOT LOCAL. Only the window bounds
 *     (date, startTimeLocalUTC, endTimeLocalUTC) are "local UTC", and OnlineHmm
 *     converts them back with startTimeLocalUtc.minusMillis(timezoneOffset)
 *     before binning against the sample clock. Shifting the samples to local
 *     too puts every reading an offset away from its bin, and because
 *     OnlineHmmSensorDataBinning recovers local time as
 *     `sample.dateTime + sample.offsetMillis`, the artificial-light window then
 *     sees a clock wrong by twice the offset. The events still come out looking
 *     plausible, which is what makes this worth a paragraph.
 *
 *  3. The series handed to the algorithms is DENSE. suripu builds one sample
 *     per minute across the whole window and overrides the ones it has data
 *     for; a gap is -1, not a missing entry. Sending only the rows that exist
 *     makes TimelineSafeguards see a data gap that suripu never sees, and the
 *     night is then thrown away with DATA_GAP_TOO_LARGE.
 */
public final class Mapping {

    /** Slot width of the series the algorithms read. SenseDataDAODynamoDB. */
    private static final int SLOT_DURATION_MINUTES = 1;

    /** What suripu writes into a minute it has no reading for. */
    static final float MISSING_DATA_DEFAULT_VALUE = -1.0f;

    // Deliberately no firmware version.
    //
    // DeviceDataDAODynamoDB's read path never sets it, so every DeviceData the
    // algorithms have ever scored carried a null here, and
    // DataUtils.calibrateAudio takes its non-blacklisted branch as a result.
    // Supplying a real version is not a harmless improvement: it is a value the
    // reference never had, and if it ever landed in BLACKLISTED_FIRMWARE the
    // sound series would quietly shift by the 40 dB noise floor. Matching the
    // reference beats being more correct than it.

    /**
     * False, matching SenseDataDAODynamoDB: "Don't use the new audio peak
     * energy since the models haven't trained on it." The audio_peak_energy_db
     * feature flag governs a different path; the models this service loads
     * never saw that signal, so passing true feeds them a sound series they
     * were not fitted against.
     */
    private static final boolean USE_AUDIO_PEAK_ENERGY = false;

    /**
     * Builds the sensor series the algorithms read.
     *
     * Mirrors Bucketing.populateMapAll for the values and
     * Bucketing.generateEmptyMap + mergeResults for the shape: a complete
     * minute-by-minute series over [startUTC, endUTC], real readings where they
     * exist and -1 everywhere else.
     */
    public static AllSensorSampleList sensors(final List<Json.Sensor> in, final int offsetMillis,
                                              final long startUTC, final long endUTC,
                                              final Optional<Calibration> calibration) {
        final AllSensorSampleList out = new AllSensorSampleList();
        if (in == null || in.isEmpty()) {
            // Matches generateTimeSeriesByUTCTimeAllSensors: no rows means no
            // series at all, not a window of -1. An all-missing series would
            // read as real data that happens to be flat.
            return out;
        }

        final Map<Sensor, Map<Long, Float>> readings = new EnumMap<>(Sensor.class);

        for (final Json.Sensor s : in) {
            final long t = s.tsMillis;

            final DeviceData raw = new DeviceData.Builder()
                    .withAccountId(0L)
                    .withDeviceId(0L)
                    .withDateTimeUTC(new DateTime(t, DateTimeZone.UTC))
                    .withOffsetMillis(offsetMillis)
                    .withAmbientTemperature(zero(s.temperature))
                    .withAmbientHumidity(zero(s.humidity))
                    // calibrateAmbientLight, NOT withAmbientLight. The two are
                    // not variants of each other: withAmbientLight stores raw
                    // counts and is what the write path uses, while
                    // calibrateAmbientLight converts counts to lux and is the
                    // only thing that populates ambientLightFloat, which is the
                    // one field CalibratedDeviceData.lux() reads. Getting this
                    // wrong returns 0.0 lux for every minute of the night, and
                    // 0.0 is a legal reading. DeviceDataDAODynamoDB:636.
                    .calibrateAmbientLight(zero(s.light))
                    .withAmbientLightVariance(zero(s.lightVariance))
                    .withAmbientAirQualityRaw(zero(s.airQualityRaw))
                    .withAudioPeakBackgroundDB(zero(s.audioPeakBackgroundDB))
                    .withAudioPeakDisturbancesDB(zero(s.audioPeakDisturbanceDB))
                    .withAudioPeakEnergyDB(zero(s.audioPeakEnergyDB))
                    .withAudioNumDisturbances(zero(s.audioNumDisturbances))
                    .withWaveCount(zero(s.waveCount))
                    .withHoldCount(zero(s.holdCount))
                    .build();

            // WHITE is the Sense 1.0 default. Colour only affects the light
            // calibration curve, and this deployment has one unit.
            //
            // The calibration is the DUST one and it is not optional in
            // practice: absent here, particulates() converts raw counts with no
            // per-device offset and reads high. That was the state until
            // 2026-08-17 and it cost a WARNING where the reference said IDEAL.
            final CalibratedDeviceData cal =
                    new CalibratedDeviceData(raw, Device.Color.WHITE, calibration);

            put(readings, Sensor.LIGHT, t, cal.lux());
            put(readings, Sensor.SOUND, t, cal.sound(USE_AUDIO_PEAK_ENERGY));
            put(readings, Sensor.HUMIDITY, t, cal.humidity());
            put(readings, Sensor.TEMPERATURE, t, cal.temperature());
            put(readings, Sensor.PARTICULATES, t, cal.particulates());
            put(readings, Sensor.WAVE_COUNT, t, (float) raw.waveCount);
            put(readings, Sensor.HOLD_COUNT, t, (float) raw.holdCount);
            put(readings, Sensor.SOUND_NUM_DISTURBANCES, t, cal.audioNumDisturbances());
            put(readings, Sensor.SOUND_PEAK_DISTURBANCE, t, cal.soundPeakDisturbance());
            put(readings, Sensor.SOUND_PEAK_ENERGY, t, cal.soundPeakEnergy());
        }

        // Bucket keys run backwards from the window end, which is what decides
        // the phase of the grid. Deriving them from the first sample instead
        // would shift every bin whenever a night starts late.
        final long slotMillis = SLOT_DURATION_MINUTES * 60_000L;
        final long endRounded = endUTC - (endUTC % slotMillis);
        final int numberOfBuckets = (int) ((endUTC - startUTC) / 60_000L / SLOT_DURATION_MINUTES) + 1;

        for (final Map.Entry<Sensor, Map<Long, Float>> e : readings.entrySet()) {
            if (e.getValue().isEmpty()) {
                continue;
            }
            final List<Sample> dense = new ArrayList<>(numberOfBuckets);
            for (int i = numberOfBuckets - 1; i >= 0; i--) {
                final long key = endRounded - (i * slotMillis);
                final Float v = e.getValue().get(key);
                dense.add(new Sample(key, v == null ? MISSING_DATA_DEFAULT_VALUE : v.floatValue(), offsetMillis));
            }
            out.add(e.getKey(), dense);
        }

        return out;
    }

    private static void put(final Map<Sensor, Map<Long, Float>> readings, final Sensor sensor,
                            final long t, final float value) {
        // A non-finite reading is dropped rather than stored, so it becomes a
        // -1 gap. A NaN inside the series propagates through the whole HMM.
        if (Float.isNaN(value) || Float.isInfinite(value)) {
            return;
        }
        Map<Long, Float> m = readings.get(sensor);
        if (m == null) {
            m = new HashMap<>();
            readings.put(sensor, m);
        }
        m.put(Long.valueOf(t), Float.valueOf(value));
    }

    private static int zero(final Integer v) { return v == null ? 0 : v.intValue(); }

    /**
     * The device's dust calibration, or absent when it has never been
     * calibrated.
     *
     * Absent and "offset zero" are NOT the same and the distinction is the
     * whole reason the field is a boxed Integer: Calibration.create derives the
     * delta as round(300 - offset * 1.3), so an offset of zero means +300
     * counts on every reading while no calibration means no adjustment at all.
     *
     * The sense id is a placeholder. Calibration.create carries it only so that
     * DataUtils can name the device in one error log line about an implausible
     * density; nothing branches on it, and this service is not told which Sense
     * the night came from. Passing the account id keeps that log line useful
     * rather than empty.
     *
     * testedAt is likewise unused by the conversion. Zero rather than a
     * fabricated timestamp: an invented date that later showed up somewhere
     * real would be worse than an obviously empty one.
     */
    public static Optional<Calibration> calibration(final Json.Request req) {
        if (req.dustOffset == null) {
            return Optional.absent();
        }
        return Optional.of(Calibration.create(
                "account-" + req.accountId, req.dustOffset, Long.valueOf(0L)));
    }

    /**
     * The night's timezone, as the map every suripu utility wants.
     *
     * The zone ID carries the offset, and the offsetMillis field does not.
     * TimeZoneOffsetMap.getOffset ignores TimeZoneHistory.offsetMillis outright
     * and resolves the ID instead, so naming the zone "UTC" here pins every
     * minute to offset 0 no matter what offset is passed alongside it. A
     * fixed-offset ID ("-04:00") is what makes the map answer with the night's
     * actual offset.
     *
     * That bug cost a day. In Timeline the minutes that came from a real pill
     * sample kept their own offset, so the wrong ones were exactly the filled
     * gaps: the timeline was right until it was quiet, which is when it is
     * least looked at.
     *
     * Shared by the segment rendering and by the feedback reprocessing, which
     * both have to agree about what local time a correction like "08:35" means.
     */
    public static TimeZoneOffsetMap timeZoneOffsetMap(final int offsetMillis) {
        return TimeZoneOffsetMap.createFromTimezoneHistoryList(
                ImmutableList.of(new TimeZoneHistory(0L, offsetMillis,
                        DateTimeZone.forOffsetMillis(offsetMillis).getID())));
    }

    /**
     * Builds the pill motion series.
     *
     * Timestamps are real UTC, matching PillDataDAODynamoDB: its range key is
     * the UTC timestamp and the local one is only a query filter, so
     * TrackerMotion.timestamp has always been UTC despite the DAO being named
     * getBetweenLocalUTC.
     *
     * They are also truncated to the minute, which that DAO does on read
     * (`withSecondOfMinute(0)`, "query results return minute-level"). It is not
     * cosmetic: motion drives the smoothing and clustering in VOTING, and
     * carrying real seconds through moved the predicted out-of-bed time by ten
     * minutes and changed every motion score.
     */
    public static OneDaysTrackerMotion motion(final List<Json.Motion> in, final long accountId,
                                              final int offsetMillis) {
        final List<TrackerMotion> out = new ArrayList<>();
        if (in == null) {
            return new OneDaysTrackerMotion(ImmutableList.<TrackerMotion>of());
        }
        for (final Json.Motion m : in) {
            if (m.svmNoGravity == null) {
                // No amplitude means nothing to score. A zero here would read
                // as "perfectly still", which is a claim, not an absence.
                continue;
            }
            final TrackerMotion.Builder b = new TrackerMotion.Builder()
                    .withAccountId(accountId)
                    .withTimestampMillis(m.tsMillis - Math.floorMod(m.tsMillis, 60_000L))
                    .withOffsetMillis(offsetMillis)
                    .withValue((int) m.svmNoGravity.longValue());

            if (m.motionRange != null)    b.withMotionRange(m.motionRange);
            if (m.kickoffCounts != null)  b.withKickOffCounts((long) m.kickoffCounts.intValue());
            if (m.onDurationSecs != null) b.withOnDurationInSeconds((long) m.onDurationSecs.intValue());

            out.add(b.build());
        }
        return new OneDaysTrackerMotion(ImmutableList.copyOf(out));
    }

    /**
     * Builds the feedback list.
     *
     * TimelineFeedback carries the corrected times as HH:MM strings against a
     * date, not as instants, which is why orb passes them through unparsed.
     *
     * `created` is not decoration. OnlineHmm.filterFeedbackInValidTimeRange
     * drops any correction whose created time falls outside the night's window
     * in real UTC, so a placeholder there means the model never learns and the
     * only symptom is that a correction changes nothing.
     */
    public static ImmutableList<TimelineFeedback> feedback(final List<Json.Feedback> in, final DateTime night,
                                                           final long accountId) {
        final List<TimelineFeedback> out = new ArrayList<>();
        if (in == null) {
            // Go marshals a nil slice as null, so an absent correction arrives
            // as null rather than []. This used to be an NPE that failed the
            // whole night.
            return ImmutableList.copyOf(out);
        }
        for (final Json.Feedback f : in) {
            out.add(TimelineFeedback.create(night, f.oldTime, f.newTime,
                    com.hello.suripu.core.models.Event.Type.fromInteger(f.eventType),
                    accountId, f.createdMillis, null, Boolean.TRUE));
        }
        return ImmutableList.copyOf(out);
    }

    /**
     * Assembles the whole request.
     *
     * The window is recomputed here from the night's date rather than trusting
     * the caller's start/end, so this service and suripu cannot disagree about
     * what "a night" is. 20:00 to 12:00 next day, DateTimeUtil.DAY_STARTS_AT_HOUR.
     */
    public static OneDaysSensorData sensorData(final Json.Request req) {
        final int offset = req.offsetMs;
        final DateTime night = DateTime.parse(req.date).withTimeAtStartOfDay().withZoneRetainFields(DateTimeZone.UTC);

        final DateTime startLocalUTC = night.withTimeAtStartOfDay().withHourOfDay(20);
        final DateTime endLocalUTC = night.withTimeAtStartOfDay().plusDays(1).withHourOfDay(12);

        // The sample clock is real UTC, so the window has to be expressed in it
        // to line the two up. This is the same conversion OnlineHmm does at
        // line 371 before binning.
        final long startUTC = startLocalUTC.minusMillis(offset).getMillis();
        final long endUTC = endLocalUTC.minusMillis(offset).getMillis();

        return new OneDaysSensorData(
                sensors(req.sensors, offset, startUTC, endUTC, calibration(req)),
                motion(req.motion, req.accountId, offset),
                // The partner's motion goes through the same truncation and
                // offset as the sleeper's own, so the two series line up by the
                // minute. Empty when there is no partner, which is also what
                // every partner-aware step treats as "no partner".
                motion(req.partnerMotion, req.partnerAccountId, offset),
                feedback(req.feedback, night, req.accountId),
                night, startLocalUTC, endLocalUTC,
                DateTime.now(DateTimeZone.UTC),
                offset,
                // Bio info drives only the neural net, which has no models and
                // never runs. Defaults avoid inventing an age or a BMI.
                new UserBioInfo());
    }

    private Mapping() {}
}
