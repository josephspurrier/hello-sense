import com.fasterxml.jackson.databind.ObjectMapper;
import com.google.common.base.Optional;
import com.hello.suripu.core.algorithmintegration.AlgorithmConfiguration;
import com.hello.suripu.core.algorithmintegration.AlgorithmFactory;
import com.hello.suripu.core.algorithmintegration.NeuralNetEndpoint;
import com.hello.suripu.core.algorithmintegration.OneDaysSensorData;
import com.hello.suripu.core.algorithmintegration.TimelineAlgorithm;
import com.hello.suripu.core.algorithmintegration.TimelineAlgorithmResult;
import com.hello.suripu.core.models.Event;
import com.hello.suripu.core.models.SleepPeriod;
import com.hello.suripu.core.models.timeline.v2.TimelineLog;
import com.hello.suripu.core.util.AlgorithmType;
import com.hello.suripu.core.util.FeedbackUtils;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import org.joda.time.DateTime;
import org.joda.time.DateTimeZone;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Paths;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedList;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

/**
 * orb-algo: the sleep algorithms, and nothing else.
 *
 * This is what remains of eleven JVM containers. It holds no database
 * connection, no Kinesis client, no S3 client and no DynamoDB client: a night
 * of samples arrives as JSON, the events come back as JSON, and any model the
 * algorithm learned comes back with them for orb to persist.
 *
 * The algorithms stay in Java deliberately. See the package comment in
 * orb/internal/timeline for why porting them would be a bad trade.
 */
public final class Server {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    /**
     * Artificial light window, used to zero out light readings that are
     * obviously a lamp rather than daylight. These are suripu's defaults from
     * TimelineAlgorithmConfiguration; the interface has only getters, so it is
     * implemented inline rather than pulling in the Dropwizard config class.
     */
    private static final AlgorithmConfiguration ALGO_CONFIG = new AlgorithmConfiguration() {
        @Override public int getArtificalLightStartMinuteOfDay() { return 60 * 21; } // 21:00
        @Override public int getArtificalLightStopMinuteOfDay()  { return 60 * 5; }  // 05:00
    };

    private final AlgorithmFactory factory;
    private final Models.FileEnsembleDAO ensembleDAO;
    private final Models.FileFeatureExtractionDAO featureDAO;

    private Server(final String modelDir) throws IOException {
        this.ensembleDAO = new Models.FileEnsembleDAO(
                Paths.get(modelDir, "normal4ensemble.base64"),
                Paths.get(modelDir, "normal4.base64"));
        this.featureDAO = new Models.FileFeatureExtractionDAO(
                Paths.get(modelDir, "featureextractionlayer.bin"));

        // Neural net endpoints are deliberately empty. The SLEEP2 and SLEEP
        // Keras models died with the company: the S3 bucket returns
        // NoSuchBucket and no weights survive in any repo. An empty map makes
        // the chain skip those steps instead of failing on every night.
        final Map<AlgorithmType, NeuralNetEndpoint> noNets = Collections.emptyMap();

        // priorsDAO is per-request, so a placeholder is used at construction
        // and the real one is swapped in per call. See timeline().
        this.factory = AlgorithmFactory.create(
                new Models.NoSleepHmmDAO(),
                new Models.RequestModelsDAO(null, null),
                ensembleDAO, featureDAO, noNets,
                ALGO_CONFIG, Optional.<UUID>absent());
    }

    public static void main(final String[] args) throws Exception {
        final int port = Integer.parseInt(System.getenv().getOrDefault("ORB_ALGO_PORT", "8090"));
        final String modelDir = System.getenv().getOrDefault("ORB_ALGO_MODELS", "models");

        final Server server = new Server(modelDir);

        final HttpServer http = HttpServer.create(new InetSocketAddress(port), 0);
        http.createContext("/timeline", server::handleTimeline);
        http.createContext("/health", ex -> respond(ex, 200, "{\"status\":\"ok\"}"));
        http.setExecutor(java.util.concurrent.Executors.newFixedThreadPool(2));
        http.start();

        System.out.println("orb-algo listening on :" + port + " models=" + modelDir);
    }

    private void handleTimeline(final HttpExchange ex) throws IOException {
        if (!"POST".equals(ex.getRequestMethod())) {
            respond(ex, 405, "method not allowed");
            return;
        }
        try {
            final Json.Request req = MAPPER.readValue(ex.getRequestBody(), Json.Request.class);
            final Json.Result result = timeline(req);
            respond(ex, 200, MAPPER.writeValueAsString(result));
        } catch (final Exception e) {
            // The message matters: orb surfaces it verbatim, and "500" alone
            // turns a diagnosable problem into a mystery.
            e.printStackTrace();
            respond(ex, 500, e.getClass().getSimpleName() + ": " + String.valueOf(e.getMessage()));
        }
    }

    /**
     * Scores one night.
     *
     * The algorithm chain is tried in order and the first usable answer wins,
     * mirroring InstrumentedTimelineProcessor. ONLINE_HMM first because it is
     * the only one with models; VOTING last because it always produces
     * something and is therefore the safety net.
     */
    private Json.Result timeline(final Json.Request req) {
        final OneDaysSensorData data = Mapping.sensorData(req);

        // Both counts, because they are meant to differ: orb sends the rows it
        // has, and the series handed to the algorithms is filled out to one
        // sample per minute across the window. A night that scores badly is
        // worth checking here first, since "sent 710, binned 710" means the
        // fill is not happening and TimelineSafeguards will see gaps suripu
        // never sees.
        System.out.println("timeline account=" + req.accountId + " date=" + req.date
                + " sent=" + (req.sensors == null ? 0 : req.sensors.size())
                + " binned=" + data.allSensorSampleList.get(
                        com.hello.suripu.core.models.Sensor.LIGHT).size()
                + " motion=" + data.oneDaysTrackerMotion.processedtrackerMotions.size()
                + " light=" + describe(data.allSensorSampleList.get(
                        com.hello.suripu.core.models.Sensor.LIGHT))
                + " sound=" + describe(data.allSensorSampleList.get(
                        com.hello.suripu.core.models.Sensor.SOUND))
                + " hold=" + describe(data.allSensorSampleList.get(
                        com.hello.suripu.core.models.Sensor.HOLD_COUNT)));

        // A per-request models DAO: it supplies the account's learned priors and
        // captures anything the algorithm learns back.
        final Models.RequestModelsDAO priors = new Models.RequestModelsDAO(req.priorModel, req.scratchpad);

        final AlgorithmFactory perRequest = AlgorithmFactory.create(
                new Models.NoSleepHmmDAO(), priors, ensembleDAO, featureDAO,
                Collections.<AlgorithmType, NeuralNetEndpoint>emptyMap(),
                ALGO_CONFIG, Optional.<UUID>absent());

        final Json.Result out = new Json.Result();
        final LinkedList<AlgorithmType> chain = new LinkedList<>();
        if (req.algorithm != null && !req.algorithm.isEmpty()) {
            chain.add(AlgorithmType.valueOf(req.algorithm));
        } else {
            chain.add(AlgorithmType.ONLINE_HMM);
            chain.add(AlgorithmType.VOTING);
        }

        // feedbackChanged drives learning. Passing true whenever feedback is
        // present is what lets a correction train the model, and is the
        // equivalent of suripu's online_hmm_learning flag being on.
        final boolean feedbackChanged = req.feedback != null && !req.feedback.isEmpty();
        final Set<String> features = Collections.emptySet();

        for (final AlgorithmType type : chain) {
            final Optional<TimelineAlgorithm> algo = perRequest.get(type);
            if (!algo.isPresent()) {
                continue;
            }
            final TimelineLog log = new TimelineLog(Long.valueOf(req.accountId), data.date.getMillis());
            final Optional<TimelineAlgorithmResult> res = algo.get().getTimelinePrediction(
                    data, SleepPeriod.createSleepPeriod(SleepPeriod.Period.NIGHT, data.date),
                    log, req.accountId, feedbackChanged, features);

            out.algorithm = type.name();
            if (!res.isPresent()) {
                out.status = "NO_RESULT";
                continue;
            }

            // MOVE EVENTS BASED ON FEEDBACK, before anything reads them.
            //
            // Feedback has two jobs and they are easy to conflate. The
            // `feedbackChanged` boolean above is the LEARNING half: it lets a
            // correction train the ONLINE_HMM model for future nights. This is
            // the DISPLAY half: it moves the event on the night the person
            // actually corrected.
            //
            // Only the first half was here, so a correction was stored,
            // acknowledged, fed to the learner, and then silently discarded
            // from the answer. The app showed the algorithm's original time
            // back, which reads as "the save did not work" and is worse than an
            // error would have been.
            //
            // The reference does this at InstrumentedTimelineProcessor:641 and
            // then reads its four main events out of the REPROCESSED map, which
            // is the part that matters: taking the times from feedback but the
            // segments from the original events would draw a timeline whose
            // sleep depth disagreed with its own labels.
            //
            // reprocessEventsBasedOnFeedback also enforces ordering, so a wake
            // time dragged past getting out of bed is reconciled rather than
            // accepted as-is.
            final FeedbackUtils feedbackUtils = new FeedbackUtils(Optional.<UUID>absent());
            final FeedbackUtils.ReprocessedEvents reprocessed =
                    feedbackUtils.reprocessEventsBasedOnFeedback(
                            SleepPeriod.Period.NIGHT,
                            data.feedbackList,
                            res.get().mainEvents.values(),
                            res.get().extraEvents,
                            Mapping.timeZoneOffsetMap(req.offsetMs));

            final Map<Event.Type, Event> events = new HashMap<>(reprocessed.mainEvents);
            out.inBed = iso(events.get(Event.Type.IN_BED));
            out.sleep = iso(events.get(Event.Type.SLEEP));
            out.wakeUp = iso(events.get(Event.Type.WAKE_UP));
            out.outOfBed = iso(events.get(Event.Type.OUT_OF_BED));

            // All four or nothing. ONLINE_HMM returns partial results when its
            // SLEEP model has collapsed, and a timeline with a bedtime and no
            // sleep is worse than falling through to VOTING.
            if (out.inBed != null && out.sleep != null && out.wakeUp != null && out.outOfBed != null) {
                out.status = "NO_ERROR";
                out.updatedModel = priors.updatedModelBytes();
                out.updatedScratchpad = priors.updatedScratchpadBytes();
                // Only for a night that scored. The segment list is a rendering
                // of the four main events against the samples, so without them
                // there is nothing to render around.
                Timeline.populate(out, data, events, req.offsetMs, req.age);
                return out;
            }
            out.status = "MISSING_KEY_EVENTS";
        }
        return out;
    }

    /**
     * Renders an event's instant.
     *
     * No conversion: the events come back on the same clock the samples were
     * on, which is real UTC. There used to be an offset subtraction here to
     * undo a shift applied when building the input, and the two cancelled at
     * the boundary while everything in between saw the wrong time of day.
     */
    /**
     * min/max/filled for a sensor series.
     *
     * "filled" counts the minutes carrying the -1 placeholder. If it equals the
     * sample count then no real reading landed on the bucket grid, which looks
     * identical to a dark room and silently removes every light-driven feature.
     */
    private static String describe(final java.util.List<com.hello.suripu.core.models.Sample> s) {
        if (s == null || s.isEmpty()) {
            return "none";
        }
        float min = Float.MAX_VALUE, max = -Float.MAX_VALUE;
        int filled = 0;
        for (final com.hello.suripu.core.models.Sample x : s) {
            if (x.value == -1.0f) {
                filled++;
            }
            min = Math.min(min, x.value);
            max = Math.max(max, x.value);
        }
        return "n=" + s.size() + ",min=" + min + ",max=" + max + ",filled=" + filled;
    }

    private static String iso(final Event e) {
        if (e == null) {
            return null;
        }
        return new DateTime(e.getStartTimestamp(), DateTimeZone.UTC).toString();
    }

    private static void respond(final HttpExchange ex, final int code, final String body) throws IOException {
        final byte[] raw = body.getBytes(StandardCharsets.UTF_8);
        ex.getResponseHeaders().set("Content-Type", "application/json");
        ex.sendResponseHeaders(code, raw.length);
        try (final OutputStream os = ex.getResponseBody()) {
            os.write(raw);
        }
    }
}
