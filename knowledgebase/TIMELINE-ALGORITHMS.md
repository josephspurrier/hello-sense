# Timeline algorithms, and the feature flags that hide them

Written 2026-08-13, after two days of chasing why the app looked like it was
working while quietly doing nothing.

## The thing that costs the most time: a missing flag row means OFF

suripu gates a large amount of working code behind feature flags held in the
DynamoDB `features` table. `DynamoDBAdapter.userFeatureActive` returns `false`
for a flag whose row does not exist, and logs one `Feature <name> is null`
warning. There is no code-level default. Nothing creates those rows:
`bootstrap-dynamodb.sh` creates the table and leaves it empty.

So the whole system runs in its most degraded configuration by default, and says
so only in a warning line buried in a very chatty log. Run
`infrastructure/docker/seed-features.sh` after bootstrapping. Value format is
`percentage|userIds|groups` split into exactly 3 parts (`Feature.java:77`);
group `all` matches everyone. Namespace is `dev` because every config sets
`debug: true` (`SuripuApp.java:484`). The adapter polls every 30 seconds
(`SuripuApp.java:488`), so no restart is needed.

The first one that bit: **timeline feedback**. Correcting a sleep time in the
app writes a row to `common.timeline_feedback` and returns 200, and the response
even shows the corrected timeline, because the in-flight edit is passed straight
through (`InstrumentedTimelineProcessor.java:607`). But every subsequent load
calls `getFeedbackList`, which checks `feedback_in_timeline` and returns an
empty list *before it ever queries Postgres* (`:1182`). So the correction
saved, and was then discarded on read, forever. The tell is in the log:

    apply_feedback num_items=1   <- during the PATCH
    apply_feedback num_items=0   <- every load after

## The algorithm chain

`InstrumentedTimelineProcessor.java:308` builds the chain and takes the first
algorithm that returns a usable result:

    NEURAL_NET_FOUR_EVENT   always first (the flag check at :317 is commented out)
    ONLINE_HMM              inserted at the front when online_hmm_algorithm is on
    HMM
    VOTING

### The neural nets are permanently dead. Stop looking.

`NEURAL_NET_FOUR_EVENT` and `NEURAL_NET` call out to **taimurain**, a small
Flask service that loads Keras models and answers `/v1/neuralnet/evaluate`.
It wants nets named `SLEEP2` (`NeuralNetFourEventAlgorithm.java:41`) and `SLEEP`
(`NeuralNetAlgorithm.java:43`), each a Keras architecture `.json` plus an HDF5
`.h5` weights file, from `s3://hello-neuralnet-models-keras-v1p1/version001`.

Those weights do not exist anywhere. Evidence, so nobody repeats this:

- Both candidate buckets return `NoSuchBucket`.
- Walked the full git tree of all 117 repos in the `hello` GitHub org via the
  API, which lists binaries that code search cannot see. Filtering for `.h5`,
  `.hdf5`, `.npy`, `.npz`, `.pkl`, `.caffemodel`, `.pb` gives 41 hits: 40 are
  the HDF5 C library's own test fixtures vendored into `kuria/haltija`, and one
  is `onsei/dataprep/logM_kwClip_1.pkl`, which is voice keyword spotting.
- taimurain's own history, all nine branches including `kerasv1p1`, contains no
  model artifact.
- `github-backup/` covers every repo in the org, so searching it locally is as
  authoritative as the API.

Retraining is not a way out either. The contract is rigid: 16 input channels
(`SensorIndices.MAX_NUM_INDICES`, a fixed enum including AGE, BMI, MALE, FEMALE)
and exactly 9 output channels, rejected otherwise at `:397`. Worse, those 9
outputs feed a 9x10 matrix of logistic regression coefficients hard-coded into
the Java (`LOG_REG_COEFS`, `:49`), so a replacement net would have to reproduce
the exact semantic meaning and ordering of all 9 original channels. The label
taxonomy that defined them is not in any repo. `kasetsu/keras/timeline/train.py`
exists but wants label files that are absent, and its data is 8 columns of one
day.

`kuria` is not a lead. `kuria/haltija` is C++ DSP for the Novelda X4 **radar**
respiration sensor, a different product. Its HDF5 hits are the vendored library
for reading MATLAB files.

Consequence: running taimurain with no models is worse than not running it. It
answers HTTP 400 `net_not_found` (`app.py:104`) instead of refusing the
connection, and the chain falls through exactly as before.

### ONLINE_HMM works, and needs three things, all recovered from test fixtures

Unlike the neural nets, this one is alive. Production's models are also gone,
but their immediate predecessors are checked in as suripu-core test fixtures,
and they form a matched set:

| Piece | Fixture | Goes to | Seeded by |
|---|---|---|---|
| default model ensemble | `normal3ensemble.model` | `s3://hello-timeline-models/normal4ensemble.base64` | `bootstrap-aws.sh` |
| per-user seed model | `normal3.model` | `s3://hello-timeline-models/normal4.base64` | `bootstrap-aws.sh` |
| feature extraction layer | `featureextractionlayer.bin` | `feature_extraction_models` DynamoDB table | `seed-algorithm-models.sh` |

They belong together: `MultiObsHmmIntegrationTest` pairs
`featureextractionlayer.bin` with `normal3.model` and `normal3ensemble.model`.
All are protobuf, nothing to do with Keras. The `.base64` naming is because the
S3 config expects base64 while the fixtures are raw.

The third one is the trap. Miss it and `OnlineHmm.java:398` logs

    failed to get feature extraction layer!  THIS IS REQUIRED!

and returns zero events, so the chain falls through to VOTING and ONLINE_HMM
looks like it simply does not work. Seed it against account ids **-1 to -5**,
not a real account: `getLatestModelForDate` tries the caller's own account and
then falls back to `getDefaultAccountId(accountId) = -1 - (accountId % 5)`,
which spreads accounts across five hash keys. Account 1 lands on -2.

Two flags, not one. `online_hmm_algorithm` puts it in the chain.
`online_hmm_learning` is what makes `feedbackChanged` true
(`InstrumentedTimelineProcessor.java:305`), which is the condition
`OnlineHmm.java:197` requires before seeding a **per-user** model from the seed
model. Without it you stay on the shared default ensemble forever and
`online_hmm_models` stays empty. With it, correcting a timeline both moves that
night's events and trains the model.

## What it actually does, so far

Honest record, two nights.

**Night of 2026-08-11.** Worked. `alg_status=NO_ERROR`, chain terminated at
ONLINE_HMM, all four events found, and the learning path fired: `new scratchpad
models: [BED-1-custom, SLEEP-1-custom]` and a row appeared in
`online_hmm_models`. Scores moved in both directions versus VOTING: 2026-08-11
went 71 -> 86 -> 76 (the drop to 76 is the moment the custom models displaced
the default ensemble), 2026-08-10 went 73 -> 65. Different, and self-adjusting,
not uniformly better.

**Night of 2026-08-12.** `alg_status=MISSING_KEY_EVENTS`. It found IN_BED and
OUT_OF_BED but no SLEEP or WAKE_UP, so it bailed and the chain fell through to
VOTING, which scored 83, then 85 once the missing data was recovered.

Sensor data for that night had also stopped at `2026-08-13 00:30` local, five
minutes after IN_BED, when LocalStack's Kinesis died
([LOCALSTACK-KINESIS.md](LOCALSTACK-KINESIS.md)). That looked like a complete
explanation and it was not. After seven hours of data were recovered from the
stream and replayed, ONLINE_HMM produced **the same IN_BED and OUT_OF_BED to the
minute** and still returned `MISSING_KEY_EVENTS`. The cause was the learned
model, not the data. See the next section.

The lesson that does survive: check the data exists before blaming an algorithm,
but do not stop once you find a data problem. Two faults can overlap, and the
loud one is not always the one doing the damage.

The lesson worth keeping: ONLINE_HMM requires all four key events and returns
`MISSING_KEY_EVENTS` rather than a partial answer, so it is strictly more
fragile than VOTING. VOTING remaining last in the chain is the safety net, and
should stay there.

## How learning works, and the two ways it surprises you

With `online_hmm_learning` on, a timeline correction does two things: it moves
that night's events, and it trains the model. Both surprises below cost time.

### Correcting one end of a pair teaches a one-sided path

There are two independent 3-state models, and `LabelMaker` builds labels for
each from a **pair** of corrected events:

| Model | Needs | States |
|---|---|---|
| BED | `IN_BED` + `OUT_OF_BED` | PRE_BED(0), DURING_BED(1), POST_BED(2) |
| SLEEP | `SLEEP` + `WAKE_UP` | PRE_SLEEP(0), DURING_SLEEP(1), POST_SLEEP(2) |

Correct both and it calls `labelEventPair`, which emits all three states.
Correct only one and it falls to `labelSingleEvent`, which emits **two states
and never the third**, and only covers
`GUARANTEED_SLEEP_PERIOD_FROM_ONE_LABEL` (180 minutes) around the event:

- Opening event alone (`SLEEP`, `IN_BED`, `isFirstEventInPair=true`,
  `LabelMaker.java:80`): states 0 and 1, never state 2.
- Closing event alone (`WAKE_UP`, `OUT_OF_BED`, `isFirstEventInPair=false`,
  `:89`): states 1 and 2, never state 0.

`reestimateForMyModel` (`OnlineHmmModelLearner.java:236`) then runs Baum-Welch
on that. A 3-state HMM trained on evidence where one state never occurs has the
transition into that state driven toward zero. From a single night it can
collapse entirely.

That is exactly what happened here. Bins are 5 minutes and the window starts
20:00 local, so a `SLEEP` correction at 00:55 with no matching `WAKE_UP` gave
the SLEEP model 59 bins of state 0, 36 bins of state 1, **zero bins of state 2**,
and 193 bins unlabelled. The result, visible in the evaluator log:

    transitions for BED   are [Transition{from=0,to=1,idx=55}, Transition{from=1,to=2,idx=140}]
    transitions for SLEEP are []

The BED model was fine because a separate night had supplied an `OUT_OF_BED`
correction, so it had seen state 2. The SLEEP model had not, and it produced an
all-zero probability path, no transitions, no SLEEP and no WAKE_UP, hence
`MISSING_KEY_EVENTS` on every subsequent night.

**So: correct both ends of a pair, or neither.** Fixing only the time you fell
asleep silently teaches the model that waking up does not happen.

In production this never showed up, because models saw many nights from many
users and the one-sided cases averaged out. With one night of feedback there is
nothing holding the model up.

### Learning is deferred by a day, so a correction never shows up the same day

`updateModelPriorsWithScratchpad` (`OnlineHmm.java:89`) promotes learning from a
scratchpad into the live model only when the scratchpad is older than the start
of the night being processed:

    if (newModel.lastUpdateTimeUtc >= startTimeUtc && !forceUpdate) {
        logger.info("scratchpad not old enough -- not updating models");

The comment is "old enough == it was created yesterday or earlier". This is
deliberate: it stops feedback on a night from immediately rewriting the model
being used to render that same night.

The practical consequence is that a correction made this morning is invisible to
the model until tonight's timeline is generated tomorrow. If you correct
something and the timeline does not change the way you expect, check for that
log line before concluding the correction was ignored. It is queued, not lost,
and the feedback row is in `common.timeline_feedback` either way.

Corollary when debugging: read `common.timeline_feedback` fresh every time.
Reasoning from a snapshot taken a day earlier produced a confidently wrong
diagnosis here, claiming a `WAKE_UP` correction had never been made when row 6
had recorded one 45 minutes before the run being analysed. Decode `event_type`
against `Event.Type` in `Event.java`: 11 IN_BED, 12 SLEEP, 13 OUT_OF_BED,
14 WAKE_UP.

### `SLEEP = NaN` on a night is normal, and is not a broken installation

The SLEEP model evaluating to `NaN` across most of a night, giving
`transitions for SLEEP are []`, then `not enough transitions found for output id
SLEEP`, then `alg_status=MISSING_KEY_EVENTS` and a fall through to VOTING, is
something **the original suripu does routinely**. On this account it happened on
2026-08-12 and not on 2026-08-13, same model, consecutive nights:

    2026-08-12  SLEEP = [0.0 x54, NaN, NaN, ...]        transitions []      -> VOTING
    2026-08-13  SLEEP = [0.0 x40, 0.0575, 0.0575, 1.0]  transitions [41,96] -> NO_ERROR

SLEEP needs six features where BED needs two (see the measurement lists above),
so it has six chances for an observation probability to reach zero, and
`log(0) = -Infinity` differences give `NaN`. A night whose data does not suit
the model produces that; the next night can be fine.

**So a run of `MISSING_KEY_EVENTS` is not by itself evidence of anything.**
Check whether it is every night or some nights before concluding the model has
collapsed. Two days went into hunting a nonexistent bug in `orb-algo` because
this was assumed to be one; one `docker logs hello-orb-suripu-app-1 | grep
"SLEEP = "` would have shown the reference doing the same thing.

### An empty bed and a perfectly still sleeper are the same signal

The Sleep Pill measures pillow motion, so "no motion" is ambiguous. On the night
of 2026-08-14 the sleeper was up until 04:50 and the pill recorded:

    23:04 - 23:36   13 samples, amplitudes to 24,172   handling the pillow
    23:36 - 04:35   nothing at all                     nobody in the bed
    04:35 - 04:37   3 samples, amplitudes to 15,846    actually getting in
    04:50 - 09:26   small movements                    actually asleep

VOTING read the five silent hours as sleep and put bedtime at 23:40, five hours
early. It is not a bug: an undisturbed pillow looks exactly like a motionless
sleeper, and nothing else in the data distinguishes them.

**ONLINE_HMM caught it and VOTING did not.** ONLINE_HMM returned
`alg_status=NO_MOTION_DURING_SLEEP` and declined the night, which is a
safeguard doing precisely its job. The chain then fell through to VOTING, which
has no equivalent check, so the worse answer won because it was the only answer.

Worth knowing when a timeline looks confidently wrong at one end: check whether
the "sleep" period contains any motion at all before assuming the algorithm
misjudged something subtle.

### Recovering from a collapsed model

The per-user model lives in the `online_hmm_models` DynamoDB table, one row per
date, with the pending scratchpad on the same rows. Deleting them reverts to the
default ensemble from `normal4ensemble.base64`, which is known good.
`OnlineHmm.java:409` only gives up when the user model and the default ensemble
are both empty. Back the rows up first: deleting also discards any queued
scratchpad, so pending corrections go with it.

## Flags deliberately left off

Recorded in `seed-features.sh` with reasons. Briefly: OTA (no firmware to serve,
and a half-configured OTA path is how an Orb gets bricked), audio capture
(streams to a Kinesis stream and S3 bucket that do not exist), push (needs real
APNS credentials), the sleep score versions (changes scoring retroactively, a
preference not a fix), `timeline_processor_v3_enabled` (untried), and the kill
switches like `timeline_lockdown` where off is the working state.

Also off, and worth trying one at a time against a night you can eyeball rather
than as a batch: `outlier_filter`, `pill_pair_motion_filter`,
`off_bed_hmm_motion_filter`, `min_motion_amplitude_high_threshold`,
`sleep_score_no_motion_enforcement`, `sound_events_use_higher_threshold`. These
are motion filtering and event threshold knobs; off is the untuned baseline.

## The BED model collapsed, and one-sided feedback is the prime suspect

2026-08-17. ONLINE_HMM has not won a night since 2026-08-15. The symptom is the
one already documented here for SLEEP, with the two models swapped.

    transitions for BED   are []
    transitions for SLEEP are [0->1 idx=58, 1->2 idx=151]

BED produces ZERO transitions, not one short of the two that
`OnlineHmm.getSleepEventsFromPredictions` needs (`transitions.size() < 2` and it
skips the output). The voted path never leaves its initial state across the
whole window. `Server.java` then applies all-four-or-nothing and falls through
to VOTING, every night.

Note the reversal. The earlier investigation in CONSOLIDATION-PLAN.md recorded
BED healthy and SLEEP producing NaN. SLEEP has since recovered and BED has since
broken. Nobody set out to trade one for the other.

### Why one-sided feedback is the suspect

The mechanism is already written down, in the package comment on
`full-instructions/infrastructure/orb/internal/timeline`: *"On 2026-08-13 a single one-sided feedback correction
silently collapsed the SLEEP model into an all-zero path."* And on the Feedback
struct: *"Both ends of a pair matter."*

`LabelMaker` builds labels per output and has three branches per pair:

    outofbeds > 0 && inbeds > 0   -> a full path
    outofbeds > 0                 -> one-sided
    inbeds > 0                    -> one-sided

There is exactly one BED correction in this account's history and it took the
third branch:

    id 13 | night 2026-08-14 | event_type 11 (IN_BED) | 22:58 -> 04:50
          | created 2026-08-15 13:32

A nearly six-hour move, with no matching OUT_OF_BED.

The timing then fits the promotion rule exactly. A scratchpad is only folded
into the model once it was "created yesterday or earlier"
(`updateModelPriorsWithScratchpad`):

| when | what | outcome |
|---|---|---|
| 08-15 13:32 | one-sided IN_BED feedback lands in the scratchpad | |
| 08-15 19:00 | batch rescore; scratchpad not old enough, models untouched | 08-10..08-14 all ONLINE_HMM |
| 08-16 15:58 | scratchpad now old enough, promoted into the model | 08-15 night -> VOTING |
| every night since | | VOTING |

ONLINE_HMM won every night up to and including 08-14 and has not won one since.

**This is a strong hypothesis, not a verified fact.** It has not been proven by
reverting the model and re-running.

### What NOT to do

Do not "train BED by correcting the bed markers for a few nights" unless both
ends are corrected on the SAME night. Correcting in-bed alone is most likely
what caused this, and more of it would deepen the hole. The first LabelMaker
branch is the safe one.

### A targeted reset is feasible

The user's priors round-trip cleanly:

    OnlineHmmPriors.createFromProtoBuf(blob)   // Models.RequestModelsDAO does this
    priors.modelsByOutputId.remove("BED")      // fields are final refs to MUTABLE maps
    priors.votingInfo.remove("BED")
    priors.serializeToProtobuf()

`serializeToProtobuf` emits whatever those two maps contain, so dropping BED
leaves the learned SLEEP models intact. The evaluator merges the default
ensemble FIRST (`allModels.mergeFrom(defaultEnsemble)` then `mergeFrom(userPrior)`),
so removing the user's BED falls back to the ensemble's 179 BED models, which
were healthy in the earlier investigation.

Deleting the `hmm_models` row instead would reset SLEEP too, which currently
works. That is the thing to avoid.

**Test the premise before doing the surgery.** The 08-14 row's `model_params`
was written 2026-08-15 19:00, before the promotion, so it should still contain a
healthy BED. Scoring a night with that blob as `prior_model` and watching for
`transitions for BED` in orb-algo's log distinguishes "the user's BED model is
poison" from "BED is broken for some other reason". Nothing needs to be mutated
to find out: `LatestModel` picks `model_params` by `date_of_night DESC`, so an
extra row with a future date is enough, and a single DELETE undoes it.

### Resolved 2026-08-17: reverted, and the trap that nearly made it temporary

Confirmed causal, not just correlated. Only two model blobs ever existed:
`2be91d…` (08-13, 08-14, when ONLINE_HMM won) and `eed125…` (08-15, 08-16,
VOTING ever since). Staging the older blob as the latest, via an extra
`hmm_models` row with a future date so nothing was mutated, and rescoring:

    with eed125:   transitions for BED are []
    with 2be91d:   transitions for BED are [0->1 idx=41, 1->2 idx=130]
                   scored night 2026-08-13 algorithm=ONLINE_HMM score=77

Same night, same data, same code, only the model swapped. It also repaired
2026-08-13, which had been damaged by an earlier forced recompute: that night
went from nine differing fields to `match (200)` against the reference.

The revert:

    UPDATE hmm_models SET model_params = <2be91d> WHERE date_of_night IN ('2026-08-15','2026-08-16');
    UPDATE hmm_models SET scratchpad = NULL WHERE date_of_night IN ('2026-08-10','2026-08-11','2026-08-12','2026-08-14');

**The second statement is the one that is easy to miss, and without it the fix
lasts until the next promotion.** `SaveModel` writes NULL to the scratchpad once
learning is promoted, and `LatestModel` then selects the newest remaining
NON-NULL scratchpad. Mapping each stale scratchpad to the feedback that produced
it shows what would have been resurrected:

| night | feedback | BED branch |
|---|---|---|
| 08-10 | OUT_OF_BED | one-sided |
| 08-11 | IN_BED + SLEEP + OUT_OF_BED | paired, safe |
| 08-12 | OUT_OF_BED + WAKE_UP | one-sided |
| 08-14 | IN_BED | one-sided, the collapse |
| 08-16 | WAKE_UP x2 | SLEEP only, safe |

Three separate one-sided BED scratchpads, any one of which re-poisons the model
when promoted. Only 08-16's was kept, its content verified as `[SLEEP-1-custom]`
in the orb-algo log.

Effect on the night that was rescored to test it (2026-08-16): wake held exactly
at the corrected 08:35 and the feedback guard stayed quiet, while in-bed moved
00:22 to 00:50, asleep 00:53 to 01:05, out-of-bed 08:48 to 08:40, score 77 to
76. That is ONLINE_HMM's opinion rather than VOTING's, not a defect.

Backup of the whole table before the change:
`/tmp/claude-501/bedtest/hmm_models_backup.sql` (session-scoped, will not
survive a reboot; re-dump if it matters).

**Nothing prevents a recurrence.** The app can send a one-sided BED correction
at any time and collapse the model again. See the open item in STATE.md.
