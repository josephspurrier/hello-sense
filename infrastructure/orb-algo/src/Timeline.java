import com.google.common.base.Optional;
import com.google.common.collect.ImmutableList;
import com.hello.suripu.core.algorithmintegration.OneDaysSensorData;
import com.hello.suripu.core.models.AgitatedSleep;
import com.hello.suripu.core.models.Event;
import com.hello.suripu.core.models.Events.MotionEvent;
import com.hello.suripu.core.models.Insight;
import com.hello.suripu.core.models.MotionFrequency;
import com.hello.suripu.core.models.MotionScore;
import com.hello.suripu.core.models.Sample;
import com.hello.suripu.core.models.Sensor;
import com.hello.suripu.core.models.SleepPeriod;
import com.hello.suripu.core.models.SleepScore;
import com.hello.suripu.core.models.SleepSegment;
import com.hello.suripu.core.models.SleepStats;
import com.hello.suripu.core.models.TrackerMotion;
import com.hello.suripu.core.util.SleepScoreUtils;
import com.hello.suripu.core.util.TimeZoneOffsetMap;
import com.hello.suripu.core.util.TimelineRefactored;
import com.hello.suripu.core.util.TimelineUtils;
import org.joda.time.DateTime;
import org.joda.time.DateTimeZone;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.UUID;

/**
 * Turns the algorithm's four events into the full segment list, the night's
 * statistics, the sleep score, and the per-sensor conditions.
 *
 * This is everything the app's timeline needs that is DERIVED FROM THE SAMPLES:
 * sleep depth per minute, light and sound events, the sleep statistics, the
 * score, and the sensor averages behind each condition. It stops short of
 * anything presentational. No message strings, no valid_actions, no event
 * renaming: those are the app's wire contract, they change more often than the
 * maths, and they live in Go where a change is a recompile and a diff rather
 * than a container rebuild. See the DECISION section of
 * knowledgebase/CONSOLIDATION-PLAN.md.
 *
 * Every step is a call into suripu's own TimelineUtils and SleepScoreUtils
 * rather than a reimplementation, for the same reason CalibratedDeviceData is
 * called rather than copied: a call cannot drift from the thing it calls.
 */
public final class Timeline {

    /**
     * Below this depth a minute counts as light sleep. suripu's default from
     * TimelineProcessor; it is a threshold on the same 0-100 scale
     * getSleepDepth produces.
     */
    private static final int LIGHT_SLEEP_THRESHOLD = 70;

    /**
     * Both are feature flags in the running stack (`sleep_stats_medium_sleep`,
     * `sleep_stats_uninterrupted_sleep`) and both have rows, so both are on.
     * Named constants rather than inline `true` so the reason survives: they
     * change what `computeStats` counts, not merely how it reports.
     */
    private static final boolean HAS_MEDIUM_SLEEP = true;
    private static final boolean USE_UNINTERRUPTED_DURATION = true;

    /**
     * `environment_in_timeline_score` has a row in the running stack's feature
     * table, so the environment score is computed rather than defaulted to 100.
     *
     * This one was settled by looking rather than by reading the flag: the
     * `sleep_stats_v_0_2` rows suripu itself wrote carry env_score 86, 90 and
     * 91 on consecutive nights. A defaulted score is exactly 100 every night,
     * so the flag is on.
     */
    private static final boolean ENVIRONMENT_IN_SCORE = true;

    /**
     * `useHigherThesholdForSoundEvents` is a feature flag and there is no row
     * for it, so sound events use the ordinary threshold.
     */
    private static final boolean USE_HIGHER_SOUND_THRESHOLD = false;

    /**
     * Builds the segment list, stats, score and conditions for a scored night.
     *
     * Returns without setting anything rather than throwing when the algorithm
     * produced no usable motion: a night that could not be scored has no
     * timeline, which is a normal outcome and not an error.
     */
    public static void populate(final Json.Result out,
                                final OneDaysSensorData data,
                                final Map<Event.Type, Event> mainEvents,
                                final int offsetMillis,
                                final int ageYears) {

        final TimelineUtils utils = new TimelineUtils(Optional.<UUID>absent());
        final List<TrackerMotion> motions = data.oneDaysTrackerMotion.processedtrackerMotions;
        if (motions.isEmpty()) {
            return;
        }

        // Motion first: this is where sleep depth comes from, derived from each
        // minute's amplitude against the night's maximum.
        final List<MotionEvent> motionEvents =
                utils.generateMotionEvents(motions, SleepPeriod.Period.NIGHT);

        // populateTimeline, not a hand-rolled map of the motion events.
        //
        // It fills EVERY minute of the window, which is what later lets
        // eventsToSegments merge runs of equal depth into the long IN_BED rows
        // the app shows. A sparse map produced 6 segments where the reference
        // produces 26, and left computeStats counting minutes that were never
        // created: sound_sleep came out as 1 against a real 253.
        final TimeZoneOffsetMap tzMap = Mapping.timeZoneOffsetMap(offsetMillis);
        final Map<Long, Event> byTime = new TreeMap<>(
                TimelineRefactored.populateTimeline(motionEvents, tzMap));

        final Event sleepEvent = mainEvents.get(Event.Type.SLEEP);
        final Event wakeEvent = mainEvents.get(Event.Type.WAKE_UP);

        // getEndTimestamp, not getStartTimestamp.
        //
        // The reference passes the END of the sleep event to getLightEvents,
        // and that value decides which light drop counts as LIGHTS_OUT. One
        // minute of difference is enough to pick a different drop.
        final Optional<Long> sleepTime = sleepEvent == null
                ? Optional.<Long>absent()
                : Optional.of(sleepEvent.getEndTimestamp());

        // Light events, and the lights-out time the sound events need.
        Optional<DateTime> lightsOut = Optional.absent();
        final List<Sample> light = data.allSensorSampleList.get(Sensor.LIGHT);
        if (light != null && !light.isEmpty()) {
            final List<Event> lightEvents =
                    utils.getLightEvents(sleepTime, light, SleepPeriod.Period.NIGHT);
            for (final Event e : lightEvents) {
                byTime.put(e.getStartTimestamp(), e);
            }
            if (!lightEvents.isEmpty()) {
                lightsOut = utils.getLightsOutTime(lightEvents);
            }
        }

        // Sound events are timeline rows AND the input to the environment
        // score, so they have to be generated even on a night the app would
        // show none: numSoundEvents feeds calculateSoundScore either way.
        final List<Event> soundEvents = soundEvents(utils, data, motionEvents,
                lightsOut, sleepEvent, wakeEvent);
        for (final Event e : soundEvents) {
            byTime.put(e.getStartTimestamp(), e);
        }

        for (final Event e : mainEvents.values()) {
            if (e != null) {
                byTime.put(e.getStartTimestamp(), e);
            }
        }

        // mergeEvents, not a sort of the map's values.
        //
        // This is what produces the long IN_BED bands the app draws: it buffers
        // consecutive SLEEPING and NONE minutes and emits one merged event
        // every 21 of them, and it merges runs of closely spaced motion. A
        // plain sort keeps every minute as its own row, and the difference does
        // not look like a bug. It looks like the app deciding to render 438
        // rows, with each band's depth then averaged by whoever renders it,
        // which is a different number from the one merged() computes: sound
        // sleep came out as 402 minutes against a real 253.
        List<Event> events = TimelineRefactored.mergeEvents(byTime);
        events = utils.smoothEvents(events);
        if (sleepEvent != null && wakeEvent != null) {
            events = utils.removeMotionEventsOutsideSleep(events,
                    Optional.of(sleepEvent), Optional.of(wakeEvent));
        }
        // greyNullEventsOutsideBedPeriod: everything before getting in and after
        // getting out becomes a null event, so the app draws flat grey there
        // rather than a marker for every stir while the room was empty. It is a
        // step in the reference sequence that was simply missing here, and
        // skipping it left roughly twenty GENERIC_MOTION rows on a night the
        // reference shows none of.
        events = utils.greyNullEventsOutsideBedPeriod(events,
                Optional.fromNullable(mainEvents.get(Event.Type.IN_BED)),
                Optional.fromNullable(mainEvents.get(Event.Type.OUT_OF_BED)));

        events = utils.removeEventBeforeSignificant(events);

        final List<SleepSegment> segments = utils.eventsToSegments(events);

        for (final SleepSegment s : segments) {
            final Json.Segment js = new Json.Segment();
            js.ts = new DateTime(s.getTimestamp(), DateTimeZone.UTC).toString();
            js.durationMillis = s.getDurationInSeconds() * 1000L;
            js.type = s.getType().toString();
            js.sleepDepth = s.getSleepDepth();
            // The segment's own offset, not the night's: they differ across a
            // DST change and the app renders each event in its own.
            js.offsetMillis = s.getOffsetMillis();
            js.sleepPeriod = s.getSleepPeriod() == null ? null : s.getSleepPeriod().toString();
            out.segments.add(js);
        }

        final SleepStats stats = TimelineUtils.computeStats(segments, motions,
                LIGHT_SLEEP_THRESHOLD, HAS_MEDIUM_SLEEP, USE_UNINTERRUPTED_DURATION);

        out.totalSleepMins = stats.sleepDurationInMinutes;
        out.soundSleepMins = stats.soundSleepDurationInMinutes;
        out.lightSleepMins = stats.lightSleepDurationInMinutes;
        out.mediumSleepMins = stats.mediumSleepDurationInMinutes;
        out.timesAwake = stats.numberOfMotionEvents;
        out.uninterruptedMins = stats.uninterruptedSleepDurationInMinutes;
        if (sleepEvent != null && mainEvents.get(Event.Type.IN_BED) != null) {
            out.timeToSleepMins = (int) ((sleepEvent.getStartTimestamp()
                    - mainEvents.get(Event.Type.IN_BED).getStartTimestamp()) / 60000L);
        }

        final int environmentScore = environmentScore(data, stats, soundEvents.size());
        out.sleepScore = score(data, stats, ageYears, environmentScore);
        conditions(out, utils, data, stats, soundEvents.size());
    }

    /**
     * The sound events, which are both timeline rows and a score input.
     *
     * The sleep and wake times handed to getSoundEvents are in LOCAL time,
     * unlike every other timestamp on this path: the reference adds the event's
     * own offset before passing them. Getting this wrong does not fail, it
     * quietly moves the window sound events are looked for in. See the "local
     * UTC" note in knowledgebase/CONSOLIDATION-PLAN.md.
     */
    private static List<Event> soundEvents(final TimelineUtils utils,
                                           final OneDaysSensorData data,
                                           final List<MotionEvent> motionEvents,
                                           final Optional<DateTime> lightsOut,
                                           final Event sleepEvent,
                                           final Event wakeEvent) {

        final List<Sample> soundSamples = data.allSensorSampleList.get(Sensor.SOUND_PEAK_ENERGY);
        if (soundSamples == null || soundSamples.isEmpty()) {
            return new ArrayList<>();
        }

        final Map<Long, Integer> sleepDepths = new HashMap<>();
        for (final MotionEvent e : motionEvents) {
            if (e.getSleepDepth() > 0) {
                sleepDepths.put(e.getStartTimestamp(), e.getSleepDepth());
            }
        }

        final Optional<DateTime> localSleep = local(sleepEvent);
        final Optional<DateTime> localWake = local(wakeEvent);

        return utils.getSoundEvents(soundSamples, sleepDepths, lightsOut,
                localSleep, localWake, SleepPeriod.Period.NIGHT, USE_HIGHER_SOUND_THRESHOLD);
    }

    private static Optional<DateTime> local(final Event e) {
        if (e == null) {
            return Optional.absent();
        }
        return Optional.of(new DateTime(e.getStartTimestamp(), DateTimeZone.UTC)
                .plusMillis(e.getTimezoneOffset()));
    }

    /**
     * The 0-100 environment score, one fifth from each of five sensors.
     */
    private static int environmentScore(final OneDaysSensorData data,
                                        final SleepStats stats,
                                        final int numSoundEvents) {
        if (!ENVIRONMENT_IN_SCORE || stats.sleepTime <= 0L || stats.wakeTime <= 0L) {
            return 100;
        }
        final int sound = SleepScoreUtils.calculateSoundScore(numSoundEvents);
        final int temperature = SleepScoreUtils.calculateTemperatureScore(
                data.allSensorSampleList.get(Sensor.TEMPERATURE), stats.sleepTime, stats.wakeTime);
        final int humidity = SleepScoreUtils.calculateHumidityScore(
                data.allSensorSampleList.get(Sensor.HUMIDITY), stats.sleepTime, stats.wakeTime);
        final int light = SleepScoreUtils.calculateLightScore(
                data.allSensorSampleList.get(Sensor.LIGHT), stats.sleepTime, stats.wakeTime);
        final int particulates = SleepScoreUtils.calculateParticulateScore(
                data.allSensorSampleList.get(Sensor.PARTICULATES), stats.sleepTime, stats.wakeTime);
        return SleepScoreUtils.calculateAggregateEnvironmentScore(
                sound, temperature, humidity, light, particulates);
    }

    /**
     * The sleep score, v5 only.
     *
     * The running stack blends score versions across a transition window, but
     * both windows closed in 2016: getSleepScoreV2V4Weighting and
     * getSleepScoreV4V5Weighting both return 1.0 for any date this code will
     * ever see, which selects v5 outright. Reproducing the blend would be
     * reproducing a branch that cannot be taken.
     *
     * Checked against the scores suripu itself stored: on four consecutive
     * nights round(0.8 * duration + 0.2 * environment) reproduced 65, 76, 85
     * and 76 exactly. Note that the motion score carries weight 0.0 in v5, so
     * it is computed for the record and does not move the result.
     */
    private static int score(final OneDaysSensorData data,
                             final SleepStats stats,
                             final int ageYears,
                             final int environmentScore) {

        final List<TrackerMotion> processed = data.oneDaysTrackerMotion.processedtrackerMotions;
        final List<TrackerMotion> original = data.oneDaysTrackerMotion.filteredOriginalTrackerMotions;

        MotionScore motionScore = SleepScoreUtils.getSleepMotionScore(
                data.date.withTimeAtStartOfDay(), processed, stats.sleepTime, stats.wakeTime);
        if (motionScore.score < (int) SleepScoreUtils.MOTION_SCORE_MIN) {
            motionScore = new MotionScore(motionScore.numMotions, motionScore.motionPeriodMinutes,
                    motionScore.avgAmplitude, motionScore.maxAmplitude,
                    (int) SleepScoreUtils.MOTION_SCORE_MIN);
        }

        // Both thresholds come from the sleep_score_parameters table, which is
        // empty in this stack. Zero is the table's own MISSING_THRESHOLD, and
        // each function substitutes its population default for it, so passing
        // zero is the personalisation being absent rather than a placeholder.
        final int durationThreshold = 0;
        final float motionFrequencyThreshold = 0.0f;

        final float durationV3 = SleepScoreUtils.getSleepScoreDurationV3(
                ageYears, durationThreshold, stats.sleepDurationInMinutes);
        final AgitatedSleep agitated = SleepScoreUtils.getAgitatedSleep(
                original, stats.sleepTime, stats.wakeTime);
        final MotionFrequency frequency = SleepScoreUtils.getMotionFrequency(
                original, stats.sleepDurationInMinutes, stats.sleepTime, stats.wakeTime);
        final float frequencyPenalty = SleepScoreUtils.getMotionFrequencyPenalty(
                frequency, motionFrequencyThreshold);
        final Integer durationV5 = SleepScoreUtils.getSleepScoreDurationV5(
                0L, durationV3, frequencyPenalty, stats.numberOfMotionEvents, agitated);

        // The times-awake penalty is zero in v5 because the duration score
        // already carries it; passing it again would charge for it twice.
        return new SleepScore.Builder()
                .withMotionScore(motionScore)
                .withSleepDurationScore(durationV5)
                .withEnvironmentalScore(environmentScore)
                .withWeighting(new SleepScore.DurationWeightingV5())
                .withTimesAwakePenaltyScore(0)
                .withVersion("v5")
                .build()
                .value;
    }

    /**
     * The per-sensor conditions the app shows as coloured dots.
     *
     * These are the timeline's insights, not the score's sensor sub-scores.
     * The two are computed from the same averages and disagree anyway: the
     * score bands at 100/75/50 while the condition comes from
     * CurrentRoomState.getSensorState, which has its own thresholds and its own
     * idea of what is missing. Deriving the dots from the score would be
     * plausible and wrong.
     *
     * Only the condition crosses the seam. The message that comes with each
     * insight is presentation and stays behind.
     */
    private static void conditions(final Json.Result out,
                                   final TimelineUtils utils,
                                   final OneDaysSensorData data,
                                   final SleepStats stats,
                                   final int numSoundEvents) {

        final List<Insight> insights = utils.generateInSleepInsights(
                data.allSensorSampleList, numSoundEvents, stats.sleepTime, stats.wakeTime);

        for (final Insight insight : insights) {
            final Json.Condition c = new Json.Condition();
            c.sensor = insight.sensor.toString();
            c.condition = insight.condition.toString();
            out.conditions.add(c);
        }
    }

    private Timeline() {}
}
