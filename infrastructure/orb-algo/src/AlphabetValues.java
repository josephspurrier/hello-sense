import com.hello.suripu.core.models.OnlineHmmModelParams;
import com.hello.suripu.core.models.OnlineHmmPriors;

import java.nio.file.Paths;
import java.util.Map;

/**
 * Looks for non-finite values in the model parameters.
 *
 * An observation log-probability is roughly
 *     logAlphabetNumerators[feature][state][symbol] - logDenominator[state]
 * so -Infinity minus -Infinity is NaN. That is the classic way a structurally
 * valid HMM still evaluates to NaN, and it is the last untested explanation for
 * SLEEP failing where BED succeeds on identical input.
 */
public class AlphabetValues {
    public static void main(String[] args) throws Exception {
        final String dir = args.length > 0 ? args[0] : "models";
        final Models.FileEnsembleDAO ens = new Models.FileEnsembleDAO(
                Paths.get(dir, "normal4ensemble.base64"), Paths.get(dir, "normal4.base64"));
        scan("DEFAULT ENSEMBLE", ens.getDefaultModelEnsemble());
        scan("SEED", ens.getSeedModel());
    }

    private static void scan(final String label, final OnlineHmmPriors priors) {
        System.out.println("== " + label + " ==");
        for (final String outputId : priors.modelsByOutputId.keySet()) {
            int modelsWithNonFiniteAlphabet = 0;
            int modelsWithNonFiniteDenom = 0;
            int modelsWithNonFinitePi = 0;
            int modelsWithNonFiniteTrans = 0;
            int total = 0;
            String example = null;

            for (final Map.Entry<String, OnlineHmmModelParams> me :
                    priors.modelsByOutputId.get(outputId).entrySet()) {
                final OnlineHmmModelParams p = me.getValue();
                total++;

                boolean badAlpha = false;
                for (final Map.Entry<String, double[][]> e : p.logAlphabetNumerators.entrySet()) {
                    for (int st = 0; st < e.getValue().length; st++) {
                        for (int sym = 0; sym < e.getValue()[st].length; sym++) {
                            final double v = e.getValue()[st][sym];
                            if (Double.isNaN(v) || Double.isInfinite(v)) {
                                badAlpha = true;
                                if (example == null) {
                                    example = me.getKey() + " " + e.getKey()
                                            + " state=" + st + " symbol=" + sym + " value=" + v;
                                }
                            }
                        }
                    }
                }
                if (badAlpha) modelsWithNonFiniteAlphabet++;
                if (anyNonFinite(p.logDenominator)) modelsWithNonFiniteDenom++;
                if (anyNonFinite(p.pi)) modelsWithNonFinitePi++;
                for (final double[] row : p.logTransitionMatrixNumerator) {
                    if (anyNonFinite(row)) { modelsWithNonFiniteTrans++; break; }
                }
            }

            System.out.println("  " + outputId + ": " + total + " models");
            System.out.println("      non-finite alphabet:    " + modelsWithNonFiniteAlphabet);
            System.out.println("      non-finite denominator: " + modelsWithNonFiniteDenom);
            System.out.println("      non-finite pi:          " + modelsWithNonFinitePi);
            System.out.println("      non-finite transitions: " + modelsWithNonFiniteTrans);
            if (example != null) {
                System.out.println("      first example: " + example);
            }
        }
    }

    private static boolean anyNonFinite(final double[] a) {
        for (final double v : a) {
            if (Double.isNaN(v) || Double.isInfinite(v)) return true;
        }
        return false;
    }
}
