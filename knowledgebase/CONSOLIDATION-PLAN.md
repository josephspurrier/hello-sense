# Consolidation plan: 16 containers to 3, Java/Clojure/Python to Go

Written 2026-08-13. A design, not a record of work done. Nothing here is built
yet except the phase 0 spike.

## Why

The stack is Hello's production architecture, sized for a customer base of about
100,000 devices. It supports one person and one Orb sending 2,255 records a day.
Every part of it is running at a rounding error of its design load, and the
operational cost is real: most of the failures in this project have come from
the plumbing (Kinesis, LocalStack, DynamoDB, the KCL) rather than from anything
to do with sleep tracking.

Costed on AWS the current shape is roughly $265 to $295 a month, dominated by
ten Kinesis shards (~$108) and eleven Fargate tasks (~$100+). The consolidated
shape is about $12. That is not the reason to do it, but it is a good measure of
how oversized it is.

## Constraints that do not move

1. **The iOS app's wire contract.** The app cannot be rebuilt, so whatever
   serves it must match exactly. The real surface is small: about 25 endpoints,
   taken from the access log rather than from the source, so it reflects what
   the app actually calls.

       GET  /v2/sensors            POST /v2/sensors
       GET  /v2/timeline/{date}    PATCH /v2/timeline/{date}/events/{type}/{ts}
       GET  /v2/account/preferences
       GET  /v2/devices            GET  /v2/alarms       GET /v2/alerts
       GET  /v2/insights           GET  /v2/insights/info/{cat}
       GET  /v2/trends/{period}    GET  /v2/ping
       GET  /v2/sleep_sounds/status, /combined_state, /sounds
       GET  /v1/account            PUT  /v1/account
       GET  /v1/app/stats/unread   PATCH /v1/app/stats
       GET  /v1/timezone           GET  /v1/questions/?date=
       POST /v1/oauth2/token

2. **The Sense protocol.** Protobuf bodies, AES-CBC signed with a per-device key,
   three hostnames (`sense-in`, `time`, `messeji`), and a TLS handshake modern
   stacks reject. See [../full-instructions/SENSE_SETUP.md](../full-instructions/SENSE_SETUP.md).

3. **The sleep algorithms.** See "what not to rewrite" below.

## Before

    Orb --TLS--> sense_server.py (host, Python/tlslite-ng)
                      |  dns_server.py (host, Python)
                      +--> hello-time      (Java)     time sync
                      +--> suripu-service  (Java)     device ingest
                      +--> messeji         (Clojure)  command long-poll
                                |
                           Kinesis (LocalStack) 10 streams
                                |
       7 Java workers: sense-save, pill-save, sense-last-seen,
                       smart-alarm, push, insights-gen, aggstats-gen
                                |
                DynamoDB 67 tables | Postgres 3 DBs | Redis | S3 | KMS

    iPhone --> suripu-app (Java) --> same stores

16 containers plus 2 host processes, ~4.4 GB resident, four languages.

## After

    Orb    --TLS--> +
    iPhone -------> +-- orb (Go, single binary)
                    |     edge:   TLS, /in/sense/batch, time sync, messeji
                    |     api:    the ~25 endpoints
                    |     worker: goroutines on tickers
                    |     dns:    *.hello.is
                    |       |
                    |       +--> postgres            the only datastore
                    |       +--> orb-algo (Java)     stateless: samples in, events out

3 containers, ~350 MB resident, two languages. Runs on a Raspberry Pi.

## What disappears

| Gone | Replaced by | Why it is safe |
|---|---|---|
| Kinesis, 10 streams | direct write, or one Postgres table | A shard carries 1,000 rec/s; this uses 0.026. The queue decouples a fleet. Four of the ten streams held zero records across 17 days. |
| DynamoDB, 67 tables | Postgres tables | Mostly empty or single-row. `alarm_info` holds one item. |
| Redis | Postgres | Used only by messeji and two workers, as a buffer for one device. |
| LocalStack | filesystem | S3 holds 3 objects, KMS 3 keys. Firehose/SNS/SQS/CloudWatch unused. |
| 7 worker containers | goroutines with tickers | They are cron jobs. Seven JVMs for a handful of periodic tasks. |
| messeji (Clojure + Redis) | ~200 lines of Go | A long-poll queue for one device. |
| hello-time | ~50 lines of Go | An NTP timestamp plus an AES signature, both already implemented in `sense_server.py`. |
| sense_server.py, dns_server.py | Go, same binary | Conditional on the phase 0 spike. |

Deleting Kinesis alone removes an entire class of failure already seen here:
the OOM restart loop, the 68 MB state file, KCL leases, `SHARD_END` poisoning,
and sequence-number corruption. See
[LOCALSTACK-KINESIS.md](LOCALSTACK-KINESIS.md).

## What not to rewrite

**Keep the timeline algorithms in Java**, reduced to one stateless service:
sensor and motion arrays in, events out, models read from a mounted directory.
No DynamoDB, no Kinesis, no S3.

This is deliberate and is the main reason the target is not "all Go". The
algorithms are subtle in ways that are invisible until they are wrong. On
2026-08-13 a single one-sided feedback correction silently collapsed the SLEEP
model into an all-zero path, and the only symptom was `MISSING_KEY_EVENTS` on a
night that otherwise looked fine (see
[TIMELINE-ALGORITHMS.md](TIMELINE-ALGORITHMS.md)). Reimplementing Baum-Welch,
the 5-minute binning, the feature extraction layer and the voting heuristics in
Go means recreating behaviour that cannot be verified, on a system whose only
ground truth is how you felt that morning.

Keeping them turns 11 JVMs into 1 and removes the risk. If a Go port is wanted
later, do VOTING first: it is heuristic (motion score, light events) and can be
diffed against the Java output night by night before being trusted.

## Progress

**Phase 1 complete** (2026-08-13). `full-instructions/infrastructure/orb/migrations/0001_init.sql` creates 21
tables in a new `orb` database; `full-instructions/infrastructure/orb/cmd/migrate` moved **20,100 rows** from
three Postgres databases and 24 DynamoDB tables. Non-destructive: the old stores
were untouched. Verified by round-tripping values, not by trusting row counts.

**Phase 2 complete** (2026-08-13). Every device endpoint reimplemented in Go:
`/in/sense/batch`, `/in/pill`, `/in/sense/state`, `/in/sense/files`, `/receive`,
and time sync. Two direct dependencies (`pgx`, `protobuf`).

Validated in three escalating stages, each of which found something the
previous could not:

1. **Unit tests.** All green, and all three later bugs were invisible to them.
2. **Shadow mode.** `sense_server.py` mirrors every request to orb, fire and
   forget, response discarded. First live request returned 401: the signature
   check compared 32 bytes when suripu compares 20.
3. **Write-mode comparison.** orb writes to Postgres while Java writes to
   DynamoDB, both fed the same uploads, then `full-instructions/infrastructure/orb/cmd/compare` diffs field by
   field. Found audio scaling and null-versus-zero. Final result over 36
   consecutive live uploads:

       compared 36, identical 36, mismatched 0

The details of all three bugs are in
[DEVICE-PROTOCOL.md](DEVICE-PROTOCOL.md). The general lesson is worth repeating
here because phase 4 faces the same risk with 25 app endpoints: **a green test
suite proves self-consistency, not conformance.**

Still open in phase 2: pill decryption is verified on one live payload only, and
the `SyncResponse` is empty (correct as "nothing to do", but alarms and OTA will
need it).

**Phase 3 in progress** (2026-08-13). The Go worker runs as goroutines inside
the same process as the edge (`full-instructions/infrastructure/orb/internal/worker`), and `orb-algo` is a
stateless Java service wrapping suripu's algorithms.

### Building Java without Maven

`mvn` is not installed and the 2016-era dependencies would not resolve anyway.
It turns out none of that is needed: `suripu-app`'s shaded jar is 81 MB and
already contains suripu-core plus every transitive dependency (guava,
joda-time, jackson, protobuf), so

    javac -cp suripu-app-0.6.0-SNAPSHOT.jar -d build src/*.java

is the entire build, run in an `eclipse-temurin:8-jdk` container so nothing is
installed on the host. See `full-instructions/infrastructure/orb-algo/build.sh`. Proving this worked was the
feasibility gate for the whole phase, exactly as the TLS spike was for phase 2.

### Seam: the algorithm layer, not the processor

`InstrumentedTimelineProcessor.createTimelineProcessor` takes **18**
dependencies and would need in-memory implementations of about fifteen DAO
interfaces. `AlgorithmFactory.create` takes **five**, of which three are
trivial:

| Dependency | Implementation |
|---|---|
| `OnlineHmmModelsDAO` | per request, from the model blobs orb stores |
| `DefaultModelEnsembleDAO` | `normal4*.base64` from a mounted volume |
| `FeatureExtractionModelsDAO` | `featureextractionlayer.bin` |
| `SleepHmmDAO` | returns absent; HMM has no models and never did |
| `NeuralNetEndpoint` map | empty; the Keras nets are gone |

The cost of the smaller seam is that **sleep scores live above it**, in
`populateTimeline`, so `orb-algo` returns events but not scores. Adding scoring
is a second step, deliberately kept separate so events and scores are not being
debugged at once.

`RequestModelsDAO` inverts the write path: the algorithm calls
`updateModelPriorsAndZeroOutScratchpad` expecting a database, and instead the
result is captured and returned in the response for orb to persist. That
inversion is what makes the service stateless.

### Current state: output matches, see "RESOLVED 2026-08-14" below

The NaN investigation recorded in the next few sections reached the wrong
conclusion and is kept because the way it went wrong is the useful part. The
sections are in the order they happened; read to the end before acting on any
of them.

### The symptom, as it appeared at the time

`orb` -> `orb-algo` -> suripu -> Postgres runs, on real nights, with real
models loaded (default ensemble BED/SLEEP 179 models each, seed 1 each, feature
extraction layer valid).

But ONLINE_HMM never wins, and the cause is now precisely located:

    BED   = [0.0, ..., 0.9944466, ..., 1.0, ...]  transitions [0->1 idx=7, 1->2 idx=92]
    SLEEP = [0.0 x6, NaN, NaN, ... NaN, 0.0]      transitions []

The BED model evaluates correctly on the same input; the SLEEP model produces
**NaN** from index 6 onward, so no transitions, so `MISSING_KEY_EVENTS`, so the
chain falls through to VOTING on every night.

### The BED/SLEEP split, which explains the asymmetry

Asking the models what they need took one command (`full-instructions/infrastructure/orb-algo/src/ModelDump.java`)
and is the single most useful fact here:

    BED:   179 models, 3 states, measurements=[motion2, motion3]
    SLEEP: 179 models, 3 states, measurements=[artificiallight, lightincrease,
                                               motion2, motion3, sound3, waves1]

**BED needs only motion**, which comes from `pill_samples` and needs no
calibration, which is exactly why it has evaluated correctly throughout. SLEEP
additionally needs four features derived from the sensor series. If any one of
them is empty or degenerate its observation probability is zero, `log(0)` is
`-Infinity`, and differences of infinities give NaN. The NaN starting around
bin 5 to 7 (roughly 20:25 local, just after the window opens) fits a feature
that is absent from the start rather than one drifting out of range.

Note these are **feature** names, not raw measurements: the feature extraction
layer turns binned measurements into the discrete alphabet symbols the models
score against. The decoded `featureextractionlayer.bin` begins with
`artificiallight ./artificiallight.json`, so the layer names the very features
SLEEP requires. Whether the fixture actually produces all six, and whether it
belongs to the same generation as the `normal3` models it is paired with, is the
next thing to check. `MultiObsHmmIntegrationTest` pairs them, but that is
inferred from a test file rather than verified against this data.

### Eliminated, with evidence. Do not retrace these.

Six hypotheses were tested against the running service. All six were wrong,
because the premise behind them was wrong: there was nothing here to find. Kept
so nobody spends the time again, and because the dump tools in
`full-instructions/infrastructure/orb-algo/src/*Dump.java` are still the right way to interrogate a model.

| Theory | Result | How |
|---|---|---|
| Models did not load | wrong | ensemble BED/SLEEP 179 each, seed 1 each, layer valid |
| Feature extraction layer is the wrong generation | wrong | layer produces exactly `[artificiallight, lightincrease, motion2, motion3, sound3, waves1]`, which is precisely what SLEEP requires |
| A required feature is never produced | wrong | all six paths present in the logs with plausible state sequences |
| A symbol falls outside its alphabet | wrong | widths 9/7/5/3/3/3 against observed maxima 8/6/0/2/2/0 |
| Model parameters contain NaN or -Infinity | wrong | zero non-finite alphabets, denominators or pi. The 179/179 non-finite *transitions* are normal: a left-to-right HMM encodes forbidden transitions as -Infinity, and BED carries them identically while working |
| Timezone or window wrong | wrong | 960 bins for a 16 hour night at 5 minutes |
| Calibration skipped | **right, but not causal** | fixed; NaN persisted |

### RESOLVED 2026-08-14: the NaN was never a porting defect

**suripu does this too.** Reading the running stack's own log for the same night
settles it:

    suripu, night 2026-08-12, 08:23:08Z
      SLEEP = [0.0 x54, NaN, NaN, ... ]
      transitions for SLEEP are []
      not enough transitions found for output id SLEEP
      alg_status=MISSING_KEY_EVENTS
      -> VOTING

    suripu, night 2026-08-13, 08:23:16Z
      SLEEP = [0.0 x40, 0.05754731, 0.05754731, 1.0, ...]
      transitions for SLEEP are [0->1 idx=41, 1->2 idx=96]
      alg_status=NO_ERROR

Same account, same ensemble, two consecutive nights, and suripu produces NaN on
one and a clean path on the other. `orb-algo` reached the identical verdict on
the identical night. It was reproducing suripu faithfully the whole time.

**Two days were spent hunting a bug in the port that was a property of the
model.** The mistake was never checking whether the reference implementation had
the same symptom. Every hypothesis in the table above was a hypothesis about
*our* code, and the question that resolved it, "does the original do this?", was
never asked. When a port disagrees with its reference, confirm the reference
actually behaves the way you assume before searching for the difference.

### What the same session did find: four real defects

Comparing `orb-algo` against the running stack line by line, rather than against
an assumption, turned up four genuine bugs. None had a visible symptom that
pointed at its own cause.

**1. Sample timestamps were shifted into local time.** The largest one. Only the
window bounds (`date`, `startTimeLocalUTC`, `endTimeLocalUTC`) are "local UTC";
the sample clock is real UTC, and `OnlineHmm` converts the window back with
`startTimeLocalUtc.minusMillis(timezoneOffset)` before binning. Both sources are
UTC despite their names suggesting otherwise:

| Source | Named | Actually |
|---|---|---|
| `Bucketing.populateMapAll` | - | keys on `deviceData.dateTimeUTC`, real UTC |
| `PillDataDAODynamoDB.getBetweenLocalUTC` | "LocalUTC" | range key is UTC; the local column is only a query *filter* |

`Mapping` added the offset to both, and `Server.iso()` subtracted it again on
the way out, so **the two errors cancelled at the boundary** and the final event
times looked right. Everything in between saw a clock wrong by the offset, and
`OnlineHmmSensorDataBinning` recovers local time as
`sample.dateTime + sample.offsetMillis`, so the artificial-light window, which
zeroes light between 21:00 and 05:00, was wrong by twice it.

Proof it is fixed, on night 2026-08-12:

    suripu   IN_BED 00:35 EDT   OUT_OF_BED 07:40 EDT
    before   IN_BED 20:35 EDT   OUT_OF_BED 03:40 EDT   (exactly -4h)
    after    IN_BED 00:35 EDT   OUT_OF_BED 07:40 EDT   (identical)

A bug that cancels at the boundary cannot be caught by comparing outputs. It was
found by comparing an *intermediate*: suripu logs its own predictions, and those
were four hours from ours.

**2. The series must be dense, and a gap is -1.** suripu builds one sample per
minute across the whole window (`Bucketing.generateEmptyMap`, then real readings
override) with `missingDataDefaultValue = -1`. orb sent only the rows it had.
The count is exact and worth remembering as a check: a 16 hour window at one
minute is **961** samples, `(end-start)/60000 + 1`. orb was sending 710 for a
night with a four hour hole, `TimelineSafeguards` saw a 238 minute gap against
its 60 minute limit, and threw the night away with `DATA_GAP_TOO_LARGE` on a
night suripu scored without complaint.

`orb-algo` now logs both numbers per request, because they are supposed to
differ and the day they stop differing is the day the fill has broken:

    timeline account=1 date=2026-08-12 sent=710 binned=961 motion=43

**3. `useAudioPeakEnergy` must be false.** `SenseDataDAODynamoDB` passes false,
with the reason in the source: *"Don't use the new audio peak energy since the
models haven't trained on it."* orb-algo passed true, and the comment asserting
that was correct claimed the opposite. The models this service loads never saw
that signal.

**4. Feedback `created` decides whether a correction is learned from at all.**
`OnlineHmm.filterFeedbackInValidTimeRange` drops any correction whose created
time falls outside the night's window in real UTC. `Mapping` was passing
`night.getMillis()` as a placeholder because the field looked unused, which is
about twenty hours before the window opens, so **every correction was silently
discarded and no feedback would ever have been learned from.** `created_at` now
travels with the correction from `timeline_feedback` through to the algorithm.

A fifth, smaller: Go marshals a nil slice as `null`, Jackson overwrites the
field initialiser with it, and a night with no feedback threw
NullPointerException before scoring. Both ends fixed: orb sends `[]`, orb-algo
tolerates null. `timeline_events.offset_ms` was also hardcoded to 0 and is now
the night's real offset.

**5. Pill timestamps are truncated to the minute.**
`PillDataDAODynamoDB.fromDynamoDBItem` does it on read, with the reason inline:
`withSecondOfMinute(0)  // query results return minute-level`. It reads as
cosmetic and is not: motion drives the smoothing and clustering in VOTING, and
carrying real seconds through moved the predicted out-of-bed time by **ten
minutes** and changed every motion score. Found by diffing VOTING's own
`Prob ... time` log lines against suripu's, which is the same trick that found
the timestamp shift.

### Forcing the fallback, because otherwise it is never tested

VOTING only runs when ONLINE_HMM fails, so on a healthy account it can go weeks
without executing, and the first real run would be an unwatched night. A
request may now carry `"algorithm": "VOTING"` to restrict the chain to one
algorithm; absent means the real chain, which is what orb always sends.

### Result: VOTING matches suripu exactly on the same night

Night 2026-08-12, orb-algo forced to VOTING against suripu's stored result:

| Event | suripu | orb-algo | |
|---|---|---|---|
| in_bed | 00:13 | 00:13 | match |
| sleep | 00:33 | 00:33 | match |
| wake_up | 07:18 | 07:18 | match |
| out_of_bed | 07:19 | 07:19 | match |

`Light samples size: 961` on both. `DATA_GAP_TOO_LARGE` no longer fires on the
night that has the four hour hole.

ONLINE_HMM likewise matches: `IN_BED`/`OUT_OF_BED` to the minute against
suripu's own predictions on the night both ran it.

### RESOLVED: the two differing scores were `withAmbientLight` vs `calibrateAmbientLight`

`DeviceData.Builder` has two methods that look like variants of each other and
are not:

    withAmbientLight(int)       // stores raw counts. The WRITE path uses this.
                                //   SenseProcessorUtils:149
    calibrateAmbientLight(int)  // converts counts to lux via
                                //   convertLightCountsToLux and sets BOTH
                                //   ambientLight and ambientLightFloat.
                                //   The READ path uses this.
                                //   DeviceDataDAODynamoDB:636

`CalibratedDeviceData.lux()` reads **only** `ambientLightFloat`. Calling
`withAmbientLight` leaves it at its zero default, so every minute of the night
calibrated to **0.0 lux**, `LightEventsDetector` never saw a value above
`noLightThreshold`, it emitted no light segments, `lightOutTimes` came back
empty, and the whole `if(!lightOutTimes.isEmpty())` block that adds two
scoring features was skipped.

Nothing failed. 0.0 lux is a legal reading, and a night with the lights never on
is a legal night.

After the fix, orb-algo's light segments are identical to suripu's and all four
scores match to the last digit:

| | suripu | orb-algo before | after |
|---|---|---|---|
| go to bed | 3.0636649344406575 | 0.7659162336101644 | 3.0636649344406575 |
| fall asleep | 0.23363540386809994 | 0.21295970441074596 | 0.23363540386809994 |
| wake up | 268.2776861510197 | 268.2776861510197 | 268.2776861510197 |
| out of bed | 44.21941313165632 | 44.21941313165632 | 44.21941313165632 |

The ONLINE_HMM features moved too: `artificiallight` went from
`[0,0,1,2,2,2,...]` to all zeros. The events for these two nights did not
change, which is worth being honest about: the earlier "ONLINE_HMM matches
suripu" was true but was matching on the motion-driven part while light was
silently zero throughout.

**How it was found, because the method is the transferable part.** The ratio of
the two go-to-bed scores was exactly **4.0**, and `LightOutScoringFunction` is
constructed with `modalityWeight = 3d` and returns `1 + modalityWeight` on a
light-out minute. An exact small-integer ratio between two floating point
results is not a data difference, it is a missing multiplicative term, and it
named the feature immediately. The same score also appeared identically on two
unrelated nights, which said it was a constant rather than anything derived from
that night's data.

Sequence, each step ruling out roughly half the surface:

1. Two of four scores differ, two match bit for bit -> the *data* is shared, a
   *feature* is missing.
2. `LightOutScoringFunction` returns `EventScores(1d, 1d, maxScore, 1d)`, with
   the source comment "light out is just for go to bed detection, the other
   scores have to be 1" -> the two differing scores are exactly the ones the
   light block touches.
3. Ratio is exactly 4.0 = `1 + modalityWeight` -> the light block is not
   contributing at all, so `lightOutTimes` is empty.
4. `LightEventsDetector` logs each segment at INFO. suripu logged two; orb-algo
   logged none.
5. Instrument the series: `light=n=961,min=-1.0,max=0.0` -> every real reading
   is 0.0.
6. `lux()` reads `ambientLightFloat`, and only one builder method sets it.

Ruled out along the way, each cheaply: the outlier filter (`outlier_filter` has
no row in `features`, so it is off on both sides), `getForSleepPeriod` windowing
(only `InstrumentedTimelineProcessorV3` calls it; the running stack uses the V2
processor), Sense colour (`sense_colors` is empty and `Device.DEFAULT_COLOR` is
WHITE, which is what orb-algo hardcodes), calibration overrides (the
`calibration` table is empty), and pill column completeness (all 43 rows fully
populated).

### The last two differences, both now closed

Both had been written off as inert. One of them was, and it still got changed;
the other was not inert at all, only invisible on the nights being looked at.

**`firmwareVersion`: removed, and the inertness measured rather than argued.**
`DeviceDataDAODynamoDB`'s read path never sets it, so every `DeviceData` the
algorithms have ever scored carried a **null** here. orb-algo was passing 4513.
`DataUtils.calibrateAudio` branches on it only for builds in
`BLACKLISTED_FIRMWARE`, a fixed set of known-bad 2015-era versions, and neither
4513 nor null is in that set.

The reasoning was right, and it was still the wrong thing to rely on. The value
is now simply not set, which is what the reference does. Proof it changed
nothing, the sound series over the same night before and after:

    before  sound=n=961,min=-1.0,max=68.238,filled=251
    after   sound=n=961,min=-1.0,max=68.238,filled=251

Only two things read `DeviceData.firmwareVersion` at all:
`CalibratedDeviceData.sound()` and `CurrentRoomState` (the room conditions view,
not the timeline).

**`HOLD_COUNT`: was a real dropped value, not a cosmetic gap.** orb ingests it
correctly, 21,039 non-null rows with three non-zero, and simply never forwarded
it: `LoadNight` did not select the column and the wire format had no field. The
sensor was registered with an all-zero series, so `getAvailableSensors()` looked
right while the data was gone.

Now carried end to end. Verified on 2026-07-27, the one night whose window
contains a hold event:

    before  hold=n=961,min=-1.0,max=0.0,filled=1
    after   hold=n=961,min=-1.0,max=1.0,filled=1

Nothing in the timeline path reads `HOLD_COUNT` today, so this changes no
result. It was worth fixing anyway: "orb has the value and throws it away" is
the same shape as the light bug, and the reason that one survived so long is
that a plausible-looking zero never announces itself.

### A night was scored once and never revisited

Found on 2026-08-15, the first night run end to end rather than re-scored from
stored data.

`NightsNeedingTimeline` selected a night only when it had no timeline at all, or
when feedback was newer than the timeline. Nothing brought a night back as more
of it arrived. The night of 2026-08-14 was therefore scored at **05:44 local
from 19 of its eventual 39 motion samples**, while the sleeper was still asleep,
and froze there:

    scored 05:44   sensors=569 motion=19   wake 05:30   <- sleeper still in bed
    after fix      sensors=794 motion=39   wake 09:11   <- actual wake ~09:22

suripu never had this problem because it computes a timeline on demand for every
app request, so it always sees everything that has arrived. orb computes on a
timer, which is the right shape for a single-user deployment but needs the night
to stay open.

A night is now recomputed on every pass until its window closes at noon the next
day. The boundary is derived **entirely database-side**, from
`date_of_night + 36h - offset`, rather than by comparing a device timestamp
against `now()`: those are two different clocks and mixing them has already
caused two bugs here. Once the window closes no pass matches, so a settled night
stops being touched without needing a separate guard. Verified:

    date_of_night   window_end               still_open
    2026-08-14      2026-08-15 16:00:00+00   t
    2026-08-13      2026-08-14 16:00:00+00   f

The cost is one algorithm call per pass for at most one or two open nights,
which is what the on-demand original was doing far more often.

### Learning was being written and then thrown away

Two defects in the model plumbing, found on 2026-08-15 immediately after the
first correction orb was ever able to learn from. Neither produced an error and
together they made every correction pointless.

**`SaveModel` nulled out the learned model.** It did
`model_params = EXCLUDED.model_params` on conflict. Most runs return a scratchpad
and **no** model, because promotion is deferred by a day, so most runs wrote NULL
over whatever the account had learned. It had already destroyed a 16,805 byte
model for 2026-08-12, and because the blob is opaque there was nothing to see.

**`LatestModel` took the newest row regardless of whether it held a model.**
Model and scratchpad do not travel together: the night that learns writes a
scratchpad and no model, the night that promotes writes a model and zeroes the
scratchpad. "Newest row" therefore returns a null model most mornings, which
reads as "this account has never learned anything" and drops the algorithm back
to the default ensemble.

suripu never hit either one: its DynamoDB rows always carry `model_params`,
because `updateScratchpad` writes only the scratchpad attribute and leaves the
model alone. orb had collapsed two DAO methods into one `SaveModel(model,
scratchpad)` where a nil model is ambiguous between "nothing was promoted" and
"there is no model".

Fixed by making the absence of a model mean "nothing new was promoted": the
previous model is carried forward on insert and `COALESCE`d on update. The
scratchpad is still overwritten including with null, because clearing it is a
real instruction rather than an absence. `LatestModel` now selects the newest
non-null of each independently. The destroyed 2026-08-12 model was recovered
from the Java stack's DynamoDB copy.

    before   2026-08-12 mp=NULL   2026-08-13 mp=16805  2026-08-14 mp=NULL
    after    2026-08-12 mp=16805  2026-08-13 mp=16805  2026-08-14 mp=16805

which is the shape suripu's own table has always had.

**The lesson is the same one as the light bug, one layer up: a null that means
two different things.** `withAmbientLight` vs `calibrateAmbientLight` was two
methods where only one populated the field that mattered; this is one method
where the caller could not say which of two things it meant.

### First head-to-head on an unseen night: exact match

Night 2026-08-14, the first scored end to end by both stacks rather than
re-scored from stored data, and the first neither had seen:

| Event | suripu | orb |
|---|---|---|
| in_bed | 22:58 | 22:58 |
| sleep | 23:40 | 23:40 |
| wake_up | 09:11 | 09:11 |
| out_of_bed | 09:12 | 09:12 |

Both are wrong about bedtime in the same way, for the reason in
[TIMELINE-ALGORITHMS.md](TIMELINE-ALGORITHMS.md): five hours of an empty bed
read as sleep. Being wrong identically is what a faithful port should do.

### Feedback does not reach orb on its own

The iOS app writes corrections to the Java stack's `common.timeline_feedback`.
orb has its own table and only ever received a copy through the migrator, so a
correction made in the app is invisible to orb until it is copied across. It was
copied by hand for 2026-08-14. Phase 4 closes this by giving orb the app API;
until then, a correction has to be moved deliberately, and re-running the whole
migrator to do it would re-import stale DynamoDB dumps over orb's newer models.

### A boundary check that came free

The 2026-07-27 probe showed `sent=960, binned=961, filled=1` on a night with
complete data, meaning the final bucket is always synthetic. That is correct and
not an off-by-one: the reference query uses
`endExclusive = end.minusMinutes(1)` with an inclusive DynamoDB `between`, so it
returns `[start, end)` in minute terms, exactly matching orb's
`ts >= $start AND ts < $end`. The grid has 961 points because
`generateEmptyMap` builds `(end-start)/slot + 1` of them, so the last one can
never be filled by data. Worth knowing before someone "fixes" the count.

### Superseded: the residual as it was first recorded

Not resolved, and recorded rather than papered over. On the VOTING run above,
two of four `MotionScoreAlgorithm` scores match suripu bit for bit and two do
not:

| | suripu | orb-algo |
|---|---|---|
| go to bed | 3.0636649344406575 | 0.7659162336101644 |
| fall asleep | 0.23363540386809994 | 0.21295970441074596 |
| wake up | 268.2776861510197 | 268.2776861510197 |
| out of bed | 44.21941313165632 | 44.21941313165632 |

The *times* all agree, so it changes nothing on this night, but a score is a
tie-breaker and could change a different one. Ruled out already: the outlier
filter (`outlier_filter` has no row in the `features` table, so it is off on
both sides, which also retires the top-ranked hypothesis from the old
elimination list) and missing pill columns (all 43 rows have `svm_no_gravity`,
`motion_range`, `kickoff_counts` and `on_duration_secs` populated).

Leading hypothesis at the time, since disproved: `getForSleepPeriod` windowing.
It was wrong, and the real cause is in the section above. Kept only because the
symptom description ("times agree, two of four scores do not") is the thing that
led to the answer.

### A contributing bug, found on the way: skipped calibration

suripu never hands the algorithms raw stored values. `CalibratedDeviceData`
sits between storage and the algorithms and converts *every* series:

| Series | Conversion |
|---|---|
| temperature | `DataUtils.calibrateTemperature` |
| humidity | `DataUtils.calibrateHumidity(temp, humidity)` |
| light | `DataUtils.calibrateLight(light, senseColor)` |
| particulates | `DataUtils.convertRawDustCountsToDensity` |
| sound | `DataUtils.calibrateAudio(dbIntToFloatAudioDecibels(disturbances), dbIntToFloatAudioDecibels(peak), fw)` |

orb-algo mapped `sensor_samples` straight into `AllSensorSampleList`, so every
value arrived in storage units. Sound was the one that produced NaN rather than
just wrong numbers, because `calibrateAudio` subtracts a 40 dB noise floor:
applied to millidecibel integers the result is large and positive, and skipping
it entirely leaves values ~1000x too big, either of which drives an observation
probability to zero and gives NaN in the log domain.

That explains the asymmetry exactly. BED is driven mainly by motion, which
comes from `pill_samples` and needs no calibration, so it evaluated fine on the
same request. SLEEP additionally uses light and sound, and broke.

**The lesson generalises, and it is the same one as phase 2 one layer deeper:
storage units are not algorithm units.** There is an explicit conversion class
in between, and it is easy to miss precisely because it is not on the code path
you are reading when you follow the algorithm down from the top.

Fixed by constructing `DeviceData` and wrapping it in `CalibratedDeviceData`
rather than reimplementing the five conversions. Copying them would be a third
opportunity to make the same class of mistake; a call cannot drift from the
thing it calls.

**This was a real bug but not the NaN cause.** It was fixed, and SLEEP still
evaluated to NaN afterwards. Worth recording as a bug that would have produced
silently wrong numbers forever even once NaN is solved, rather than as a
red herring.

### Other bug fixed on the way

`TimelineFeedback.create`'s 8-argument overload wraps `accountId` and `created`
in `Optional.of`, which rejects null. Passing null for `created` (unused by the
algorithms) threw NullPointerException on every night that had feedback.

### Method note

**Check the reference has the symptom before hunting the difference.** Every
hypothesis in the elimination table assumed suripu scored a night that orb-algo
could not. One `docker logs | grep "SLEEP = "` showed suripu failing the same
night the same way, and it would have been just as available on day one.

**A port is verified against intermediates, not outputs.** The timestamp shift
survived an output comparison because `Server.iso()` undid it. What exposed it
was suripu logging its own `IN_BED`/`OUT_OF_BED` predictions, which were four
hours from ours on the same night. Two of the four defects here were invisible
at the boundary: one cancelled, and one (feedback `created`) had no output at
all, since discarded feedback simply means nothing happens.

**Read the reference's call site for every argument, including the boring
ones.** `useAudioPeakEnergy` and `missingDataDefaultValue` are both single
arguments in a DAO call, both carry the reason in a comment beside them, and
both were wrong here. The `created` field looked unused and was load-bearing.
Three of the six defects were a wrong argument or a wrong builder method at a
call site that had the answer written next to it.

**Beware of paired methods where one name is a superset of the other.**
`withAmbientLight` and `calibrateAmbientLight` read as a plain setter and a
fancier setter. They are the write path and the read path, they populate
different fields, and only one of those fields is the one the algorithms read.
Any time a builder offers two ways to set "the same" value, find out which one
the reference's *read* path calls, because that is the shape the algorithms
were written against.

**An exact small-integer ratio between two float results is a missing term, not
a data difference.** The go-to-bed scores differed by exactly 4.0, and
`LightOutScoringFunction` contributes `1 + modalityWeight` with
`modalityWeight = 3d`. That one arithmetic observation named the feature and
skipped the entire search. Check the ratio before checking the inputs.

**A zero is not an error.** Every one of these bugs produced a legal-looking
value: 0.0 lux is a dark room, an empty feedback list is a night with no
corrections, a missing minute is a gap, a hold count of zero is nobody touching
the Sense. Nothing threw, nothing logged a warning, and the timeline still
rendered. The only way to catch this class is to compare intermediates against a
reference that is known to work.

**"Inert" is a claim that needs a measurement, and it is not a reason to keep a
difference.** Both leftovers were written off as harmless. `HOLD_COUNT` turned
out to be a real dropped value that merely had no consumer yet, and
`firmwareVersion` was genuinely harmless and was still worth deleting, because
the argument for keeping it depended on a blacklist staying constant. Where the
reference does nothing, do nothing.

**Raising a Dropwizard log level needs no restart:**
`curl -X POST "localhost:9998/tasks/log-level?logger=com.hello.suripu.algorithm&level=DEBUG"`.
Set back to INFO afterwards. In the end the decisive lines
(`LightEventsDetector` segments, `MotionScoreAlgorithm` scores) were already at
INFO and sitting in the existing container log, so **read the log you already
have before generating a new one**.

**Instrument the running system rather than reasoning about it from the
source.** Still true, and still the same lesson as the shadow run and the ingest
comparison, but insufficient on its own: this session instrumented plenty and
instrumented the wrong side.

Nothing is committed: `orb-algo` runs beside the untouched Java stack, and any
`timeline_events` rows overwritten during testing are restorable by re-running
the migrator.

## Phase 4 progress: foundation and harness, two endpoints verified

Started 2026-08-15.

### The endpoint list came from the access log, not the source

Two weeks of `docker logs hello-orb-suripu-app-1 | grep -oE '"(GET|POST|...)'`
gives what the app actually asks for. suripu-app publishes far more than this,
and building to the published surface means writing code nothing calls while
missing something called constantly:

    852 GET /v2/sensors        832 POST /v2/sensors     691 GET /v2/timeline/{date}
    326 GET /v1/account        323 GET /v2/account/preferences
    316 GET /v2/devices        300 GET /v1/app/stats/unread
    218 GET /v2/trends/{period} 169 GET /v2/insights    168 GET /v2/alerts
    168 GET /v1/questions/     140 GET /v2/alarms       118 PATCH /v1/app/stats
    116 GET /v1/timezone        72 GET /v2/sleep_sounds/status
     26 POST /v1/oauth2/token

Writes: `PATCH /v2/timeline/{date}/events/{event}/{ts}` (the correction that
feeds learning), `POST /v2/alarms/{ts}`, `POST /v1/timezone`, `PUT/POST
/v1/account`, `POST /v1/questions/save/`.

### `cmd/apidiff`, built before the first endpoint rather than after

Sends the same authenticated request to both stacks and diffs the decoded JSON.
It reads a live token from orb's own `oauth_tokens` and never prints it, so no
credential is pasted into a shell or a log.

It paid for itself on the first run.

### The token on the wire is not the token in the column

`AccessToken.serializeAccessToken()` renders `{appId}.{uuid without dashes}`;
the column holds the plain UUID. orb compared the header against the column,
which is **self-consistent and completely wrong**: it authenticated its own
tokens happily and would have rejected every real one from the app. The first
diff showed `orb=200 java=401` and nothing else would have.

A Go test would have passed. This is the argument for the harness in one line.

### Nine more corrections on two of the smallest endpoints

`GET /v1/account`, after reading `Account.java` carefully first:

| Field | Expected | Actually |
|---|---|---|
| `id` | the numeric key | the **external UUID**; the app never sees the integer |
| `dob` | epoch millis | ISO string `1988-04-17`, while `created` beside it IS millis |
| `firstname` | `name` split on the first space | the whole stored name; suripu keeps the columns separate and lastname is null |
| `email_verified` | true | false |
| `ext_id`, `gender_other` | absent | present |

`GET /v1/timezone` is `{timezone_offset, timezone_id}`, not the obvious
`offset_millis`. Both halves were guessed wrong in different directions on
successive attempts.

`external_id`, `firstname`, `lastname` and `gender_other` were missing from
orb's schema entirely: migration `0002_account_identity.sql`.

Both endpoints now match byte for byte.

### Second batch: `/v2/devices`, `/v1/app/stats/unread`

`apidiff -show` prints the Java response without calling orb, which is how each
endpoint now starts. Reading a resource class to learn a shape has been wrong
twice; asking the running service takes one command and cannot be misread.

Field notes worth keeping:

- Sense firmware is **hex** (`11a1` for 4513), pill firmware is **decimal**
  (`3`). The two devices are not consistent with each other, and making them
  consistent would be wrong for one of them.
- `senses` and `pills` marshal as `[]`, never null.
- Sense `hw_version` is the string `SENSE`, colours are `UNKNOWN` and `BLUE`.

Two migrator bugs fell out of it:

**The pill heartbeat attribute is `fw_version`, not `firmware_version`.** The
migrator asked for the latter, a missing DynamoDB attribute reads as absent
rather than as an error, and every pill's firmware was silently null. The app
shows a blank version on the pill's settings row. `java=3 orb=0`.

**`firmware_version` was missing from the pill upsert's DO UPDATE list**, so
even after the attribute name was fixed, a re-run left the existing null in
place. An insert-only column on an idempotent migrator is a column that can
never be repaired.

### `last_seen_at` is monotonic, and the migrator was walking it backwards

Re-running the migrator reset `senses.last_seen_at` and `pills.last_seen_at` to
the values in a dump taken two days earlier, because the upserts assigned rather
than compared. On the app that reads as a device that has stopped reporting.

Both are now `GREATEST(EXCLUDED.last_seen_at, <table>.last_seen_at)`. The edge
maintains these live and the dump is only a seed. Confirmed by watching the
pill's row repair itself from live traffic within one reporting interval, which
also confirms `touchPill` fires on the data path and not only on heartbeats.

This is the same lesson as the derived tables above and the third instance of
it in one session: **the migrator seeds, it does not own.** Anything the running
system maintains needs `DO NOTHING`, `COALESCE`, or `GREATEST`, never a bare
assignment.

### apidiff: drift vs difference

`last_updated` fields legitimately move between two calls, and the two stacks
learn liveness from different sources at different rates. They are printed as
`drift` and do not fail the run.

The list is one field name, matched whole rather than as a substring.
Suppressing a difference is how a real one hides, so an addition needs a reason
about the value being live, not about the diff being inconvenient.

### Known difference: `has_unanswered_questions`

suripu returns true, orb returns false. The questions subsystem
(`/v1/questions/`, `/v1/questions/save/`) is not built, so nothing could have
been answered; a true here would put a badge on a screen with nothing behind it.
Left failing in apidiff deliberately, because the harness doubles as the to-do
list and an endpoint that is not built should not look finished.

### Third batch: `/v2/alarms`, `/v2/alerts`

`/v2/alerts` has returned **403 on all 170 calls** in the log. The feature is not
enabled for this deployment, so orb returns the same 403 and the same
`{"code":403,"message":"Forbidden"}` body. Building something that returns data
would invent a behaviour the app has never seen.

`/v2/alarms` took four passes and each one found something different.

**The alarm `definition` blob is echoed, not rebuilt.** It carries `editable`,
`source`, `year`, `month`, `day_of_month` and a `sound` object with a url, none
of which orb has columns for. The columns exist for the worker to query on; the
blob is what the app gets back.

**The DynamoDB `alarm` table is versioned.** One item per account per edit, each
holding the *whole* list as it stood then. Importing every item unions every
alarm that ever existed, so a deleted alarm rises from the dead. The migrator
now keeps only the newest item per account.

**`updated_at` is stored as N, and reading it as S returns "" for every item.**
Every comparison was then false and the map kept whichever item came first,
which produced exactly one alarm, of the right shape, that was the wrong one. A
silent type mismatch that yields a plausible answer is worse than one that
errors.

**The alarms insert had no conflict target at all**, so every migrator run
appended the whole set again: eight runs, twenty-four rows, three alarms. Now
guarded on the alarm's own id inside the blob.

### `enabled` on a one-off alarm is computed, not stored

The last difference was `enabled: java=false orb=true`, and the obvious reading
was a stale dump. It was not: **live DynamoDB also says `true`**. suripu applies
`Alarm.Utils.disableExpiredNoneRepeatedAlarms` on read, which reports any
non-repeating alarm whose ring time has passed as disabled:

    if (alarm.isRepeated) return false;
    ringTime = DateTime(year, month, day, hour, minute, timeZone)
    return ringTime.isBefore(now.withSecondOfMinute(0).withMillisOfSecond(0))

In the account's zone, not UTC: whether a one-off alarm has already rung is a
question about the sleeper's wall clock.

Checking the live source rather than assuming staleness is what turned this from
"ignore, the dump is old" into a rule worth implementing. **"The data is stale"
is a hypothesis, and it is checkable.**

### Fourth batch: preferences, sleep sounds, and the WiFi timestamp

**`/v2/account/preferences` is defaults, not stored data.** The DynamoDB
`preferences` table is **empty** and the API still returns seven values, so the
set lives in code. Only `PUSH_ALERT_CONDITIONS` and `PUSH_SCORE` default on.

orb stores overrides only. Writing the defaults as rows would make "never set"
indistinguishable from "set to the default", and a later change to a default
would silently not reach this account.

**`/v2/sleep_sounds/status`** is four nulls and a false. The app polls it
constantly and expects the shape, not the feature.

**`wifi_info.last_updated` is a datetime string, not epoch millis.** Every other
timestamp in these dumps is millis, so `millis()` was the obvious call, and it
returned nil for every row. The step reported `wifi_info 0`, which reads as an
empty source table rather than a parse that never matched. This is the second
silent DynamoDB type mismatch in one session, after `updated_at` on alarms.

**Two attribute-type traps is a pattern, not bad luck.** A DynamoDB dump gives
no type errors: ask for the wrong one and you get a zero value. Anywhere a
migrator step reports 0 rows against a non-empty dump, suspect the accessor
before suspecting the data.

The Sense's WiFi reading now carries its own timestamp
(`senses.wifi_updated_at`), which is not `last_seen_at`: the Sense reports every
minute while the WiFi record changes only when the network does. Sending
`last_seen_at` made the WiFi row appear to refresh every minute.

### Status: 8 endpoints

    GET /v1/account              match
    GET /v1/timezone             match
    GET /v2/devices              match  (2 drift, both genuinely live)
    GET /v2/alarms               match
    GET /v2/alerts               match (403)
    GET /v2/account/preferences  match
    GET /v2/sleep_sounds/status  match
    GET /v1/app/stats/unread     1 known difference (questions not built)

### There is a THIRD Postgres database, and the migrator never reads it

`-src` points at `common`. There is also an `insights` database, and it holds
everything two unbuilt endpoints need:

    info_insight_cards          category_name and the image urls for /v2/insights
    questions                   the question catalogue
    response_choices            the answers offered
    account_questions           which were asked, and when
    responses                   which were answered
    account_question_ask_time

This is why `/v2/insights` cannot be finished from the DynamoDB dump alone: the
dump carries the insight rows (account, category as a NUMBER, title, message,
timestamp) but `category_name` ("Wake Variance", and "Sense" for GENERIC) and
the three image URLs come from `info_insight_cards`, which is relational and was
never migrated.

It is also the real answer to `has_unanswered_questions`, currently orb's one
known difference: the questions subsystem is not "not built", it is **not
migrated**. Four tables and a catalogue exist and nothing reads them.

The migrator needs a second source connection before either endpoint can be
finished. `/v2/insights` and `/v2/trends` are 387 calls between them, so this is
not optional cleanup.

### The correction PATCH is gated on the seam too

`PATCH /v2/timeline/{date}/events/{TYPE}/{oldTimestampMillis}` returns **200
with a 4 to 6 KB body**: the whole re-rendered timeline, not an acknowledgement.
So the endpoint that feeds learning cannot be finished before the timeline
renderer exists either. It is not an independent piece of work.

`PUT` on the same path is the VERIFY/INCORRECT action and returns **202 with an
empty body**, so that half IS independent. One observed `412` on a PATCH, which
is presumably a correction the safeguards rejected.

### `POST /v1/oauth2/token`: shape known, success path unverifiable

    { "token_type": "Bearer", "expires_in": N, "refresh_expires_in": N,
      "account_id": "<external uuid>", "access_token": "{appId}.{hex32}",
      "refresh_token": "{appId}.{hex32}" }

`account_id` is the external UUID as a string, consistent with `id` on
`/v1/account`. Note the app has hit this 22 times successfully, twice with 401
and **twice with 500**.

Deliberately not built yet. The success path needs a real password to exercise
and apidiff cannot mint one, so it would be the first endpoint written without a
diff behind it. It is also not needed until the app is pointed at orb. Build it
immediately before the cutover, with the sign-in done by hand as the test.

## DECISION: the seam is computed vs presented, not algorithm vs everything

Taken 2026-08-15, when `/v2/timeline/{date}` made the original seam untenable.

**The rule: does it need the night's raw samples?**

| Side | What lives there |
|---|---|
| **Java (orb-algo)** | sleep depth from motion amplitude, light/sound/motion event detection, the sleep score, metric values |
| **Go (orb)** | message strings, `valid_actions`, condition banding, the summary sentence, JSON shape |

### Why not raise the seam and have Java return finished JSON

It is the cheapest option today and the one that quietly undoes the exercise. It
puts English strings and the API shape inside the Java service, and the API
shape is exactly what has needed the most iteration: **fifteen-plus fields
corrected by the diff loop in two days** (`id` is a uuid, `dob` a string,
`firstname` unsplit, `timezone_offset` not `offset_millis`, `enabled`
computed...). Each was a one-line Go edit and a re-diff. In Java each is a
`build.sh` and a container restart.

The consolidation exists to shrink Java to the part nobody can safely rewrite.
Strings and JSON shape are not that part, and apidiff proves it.

### Why not port it all to Go

Depth, detection and scoring are the "invisible until wrong" class that kept the
algorithms in Java in the first place. `suripu-core` is already on orb-algo's
classpath, so calling `TimelineUtils` is a **call, not a port** - the same
argument that made `CalibratedDeviceData` a call rather than five reimplemented
conversions. A call cannot drift from the thing it calls.

### The argument that actually decides it

**Asymmetric reversibility.** If this is wrong, moving presentation from Go into
Java later is easy. Moving computation from Java into Go is the expensive,
risky direction. This choice keeps the cheap move available and defers the
expensive one, possibly forever.

### Reopen when

Presentation logic starts appearing in Java "because it is easier there":
string formatting, condition thresholds, response shaping. That means the line
is in the wrong place and is worth redrawing rather than eroding.

### Held loosely, now settled: grouping is Go's

Depth per minute is Java. **Merging minutes into the app's long `IN_BED` rows is
Go's**, and the reference itself is what settles it.

suripu produces per-minute segments and then converts with
`timeline.v2.Timeline.fromV1(...)`. That single method does the merging **and**
the message strings, `valid_actions` and banding. It spans the line exactly
where the line was drawn, which is why it is the one method not to depend on:
calling it would be option (a) by the back door, dragging every presentational
decision back into Java.

So orb-algo stops at per-minute segments carrying type and depth, plus stats.
Everything `fromV1` does is Go's.

That the awkward method sits precisely on the boundary is mild evidence the
boundary is in a real place rather than an arbitrary one.

### The pipeline orb-algo has to run, from the reference

`InstrumentedTimelineProcessor` lines ~650-780. Every call is `TimelineUtils`,
which is already on orb-algo's classpath, so this is wiring rather than a port.
Recorded because rediscovering it costs an afternoon:

    motionEvents  = generateMotionEvents(trackerMotions, NIGHT)
    timeline      = TimelineRefactored.populateTimeline(motionEvents, tzMap)   // Map<Long,Event>
    lightEvents   = getLightEvents(sleepTime, allSensorSampleList.get(LIGHT), NIGHT)
                    -> merged into timeline; getLightsOutTime(lightEvents) gives LIGHTS_OUT
    soundEvents   = getSoundEvents(...)                    -> merged into timeline
                    + the four main events from the algorithm

    smoothed      = smoothEvents(eventsWithSleepEvents)
    cleaned       = removeMotionEventsOutsideSleep(smoothed, sleep, wake)
    grey          = greyNullEventsOutsideBedPeriod(cleaned, ...)
    filtered      = removeEventBeforeSignificant(grey)
    segments      = eventsToSegments(filtered)             // the 26 events the app sees
    stats         = computeStats(segments, trackerMotions, lightSleepThreshold,
                                 hasSleepStatMediumSleep, useUninterruptedDuration)
    score         = computeAndMaybeSaveScore(processed, filteredOriginal, numSoundEvents,
                                             allSensorSampleList, targetDate, accountId, stats)

Under the decision above, orb-algo runs all of it and returns **segments, stats
and score as data**. orb turns segments into the app's JSON: message strings,
`valid_actions`, condition banding, the summary sentence.

Note `computeStats` takes feature-flag-derived booleans
(`hasSleepStatMediumSleep`, `useUninterruptedDuration`). Both have rows in the
`features` table and are ON, so they are constants here, but they are constants
with a reason and belong beside the call rather than inlined as `true`.

### Seam implemented, first cut running, one gap identified

`full-instructions/infrastructure/orb-algo/src/Timeline.java` runs the pipeline and the contract now carries
`segments` and `stats`. On night 2026-08-12 it returns:

    segments 6   LIGHTS_OUT, IN_BED, SLEEP, MOTION, WAKE_UP, OUT_OF_BED
    stats    total 405, uninterrupted 359, time_to_sleep 15, times_awake 1

LIGHTS_OUT proves the derived-event half works: that event exists nowhere in
orb's data and is computed from the light series.

**The gap was `populateTimeline`, and it is fixed.** A hand-rolled `TreeMap` of
the sparse motion events left nothing to merge and made `computeStats` count
minutes that were never created. The reference calls

    TimelineRefactored.populateTimeline(motionEvents, timeZoneOffsetMap)

which fills **every minute** of the window first. With that call in place, on
night 2026-08-12:

    before   6 segments,   sound_sleep_mins 1
    after  420 segments,   sound_sleep_mins 377

420 is per-minute and correct for this side of the seam: 378 `SLEEPING`, plus
`LIGHTS_OUT`, `IN_BED`, `SLEEP`, `MOTION`, `WAKE_UP`, `OUT_OF_BED` and 36
untyped. Merging them into the app's ~26 rows is Go's job, per the decision
above.

`TimeZoneOffsetMap` is built from a single synthetic `TimeZoneHistory` at epoch
carrying the night's offset. One offset per night is what the rest of this code
already assumes.

**orb-algo's part of the timeline is complete.**

### `/v2/timeline/{date}` is live end to end, with four gaps

Segments are stored as JSONB on `timeline_events` rather than recomputed per
request. suripu recomputes the whole timeline on every app call, which is why
one screen refresh runs the algorithms three times; orb serves what it scored,
so the endpoint is a read and the app sees exactly the night that was scored.

The pipeline works: 408 segments stored, stats persisted, the endpoint renders.
The diff against the reference for 2026-08-14:

| Field | java | orb | |
|---|---|---|---|
| events | 23 | 76 | merge not aggressive enough |
| metrics | 11 | 4 | seven metrics not emitted |
| score | 68 | 0 | never computed |
| message | 6.1 hours | 7.1 hours | stats differ, see below |

1. **The merge is fixed-width buckets, not runs of equal depth.** The
   reference's `IN_BED` rows carry visibly different depths (24, 27, 32, 44...)
   at a uniform **21 minutes**, on every night sampled, with a short remainder
   row at the end. Merging on equality gave 76 rows against 23, because real
   sleep depth almost never repeats exactly minute to minute. Bucketing at 21
   minutes with an averaged depth brings it to **43**.

   The 21 is measured, not found in the source. If a night ever comes back with
   a different width, that constant is the first thing to check.

   **Remaining excess is motion rows: ~20 against the reference's zero.**

   The obvious hypothesis is eliminated. The display filter is
   `convertLightMotionToNone(motionEventList, 5)`, which turns any MOTION event
   with a sleep depth above 5 into a NullEvent so it renders as a plain band
   rather than a marker. It lives **inside `TimelineRefactored.populateTimeline`**
   (line 54), which orb-algo already calls, so the filter is running and the
   threshold is right.

   So the excess motion has a different cause. Candidates not yet tested, in
   order: `smoothEvents` merging adjacent motions differently because orb's
   event map is built in a different order; the sleep window being 425 minutes
   against the reference's 367, so orb simply has more night to show; or
   `greyNullEventsOutsideBedPeriod`, which the reference calls between
   `removeMotionEventsOutsideSleep` and `removeEventBeforeSignificant` and
   `Timeline.java` skips.

   **Resolved: it was the missing `greyNullEventsOutsideBedPeriod`.** Adding it
   took `GENERIC_MOTION` from ~20 to **zero**, matching the reference. It turns
   everything before getting into bed and after getting out into null events, so
   the app draws flat grey rather than a marker for every stir while the room
   was empty.

   **The remaining 43-against-23 is NOT a rendering defect.** It is the model
   divergence again, and it was nearly chased as a bucketing bug.

   The arithmetic is right on both sides:

       orb   in bed 23:20 -> 11:40 = 740 min -> 738 one-minute segments
             738 / 21 = 35 bands, +3 partials broken by named events, +5 named = 43
       java  in bed ~04:50 -> ~11:00 = ~370 min
             370 / 21 = 17 bands, +6 named = 23

   Every stored segment is exactly one minute, the buckets fill to 21 wherever a
   named event does not interrupt, and the odd durations (4, 7, 8, 11, 15) are
   the partial bands either side of those interruptions. The renderer is
   behaving identically to the reference; it is being handed a night that is
   twice as long, because orb's `GOT_IN_BED` is 23:20 and suripu's is 04:50.

   **This is the second time in one session that a diverged-model value
   difference looked like a rendering bug.** The rule from the section above
   holds: on this endpoint compare vocabulary, units, shapes and formats, and
   check the arithmetic against orb's OWN events before suspecting the code.
2. ~~Only four of eleven metrics~~ **Done.** All eleven now emitted in the
   reference's order, which matters because the app reads some positionally.
   The last five are environment conditions carrying a null value and a band
   only; they are `UNKNOWN` until the sensor averages over the sleep period are
   computed, which is sample-derived and belongs on the Java side.
3. **No score.** `computeAndMaybeSaveScore` lives in the processor rather than
   `TimelineUtils` and was not wired. It is Java-side work: it needs the night's
   samples.
4. ~~`sleep_onset_mins` is 300~~ **Not a defect.** orb's own events for that
   night are in_bed 23:20 and sleep 04:20, which is exactly 300 minutes. The
   calculation is right; the events differ. See below.

### The timeline endpoint cannot be value-diffed any more

On 2026-08-14 orb reports asleep 04:20 / woke 11:25 and suripu reports asleep
04:51 / woke 10:58. Neither is wrong: **the two stacks' learned models have
diverged**, permanently, and on purpose. orb learned from the correction that
was copied into it; suripu promoted its own scratchpad separately. They will
never agree on a night's events again.

That retires the method that has carried this whole rewrite for two days. For
every other endpoint the reference is ground truth. For this one only the
*rendering* can be compared:

    compare    metric names, units, event_type vocabulary, valid_actions,
               message format, field shapes, count of rows for a given segment list
    do not     timestamps, durations, scores, sleep totals

Both are now much closer to the truth than the "asleep at 23:40" answer either
gave before the correction, which is the mechanism working.

Worth stating plainly because the temptation is to treat a value difference here
as a bug and chase it, which is exactly the mistake that cost two days on the
NaN.

### A restart trap worth naming

The stats came back null on the first run and the cause was **a compiled change
in an unrestarted container**. `build.sh` writes to a mounted volume, so a build
succeeds and the running JVM keeps serving the old classes: orb-algo was still
emitting a nested `stats` object after the code had been flattened, and Go
silently decoded nothing. Same shape as the `sense_server.py` `/receive`
timeouts: **compiled is not running.** After any orb-algo change, restart the
container before believing a diff.

### The two big ones, and why they are not next-in-line trivia

**`GET /v2/timeline/{date}`** (691 calls) returns far more than the four main
events orb stores: 26 events for one night, including 19 derived `IN_BED`
segments carrying a `sleep_depth`, plus `LIGHTS_OUT`, `GENERIC_SOUND` and
`GENERIC_MOTION`, each with a rendered English `message` and a `valid_actions`
list. Alongside them sit `metrics` with per-metric `condition` bands, a rendered
summary sentence, `score` and `score_condition`.

Almost none of that exists below the seam orb chose. The algorithm service
returns four timestamps; sleep-depth segmentation, event message rendering,
metric computation and scoring all live above it, in suripu's `TimelineUtils`
and the scoring package. **Phase 4 cannot finish without either extending
orb-algo's response or porting that rendering.** Extending the seam is almost
certainly right: the segmentation is derived from the same binned data the
algorithm already has in hand.

**`GET/POST /v2/sensors`** (1,684 calls, the heaviest by far) is the room
conditions graph. It reads from `sensor_samples`, which orb owns completely, so
it is bounded work, but the response is a time series per sensor with its own
bucketing rules and the same `condition` banding as the timeline metrics.

Neither is a morning's work, and both are worth doing after the cheap reads are
exhausted rather than before.

### The migrator must not own derived tables

Re-running `cmd/migrate` to pick up the new account columns **overwrote three
nights of computed timelines** with the Java stack's 2026-08-13 snapshot,
including one rescored after the timestamp and calibration fixes. The risk had
been written down two hours earlier and walked into anyway.

`timeline_events` and `sleep_stats` are now `ON CONFLICT DO NOTHING`. Seeding an
empty table is the migrator's job; owning it afterwards is not. The clobbered
nights were recovered by deleting them and letting the worker recompute, which
works only because that path exists.

## `/v2/timeline/{date}` matches the reference exactly

2026-08-15. The endpoint now returns byte-identical JSON to suripu on a night
where both stacks agree on the four main events. 2026-08-13 diffs clean; so does
every stat behind it: score 76, duration 430, sound 253, medium 148,
uninterrupted 363, times awake 1. 2026-08-10 differs by a single event
timestamp. The other three nights differ because the models diverged after the
2026-08-14 correction, which is the expected and already-documented state.

### The score is v5 and nothing else

Both version-transition windows closed in 2016, so `getSleepScoreV2V4Weighting`
and `getSleepScoreV4V5Weighting` both return 1.0 for any date this code can see.
That selects v5 outright and the blend is dead code. The whole score is:

    round(0.8 * durationScoreV5 + 0.2 * environmentScore), floored at 15

**The motion score has weight 0.0 in v5.** It is computed, stored, and does not
move the result.

The oracle for this was suripu's own `sleep_stats_v_0_2` rows, which carry every
sub-score alongside the final value. Four consecutive nights reproduced exactly
before a line was written. Reading a formula out of the source and reading the
numbers it already produced are different kinds of confidence, and the second is
available here for free.

`env_score` in those same rows is 86, 90, 91 on consecutive nights. A defaulted
environment score is exactly 100, so `environment_in_timeline_score` is on. That
is worth more than reading the feature table, because the flag value is
`0.0||all` and what a rollout library does with a 0 percent rollout to group
"all" is not obvious.

`sleep_score_parameters` is empty, so both personalisation thresholds are 0.
Zero is the table's own `MISSING_THRESHOLD` and each function substitutes its
population default, so passing zero is the personalisation being absent rather
than a placeholder to fill in later.

### Four wrong guesses, all of which looked right

Everything below was invented in Go, was self-consistent, and disagreed with the
reference. None of it would have been caught by reading the Go code.

- **The six measured metrics are `IDEAL` unconditionally.** `SleepMetrics.fromV1`
  passes `Condition.IDEAL` for all six and the app colours them itself. The
  invented thresholds (under six hours is `ALERT`, and so on) were plausible and
  wrong on most nights.
- **A zero minute count or timestamp renders as `null`, not `0`.** The v1 stats
  default to 0 rather than being optional, so `create()` converts. A measured
  zero and an unmeasured one are the same value and must not be the same JSON.
- **The summary's "sleeping soundly for N hours" is UNINTERRUPTED sleep, not
  sound sleep.** On 2026-08-13 that is 363 minutes against 253. Both fields are
  in the same struct and the wrong one reads perfectly.
- **`WOKE_UP` greets the time of day.** "Good morning." / "Good afternoon." /
  "Good evening." by local hour. "You woke up." exists in the reference as the
  message for stirring mid-night, which is exactly why picking it looks correct.

`SleepState` also comes from depth alone, with thresholds 5/10/70. The reference
sends `SOUND` at depth 77 and `MEDIUM` at 31, which no round-number banding
produces. The earlier version forced `AWAKE` on the named events, which is right
on almost every night because those usually land on depth 0.

### The 21-minute band is 21 events, not 21 minutes

This was the big one, and it is the one general lesson worth keeping.

The app's timeline shows long uniform `IN_BED` rows. Measuring them gave 21
minutes, so Go re-banded the per-minute segments on a 21-minute clock and
averaged each band's depth. That produced bands of about the right shape
carrying the wrong numbers, and it was wrong in three ways at once:

- The band is **21 buffered events**, from `buffer.size() <= 20` in
  `TimelineRefactored.mergeEvents`, not a 21-minute time slice.
- The band's depth is the **minimum** depth in the buffer, not the average.
  Hence the reference's 10, 20, 31 where an average gives 90-plus.
- `mergeEvents` also merges runs of closely spaced motion, which no amount of
  banding reproduces.

The cost was 149 minutes of misclassified sound sleep (402 against a real 253),
plus an extra row, plus every band's depth. The fix was deleting the Go
implementation and calling `TimelineRefactored.mergeEvents` in Java, which is
what the file's own header already said to do: *a call cannot drift from the
thing it calls*. It had been replaced with `new ArrayList<>(byTime.values())` on
the assumption it only sorted, which is what the reference's own comment says it
does. Benjo's "plus some other shit that only Pang knew about" was the load
bearing half of that sentence.

**Banding is arithmetic on the samples, so it was on the wrong side of the seam
to begin with.** The seam test is not "does this feel like presentation"; it is
"does this need the samples". A 21-minute row looks like a rendering decision
and is not one.

### `TimeZoneHistory.offsetMillis` is ignored

`TimeZoneOffsetMap.getOffset` never reads the offset field. It resolves the
**zone ID** and asks Joda. Constructing the map with id `"UTC"` and the night's
offset alongside it therefore pinned every gap-filled minute to offset 0, while
minutes carrying a real pill sample kept their own offset. The timeline was
correct until the night went quiet, which is when nobody looks.

The fix is a fixed-offset ID: `DateTimeZone.forOffsetMillis(offsetMillis).getID()`
yields `"-04:00"`, which resolves. A field that is present, populated, plausible
and unread is worse than a missing one.

### Two more that cost nothing but would have

- **Java's `%.1f` rounds half up; Go's rounds half to even, from the exact
  binary value.** 363 minutes is 6.05 hours and lands exactly on the boundary:
  Java prints 6.1, Go prints 6.0. `hours()` does the rounding in integer
  arithmetic so there is no half-way case left to disagree about.
- **`getLightEvents` takes the sleep event's END timestamp**, not its start.
  One minute, and it decides which light drop becomes `LIGHTS_OUT`.

### Still open on this endpoint

- 2026-08-10 differs by one event timestamp, `events[20]`, by 48 minutes. Every
  stat on that night matches exactly, so it is a single misplaced non-main
  event.
- 2026-08-10 differs by one event timestamp, `events[20]`, by 48 minutes.

## `/v2/insights` and the third database

2026-08-15. The `insights` Postgres database is now migrated and `/v2/insights`
matches. Nine of the ten implemented endpoints match; the tenth differs only on
`has_unanswered_questions`, which is no longer blocked but is deliberately not
built (below).

An insight card needs both halves of a split that had never been crossed. The
cards are in **DynamoDB** and carry `category` as a bare ordinal, 19. The names
the app displays are in the **`insights` Postgres database**, in
`info_insight_cards.category_name`, which is where "Wake Variance" comes from.
Neither half renders alone. The ordinal is resolved on the way in rather than
stored, because 19 means nothing to anyone reading the table later.

The image URLs are a naming convention rather than stored data: the lowercased
category under `hello-data/insights_images/`, with `@2x` and `@3x`. A category
with no artwork yields three URLs that 404, which is what the reference does.

**Corrected 2026-08-19: that bucket no longer serves.** It was described here as
"still serving", which was true of the URL shape and not of the content. See
"The insight card art is gone" below.

`info_preview` is always null. The reference's method is called
`insightCardsWithInfoPreviewAndMissingImages` and it never sets a preview: it
backfills images and category names only. The field exists because the app wants
the key.

### `has_unread_insights` was right only because the table was empty

`NOT seen` is the obvious reading of "unread" and it is wrong. Nothing ever
writes `seen`, so the moment insights actually existed the flag went true for
every account, and the endpoint that had matched for days started differing.

The reference compares the newest insight against
`app_stats.insights_last_viewed`, and returns **false** when that column is
null. Never having opened the screen is not the same as having unread items:
the flag drives a badge, and badging a feature the account has never visited is
not what unread means. An `INNER JOIN` encodes it, so a missing row and a null
column both fall out as false.

This was caught only because the migration ran against a live diff. A stub that
returns a constant agrees with everything until it has data.

### The migrator now has `-only`

Re-running every step to pick up one new table is what destroyed three nights of
timelines on 2026-08-14. `-only insights,insight_categories` runs named steps
and nothing else. The steps are individually safe to repeat now, but that is a
property of sixteen separate functions and it only has to lapse once.

It earned itself immediately: the first run surfaced that `parseTS` rejects
`2026-07-27T15:40:18.039Z`, because every layout it knew was space-separated
with no zone. RFC3339 is now tried first.

### Questions are deliberately not built

`has_unanswered_questions` needs `QuestionProcessor.getQuestions`: 670 lines
covering onboarding questions, a skip-based pause, per-category feature flags,
CBTI goals, anomaly questions, and inter-question dependencies. That is a
substantial port for a feature that is one boolean on one screen, and the
`/v1/questions/` endpoints behind it were barely used.

The data is migrated and the port is unblocked whenever it is wanted: 47
questions, 46 asked, 6 answered. It is not scheduled. **This is the one endpoint
difference that is a choice rather than a gap**, and it should stay written down
as a choice so it is not rediscovered as a bug.

## `/v2/sensors`: all four exact, by reproducing a bug

2026-08-15. Eleven endpoints implemented, ten matching. The only remaining
difference is `has_unanswered_questions` on `/v1/app/stats/unread`, which is the
deliberate non-port above.

The endpoint is one database row. Everything the app shows comes from the most
recent sensor sample, calibrated. That calibration is arithmetic rather than
analysis, so unlike the timeline it stays in Go.

The scale bands carry their own name, message and condition, and the endpoint
picks the band the value falls in. It does NOT use the `CurrentRoomState`
classifiers, which compute the same conditions from the same values with
different thresholds and different wording. Two plausible sources, one right.

Details worth keeping:

- **The bands have gaps, and they are in the original.** Temperature runs to
  9.99 then resumes at 10, so 9.995 is in no band. Closing the gaps moves the
  boundaries, so `classify` walks in order and takes the first containing band,
  falling back to the last.
- **Humidity is not a subtraction.** The reading was taken at Sense's own
  temperature, 3.89 degrees too warm, so it converts to a dew point and back at
  the corrected temperature. Subtracting an offset from the humidity gives a
  reasonable-looking number whose error grows as the room dries out. Go
  reproduces it to the last decimal.
- **Sense reads 7 degrees Fahrenheit warm** because it measures its own board.
  That is what the 389 is.
- **The light conversion has a known gap in the reference itself**, whose
  comment reads "set conversion to 2x for now until we have a way to get sense
  color". A black Sense should get 5x and does not. Matching means keeping the
  same gap, not fixing it.
- **The type is SOUND and the label is "Noise".** Both appear in the response
  and they are not interchangeable.

### The sound value: the reference converts the same number twice

orb read about 1 dB high (28.614 against 27.591; 29.745 against 28.696). The
formula was right and the input was right; the reference applies an extra
conversion on the way out of the database.

`getMostRecent` turns the DynamoDB row into a `DeviceData` through
`attributeMapToDeviceData`, which calls `withAudioPeakEnergyDB`. That builder
assumes it has been handed a **raw ADC count**: it divides by 1024 and
multiplies by 1000. But the stored column is already millidecibels, written that
way at ingest. The sibling converter in the same class,
`dynamoItemToRawDeviceData`, calls `withAlreadyCalibratedPeakEnergyDB` and does
not convert. Two converters, two builders, and the one this endpoint reaches is
the wrong one.

So the value is scaled by 1000/1024, about 2.3% low, and the truncation back to
an int loses a little more:

    stored 43614 -> (int)(43614 / 1024 * 1000) = 42591 -> 42.591 - 15 = 27.591

which is the reference's answer to the millidecibel, for both observed samples.
orb reproduces this in `reReadAudio`, with the two pairs pinned in
`sensors_test.go` so a later "fix" fails loudly.

**Every sound reading the app has ever shown is about 2.3% low.** Serving the
correct number would be a visible disagreement with the stack being replaced, so
the bug is part of the contract until the reference is gone.

Two things worth taking from how this was found:

- **The shape of the error named it.** A gap that would not sit still (1.023,
  then 1.049) is what a proportional error looks like. Three plausible
  explanations had already been eliminated by measurement (not a different
  sample: temperature, humidity and light matched exactly on the same request;
  not a bad ingest: `audio_peak_energy_db` is byte-identical to `apedb`; not the
  formula: `calibrateAudio` is `max(peak - 40 + 25, 0)` either way, and its
  `backgroundDB` argument is dead code). What was left was the input, and the
  input was arrived at by arithmetic before the code confirming it was read.
- **The recorded hypothesis was wrong, and cheap.** The guess written down last
  session was that the running jar might not match the source. It does: only
  `SuripuApp.java` and `SenseProcessorUtils.java` differ between
  `github-backup` and `infrastructure`, and every file on this path is
  identical. One `diff -r --brief` eliminated it, which is the right cost for a
  guess.

Also fixed while here: the endpoint prefers peak energy and falls back to peak
disturbances when energy is zero, and the reference tests for zero **after** the
re-read. A zero energy reading means the minute carries no energy figure, not a
silent room. orb was not loading the fallback column at all.

`cmd/apidiff` now defaults to the whole implemented read surface rather than two
endpoints, so a bare run is the regression sweep. A handler that is not on that
list is only checked when somebody remembers to name it.

## `/v2/trends`: verified against golden files, not against the live stack

2026-08-15. Twelve endpoints implemented. Three graphs: Sleep Score as a
calendar grid, Sleep Duration as bars, Sleep Depth as bubbles.

This reads the per-night aggregates the timeline already stores and decides
where each night is drawn. No raw samples, so by the seam rule it is Go rather
than orb-algo. It is a calendar problem, not a statistics problem: nearly all
the code decides which cell a night belongs in.

**Three kinds of empty, and two of them look alike.** A `null` is a cell outside
the account's life, drawn as nothing. A `-1` is a night inside it with no data,
drawn as a gap. A number is a night. Conflating null with -1 renders the month
before somebody signed up as a wall of failures. The account creation date is
the only thing separating them.

### It cannot be verified by diffing the live stack

orb's scoring model diverged from suripu's on 2026-08-14, and three of this
account's five nights now differ (08-11, 08-12, 08-14; 08-10 and 08-13 agree
exactly). Every one of those feeds trends, so a live diff shows eleven
differences and **none of them tell you whether the rendering is right**.

So the check runs the other way round: the reference's own aggregates were read
out of its DynamoDB table and are fed through orb's rendering, and the whole
payload is compared against responses captured from the running Java stack.
`TestTrendsMatchesReference` does this for LAST_WEEK and LAST_MONTH and both
match exactly. That separates a rendering bug from a scoring difference, which
is the distinction a live diff has permanently lost here.

`/v2/trends` is therefore **not** in the apidiff default sweep, and the reason is
recorded there: a permanently red line in a sweep is one nobody reads.

The comparison is whole-payload rather than field-by-field, deliberately. The
bugs this endpoint invites are structural, and a test asserting the values would
have passed while the calendar was shifted by a day. That is not hypothetical:
it is what the first run caught.

### The bug it caught: `civil()` applied the offset twice

The reference builds a local date by adding the account's UTC offset and
truncating. orb did the same, and then read the wall clock in the machine's own
zone: pgx returns a TIMESTAMPTZ in local time, and `Date()` answers in whatever
zone the value carries. This account was created 04:03 UTC, which is 00:03
local, so reading it in a -4 zone **after** the offset had already been applied
moved the creation date back a day.

The visible symptom was one cell, three sections away, showing a missed night
instead of a day before the account existed. Nothing else in the payload moved.
This is the same local-UTC trap as the timeline window bounds, in a new place:
**an instant that has had an offset added is not in any zone any more, and must
be read as UTC.** `civil()` now does, with the failure recorded on it.

### Details worth keeping

- **Annotations are all three or none.** The reference computes weekday,
  weekend and overall averages and drops the entire set if it cannot produce
  three. A week slept entirely on weekdays yields two, so a real response
  carries an empty annotations list on an account well past the age gate. That
  looks like a bug and is not; there are tests for both directions.
- **The initial maximum is `Float.MIN_VALUE`**, the smallest positive float,
  not negative infinity. A graph whose values are all zero reports a maximum of
  1.4e-45. Reproduced rather than corrected, because it is visible in
  `max_value`.
- **The min/max highlights are asymmetric on purpose**: the last minimum and the
  first maximum. When one night is both, only the maximum is highlighted.
- **`LAST_3_MONTHS` is transcribed but unverified.** The account is 20 days old,
  so that scale is not offered to it and apidiff cannot reach the path. Labelled
  in the code as a transcription rather than a checked port.

## The timeline writes: built, wired, and driven end to end

2026-08-15. The three writes are PATCH (move an event to a new time), PUT (mark
the algorithm right) and DELETE (mark it wrong). All three build the same
feedback record, run it past the same two rules, and store it only if it passes.
PATCH returns the re-rendered timeline, DELETE returns it with 202, PUT returns
202 empty.

This is not a comment box. **Feedback is training data**: it feeds the model
that scores every later night, which is why a bad correction is worse than no
correction and why the rules in front of it exist.

`internal/feedback` implements both rules with tests, and the three handlers are
wired and exercised against the running stack. **Fifteen endpoints.**

### The finding: the same correction is accepted or refused depending on the verb

The window check asks whether a corrected time is one the night could contain.
It takes the account's UTC offset, and **the reference passes a different offset
depending on which verb was used**: a literal `0` when amending a time, the real
offset when marking an event correct or incorrect.

Those are different windows. With offset 0 the bedtime window is 18:00 to 16:00
across midnight. At -4 hours it collapses to 00:00 to 20:00 on a single day, and
**every ordinary bedtime falls outside it**.

Confirmed against the running stack rather than reasoned about. Marking a real
`GOT_IN_BED` at 23:25 correct returns:

    412  {"code":412,"message":"This adjustment could not be made because
          it is too early or too late."}

and the feedback table is unchanged, nine rows before and after. So a user can
drag their bedtime to a new time and have it accepted, but cannot tap the tick
to say it was already right.

The experiment was safe by construction and that is why it was worth running:
validation happens before any write, so a correction predicted to be refused
mutates nothing if the prediction holds. Predicting the refusal, then confirming
it wrote nothing, cost one request and settled a question that reading could
not.

Reproduced rather than fixed, on the same grounds as the sound calibration: the
app is built against what the reference returns, and quietly accepting
corrections the old stack refused is a behaviour change wearing a bug fix's
clothes. Fix it once the reference is gone. `TestVerbChangesWhetherBedtimeIsAccepted`
pins both halves so the asymmetry cannot be tidied away by accident.

### The other rules

- **The dead zone is real and asymmetric.** With no offset a night cannot
  contain a bedtime between 16:00 and 18:00, nor a wake between 16:00 and
  20:00. Two different windows, deliberately.
- **Ordering is checked against stored feedback, not against the algorithm's
  events.** An empty history always passes, so the first correction of a night
  is never refused for ordering.
- **Strictly increasing**: two events at the same minute are refused. In bed
  and asleep at the same instant is not a night worth teaching.
- **Non-sleep events skip both rules.** There is no ordering a noise could
  violate, and refusing one would leave the user unable to say a sound was
  wrong.
- **Event numbering is not event order.** `GOT_OUT_OF_BED` is 13 and `WOKE_UP`
  is 14, but waking comes first. Deriving the order from the numbers puts them
  backwards; they are separate tables.

### The refactor the writes forced, and why it was worth doing properly

PATCH and DELETE return a re-rendered timeline, so a correction has to score its
night inside the request that made it. Only the worker could do that:
`scoreNight` built the algorithm request and the API handler had no algorithm
client at all.

`internal/scoring` now owns `ScoreNight`, and the worker and the API share one
`Scorer`. The alternative, a second request builder in the API, would have
drifted from the worker's, and the failure mode is a night scored one way by the
timer and another way by a correction: two different timelines for the same
night depending on what caused the scoring, which nothing would report.

`writeTimeline` was likewise extracted from `getTimeline` for the same reason.
A corrected timeline and a fetched one have to be the same document, or the app
shows one thing on save and another on refresh.

The other piece is `OffsetForNight`, which prefers the offset stored with the
night's own timeline over the account's current one. Corrections carry local
wall-clock times, so a bedtime corrected after flying east would come back an
hour out and teach the model the wrong time.

**The standing chore of hand-copying feedback from `common.timeline_feedback`
into orb is now gone.** orb records its own.

### Verified against the running stack

- **The rejection matches byte for byte.** The same PUT that the Java stack
  refuses, orb refuses, with an identical body:
  `{"code":412,"message":"This adjustment could not be made because it is too
  early or too late."}`. Neither wrote a row.
- **The accepting path runs end to end.** A mark-correct on a wake returns 202
  with no body, inserts the row (`14 | 06:50 | 06:50 | t`), and re-scores the
  night in the same request: `scored night ... algorithm=ONLINE_HMM score=76
  feedback=1`.
- **PATCH returns the re-rendered timeline**, 200, same shape as the GET, and
  412s a wake amended to 17:30 in the dead zone.
- **The reads still match** after all of it: the sweep is unchanged at 10 of 11
  and `/v2/timeline/2026-08-13` still matches exactly.

The accepting writes were chosen to teach the model nothing: mark-correct is
feedback equal to prediction, and the PATCH used a new time identical to the
existing one. That exercises every line of the path without altering what the
model has learned, which matters because feedback is training data and a test
correction would be indistinguishable from a real one forever after.

### Still not exercised

DELETE has no live test. It shares `prepare` and `applyCorrection` with the
other two, so the untested part is the 202-with-timeline response, but it is
untested. Its one surprise is recorded in the code: **despite the verb, nothing
is deleted.** It inserts a correction with `is_correct` false and the event
stays on the timeline, because the reference never built the API change that
removing an intermediate event would need.

## `POST /v2/sensors`: built and correct; it exposed a mirror fidelity bug

2026-08-15. The second most called endpoint in the whole app (832 calls against
852 for the GET), and the last big unbuilt read. It backs the graphs behind each
sensor dial. Not implemented; this section is the groundwork so the next attempt
starts from facts rather than from reading Java again.

**It is a read despite the verb**, so it can be diffed like any other endpoint.
The body is `{"scope": ..., "sensors": [...]}` and nothing is written.

Three scopes, each a window and a slot size:

| scope | window | slot | slots |
|---|---|---|---|
| `LAST_3H_5_MINUTE` | 3 hours | 5 min | 37 |
| `DAY_5_MINUTE` | 24 hours | 5 min | 289 |
| `WEEK_1_HOUR` | 7 days | 60 min | 169 |

The response is two parallel arrays, which is what makes it compact:

```json
{"timestamps": [{"t": 1786823700000, "o": -14400000}, ...],
 "sensors": {"TEMPERATURE": [23.1, ...], "HUMIDITY": [47.4, ...]}}
```

**The grid is generated from the window, not from the data.** Every scope
returned exactly window/slot + 1 entries, with a regular step and no nulls, so
the slot count is arithmetic rather than however many samples happened to exist.
What fills a genuine gap is **untested**: this Sense has no gaps in any of these
windows, so nothing here says whether a missing slot interpolates, carries the
previous value, or nulls.

### The aggregation is per sensor and it is not all mean

This is the part that would be got wrong by assumption. From
`DeviceDataDAODynamoDB`, each slot reduces its samples with:

| column | reducer |
|---|---|
| ambient temperature | **min** |
| ambient humidity | rounded mean |
| ambient light | rounded mean, through `calibrateAmbientLight` |
| light variance | rounded mean |
| air quality raw | rounded mean |
| audio peak energy / disturbances / background | **max** |
| audio num disturbances | **max** |
| wave count, hold count | **sum** |

**Temperature is the minimum of the slot, not the mean.** Sense measures its own
board as well as the room, so the coolest reading in a window is the closest to
the truth; averaging would bake the self-heating back in that the 389 constant
exists to remove. Audio is the maximum because a peak is the point.

### The audio double-conversion applies here too

This path builds its `DeviceData` with `withAudioPeakEnergyDB`, the same builder
that assumes a raw ADC count and re-divides by 1024, so **the same 2.3% error
documented for the GET is in the graph as well**. Whatever `reReadAudio` does in
`internal/api/sensors.go` has to happen here, applied after the max rather than
before, since the max runs on the stored values.

### Built, and what matches

Implemented in `internal/api/sensorseries.go`. Against the live stack:

| scope | values differing |
|---|---|
| `LAST_3H_5_MINUTE` | 2 of 148, both at index 0 |
| `DAY_5_MINUTE` | 32 of 1156 |
| `WEEK_1_HOUR` | 8 of 676 |

The timestamp arrays match **exactly** for all three scopes: same slot count,
same first and last instant, same offset. Light and sound match in full at day
and week scope. Everything remaining is temperature, off by 0.1 to 0.3 degrees.

Two things found by diffing that reading would not have given up:

- **The wire carries one decimal place.** The reference rounds before sending
  (48.5, not 48.11301), half away from zero. Unrounded values are visibly
  wrong on every single point.
- **The data window and the grid do not start at the same instant.** The grid
  starts at a rounded slot boundary; the data window starts at the *unrounded*
  `now - scope`. So the oldest slot is deliberately partial. Using the grid
  start for both fills that slot with a few extra minutes and is wrong on
  exactly one point out of 37, which is easy to dismiss as clock skew.

Index 0 is inherently racy between the two stacks, because the two requests are
issued at different sub-minute instants and that slot is partial by design.

### The endpoint is correct; the mirror is not

The residual differences are **not** an endpoint bug. Fed the reference's own
inputs, orb's rendering reproduces the reference's output exactly.

The chase is worth recording because the first hypothesis was wrong in a
convincing way. Temperature came out 0.1 low in most places and 0.1 high in a
few, and a few high rules out orb's bucket simply holding extra samples, since a
wider bucket can only push a minimum down. Reading the raw rows in orb, the
reference's slot at 01:00 returned 21.2 where orb's samples for 01:00 to 01:04
have a minimum of 21.3, and 21.2 was exactly the minimum of the *previous* five
minutes. That looked like a one-slot shift, and it explained 01:05 as well. It
failed on 01:10, which read 21.4 when nothing nearby produced that.

The shift was a coincidence. Querying DynamoDB directly for the same minutes
showed **the two stores hold different values**:

    minute   DynamoDB   orb
    01:01      2522     2522
    01:02      2511     2525
    01:03      2514     2527

Running orb's own algorithm over DynamoDB's numbers reproduces the reference on
all three slots, including the 21.4 that broke the shift theory:

    slot 01:00  min=2511 -> 21.2  java=21.2  MATCH
    slot 01:05  min=2519 -> 21.3  java=21.3  MATCH
    slot 01:10  min=2524 -> 21.4  java=21.4  MATCH

So floor-to-slot bucketing, minimum for temperature, and half-up rounding to one
decimal are all confirmed correct.

**The lesson is the one this project keeps relearning: when two systems
disagree, check that they are reading the same bytes before theorising about the
code.** The shift hypothesis fit two of three cases and would have justified an
elaborate and entirely wrong change to the bucketing.

### The secondary finding: one 16-minute window where the mirrors disagree

Comparing every minute of temperature for account 1:

| day | common minutes | differing | missing from orb |
|---|---|---|---|
| 2026-08-10 | 1180 | **0** | 0 |
| 2026-08-11 | 1439 | **0** | 0 |
| 2026-08-12 | 1422 | **0** | 0 |
| 2026-08-13 | 1189 | **0** | 0 |
| 2026-08-14 | 1439 | **0** | 0 |
| 2026-08-15 | 1388 | 16 | 3 |

**Six days, 8057 minutes, 16 differences, all in one quarter of an hour.**

All sixteen differences fall in one contiguous run, **01:02 to 01:17 UTC**
(21:02 to 21:17 local on 08-14). Every other minute of all three days is
identical. The three missing minutes (13:47, 13:58, 14:02) are separate and
scattered, and look like dropped uploads rather than part of the same event.

An earlier draft of this section called the mirror unfaithful and made it the
most important open question in the port. **That was an overstatement drawn from
a single day.** Two clean days either side make this an isolated incident rather
than a systemic decode difference, and the scope changes what it means: a
recurring 1.2% error in the mirror would block cutover, whereas one 16-minute
window on one evening is something to explain, not something to stop for.

What the numbers look like: both series are smooth curves over the window but
out of phase, with DynamoDB lagging orb by roughly five to seven minutes at the
start of the run, and orb recording a dip to 2501 that DynamoDB does not show at
all. That shape is consistent with one side processing a delayed or replayed
batch while the other took it live, which would make it a timing incident rather
than a decoding bug.

**Cause not established.** No logs survive from that window, and which of the two
series is physically correct is undetermined.

The one structural question has been answered, and it is the answer that makes a
divergence possible: **the two stacks ingest independently.** orb's edge writes
Postgres directly and does not publish to Kinesis (the only mention of Kinesis in
`internal/` is a comment). `suripu-service` and `sense-save-worker` are running
and feeding DynamoDB on their own. So the device's uploads are decoded twice, by
two separate implementations, and nothing downstream reconciles them.

That rules out the reassuring explanation. If DynamoDB were downstream of orb, a
divergence would require a bug in one path and would be reproducible. Two
independent decoders of the same bytes can differ for reasons that never recur,
which fits two clean days around one bad quarter of an hour.

It also means **this comparison is a genuinely independent check on orb's
decoder**, which is worth keeping: run it across a few more days, and any
recurrence localises the fault to a batch shape rather than to a theory. Worth
watching for a recurrence rather than chasing this one cold.

Also still unread: `calibrateAmbientLight`, a different builder from
`withAmbientLight`. Light matches at day and week scope, so whatever the
difference is, it is not currently visible.

## The two small writes: app stats and timezone

2026-08-15. **Eighteen endpoints.** Both verified against the running stack.

### `PATCH /v1/app/stats`

Records that the insights or questions screen was opened, which is what clears
its badge. Matches: 202 when a field was supplied, and **304 when neither was**.
The 304 is worth keeping in mind, because a body with no fields is not an error
here, it is a no-op, and the reference says so with Not Modified rather than a
400.

The store method takes pointers rather than times for a reason. The app sends
one field or the other, never both, and writing a null for the absent one would
clear the other screen's badge as a side effect of opening this one.

### `POST /v1/timezone`

Success path matches byte for byte. Two things worth recording:

- **The offset is recomputed from the zone, not trusted from the body.** The app
  sends both, and the two disagree across a DST boundary: a phone that cached
  its offset before the clocks changed sends yesterday's number with today's
  zone, and storing that shifts every later night by an hour. The zone name is
  the durable fact.
- **Refused without a paired Sense**, matching the reference. That looks like a
  technicality and is not: the zone is what the device's alarms are scheduled
  in, so accepting one for an account with no device stores a preference nothing
  reads and implies an alarm moved.

`timezone_history` stays append-only, keyed by when the zone took effect,
because every sample and every night renders with the offset in force at the
time. A single current-zone column would move last month's nights when somebody
flies.

**One deliberate divergence, flagged rather than silent.** An unparseable zone
gets 400 from orb and 500 from the reference, which lets the lookup throw. orb
answers 400 because a bad zone is a bad request, and this is safe to differ on
in a way most things are not: the app sends the phone's own zone, so it cannot
reach the branch, and nothing is built against the 500. It is called out in the
code as well, because "orb and the reference disagree" has to stay auditable
rather than accumulating as folklore.

## `POST /v2/alarms/{ts}`: the alarm write

2026-08-15. **Nineteen endpoints.** Both the accept and the reject path match
the running stack exactly, including the error body.

**The path carries the phone's own clock**, and it is checked. An alarm set by a
phone whose clock is wrong rings at the wrong time, so a skew beyond 110 seconds
(one minute plus the reference's own 50 second allowance on this endpoint) is
refused rather than corrected.

**The whole set is replaced, not merged.** The app owns the alarm list and sends
it entire, so an alarm it did not send is one the user deleted. Done as
delete-then-insert inside a transaction, because a failed insert after a
successful delete would leave the account with no alarms at all, and the user
would discover that in the morning.

**Two smart alarms on the same day are refused.** A smart alarm moves to meet
your sleep, so two competing targets on one day have no answer; the reference
rejects the save rather than picking one. A one-off smart alarm still occupies
its day for this check.

Refused without a paired Sense, for the same reason as the timezone write: an
alarm with no device to ring on is a silent failure.

### The error message is part of the contract

The first version returned the right status with wording I had written myself.
The status matched and the response did not:

    java: "Your device's time is significantly different from our reference
           time. From your device's Settings app, please enable automatic
           Date & Time, or enter the correct time manually."
    orb:  "Please check your phone's clock. It seems to be out of sync."

The app shows this string to the user verbatim. Paraphrasing it is a visible
product change, not a detail, and the original is better: it says which setting
to open. **Any user-facing string in an error body is contract, not log text**,
and comparing only status codes would have shipped this.

## `PUT /v1/account`: the profile edit, and the last endpoint

2026-08-15. **Twenty endpoints.** This is the last one that can be built before
cutover.

**`POST /v1/account` is register**, not modify, and is deliberately not built:
this is a revival of one household's existing account, and account creation
needs password hashing that belongs with `POST /v1/oauth2/token`. The app's
traffic log lists "PUT/POST /v1/account" as one line, which is what made it look
like two verbs of the same operation. It is not.

### Optimistic concurrency is the whole endpoint

The app sends back the **entire** account object it last read, not a patch, and
the update is guarded by `last_modified`. A mismatch is 412, not a silent no-op:

    java: {"code":412,"message":"pre condition failed"}
    orb:  {"code":412,"message":"pre condition failed"}

Without the guard, two phones editing the same profile would have the slower one
overwrite the faster one's changes with values it read before they existed. The
app recovers by re-reading and retrying, which only works if it is told, so the
412 is load-bearing rather than pedantic.

`last_modified` is compared to the millisecond, because that is the precision
the app was given. Comparing the `timestamptz` directly would fail on
sub-millisecond digits that never left the database.

`renderAccount` is now shared by the read and the write, so an edited profile
comes back in exactly the shape a fetched one does. Two renderers would disagree
the first time either changed, and the app would show one thing on save and
another on refresh.

### Testing a write whose side effect breaks the diff

The 412 path was compared against the live stack directly: it writes nothing, so
it is free to run on both.

The **success** path was run against orb only, on purpose. A successful edit sets
`last_modified = now()` in whichever database served it, and the two stacks have
separate databases, so writing to both would leave `/v1/account` permanently
differing in the sweep over a timestamp that means nothing. Instead: PUT the
account's current values back to orb, confirm the response equals a subsequent
GET, confirm **exactly one field changed** (`last_modified`), then restore it and
re-check the sweep.

That pattern is worth reusing for any write whose only side effect is a
timestamp: verify the shape on one side, verify the change set is what you
expect, then put it back.

## `POST /v1/oauth2/token`: built early, not at cutover

2026-08-15. **Twenty-one endpoints. Every endpoint the app calls is now built
except the questions pair.**

This was repeatedly deferred on the grounds that the success path cannot be
diffed without Joe's real password. That reasoning was half right and led to the
wrong plan: **the failure paths need no password at all**, and they are most of
the endpoint. Deferring meant planning to write authentication code under
cutover pressure, which is the worst possible time.

All eight failure paths were compared against the running stack and match:

| case | java | orb |
|---|---|---|
| no `grant_type` | 401 | 401 |
| `client_credentials` | 400 | 400 |
| garbage grant | 400 | 400 |
| unknown `client_id` | 401 | 401 |
| empty password | 401 | 401 |
| unknown user | 401 | 401 |
| wrong password | 401 | 401 |
| wrong-case email | 401 | 401 |

**Three states of `grant_type`, three answers**, and the diff is what found it:
absent is 401 from an explicit null check, unrecognised is 400 because the
parameter fails to parse before any handler runs, and a recognised-but-unbuilt
grant is 401 here. orb originally answered 401 for all of them.

### Verifying the success path without the password

Save the account's bcrypt hash, replace it with the hash of a password chosen
here, sign in, then put the original back. That confirmed the whole path: 200,
the six expected keys, `expires_in=31536000`, an `access_token` of the form
`{appId}.{32 hex}`, and **the minted token then authenticated a request to
`/v1/account`**, which is the only check that proves minting and parsing agree.
The test token was deleted and the original hash restored.

The 365 day expiry is not from the source, where it is threaded through
constructor overloads; it is from the migrated tokens, every one of which has
exactly 31536000 seconds between `created_at` and `expires_at`.

### Deliberate choices

- **Only the password grant.** `authorization_code` and `refresh_token` exist in
  the reference and are unreachable from this app: no web consent screen, and
  tokens last a year and are replaced by signing in again. Writing unused
  branches in the one endpoint that hands out credentials is not free.
- **Every failure is 401 with no body**, matching the reference. Distinguishing
  "no such application" from "wrong password" tells an attacker which half to
  keep trying.
- **The bcrypt comparison runs even when the account does not exist**, against a
  placeholder hash. Without it an unknown address returns immediately while a
  known one costs a bcrypt at cost 12, and that difference is trivially
  measurable over a network and enumerates accounts. The reference does not do
  this; it is a deliberate improvement, flagged here and in the code.
- **`refresh_expires_in` is UNVERIFIED** and set equal to `expires_in`. The
  reference never sets it on this path.
- **One new dependency**, `golang.org/x/crypto` for bcrypt. Taken deliberately:
  the stored hashes are bcrypt cost 12 and hand-rolling that is not an option.

## The Kinesis worker is unstable, and it explains the 08-15 mirror divergence

2026-08-16. The sweep went to 2 of 11 with `/v2/sensors` showing java returning
`UNKNOWN` and null for every sensor while orb returned live values. **That is not
an orb regression**: `UNKNOWN` with nulls is the reference's own stale-data
state, returned when the newest sample is more than 15 minutes old.

orb's newest sample was 00:11; DynamoDB's was 23:54. The reference was 17
minutes behind and had timed itself out.

The cause is in `sense-save-worker`, looping on:

    KinesisClientLibIOException: Shard [shardId-000000000000] is not closed.
    This can happen if we constructed the list of shards while a reshard
    operation was in progress.
    SHUTDOWN: TERMINATE / Going to checkpoint / Checkpointed successfully

The KCL believes the shard has closed, tries to terminate, finds it open,
errors, and re-initialises. The stream itself is ACTIVE with one open shard, so
this is KCL friction against LocalStack rather than a real reshard.

**This is very likely the cause of the 08-15 01:02 to 01:17 divergence.** A
consumer that stalls and then catches up, replaying and reordering records,
produces exactly what was observed there: a contiguous window, both series
smooth, DynamoDB lagging orb by several minutes and converging afterwards. It
also fits the clean days either side, since the fault is intermittent.

Two consequences worth keeping:

- **orb is the more reliable ingest of the two.** The consolidation removes this
  entire failure mode along with the worker, which is a point in its favour
  rather than a risk.
- **A sweep difference on `/v2/sensors` may be the reference being stale, not
  orb being wrong.** Check the newest sample on both sides before believing it.

## The questionnaire: ported functionally, and the personalisation revived

2026-08-15. **Twenty-three endpoints. Every endpoint the app calls is now
built.** `has_unanswered_questions` matches, which was the last difference in
the read sweep.

### Why it was worth deciding carefully

The port was nearly dropped. The evidence for dropping it was strong, and
gathering it is what made the decision possible:

- `AccountInfoProcessor` exposes five accessors that read answers.
  **`checkTemperaturePreference`, `checkUserSnore`, `checkUserSleepTalk` and
  `checkUserIsALightSleeper` have zero callers** anywhere in suripu-core,
  suripu-app or suripu-workers. Only `checkUserDrinksCaffeine` is called, by
  `CaffeineAlarm`, which returns nothing if the answer is missing.
- `QuestionResponseReadDAO` has no readers outside the question machinery
  itself plus that one processor, so the trace is exhaustive rather than a
  sample.
- `AccountInfo.Type` declares 11 categories; **only 5 ever got an accessor.**
  `NAP`, `TROUBLE_SLEEPING`, `EAT_LATE`, `BEDTIME` and `WORKOUT` have no reader
  even in principle, and two of the six answers on file are in that set.
- **Nothing reaches the algorithms.** No reference to `AccountInfo` or
  `QuestionResponse` in the timeline utilities or `suripu-algorithm`. Questions
  have never influenced a sleep score, a timeline or an event.

So the questionnaire collected data that fed one boolean gate on one insight.

### What was built

A **functional port, not a faithful one**, and the difference is which
questions come back. The reference selects through ~670 lines of onboarding
sequencing, skip-based pausing, category feature flags, CBTI goals, anomaly
questions and inter-question dependencies. orb serves the live pending set,
daily questions first, capped at five.

Four tables migrated (47 questions, 161 choices, 46 askings, 6 answers).
`account_question_ask_time` is skipped: empty, and part of the scheduling this
port does not reproduce.

**The generating half is not optional, and discovering that is what stopped
this being a one-query endpoint.** Every asking expires after a day, so all 46
migrated askings were already expired: serving only what is pending returns
either nothing or, as the first version did, 43 stale rows. The reference
creates fresh askings on each request, and so does orb, by frequency:

    one_time      never answered
    daily         not asked in the last day
    occasionally  not answered in the last 30 days
    trigger       NEVER

`trigger` is the survey, goal and anomaly categories, fired by conditions this
port does not reproduce. Serving them on a timer would ask somebody a
sleep-therapy goal question with nothing behind it.

Two things the diff caught that reading would not have:

- **The enum columns are lowercase.** The database holds `choice` and `morning`;
  the app reads `CHOICE` and `MORNING`. Sending the stored value looks right in
  a database query and is a wire format the app cannot parse.
- **The reference caps the set.** Returning all 43 pending questions is a wall,
  not a screen.

`account_question_id` is re-checked against the session's account before any
answer is stored. It arrives from a browser, and an unchecked one answers
somebody else's question.

### The personalisation, recovered from git history

The one deliberate improvement over the reference, agreed in advance.

The reference once shifted its ideal temperature range by the answer to "do you
sleep better when it's hot or cold", then removed it. The remaining TODO says
so. Rather than invent an adjustment, the original was recovered from history:
commit `2c2997a39` added it, `340fd53d8` removed the last of it.

    ideal band     60 to 70 °F
    cold sleeper   shift DOWN 3 °F
    hot sleeper    shift UP 5 °F

**The asymmetry is theirs and is kept**: sleeping hot is the more strongly felt
complaint. The response ids are the enum values (`HOT(1)`, `COLD(2)`,
`NONE(3)`, with a source comment saying "values are response_id"), so the
mapping is positional and cannot be read off the answer text.

Applied to the `/v2/sensors` temperature scale, because **orb does not generate
insights** - its `insights` table is a frozen copy written only by the migrator,
so the insight the adjustment originally lived in does not exist here. The
sensor dial is where a person sees an ideal temperature band daily, so that is
where it landed. The whole scale shifts, not just the ideal band, or the
neighbours would overlap it.

Verified end to end: answering "Cold" moves the ideal band from 15.00-19.99 to
13.33-18.32.

**This makes `GET /v2/sensors` deliberately diverge from the reference** for any
account that answered the question. It is the one place in the port where orb is
meant to be better rather than identical, and it is flagged in the code as well
as here.

### Still not done

The caffeine answer now exists (`Coffee`), so `CaffeineAlarm` would fire - if
anything generated insights. **Insight generation is not ported.** That is the
natural next piece if the questionnaire is to pay for itself.

## Insight generation: scoped, not built, and hard to verify

2026-08-16. orb serves insights but **does not generate them**. The `insights`
table is written only by the migrator, so after cutover the feed freezes at
whatever was copied. This is the largest remaining functional gap and it is not
started.

### The size

18 categories and ~19 generator classes, behind a selection framework that
picks a weekly category, then high-priority categories, then a random one, then
a one-time one, each gated by feature flags. Comparable to `QuestionProcessor`.

Roughly half the categories are lifestyle marketing (`DRIVE`, `EAT`, `LEARN`,
`LOVE`, `PLAY`, `RUN`, `SWIM`, `WORK`). The sleep ones are `AIR_QUALITY`,
`BED_LIGHT_DURATION`, `BED_LIGHT_INTENSITY_RATIO`, `CAFFEINE`, `HUMIDITY`,
`SLEEP_DEPRIVATION`, `SLEEP_QUALITY`, `SLEEP_TIME`, `TEMPERATURE`,
`WAKE_VARIANCE`.

The individual generators are small: `WakeVariance` is 90 lines,
`IntroductionInsights` 45. The framework around them is the bulk.

### The observed output is two cards

In three weeks the reference has generated exactly two insights for this
account: `GENERIC` at signup (2026-07-27) and `WAKE_VARIANCE` (2026-08-15). So
generation is live, and slow.

### Why it cannot be verified by diffing, which is the real problem

The `WAKE_VARIANCE` card says the wake time "varied an average of **2.6 hours**
last week", which is a standard deviation of about 156 minutes, and puts it at
the 97th percentile.

**That number cannot be reproduced from the reference's own current data.** Its
stored wake times for the five available nights give a sample standard deviation
of 135.3 minutes (2.3 hours), and no contiguous subset of three or more nights
produces 156:

    08-10..08-12  n=3  135.5   08-11..08-13  n=3   98.5
    08-10..08-13  n=4  114.0   08-11..08-14  n=4  115.7
    08-10..08-14  n=5  135.3   08-12..08-14  n=3  129.9

So the card was written from a state that no longer exists: the nights behind it
were recomputed afterwards. **An insight card is a snapshot, not a view.**

That has a consequence for any port. The usual method here, diff orb against the
running stack, does not work for insight generation:

- cards are historical snapshots, so an old card cannot be regenerated;
- the reference emits one every few weeks, so waiting for a fresh card to
  compare against is not a workflow;
- orb's sleep model has diverged from the reference's on three of five nights
  anyway, so even a perfect port would produce different numbers.

What **is** verifiable is the bucket. `WakeVariance` sorts the deviation into
four bands at 50, 79 and 108 minutes, and both 135 and 156 land in the same top
band, giving the same title and the same message template. So a port can be
checked at the level of "which card, with what wording", never "with what
number".

### Built, that way

`internal/insights` now generates cards, run by a worker job every six hours.
Each generator carries its own much longer cadence, so the interval only bounds
how soon a due card appears, never how often one is written.

**One generator: `WakeVariance`.** It is the only thing beyond the welcome card
the reference has ever produced for this account, so it is the only one worth
having yet. `IntroductionInsights` is deliberately absent: it fires once at
signup, and that already happened.

Due-ness is decided from what is stored, not from a timer in memory, so a
restart cannot produce a second card the same day. A generator returning nothing
is the normal case and is not logged as a problem.

**The percentile table is generated from the reference's source, not
transcribed.** It embeds a CSV of a real distribution measured on 2015-07-25,
and the values are TRUNCATED rather than rounded, because the reference parses
each row with `(int) Float.parseFloat(...)`. Rounding shifts much of the table
by one, and the error would be invisible in every test that did not check an
exact number.

Verified three ways, none of which is a live diff:

- **Against the one real card.** It quotes the 97th percentile for a deviation
  of 156 minutes, and the generated table returns exactly 97. Three more
  boundaries are checked against the percentiles the reference's own comments
  claim for them (50 to 24, 79 to 50, 108 to 75), which is an independent
  statement of the same table and catches an off-by-one across all of it.
- **The exact wording**, reproduced from the same inputs, including the trailing
  space before the blank line that the migrated cards carry.
- **End to end against the live database.** Backdating the stored card by eight
  days makes the generator due; it produced "Hello, very irregular ... varied an
  average of 2.3 hours ... less consistent than 92%". Same band and same wording
  as the reference, with orb's own figure, which is exactly the expected
  outcome given the two stacks disagree on three of five nights. The test card
  was deleted and the timestamp restored.

Two details that decide whether the card insults somebody:

- **Sample standard deviation, dividing by n-1**, matching Apache Commons'
  `DescriptiveStatistics`. The population estimator gives a visibly smaller
  number on a week of data and would move people into a calmer band than they
  belong in.
- **The comparison inverts between halves.** The consistent bands say "more
  consistent than 100 minus percentile"; the inconsistent ones say "less
  consistent than percentile". Backwards, it tells a regular sleeper they are
  worse than nearly everybody. There is a test for the inversion alone.

Wake times are read in LOCAL time via each night's own stored offset. In UTC,
anyone who changed zones looks wildly inconsistent, which is the very thing this
measures.

### Adding the next generator

Implement `Generator` (category, cadence, generate), add a line to `All()`. The
narrow `Data` interface is deliberate: a generator that can reach the whole
store will eventually query something expensive on a timer.

## Freshness: the gap the reference exposed by breaking

2026-08-16. The read sweep showed `/v2/sensors` differing with
`status: java=WAITING_FOR_DATA orb=OK`. The reference's Kinesis ingest had
stalled far enough that it had no recent data at all, and **orb was cheerfully
serving readings from hours earlier as though they were current**.

That is a real defect in orb, not a difference in the reference, and it was
found only because the other stack failed first.

The reference has two degradations and orb had neither:

| state | reference | orb before |
|---|---|---|
| sample older than 15 min | sensors `UNKNOWN`, value null, status `OK` | live values |
| no data in the window at all | status `WAITING_FOR_DATA`, all available sensors `UNKNOWN` | live values |

**The first is now implemented.** A sample older than fifteen minutes keeps its
sensor on the screen, because the name, unit and scale are what draw the dial,
but the value goes null, the condition goes `UNKNOWN` and the message empties.
`isStale` is split out so the boundary is testable without a database, and the
comparison is strictly greater than, so a sample exactly at the threshold still
counts as current.

**The second is not.** `WAITING_FOR_DATA` returns the full list of sensors
available for the hardware version, which is five for a Sense One and includes
one orb never populates. Reproducing it needs a hardware-version sensor table
that nothing else here wants. orb answers `OK` with four `UNKNOWN` sensors
instead, which tells the app the same thing in a shape it already handles.

Showing a stale number is worse than showing none. The whole point of that
screen is that it is now.

## Alarms: served from the edge, and the shadow that hid the test

2026-08-15. orb's edge replied to every device sync with an empty
`SyncResponse`. That was correct as "nothing to do" and it also meant **a
cutover today would have stopped every alarm from ringing**, ordinary ones
included: the Sense learns its ring time from the sync response and nothing
else. That is a larger hole than any endpoint mismatch, and it sat behind a
one-line comment.

`internal/alarm` now computes the next ring, and the edge fills
`SyncResponse.Alarm` with start, end, ringtone id and path, duration and offset,
plus `RingTimeAck`.

### Ordinary alarms only

Smart wake, which moves the ring earlier inside a window when the sleeper looks
to be in light sleep, is **not** implemented. A smart alarm rings at its set
time. That is the safe direction: it wakes you when you asked rather than not at
all. `RingProcessor` is 832 lines and most of it is that feature.

Two more choices in the same spirit: **any failure returns nil**, meaning
"nothing to do", because a missed ring costs one morning while a malformed
response the device rejects costs the connection; and an unknown ringtone id
falls back to `DIG001` rather than to silence, because the wrong tone still
wakes somebody.

### The countdown must not use the floored clock

The reference floors now to the minute for matching, so an alarm set for 07:00
still fires at 07:00:30. Using that floored value as the base of the countdown
puts "now" up to 59 seconds in the past, and the Sense rings that late. The
offset is measured from the unfloored clock, and `now` is read **after** the
database lookups so a slow query cannot inflate it.

### What the live test actually proved, and what it did not

A test alarm six minutes out produced a clean countdown across five syncs: 257,
198, 138, 78, 18, each resolving to the right second, with ingest never
faltering.

**It did not ring, and it was never going to.** `sense_server.py` runs with
`SENSE_SHADOW=http://127.0.0.1:8081`: the device's real upstream is
suripu-service on :5555, and orb receives a shadow copy whose **response is
discarded**. orb computed the alarm correctly and replied into a void; the
device acted on suripu-service's answer, which had no alarm because the test row
existed only in orb's database.

**Verifying the computation and then asserting a physical outcome is the
mistake to avoid here.** The shadow arrangement was already documented and the
flag was visible in the process list. Testing a ring for real requires promoting
orb from shadow to primary, which is a cutover step and not a test fixture.

### The bug the failed test found anyway

The last sync read `21:05:44 ... in_seconds=1`, forty-four seconds after the
alarm had passed. With the reference's minute floor, 21:05:00 is still "not
before" 21:05:44, so every sync for the rest of that minute reports the alarm as
due. The reference survives this because it keeps ring history and the device
acknowledges each ring; **orb has neither**, so on a listening device that is a
second ring a minute after the first.

`Next` now compares against the exact local time. The cost is that an alarm
created in the same minute it is due may wait for the next occurrence, which is
the safe direction to be wrong in: a missed degenerate case against an alarm
that will not stop. `TestPassedAlarmIsNotServedAgain` pins both sides of the
boundary.

### It rang

2026-08-16, 10:50:00 EDT. **orb drove the Sense for the first time.** A three
minute test alarm, with `sense_server.py` restarted as

    SENSE_UPSTREAM_SENSE=http://127.0.0.1:8081   # orb primary
    SENSE_SHADOW=http://127.0.0.1:5555           # suripu shadow

The reversal matters: suripu keeps receiving copies, so the iOS app carries on
updating from DynamoDB while orb drives the hardware. Without it the conditions
tab goes stale the moment the swap happens.

The trace, from orb's own log:

    10:48:28  next alarm at 10:50:00  in_seconds=91
    10:49:28  next alarm at 10:50:00  in_seconds=31
    10:50:00.892  recorded sense state
    10:50:01.896  recorded sense state

Two syncs, both resolving to 10:50:00, the second 32 seconds before the ring,
then a burst of device state posts landing exactly on the second. And no line
after 10:50 reports the alarm again, which is the re-trigger fix holding: the
same log yesterday said "ring in 1 second" for the rest of the minute.

Before the swap, every path the device uses was checked against orb's edge:
`/in/sense/batch`, `/in/pill`, `/in/sense/state`, `/in/sense/files`, `/receive`,
and `/logs` and time sync through the catch-all, with `/logs` answering 204 as
suripu does. That check is what the previous attempt skipped.

**orb was not supervised** when it rang: `/tmp/orb-bin` under `nohup`, with
nothing to restart it. `full-instructions/infrastructure/orb/deploy/` now carries a launchd daemon and an install
script, and the procedure for switching orb into and out of the device's path is
in [RUNNING-ORB.md](RUNNING-ORB.md). Installing it is the first step of cutover;
until it is installed, orb is whatever `nohup` left running.

### Smart wake, rebuilt honestly rather than ported

The reference's smart alarm is mostly random, and reading it before porting is
what stopped a bad port.

`SleepCycleAlgorithm.getSmartAlarmTimeUTC` has three paths. On detecting light
sleep it rings at `random.nextInt(span)`. On detecting deep sleep it guesses the
next light moment by adding a hard-coded 1.5 hour cycle. When neither fits it
calls **`fakeSmartAlarm`**, named that by its own author, which returns a
uniformly random time in the window **using no sleep data at all**. Two of three
paths are coin flips and the third is fixed arithmetic.

Porting that faithfully would mean putting `java.util.Random` in the code path
that decides when a person wakes up, and it could not be tested: a random ring
time cannot be diffed against anything.

So orb rings early **only on evidence**, and otherwise rings exactly when asked.
The evidence is the reference's own `isUserAwakeInGivenDataSpan`, which is
deterministic and is the one part of that file not built on a coin flip. Its
thresholds are used unchanged: any kick above 5, or any burst longer than 9
seconds, or two minutes above 4500 milliG.

The guarantees, each with a test:

- never earlier than 30 minutes before the set time
- **never later than the set time**, whatever happens
- unchanged without evidence, and no data counts as no evidence

An alarm is a promise, and guessing against it is how smart wake earned its
reputation. A smart alarm whose motion lookup fails is treated as an ordinary
alarm, which is the safe outcome.

This is a **deliberate divergence**: orb will not reproduce the reference's ring
times for smart alarms, because those times are random. Flagged in the code.

Untested against hardware: the account has never had a smart alarm (0 of 1), so
the path has unit tests and no live run.

### OTA: implemented, and armed by hand only

**An earlier version of this note said OTA should never be built, because no
firmware image would ever exist. That premise was wrong.**
`full-instructions/sense/firmware/kitsune-4513/` rebuilds Sense firmware
**byte-for-byte**: `kitsune.bin`, SHA1 `0c5f639e1290df0e3a5f8641d670923ed71a5e63`,
146,864 bytes, identical to the on-device dump and the official 1.9.2 release.
If firmware can be built, it needs a way to reach the device, and that is OTA.

The reference decides OTA from feature flags and device groups. That suits a
fleet and is exactly wrong here: a flag flipped for a group is an update nobody
deliberately aimed at this device, and there is one device and no spare.

So in orb an update is a **row in `firmware_updates` naming the device**, and
offering it requires `armed = true`, which defaults to false. Inserting a row is
preparation; arming it is the decision. No row means no OTA, which is what every
device gets unless somebody deliberately changes it.

`internal/ota` holds the gates, and **every one can only refuse**:

    not armed                    the default
    device not on from_version   also how a finished update stops re-offering
    already on to_version
    digest missing or not 20 bytes
    file size missing
    no host or url
    uptime not reported          unknown is refused, not assumed
    up less than 20 minutes      never hand an image to a boot loop
    outside 02:00-05:00 local    it is also the alarm clock

Sixteen cases are pinned in tests, each breaking exactly one thing from a
configuration that would otherwise be offered.

**Success is inferred, never acknowledged.** The device does not confirm an
update; it comes back running a different version. `from_version` stops matching
and `completed_at` is set. That is the whole completion protocol.

Verified against the live device in three states: no row (nothing logged, the
normal case), an unarmed row (refused), and an armed row outside the window
(refused with the reason, `offer_count` still zero). No image has been offered
to real hardware.

### The defect that verification found

The first version nested OTA inside the alarm path. `syncResponse` returned
early when a device had no enabled alarms, **so a device with no alarm could
never be offered firmware**. Alarms and firmware share only the zone and the
response, and they are now computed independently.

It was invisible in tests and obvious the moment the log stayed empty with an
armed row present. Building the observability into the path is what surfaced
it: a silent refusal would have looked identical to a working gate.

The reference is not serving OTA either:

    ota_file_settings          1 row, seeded at bootstrap on 2026-07-27
    ota_history                0 rows, no OTA has ever been performed
    firmware_versions_mapping  0 rows, no upgrade path configured

and no OTA appears in suripu-service's logs. So both stacks currently offer
nothing, and the device is content: it has been syncing against orb's empty
response for weeks in shadow, and drove its alarm from one on 2026-08-16.

### How an update is triggered: by hand, and only by hand

**Neither automatic nor app-initiated.** There is no schedule, no cohort, no
version-mapping table that decides on its own, and no endpoint a phone can call.
The only trigger is two statements against the database:

    INSERT INTO firmware_updates (device_id, from_version, to_version,
                                  host, url, sha1, file_size, notes) VALUES (...);
    UPDATE firmware_updates SET armed = true WHERE id = ...;

The insert and the arm are separate on purpose, so the thing that authorises a
flash is not the same act as the thing that describes one. After arming, the
device collects it on its next sync inside the window, having been up 20 minutes.

The reference has both of the paths orb omits. `OTAResource` exposes
`GET /v1/ota/status` and `POST /v1/ota/request_ota`, and `request_ota` sets a
`FORCE_OTA` response command which `ReceiveResource` reads as
`bypassOTAChecks = true`: **it skips the window and the uptime delay entirely.**
Neither endpoint appears in the two weeks of access logs orb's endpoint list was
built from, so the iOS app either does not surface it or it has never been used
here.

orb deliberately has no equivalent, because a bypass of the safety gates is the
one thing that should not be reachable from a phone in a pocket. If a button is
ever wanted, the split to preserve is:

- **may skip** the window and the uptime delay. Both exist to protect a sleeper
  and to avoid handing an image to a boot loop, and a deliberate press accepts
  those risks knowingly.
- **must not skip** `armed`, the digest, the file size, or the version match.
  Those are not about timing; they are about whether the image is real and aimed
  at this device. A button that skips them is a button that flashes anything.

That keeps the row as the authorisation and reduces the button to timing. It is
not built.

### Still missing from the device path

`push` has no orb equivalent, and it is not yet established whether push is
functional here at all (it needs Apple credentials). `sense-last-seen` is
covered inline by `TouchSense` on ingest, which is what that worker exists for.
`aggstats-generator` is deliberately not ported: see the note on `agg_stats`
having no readers.

## Air quality: the sensor that was there all along

2026-08-16. orb served four dials. The Sense has five, and the dust readings
had been ingested and stored since the beginning: `internal/edge` writes
`air_quality_raw` on every batch and the migrator carried `aqr` across. **The
data was never missing. It was never rendered.**

`getSensors` hardcoded four `add(...)` calls and the file's own doc comment said
"four dials", so nothing looked wrong from inside. The gap was only visible by
comparing against what the hardware actually reports.

Now implemented on both sensor endpoints, with the six-band scale copied from
`ParticulatesScale`. One detail that is easy to get backwards: **"Moderate"
carries condition IDEAL, not WARNING.** The first two bands are both green. The
top band is also CLOSED at 399.9, unlike temperature's open-ended "Hot", so a
reading above it matches no band and arrives at Hazardous through `classify`'s
fallback rather than through its range.

### The calibration is derived, not stored

The number in the database is **not** the number applied. The reference keeps a
factory-measured `dust_offset` and derives the delta at read time:

    delta = round(300 - dustOffset * 1.3)

This device's offset of 395 gives -213. orb stores the offset and derives the
delta for the same reason: storing the delta would leave a value in the database
that matches nothing in the reference and drifts the moment the formula changes.

**The rounding is Java's, and the two disagree on exactly this value.** Java's
`Math.round` rounds half toward positive infinity, so -213.5 becomes -213; Go's
`math.Round` rounds half away from zero and gives -214.

### Uncalibrated is not the same as calibrated to zero

The mistake made while writing this, caught by a test that had been written
first: reading the column with `COALESCE(dust_offset, 0)` treats an
uncalibrated device as one with offset zero, which derives a delta of **+300**
and silently inflates every reading. The reference applies **no delta at all**
when there is no calibration row. The column is nullable in Go as well as in
Postgres, and `TestUncalibratedIsNotOffsetZero` pins the difference.

With the real calibration migrated, both stacks now report **50.33, IDEAL**,
identical to the hundredth.

### Why it was invisible for so long

In the reference, a device with no calibration row makes
`CurrentRoomState.particulates()` null and `SensorViewFactory` **drops the
sensor from the response entirely**. No dial, and nothing on screen explaining
why. That is what hid air quality on this account until a calibration row was
added by hand.

orb diverges deliberately: a null offset means uncalibrated, and the dial is
still drawn. An uncalibrated number is more useful than a silent absence, and
the silence is what made this take months to notice.

`calibration` is now a migrator step of its own, so the offset survives a
rebuild rather than depending on somebody remembering to set a column.

## Phases

Each is independently useful; stopping after any of them leaves a working system.

| Phase | Work | Rough effort |
|---|---|---|
| 0 | TLS spike: prove Go can complete the Orb's handshake | half a day |
| 1 | Postgres schema, one-off DynamoDB to Postgres migrator | 2 to 3 days |
| 2 | Edge: TLS, ingest, time sync, messeji, DNS in Go | 3 to 5 days |
| 3 | Worker in Go; strip Java to a stateless algo service | 3 to 5 days |
| 4 | App API: the 25 endpoints | 5 to 8 days |
| 5 | Delete LocalStack, DynamoDB, Redis, Kinesis, 7 workers | 1 day |

Four to six weeks of evenings. This is a cutover, not a strangler: the Java
services are welded to DynamoDB and Kinesis, so there is no clean way to run old
and new against one dataset.

## Phase 0 RESULT: Go cannot serve the Sense. Keep a TLS terminator.

Run 2026-08-13. **Go's standard library cannot complete this handshake, and no
`tls.Config` setting changes that.** The rest of the plan is unaffected.

The spike did not need the device: `working-files/go_tls_probe.go` already
contained the real captured ClientHello, so it was replayed against a Go
listener offline, with no interruption to uploads.

The Sense ClientHello offers exactly one cipher, `0xC014`
(ECDHE-RSA-AES256-SHA), and carries **only** a `signature_algorithms`
extension. It sends no `supported_groups` (elliptic_curves) and no
`ec_point_formats`. RFC 4492 section 4 explicitly permits this and says the
server may then choose any curve it likes.

Go does not implement that fallback. Replaying the real hello and then adding
one extension at a time:

    sense as-is (sig_algs only)             REJECTED (alert 40)
    + supported_groups                      ACCEPTED (ServerHello)
    + supported_groups + ec_point_formats   ACCEPTED (ServerHello)
    sense as-is, CurvePreferences=P256      REJECTED (alert 40)

    tls: no cipher suite supported by both client and server; client offered: [c014]

Go *does* implement `0xC014` (`tls.CipherSuites()` lists
`TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA`, not even marked insecure) and it was
explicitly in `CipherSuites`. Go still eliminates it, because selecting an ECDHE
suite requires a mutually supported curve and the client named none. With the
only offered cipher eliminated, nothing is left and it sends handshake_failure.

Setting `CurvePreferences` does not help: it constrains which curves the server
will agree to, it does not supply a default when the client is silent. This is a
stdlib behaviour, not a configuration gap, so it cannot be worked around from
`tls.Config`.

Scope of the finding: this proves Go rejects at **cipher selection**. The
"ACCEPTED" rows show a ServerHello being emitted; they do not show a completed
handshake, because the replay client sends only a ClientHello and then closes
(hence the server-side `EOF`). That is sufficient for the decision, since the
Sense will never send `supported_groups` anyway.

### Consequence: adopt the fallback

Keep TLS termination in `sense_server.py` (tlslite-ng), reduced to a pure
TLS-to-plaintext proxy: accept on 443, forward verbatim to the Go binary, and
move all routing, signing and protobuf handling into Go. That drops it to well
under 100 lines and removes it as a place where logic can rot.

Target becomes **3 containers plus one host Python process**, rather than 3
containers. Everything else in this plan stands.

Rejected alternatives: forking `crypto/tls` (permanent maintenance burden for
one device), and putting stunnel or an old-OpenSSL nginx in front (another
container, no better than the ~80 lines of Python that already works).

## Original phase 0 rationale, kept for context

`sense_server.py` uses tlslite-ng precisely because modern TLS stacks refuse the
CC3200's handshake. Its settings:

    minVersion TLS 1.0, maxVersion TLS 1.2
    cipherNames  aes256, aes128
    macNames     sha, sha256
    keyExchange  ecdhe_rsa
    eccCurves    secp256r1, x25519
    certificate  dated 1950 (the device's clock starts ~70 years behind)

Go can be configured to match: `MinVersion: tls.VersionTLS10`, explicit
`CipherSuites` including `TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA` (present in
`tls.InsecureCipherSuites()` but still selectable), and recent Go versions may
require `GODEBUG=tls10server=1`. Configurable is not the same as "the Orb
completes the handshake", which is why this is a spike and not an assumption.

**If it fails**, the fallback is cheap and the rest of the plan is unaffected:
keep `sense_server.py` as a thin TLS terminator proxying to the Go binary, which
is exactly what it already does. The target becomes 3 containers plus one host
process.

The spike must be run against the real Orb. There is no substitute, because the
whole question is whether one specific ancient client stack is satisfied.
Testing it requires briefly taking port 443, which interrupts uploads for the
duration.

## The Orb's LED, and the light bug in the reference

2026-08-17. Found while auditing which feature flags were still doing anything.
Two flags on the DEVICE path were unset and mattered; almost everything else on
the app path is now dead, because the app was cut over to orb and orb has no
feature flipper at all.

### What was wrong

`new_room_condition` and `calibration` were both null, and `suripu-service` was
querying each of them 648 times a day. With `new_room_condition` off,
`ReceiveResource` takes the legacy branch and sends only `room_conditions`. The
V2 branch sends `room_conditions_lights_off` as well.

That second field is not cosmetic. The firmware keeps two colour slots in
`room_color[2]` (`led_cmd.c`), indexed by the bool argument to
`led_set_user_color`, and picks between them by whether the room is lit. Both
entries initialise to `0x00fe00`. So the lights-off colour was not merely unset,
it was **pinned to ideal-green** for the entire revival: the Orb reported a
perfect room every night no matter what the room was doing.

The scoring differs too, and in the opposite direction to what you would guess.
Legacy (`getGeneralRoomCondition`) weights temperature, humidity and dust into a
percentage and tolerates one bad reading. V2 is a hard any-one-fails over
temperature, humidity, light and sound, plus dust when calibrated. **V2 is
stricter**, so turning it on made this room's LED redder, not greener.

Both flags were enabled, `0.0||all` in ns `dev`. Confirmed four ways: the null
warnings stopped, the sync response grew 93 to 99 bytes (field 25 and field 27,
3 bytes each), the device's own log went from `lightson 2` to `lightson 3` plus
a `lightsoff 2` that had never appeared before, and both readings were traced
back to the sensors driving them.

### The bug

`CurrentRoomState.fromRawData` calibrates temperature, humidity, dust and audio
through `DataUtils`, then passes the **light count straight through** to a
classifier whose thresholds are in lux. `DataUtils.convertLightCountsToLux`
exists and is never called.

The alert threshold is 8 lux and raw counts run to the thousands, so the
reference's lights-on condition is ALERT essentially always. It survived for
years because the legacy scoring ignored light entirely; the bug only becomes
reachable the moment `new_room_condition` is switched on. At 13:29 on the day it
was found, a raw count of 2156 was a real 8.2 lux room, and both the correct and
the incorrect reading happened to alert, which is exactly the sort of
coincidence that keeps a bug alive.

The lights-OFF slot is immune, because `getRoomConditionV2LightOff` forces light
to IDEAL before classifying. The slot that was broken now works; the slot that
worked is now wrong.

### What orb does

`internal/roomstate` is a new package holding the five scales, the calibration
arithmetic and the V2 rule. It was moved wholesale out of `api/sensors.go`
rather than copied, because the app's dials and the Orb's LED are the same
judgement about the same room and two copies drift. `api/sensors.go` keeps local
aliases onto it so the verified call sites there did not have to be rewritten.
`internal/edge` sets both fields on every sync response, computed from the LAST
sample in the batch, matching the reference's `i == batch.getDataCount() - 1`.

**Deliberate divergence, the second one after Air Quality always being shown:
orb converts light to lux and the reference does not.** So orb's lights-ON
condition will disagree with suripu's for as long as both run, and apidiff will
show it. `TestDarkRoomIsNotAnAlert` in `internal/roomstate` is the guard; if it
ever fails the Orb glows red all night and nothing else says why.

**One deliberate omission**: orb does not send `lights_off_threshold`, a 3-byte
difference. It carries the calibration row's `lights_out_delta`, orb has no
column for it, and the only value in play is 100, which is already the
firmware's compiled-in default (`light_off_threshold`, `commands.c`). Worth
revisiting only if a device ships with a different delta.

This closes a cutover blocker that was not on the list: before this, orb's
`syncResponse` set only `Files`, `Alarm` and `RingTimeAck`, so cutting the
device over would have dropped LED colouring entirely, both slots.

## Corrections were stored, acknowledged, and then discarded

2026-08-17. Found by using the thing: a wake time was dragged to 08:35 in the
app and the screen came back showing the algorithm's original 07:51.

Nothing was broken at the app end, and nothing failed. The feedback row was
written, `applyCorrection` rescored the night synchronously, and the log said
`scored night account=1 date=2026-08-16 algorithm=VOTING feedback=1`. The
correction was carried all the way into the algorithm and then dropped on the
floor.

### Cause

Feedback has two jobs and orb-algo only did one of them.

`Server.java` computed `feedbackChanged` and passed it to
`getTimelinePrediction`. That is the LEARNING half: it lets a correction train
the ONLINE_HMM model for future nights. It does nothing at all to the night in
front of you.

The DISPLAY half is a separate call the reference makes at
`InstrumentedTimelineProcessor:641`:

    reprocessedEvents = feedbackUtils.reprocessEventsBasedOnFeedback(
            SleepPeriod.Period.NIGHT, feedbackList, result.mainEvents.values(),
            result.extraEvents, timeZoneOffsetMap);

and then reads its four main events out of `reprocessedEvents.mainEvents`
rather than out of the algorithm's result. `Server.java` read straight from
`res.get().mainEvents`, so the corrected time never reached the answer.
`FeedbackUtils` was present in the jar orb-algo compiles against and was simply
never called.

**This is a whole feature that was silently absent rather than a wrong number.**
Worth noting how it hid: the endpoint returned 200, the feedback table filled up
correctly, a rescore genuinely ran, and the model genuinely learned. Every
observable signal except the one the user looks at said it had worked. There are
five older feedback rows in the table from before this was found, none of which
ever moved anything.

### Fix

`Server.java` now reprocesses before reading the events, and passes the
reprocessed map on to `Timeline.populate` so the SEGMENTS agree with the labels
too. Taking the times from feedback but the segments from the original events
would draw a timeline whose sleep depth contradicted its own markers.

`reprocessEventsBasedOnFeedback` also enforces the intended ordering, so a wake
time dragged past getting out of bed is reconciled rather than accepted.

With an empty feedback list it is a no-op: `eventsByType` starts empty, every
algorithm event passes `checkEventOrdering` against the accumulating map and is
inserted unchanged. Uncorrected nights score exactly as before.

The timezone map construction moved to `Mapping.timeZoneOffsetMap`, shared by
the segment rendering and the feedback reprocessing. Those two have to agree
about what local time "08:35" means, and the fixed-offset-ID subtlety recorded
there had already cost a day once.

### Verified

    PATCH /v2/timeline/2026-08-16/events/WOKE_UP/1786967460000
    {"new_event_time":"08:35"}

returned the corrected timeline in the PATCH response itself, wake 07:51 to
08:35, score 72 to 76, duration 418 to 462 minutes. A subsequent GET agreed, so
it persisted rather than being rendered once.

### Still open

The night's recompute window (`DayEndsAtHour`, 12:00 local) closes 36 hours
after the night starts. A correction made after that still writes feedback and
still triggers a recompute, because `NightsNeedingTimeline` matches on feedback
newer than the timeline. But a correction made just BEFORE the window closes can
leave `timeline_events.updated_at` marginally newer than the feedback row that
caused it, which makes both clauses false and strands the night. Re-submitting
the correction clears it. Not yet fixed; the honest fix is for the rescore to
stamp `updated_at` from the feedback rather than from `now()`.

## Enabling `calibration` exposed a sensor-ordering difference

2026-08-17, found by apidiff immediately after the two LED flags went on.

`GET /v2/sensors` returned the same five sensors from both stacks in different
ORDER: the reference gives TEMPERATURE, HUMIDITY, LIGHT, PARTICULATES, SOUND and
orb gave PARTICULATES last. The app reads this array positionally in places, so
the order is part of the contract rather than a nicety.

**It was invisible until today and could not have been caught earlier.** With
`calibration` off, `CurrentRoomState.particulates()` is null and
SensorViewFactory drops the sensor from the response entirely, so the reference
returned four sensors and orb returned five. There was no shared fifth element
to disagree about. Turning the flag on gave both stacks the same set for the
first time and the ordering difference appeared in the same apidiff run.

orb now emits PARTICULATES before SOUND, matching the reference. The Air Quality
divergence itself is unchanged and still deliberate: orb draws the dial even
when uncalibrated, where the reference omits it.

Worth drawing the general lesson: a divergence that hides behind a disabled
feature flag is not absent, it is queued. Re-run apidiff after enabling anything.

## Forcing a recompute of a settled night is not free

2026-08-17. Learned by doing it and making a night worse.

`/v2/timeline/2026-08-13` differed from the reference by two fields, so its
`timeline_events.updated_at` was set back to force the worker to rescore it
through the newly fixed feedback path. The rescore succeeded, applied the
feedback correctly, and produced a WORSE answer: the night flipped from
`ONLINE_HMM` to `VOTING` and the divergence went from two fields to nine.
Total sleep moved 430 to 447 minutes, times awake 1 to 2, and the event count
26 to 28.

**Nothing was wrong with the rescore. The model state had moved on.** When
08-13 was first scored, ONLINE_HMM could produce all four main events. It no
longer can: it holds a scratchpad SLEEP model only and returns
`MISSING_KEY_EVENTS`, so the chain correctly falls through to the VOTING
fallback. A stored timeline is a record of what the algorithm knew THEN, and
recomputing it replays today's model against an old night.

This is the real reason the `DayEndsAtHour` window freezes a night rather than
recomputing forever, and it was not obvious from the code: the freeze reads like
an efficiency measure and is actually protecting the answer.

Practical rules:

- Do NOT bump `updated_at` to force a rescore of a night outside its window
  unless you are prepared to lose the stored answer. There is no undo: the raw
  pill and sensor data survive, but the derived timeline is overwritten and the
  old model state that produced it is gone.
- A correction submitted through the app is safe and is the supported path. It
  rescores too, but it is the user asking for a new answer rather than a
  maintainer replaying an old one.
- When comparing an OLD night against the reference, expect disagreement that is
  model-state drift rather than a port defect. The two stacks keep independent
  model state (Postgres `hmm_models` versus DynamoDB `online_hmm_models`) and
  have been diverging since the cutover. Compare recent nights.

08-13 was left as it is. Restoring it would mean surgery on the model table to
pin an older model, which risks every future night to repair one historical one.

## The timeline's air quality was computed without the dust calibration

2026-08-17, the third and last thing enabling the `calibration` flag exposed.

`full-instructions/infrastructure/orb-algo/src/Mapping.java` built every `CalibratedDeviceData` with
`Optional.<Calibration>absent()`, so `particulates()` converted raw dust counts
with no per-device offset. On this Sense the offset is -213 counts, enough to
report the timeline's particulates condition as WARNING where the reference said
IDEAL, and because `ENVIRONMENT_IN_SCORE` is on, enough to move the night's
score by a point.

Like the sensor ordering, it could not have been seen before today: with
`calibration` off the reference had no offset to apply either, so both stacks
were uncalibrated and agreed.

### Fix

The offset now travels with the request, because the algorithm service has no
database:

- `store.NightData.DustOffset`, read once per night from the paired Sense
  rather than joined per sample.
- `timeline.Request.DustOffset`, a POINTER. An offset of zero is a real
  calibration deriving a delta of +300 and is not the same as no calibration;
  a primitive would silently turn the second into the first. `Json.Request`
  uses a boxed `Integer` for the same reason.
- `Mapping.calibration(req)` builds the `Calibration`, and `Mapping.sensors`
  takes it. The sense id passed to `Calibration.create` is a placeholder:
  nothing branches on it, and `DataUtils` uses it only to name a device in one
  error log line about an implausible density.

### Verified

2026-08-13 and 2026-08-16 both rescored from 76 to 77, matching the reference,
with particulates moving WARNING to IDEAL. On 08-16 nothing else moved: same
algorithm, same sleep and wake times, same duration, same times awake, and the
person's 08:35 wake correction preserved. That night is the good check, because
it isolates the dust change from everything else.

2026-08-13 still differs from the reference on seven event-shaped fields. That
is the model-state drift described above, not this bug.

## A night that could never be satisfied

Found 2026-08-18 while reading logs. Every worker pass, all day, forever:

    msg="scoring nights" count=1
    msg="skipping night with no motion" account=1 date=2026-08-09

Harmless in effect (one query and one skip per 15 minutes) but it is a real
disagreement between two definitions of the same thing, and the churn was the
only visible symptom of it.

### Two answers to "which night does this sample belong to"

`NightWindow` says a night runs 20:00 local to 12:00 local the next day. That is
a 16 hour window, so the eight hours from 12:00 to 20:00 belong to **no night at
all**. `LoadNight` selects on that window, so it is the definition that decides
what the algorithms actually see.

`NightsNeedingTimeline` asked a different way. It derived the night from every
pill sample as `(local_ts - 20 hours)::date`, which has no concept of a gap: it
maps an afternoon reading onto the *previous* night, which that night's window
then excludes.

For 2026-08-09 the two disagreed completely. All eight of its samples were
afternoon movement on 08-10, between 13:32 and 14:08 local:

    ts (UTC)                 local    in night 08-09's window?
    2026-08-10 17:32:39      13:32    no  (window closed at 12:00)
    ...
    2026-08-10 18:08:40      14:08    no

So the night appeared in the work list because it had samples, loaded zero
motion because none were in the window, was skipped without writing a timeline,
and appeared again on the next pass. There is no state that ends the loop: the
`e.account_id IS NULL` test can only be satisfied by a row that only a
successful score would write.

The 08-09 case is the pure one, but the mis-attribution is routine. Every night
in the sample had afternoon readings credited to the night before it, 23 of them
on 08-14. They were invisible because those nights had plenty of real in-window
data and scored anyway.

### Fix

Filter the samples to the hours a night actually covers, before bucketing them:

    WHERE EXTRACT(hour FROM local_ts) >= $1   -- DayStartsAtHour
       OR EXTRACT(hour FROM local_ts) <  $4   -- DayEndsAtHour

The `(local_ts - 20 hours)::date` arithmetic is correct for both real ranges and
is kept; only the dead zone needed excluding. The local timestamp is now derived
once in its own CTE and the zone dropped with `AT TIME ZONE 'UTC'`, so the dates
and hours taken from it are the sleeper's rather than the connection's. The old
form leaned on the session `TimeZone` being UTC, which it is, and which this
query should not have to depend on.

### Verified

Both forms run against the live database. Old returns 2026-08-09, new returns
nothing, and a full outer join of the two night sets differs by that one row:
every real night survives.

Worth stating plainly: this changes which nights are *considered*, not how any
night is *scored*. No stored timeline moves.

## The insight card art is gone, and this is the search so nobody repeats it

The Insights tab shows an empty grey box above every card. The art was never in
the app: orb synthesises three URLs per card from the lowercased category, the
same convention as the reference's `InsightsDAODynamoDB.S3_BUCKET_PATH`, and the
app fetches them directly.

### What the bucket does now

    s3.amazonaws.com/hello-data/insights_images/...   301 PermanentRedirect
    hello-data.s3.amazonaws.com/insights_images/...   403 AccessDenied
    hello-data.s3.amazonaws.com/                      403 AccessDenied

Path-style addressing was retired by AWS, and the bucket behind the modern name
is private. Not a URL to repair: content we no longer have access to.

### Where the art is not

Searched **every object on every ref of all 128 repositories** in
`github-backup/` and `infrastructure/` (`git rev-list --objects --all`), not
working trees. A working-tree grep is what produced the earlier wrong note, and
the same mistake was made again here before this sweep.

| searched | result |
|---|---|
| paths matching the 11 category names, any image extension | only sensor icons and hardware schematics |
| any image path containing "insight" | only tab-bar and pre-sleep icons |
| `insights_images` in file contents | only `InsightsDAODynamoDB.java` and `create_info_insights.sql`, both consumers |
| archives, `.car`, `.ipa`, `.apk`, design files | nothing relevant |
| `Assets.car` from a shipped `Sense.app` (`mobile-automation`) | extracted with `assetutil`; no card art, confirming it was always remote |
| shipped `app-debug.apk` | icons only |
| `sharing`, the public insight web page | uses `hero.phone_3x` from the API, so no local copy |
| `Nonsense.apk/assets/images.manifest.json` | the 11 category names and their URLs, no bytes |
| Wayback Machine | not archived; S3 asset URLs rarely are |

The manifest is the only surviving artefact and it lists names only:
`AIR_QUALITY, GENERIC, HUMIDITY, LIGHT, PARTNER_MOTION, SLEEP_DURATION,
SLEEP_HYGIENE, SLEEP_QUALITY, SOUND, TEMPERATURE, WAKE_VARIANCE`.

### One find worth knowing

`SleepModel/Main.xcassets/cardLoadingState.imageset/defaultCard` existed in
`suripu-ios` history as the placeholder behind a loading card. It was deleted
before the snapshot we build from and nothing references it, so the grey box
today is just the image view's own background.

### Replaced, 2026-08-19

Dropping the field was never an option: `HEMInsightCellBaseHeight = 235.0f` is
fixed and includes the image area, so an absent image leaves the same empty box.
Making the art optional means changing the cell layout, not the payload.

So orb draws and serves its own. `scripts/insight-art/generate.py` renders 11
banners at three densities, deliberately abstract (a gradient, a soft highlight,
a few sine bands shaped per category) because a wrong photograph reads as a bug
where a coloured field reads as a design. They are embedded with `go:embed` in
`internal/api/insightart`, which keeps a deploy at one file and costs about 2 MB
of binary, and served from `/v1/insights/images/` unauthenticated, since a
`UIImageView` fetch carries no token and this is how the app fetched from S3.

Two things that are easy to get wrong here:

- **The URLs must be absolute.** The app hands the string straight to
  `[NSURL URLWithString:]` with no base, so a relative path silently yields no
  image. orb builds the origin from the request's `Host`, honouring
  `X-Forwarded-Proto`/`-Host`, rather than from configuration: it has no idea
  what address the phone reached it on, and this survives a LAN address change
  without anyone editing a constant.
- **An unknown category falls back to `generic`** rather than to a 404. The
  reference let it 404, which was invisible while every card 404'd anyway. Now
  that the rest load, one broken card would look like a defect.

The route is a subtree, which it must be to serve files, so it is covered in
`routing_test.go` per the trap in STATE.md. Note that registering
`/v1/insights/images/` makes net/http add an implicit redirect for the bare
`/v1/insights/images`; that is ours and suripu serves nothing there.

## A second insight generator, and the rule that had to land first

Once the banners loaded, the Insights tab became a surface worth looking at, and
it had two cards on it: the welcome card and one WAKE_VARIANCE from 2026-08-15.
That is not a fault. There was one generator and it is deliberately weekly. But
one card a week is thin, so on 2026-08-20 a second generator was added.

### The rule had to land first

`runInsights` looped every due generator and stored a card from **each**, with no
break. Invisible with one generator, and wrong the moment there were two: due-ness
comes from what is stored, so on a fresh account every generator is due at once
and the first pass after a deploy would write the whole set, all stamped the same
minute. A feed that arrives in a clump is one people stop reading, which is the
exact failure the package comment already warned about.

The comment in `internal/insights/insights.go` even claimed the code "takes the
first card produced". It did not. One of the two was wrong and it was the comment.

Now: at most one card per account per pass, first generator with something to say
wins. Nothing starves, because a generator that fires is not due again for its own
interval, so the next pass falls through to the next one. The runner-up waits one
`InsightInterval` (6 hours), not a week.

The per-account body moved to `insightPass`, taking a narrow `insightStore`
interface, purely so the rule is testable without a database. The rule is one
`break` in a loop containing three `continue`s, which is precisely the thing that
comes back when somebody rearranges the error handling, and the symptom would be
a clumped feed rather than a failing test.

Note which error path keeps its `continue`: **a failed write is not the account's
one card.** Turning that into a break would silence the feed for a whole interval
on a transient database error.

### A trap found by writing the fake

The fake generator in the new test returned a `Card` with no `Timestamp`. Due-ness
is `last.IsZero()` and `LastInsightAt` reads `max(timestamp)`, so a card stored
with the zero time is indistinguishable from a category that has never fired: the
generator becomes due on every pass and quietly fills the feed. Every real
generator sets the field, and the field is a copy of an argument they were already
handed, so the only way to get it wrong is to write a new generator and forget.
`insightPass` now stamps a zero timestamp with the pass's own, and there is a test.

### SLEEP_DURATION

Part port, part revival, and it matters which half is which.

The recommendation table is a straight port of the reference's
`SleepDuration.getSleepDurationRecommendation`: it is cited to a published expert
panel, it decides the bands, and **the same table already drives orb's sleep score
through the algorithm service**, so changing it here would put this card and the
score beside it into disagreement. The order of its tests is load-bearing: age 0
is checked FIRST and means "no birthdate on file", so it must not fall through to
the preschooler branch and tell an adult with a blank profile to sleep thirteen
hours. There is a test named after exactly that.

The card is new. The reference never shipped a recurring sleep duration insight;
`SLEEP_DURATION` exists there only as the category on a static welcome tip, and
the table is consumed by the score and by `SleepDeprivation`, never by a card. So
no original wording is claimed. The tone follows `SleepDeprivationMsgEN`, the
nearest real data card: short title, then two sentences, one with the number and
one saying why it matters.

Weekly, unlike the temperature card that was considered and dropped. The
distinction is whether the number can move on its own. A room held at a set
temperature reports the same thing every week and a card about it is a nag; a
week's sleep is genuinely different information each time, even when the band is
the same.

Two new store methods, `SleepMinutes` and `AgeYears`. `AgeYears` shares one SQL
expression with `LoadNight`'s, down to the divide-by-365 that moves a birthday by
a day in a leap year, because two answers to "how old is this person" that
disagree for a few days a year would be miserable to debug. `SleepMinutes` reads
`sleep_stats.sleep_duration_mins` rather than deriving it from `timeline_events`:
the difference between `wake_up_at` and `sleep_at` is a different number, since it
counts the time awake in the middle of the night that the algorithm subtracts.

Verified against live data on 2026-08-20: six nights, age 38, producing

> **Hello, well rested**. You averaged **7.4 hours** of sleep over the last 6
> nights, which is inside the 7 to 9 hours recommended for your age.

### Open: the category name

`insight_categories` maps `SLEEP_DURATION` to **"Sleep Tips"**, which the app shows
as the card's header. That name is imported from the reference's own database,
where it was chosen for a static tip card, and it reads oddly above a card full of
statistics. Left alone rather than hand-edited, because the table is imported
reference data and a manual `UPDATE` would drift from what the migration produces.
`SLEEP_HYGIENE` shares the same name. Changing it is a one-row decision, not a
code change.

### Why temperature is not here

The first version of this list led with a TEMPERATURE generator, on the grounds
that the room ran 76 to 78°F every night, just under the reference's 79°F alert.
**That was wrong, and the way it was wrong is worth recording.**

The numbers came from querying `sensor_samples.temperature` directly and reading
the raw integer as hundredths of a degree. It is not the number the app shows.
Sense measures its own board along with the room, and
`roomstate.TemperatureCalibrationCelsius = 389` (3.89°C, 7°F) comes off before
anything sees it. The user caught it by comparing against the app and a thermostat
schedule, which matched each other and not the analysis.

Calibrated, the room runs **69.6 to 71.7°F**, and the nightly setback is plainly
visible: 72.5°F at 22:00, falling to 70 by midnight, flat until 07:00, climbing
after. A weekly card telling someone their deliberately-set 70°F room is "a bit
warm" is a nag, not an insight, so the generator was dropped.

The same mistake had also declared humidity 37 to 41% (really 48 to 52%, since
that correction is a dew-point round trip and moves the number *up*), and had
called air quality and light "blocked on calibration" when both are calibrated and
working: `senses.dust_offset` is 395 and `CalibratedLux` puts the bedroom at 0.05
to 0.14 lux overnight.

**Read sensor data through `internal/roomstate`, or through the API. Never
straight off the table.** That package exists so this arithmetic has one home, and
every one of those four errors was the result of going around it.

One thing worth keeping from the exercise: orb's own `TemperatureScale` puts Ideal
at 15 to 19.99°C and Warm at 20 to 25.99°C, so 21.1°C lands in Warm by about a
degree. The nightly timeline will report `temperature: WARNING` every night at this
thermostat setting. That is the reference's scale, faithfully copied, and it is not
reporting a problem.

### The first card it wrote was indefensible, 2026-08-26

Deployed at 11:27 and the generator fired on the first pass, correctly, exactly
one card. It said:

> **Hello, a little short**. You averaged **6.9 hours** of sleep over the last 4
> nights. The recommendation for your age is 7 to 9 hours, so you are about **0.1
> hours short** of it.

The arithmetic was right: `(320+375+383+580)/4 = 414.5` minutes. The sentence was
not. **0.1 hours is six minutes**, and it was derived from four nights ranging
from 5.3 to 9.7 hours. Nothing in that data supports a claim at that precision,
and a card fussing over six minutes teaches people to stop reading cards.

Worth noting how it surfaced: the window caught four nights instead of seven
because the account was away for the weekend, which pushed the average close
enough to the boundary to expose wording that a comfortable average would have
hidden for months. **The band logic was never wrong. The rendering was**, and only
an unusual week made it visible.

Fixed with `gapPhrase`, which decides both the unit and whether the gap is worth
naming at all:

- under 15 minutes: no gap named, the sentence says only which side of the range
  the average sits on ("just under the 7 to 9 hours recommended for your age")
- under an hour: whole minutes, the unit a person thinks in at that size
- above that: hours, as before

Only the two bands ADJACENT to the recommended range can produce a small gap, so
only those two needed a no-gap variant; the outer two start a full hour beyond it.

The regression test sweeps both adjacent bands minute by minute and asserts no
gap is ever rendered in tenths of an hour. It has to strip the bolded average
first: that figure is legitimately decimal hours, and an average of exactly 10.0
trips a naive substring scan and reports a bug that is not there. The first
version of the test did exactly that.

## The night orb drove the Sense

2026-08-27 23:26 to 2026-08-28 07:40. **The device cutover, for real, across a
full night, with a scheduled alarm.**

    sudo SENSE_UPSTREAM_SENSE=http://127.0.0.1:8081 \
         SENSE_SHADOW=http://127.0.0.1:5555 \
         ./venv/bin/python sense_server.py

### The pre-flight that made it safe

The alarm was set in the app BEFORE the swap, and orb's answer checked while orb
was still in shadow and its reply still discarded:

    23:25:28  next alarm  at=2026-08-28T07:15:00-04:00  in_seconds=28171  sound=5

Independently: 23:25:28 to midnight is 2,072s, midnight to 07:15 is 26,100s,
total 28,172. orb said 28,171, one second low because `now` is read AFTER the
database lookups. That one-second lag is the countdown fix, so seeing it off by
one in that direction is confirmation rather than a rounding slip.

**This is the whole lesson of 2026-08-15 applied.** That evening orb computed a
perfect alarm, replied into a void and nothing rang. Verifying the computation
before it becomes the one that matters is the difference between the two nights.

### What the night proved

| | |
|---|---|
| Syncs, 23:27 to 07:40 | 494 |
| Alarm computations | 468 |
| WARN or ERROR | 0 |
| Alarm re-served after the ring passed | **0** |

The ring, from orb's own log:

    07:13:30  in_seconds=89
    07:14:31  in_seconds=28
    07:14:59.680  recorded sense state   <- audio starts
    07:15:00.693  recorded sense state

The Sense reports state only on an audio-state CHANGE, so that pair is the ring.
Against a set time of 07:15:00 it fired **within a second**, which is the
minute-floored countdown fix holding over a full night rather than a three
minute test.

The zero in the last row is the other fix proven on hardware. The re-trigger bug
made a passed alarm report as due for the rest of that minute, which on a
listening device is a second ring a minute after the first.
`TestPassedAlarmIsNotServedAgain` pinned it; this is the night that confirmed it.

The night also scored normally: 2026-08-27, **82**, 7h35m, 0 awakenings.

### Two predictions that were wrong

**The log did not go silent around the ring.** suripu's
`computePassRingTimeUploadInterval` pushes the next upload out to ring time plus
two minutes when a ring is close, and that silence was described beforehand as
expected. The device kept its ordinary one-minute cadence straight through
(07:14:31, 07:15:30, 07:16:31), because **orb does not implement that interval
adjustment**. Harmless, arguably better since it yields a continuous trace, but
it is an undocumented divergence and was stated as expected behaviour when it was
nothing of the kind.

**The LED was said to visibly change colour.** orb does fix the reference's
unconverted-light bug so the lights-ON colour should differ, but nobody has
confirmed it by eye. Unverified, not observed.

### A find for the audio recovery

The stored sense state names the file that played:

    "audioState": { "filePath": "/RINGTONE/DIG002.raw",
                    "volumePercent": 95, "durationSeconds": 120 }

So ringtones live at `/RINGTONE/` in four families, per `alarm.SoundPath`:
`DIGO00x` (ids 0-3), `DIG00x` (4-8, offset by three), `NEW00x` (9-14) and
`ORG00x` (15-18). Sound id 5 is `DIG002.raw`.

That makes the one surviving audio file anywhere, `kasetsu/audio/server/raw/
DIG005.raw`, **sound id 8**, and a genuine sample of exactly the format on that
SD card, with a decoded `.wav` sibling in the same repo's history. See
GOING-PUBLIC.md for the search that established nothing else survives.

### What was deliberately NOT done

Nothing has been switched off. suripu-service is still fed as the reversed
shadow, because that is what keeps rollback to one restart, and one clean night
is one night.

**The rollback now has a trap the 2026-08-16 rehearsal did not.** orb is the only
writer of alarms since `suripu-app` stopped, so DynamoDB's `alarm_info` is frozen
at whatever it held that day. Rolling the device back to suripu-service restores
connectivity and **silently loses the alarm**.

## The day the old system went off

2026-08-28. One clean night after the device cutover, the remaining twelve
containers were stopped and the target shape was reached: orb, orb-algo,
Postgres.

The plan in STATE.md said to wait for a second clean night. That was overruled
deliberately, and the reasoning is worth keeping: the second night was already
armed and running (the 07:15 alarm was set, orb was already primary), so waiting
bought nothing that the shutdown itself would not also test, and every stopped
container was one `docker compose start` from coming back.

### Backup first

`backups/2026-08-28-precutover/`, 66 MB, taken while every old service was still
running. Postgres logical dumps of all four databases plus roles, a fresh
DynamoDB dump (26 tables, 42,497 rows), and byte-level tarballs of the four
volumes.

**It was restore-tested, which is the only thing that makes it a backup.**
`orb.dump` was restored into a throwaway `postgres:14-alpine` and compared table
by table: 32 tables, 31 of the 32 row counts identical. `sensor_samples` was one
row higher on the live side, which is the Sense uploading a sample during the
dump, not a defect.

Worth being honest about what it is not: one copy, on the same Mac as the
original. It protects against a bad shutdown or a wrong `docker volume rm`. It
does not protect against losing the machine.

### The clock bug, which is the real story of the day

Repointing `time.hello.is` at orb meant orb would hand the Sense its clock for
the first time. Before doing that, `cmd/timecheck` was written to do one real
signed round trip and **decode** the reply.

orb answered **1958-08-10**.

    Java hello-time   raw transmit  -1279979476529042162  ->  2026-08-28 23:34:10
    orb (deployed)    raw transmit  +7943392557680033792  ->  1958-08-10 20:20:02
    orb (fixed)       raw transmit  -1279979479174742016  ->  2026-08-28 23:34:10

The cause, in `internal/edge/edge.go`:

    func toSigned64(v uint64) int64 {
        if v >= 1<<63 {
            return int64(v - 1<<63)   // clears the sign bit
        }
        return int64(v)
    }

Java's `TimeStamp.ntpValue()` returns a two's-complement `long`, which for any
present-day NTP timestamp is negative. Go's `uint64` to `int64` conversion
already does exactly that. Subtracting `1<<63` clears the top bit instead, which
is a different number.

Why it matters more than an off-by-one: the firmware reads **only**
`transmit_ts` (`sys_time.c:199`, `current_ntp_time = time->transmit_ts>>32`),
and the device stamps every sample it uploads against the clock it was handed.
One bad reply corrupts a whole night, silently, and the corruption looks like
sensor data rather than like an error.

Three things let it survive:

- **The unit test asserted only that the reply was signed.** It even had a
  `var pkt struct{}` stub sitting where the decode should have been. Any
  timestamp at all passed it.
- **Nothing decoded the value anywhere.** The epoch constant was right, so the
  code read correctly to a reviewer.
- **orb had never served time to the device**, so there was no live data to
  contradict it. This is precisely what the "built but never run for real" list
  in STATE.md exists to flag, and it earned its keep here.

Fixed to `return int64(v)`. `TestTimeResponseDecodesToNow` now decodes all three
timestamp fields and asserts they are negative and land on the right second; it
was verified to FAIL against the old code before the fix was restored, because a
regression test that does not fail on the original bug is decoration.

This is the **second** epoch bug in this one function. The first sent Unix time
where the device expected NTP and put it in 1956. If a third is ever suspected,
run `cmd/timecheck` before reading any code.

### A gap this exposed, and closed

A successful time sync was served completely silently. Now that orb owns the
clock, the single action that can corrupt a night had no log line at all, so a
recurrence would surface as bad data the next morning rather than as anything
observable at the time. `timeSync` now logs at INFO, with the decoded UTC time
rather than the raw fixed-point field, so a wrong year is readable at a glance.
It is rare enough (the firmware syncs every three hours) that this costs
nothing.

### The order, and why

Each step was verified before the next.

1. **The five Kinesis/DynamoDB workers.** Chosen first because they are
   downstream of Kinesis and in no request path, so the step could not affect
   the device. Confirmed beforehand that orb has no runtime dependency on
   DynamoDB, Kinesis or Redis: every reference in the Go source is in
   `cmd/migrate`, `cmd/compare`, or a comment.
2. **`sense_server.py` restarted** with all three hostnames on orb's `:8081` and
   `SENSE_SHADOW` unset. This is the step the clock bug was hiding in.
3. **`suripu-service`, `hello-time`, `messeji`.**
4. **`dynamodb`, `localstack`, `redis`.**

After each step: ingest continuing on the minute, zero errors and zero warnings
in orb's log, and for step 3 a re-run of `cmd/timecheck` (1 ms skew) and a live
messeji long-poll (204 at the ten second horizon) with the Java services already
stopped.

All thirteen app API endpoints were then exercised against orb. Twelve 200s and
one 403, and the 403 is correct: `/v2/alerts` returned 403 on all 170 calls in
the log, so answering anything else would invent a behaviour the app has never
seen.

### What is left running

`postgres` and `orb-algo`, plus orb as a LaunchDaemon and `sense_server.py`
under sudo. Down from sixteen containers and roughly 4.4 GB.

Nothing was deleted. Every container is stopped rather than removed and every
volume is intact, so rollback is `docker compose start` plus a `sense_server.py`
restart. **Do not `docker compose down -v`**: outside the backup, those volumes
are the only remaining copy of the pre-migration history.

The rollback trap from the 08-27 cutover still applies and is now the main
hazard: orb is the only writer of alarms, so DynamoDB's `alarm_info` is frozen.
Rolling back restores connectivity and silently loses the alarm.
