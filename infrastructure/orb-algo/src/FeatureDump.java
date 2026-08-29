import com.google.common.base.Optional;
import com.hello.suripu.core.util.DeserializedFeatureExtractionWithParams;

import java.nio.file.Paths;
import java.util.TreeSet;
import java.util.UUID;

/**
 * Prints the feature names the extraction layer can produce, and compares them
 * against what the SLEEP model requires.
 *
 * A model scoring a feature the layer never emits sees probability zero, which
 * is -Infinity in the log domain and NaN once differenced. This is the direct
 * test for that.
 */
public class FeatureDump {
    public static void main(String[] args) throws Exception {
        final String dir = args.length > 0 ? args[0] : "models";

        final Models.FileFeatureExtractionDAO fx =
                new Models.FileFeatureExtractionDAO(Paths.get(dir, "featureextractionlayer.bin"));
        final DeserializedFeatureExtractionWithParams d =
                fx.getLatestModelForDate(1L, null, Optional.<UUID>absent()).getDeserializedData();

        final TreeSet<String> produced = new TreeSet<>(d.sensorDataReduction.hmmByModelName.keySet());
        System.out.println("layer produces: " + produced);
        System.out.println("measurement params: numMinutesInMeasPeriod=" + d.params.numMinutesInMeasPeriod);

        final Models.FileEnsembleDAO ens = new Models.FileEnsembleDAO(
                Paths.get(dir, "normal4ensemble.base64"), Paths.get(dir, "normal4.base64"));

        for (final String outputId : ens.getDefaultModelEnsemble().modelsByOutputId.keySet()) {
            final TreeSet<String> needed = new TreeSet<>();
            for (final com.hello.suripu.core.models.OnlineHmmModelParams p :
                    ens.getDefaultModelEnsemble().modelsByOutputId.get(outputId).values()) {
                needed.addAll(p.logAlphabetNumerators.keySet());
            }
            final TreeSet<String> missing = new TreeSet<>(needed);
            missing.removeAll(produced);
            System.out.println(outputId + " needs " + needed
                    + (missing.isEmpty() ? "  ALL PRESENT" : "  MISSING=" + missing));
        }
    }
}
