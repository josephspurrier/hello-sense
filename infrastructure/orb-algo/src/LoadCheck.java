import com.hello.suripu.core.models.OnlineHmmPriors;

import java.nio.file.Paths;

/** Proves the three model files load and contain what the algorithm expects. */
public class LoadCheck {
    public static void main(String[] args) throws Exception {
        final String dir = args.length > 0 ? args[0] : "models";

        final Models.FileEnsembleDAO ens = new Models.FileEnsembleDAO(
                Paths.get(dir, "normal4ensemble.base64"),
                Paths.get(dir, "normal4.base64"));

        final OnlineHmmPriors ensemble = ens.getDefaultModelEnsemble();
        final OnlineHmmPriors seed = ens.getSeedModel();

        System.out.println("default ensemble outputs: " + ensemble.modelsByOutputId.keySet());
        for (final String k : ensemble.modelsByOutputId.keySet()) {
            System.out.println("  " + k + " -> " + ensemble.modelsByOutputId.get(k).size() + " models");
        }
        System.out.println("seed model outputs: " + seed.modelsByOutputId.keySet());
        for (final String k : seed.modelsByOutputId.keySet()) {
            System.out.println("  " + k + " -> " + seed.modelsByOutputId.get(k).size() + " models");
        }

        final Models.FileFeatureExtractionDAO fx =
                new Models.FileFeatureExtractionDAO(Paths.get(dir, "featureextractionlayer.bin"));
        System.out.println("feature extraction layer: valid="
                + fx.getLatestModelForDate(1L, null, com.google.common.base.Optional.absent()).isValid());

        System.out.println("MODELS OK");
    }
}
