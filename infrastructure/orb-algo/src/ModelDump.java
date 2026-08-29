import com.hello.suripu.core.models.OnlineHmmModelParams;
import com.hello.suripu.core.models.OnlineHmmPriors;

import java.nio.file.Paths;
import java.util.Map;
import java.util.TreeSet;

/**
 * Prints which measurement types each model's alphabet covers.
 *
 * A model can only score a measurement it has an alphabet for. If SLEEP
 * requires a type the binning never fills, its observation probability is zero,
 * log(0) is -Infinity, and differences of infinities are NaN. That is the
 * shape of the failure being chased.
 */
public class ModelDump {
    public static void main(String[] args) throws Exception {
        final String dir = args.length > 0 ? args[0] : "models";
        final Models.FileEnsembleDAO ens = new Models.FileEnsembleDAO(
                Paths.get(dir, "normal4ensemble.base64"), Paths.get(dir, "normal4.base64"));

        dump("DEFAULT ENSEMBLE", ens.getDefaultModelEnsemble());
        dump("SEED", ens.getSeedModel());
    }

    private static void dump(final String label, final OnlineHmmPriors priors) {
        System.out.println("== " + label + " ==");
        for (final String outputId : priors.modelsByOutputId.keySet()) {
            final Map<String, OnlineHmmModelParams> byId = priors.modelsByOutputId.get(outputId);
            final TreeSet<String> allMeas = new TreeSet<>();
            int states = -1;
            for (final OnlineHmmModelParams p : byId.values()) {
                allMeas.addAll(p.logAlphabetNumerators.keySet());
                states = p.pi.length;
            }
            System.out.println("  " + outputId + ": " + byId.size() + " models, "
                    + states + " states, measurements=" + allMeas);
        }
    }
}
