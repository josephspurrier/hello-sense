import com.google.common.base.Optional;
import com.hello.suripu.core.db.DefaultModelEnsembleDAO;
import com.hello.suripu.core.db.FeatureExtractionModelsDAO;
import com.hello.suripu.core.db.OnlineHmmModelsDAO;
import com.hello.suripu.core.db.SleepHmmDAO;
import com.hello.suripu.core.models.OnlineHmmData;
import com.hello.suripu.core.models.OnlineHmmPriors;
import com.hello.suripu.core.models.OnlineHmmScratchPad;
import com.hello.suripu.core.util.FeatureExtractionModelData;
import com.hello.suripu.core.util.SleepHmmWithInterpretation;
import org.joda.time.DateTime;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.UUID;

/**
 * In-memory implementations of the five DAOs the algorithm layer needs.
 *
 * The whole point of orb-algo is that it holds no database connection: every
 * input arrives in the request or comes from files on a mounted volume. These
 * classes are what make that possible.
 *
 * Model provenance is not obvious and is worth stating, because the originals
 * died with the company. The two ensemble files are suripu-core test fixtures
 * (normal3.model, normal3ensemble.model) uploaded under the normal4 names the
 * config expects, and the feature extraction layer is
 * fixtures/algorithm/featureextractionlayer.bin. MultiObsHmmIntegrationTest
 * pairs exactly these three, so they are a matched set from one generation.
 * See knowledgebase/TIMELINE-ALGORITHMS.md.
 */
public final class Models {

    /**
     * Loads the shared default ensemble and per-user seed once at startup.
     *
     * Failing loudly here is deliberate. Without these the ONLINE_HMM algorithm
     * returns no events and the chain falls through to VOTING, which looks like
     * "the algorithm does not work" rather than "a file is missing". That
     * misdiagnosis has already cost a day once.
     */
    public static final class FileEnsembleDAO implements DefaultModelEnsembleDAO {
        private final OnlineHmmPriors ensemble;
        private final OnlineHmmPriors seed;

        public FileEnsembleDAO(final Path ensemblePath, final Path seedPath) throws IOException {
            this.ensemble = load(ensemblePath, "default ensemble");
            this.seed = load(seedPath, "seed model");
        }

        private static OnlineHmmPriors load(final Path p, final String what) throws IOException {
            if (!Files.exists(p)) {
                throw new IOException("missing " + what + " at " + p);
            }
            // Stored base64 in S3, matching bootstrap-aws.sh. Decoded here so
            // the file on disk stays identical to the object in the bucket.
            final byte[] raw = java.util.Base64.getMimeDecoder().decode(Files.readAllBytes(p));
            final Optional<OnlineHmmPriors> parsed = OnlineHmmPriors.createFromProtoBuf(raw);
            if (!parsed.isPresent()) {
                throw new IOException("could not parse " + what + " from " + p);
            }
            return parsed.get();
        }

        @Override public OnlineHmmPriors getDefaultModelEnsemble() { return ensemble; }
        @Override public OnlineHmmPriors getSeedModel() { return seed; }
    }

    /**
     * The feature extraction layer, read once from a file.
     *
     * OnlineHmm.java:398 treats an invalid layer as fatal ("THIS IS REQUIRED!")
     * and returns zero events, so an unreadable file must not be allowed to
     * degrade quietly into a fallthrough.
     */
    public static final class FileFeatureExtractionDAO implements FeatureExtractionModelsDAO {
        private final FeatureExtractionModelData data;

        public FileFeatureExtractionDAO(final Path path) throws IOException {
            if (!Files.exists(path)) {
                throw new IOException("missing feature extraction layer at " + path);
            }
            final byte[] raw = java.util.Base64.getMimeDecoder().decode(Files.readAllBytes(path));
            final FeatureExtractionModelData d = new FeatureExtractionModelData(Optional.<UUID>absent());
            d.deserialize(raw);
            if (!d.isValid()) {
                throw new IOException("feature extraction layer at " + path + " did not deserialize");
            }
            this.data = d;
        }

        @Override
        public FeatureExtractionModelData getLatestModelForDate(final Long accountId,
                                                               final DateTime dateTimeLocalUTC,
                                                               final Optional<UUID> uuid) {
            return data;
        }
    }

    /**
     * The account's learned model, supplied per request rather than fetched.
     *
     * Anything the algorithm learns is captured here and read back by the
     * caller, so orb can persist it. That inversion, learning returned rather
     * than written, is what keeps this service stateless.
     *
     * Empty priors mean "use the default ensemble", which is both the correct
     * start for a new account and the documented way to recover from a
     * collapsed model.
     */
    public static final class RequestModelsDAO implements OnlineHmmModelsDAO {
        private final OnlineHmmData incoming;
        private OnlineHmmPriors updatedPriors;
        private OnlineHmmScratchPad updatedScratchpad;

        public RequestModelsDAO(final byte[] modelBlob, final byte[] scratchBlob) {
            OnlineHmmPriors priors = OnlineHmmPriors.createEmpty();
            OnlineHmmScratchPad scratch = OnlineHmmScratchPad.createEmpty();

            if (modelBlob != null && modelBlob.length > 0) {
                final Optional<OnlineHmmPriors> p = OnlineHmmPriors.createFromProtoBuf(modelBlob);
                if (p.isPresent()) {
                    priors = p.get();
                }
            }
            if (scratchBlob != null && scratchBlob.length > 0) {
                final Optional<OnlineHmmScratchPad> s = OnlineHmmScratchPad.createFromProtobuf(scratchBlob);
                if (s.isPresent()) {
                    scratch = s.get();
                }
            }
            this.incoming = new OnlineHmmData(priors, scratch);
        }

        @Override
        public OnlineHmmData getModelDataByAccountId(final Long accountId, final DateTime date) {
            return incoming;
        }

        @Override
        public boolean updateModelPriorsAndZeroOutScratchpad(final Long accountId,
                                                            final DateTime date,
                                                            final OnlineHmmPriors priors) {
            this.updatedPriors = priors;
            // Zeroing the scratchpad is part of this call's contract: the
            // learning has been promoted into the model, so keeping the
            // scratchpad would apply it a second time on the next run.
            this.updatedScratchpad = OnlineHmmScratchPad.createEmpty();
            return true;
        }

        @Override
        public boolean updateScratchpad(final Long accountId, final DateTime date,
                                        final OnlineHmmScratchPad scratchPad) {
            this.updatedScratchpad = scratchPad;
            return true;
        }

        /** Serialized learned model, or null if the algorithm learned nothing. */
        public byte[] updatedModelBytes() {
            return (updatedPriors == null || updatedPriors.isEmpty()) ? null : updatedPriors.serializeToProtobuf();
        }

        public byte[] updatedScratchpadBytes() {
            return (updatedScratchpad == null || updatedScratchpad.isEmpty()) ? null
                    : updatedScratchpad.serializeToProtobuf();
        }
    }

    /**
     * HMM (the older "YeOlde" algorithm) has no models here and never did: the
     * running stack logs "Failed to retrieve HMM model" on every night. Absent
     * is the honest answer, and it makes the chain skip straight to VOTING
     * rather than pretending.
     */
    public static final class NoSleepHmmDAO implements SleepHmmDAO {
        @Override
        public Optional<SleepHmmWithInterpretation> getLatestModelForDate(final long accountId,
                                                                          final long timeOfInterestMillis) {
            return Optional.absent();
        }
    }

    public static Path path(final String dir, final String name) {
        return Paths.get(dir, name);
    }

    private Models() {}
}
