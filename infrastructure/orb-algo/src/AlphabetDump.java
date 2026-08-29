import com.hello.suripu.core.models.OnlineHmmModelParams;
import com.hello.suripu.core.models.OnlineHmmPriors;

import java.nio.file.Paths;
import java.util.Map;
import java.util.TreeMap;

/**
 * Prints each model's alphabet dimensions per feature.
 *
 * Every output model carries its OWN alphabet for each feature, so SLEEP's
 * motion3 alphabet can be a different width from BED's. If a feature path
 * contains a symbol index at or beyond the alphabet width, or the alphabet has
 * no probability mass for it, the observation probability is zero, log(0) is
 * -Infinity, and the forward pass yields NaN.
 *
 * Observed path maxima from a real night, taken from the running service logs:
 *   motion3 up to 8, motion2 up to 6, artificiallight up to 2,
 *   waves1 up to 2, sound3 and lightincrease 0
 */
public class AlphabetDump {
    public static void main(String[] args) throws Exception {
        final String dir = args.length > 0 ? args[0] : "models";
        final Models.FileEnsembleDAO ens = new Models.FileEnsembleDAO(
                Paths.get(dir, "normal4ensemble.base64"), Paths.get(dir, "normal4.base64"));

        report("DEFAULT ENSEMBLE", ens.getDefaultModelEnsemble());
        report("SEED", ens.getSeedModel());
    }

    private static void report(final String label, final OnlineHmmPriors priors) {
        System.out.println("== " + label + " ==");
        for (final String outputId : priors.modelsByOutputId.keySet()) {
            // Widths can vary between the models in an ensemble; report the range.
            final TreeMap<String, int[]> minMax = new TreeMap<>();
            int states = 0;
            for (final OnlineHmmModelParams p : priors.modelsByOutputId.get(outputId).values()) {
                states = p.pi.length;
                for (final Map.Entry<String, double[][]> e : p.logAlphabetNumerators.entrySet()) {
                    final int width = e.getValue()[0].length;
                    final int[] mm = minMax.get(e.getKey());
                    if (mm == null) {
                        minMax.put(e.getKey(), new int[]{width, width});
                    } else {
                        mm[0] = Math.min(mm[0], width);
                        mm[1] = Math.max(mm[1], width);
                    }
                }
            }
            System.out.println("  " + outputId + " (" + states + " states):");
            for (final Map.Entry<String, int[]> e : minMax.entrySet()) {
                final int[] mm = e.getValue();
                final String w = (mm[0] == mm[1]) ? String.valueOf(mm[0]) : mm[0] + ".." + mm[1];
                System.out.println("      " + pad(e.getKey()) + " alphabet width " + w
                        + "  (max symbol index " + (mm[0] - 1) + ")");
            }
        }
    }

    private static String pad(final String s) {
        final StringBuilder sb = new StringBuilder(s);
        while (sb.length() < 16) sb.append(' ');
        return sb.toString();
    }
}
