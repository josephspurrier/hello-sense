# The suripu patches

`orb-algo` runs on `suripu-app-0.6.0-SNAPSHOT.jar`, a 77 MB shaded jar built
from Hello's own source. **That jar is not pristine.** Four fixes are applied
before it is built, and they are recorded here because nothing else records
them: the first three lived only as uncommitted working-tree edits in two
scratch clones, where a stray `git checkout .` would have destroyed them
silently.

The jar itself is gitignored (too large, and not ours to redistribute), so
anyone who needs to rebuild it starts from upstream plus these.

## Upstream, and the exact commits they apply to

| Repo | Base commit |
|---|---|
| https://github.com/hello/suripu-app | [`1b1a1dd`](https://github.com/hello/suripu-app/commit/1b1a1dd07e1da66ed893eb23bde35a44021f8f10) "Fix logging and pass mapper", 2017-06-15 |
| https://github.com/hello/suripu | [`f0d1c13`](https://github.com/hello/suripu/commit/f0d1c139204eccd2c080d4c906f8afcc8c372055) "use hardware version to pick correct file_info DAO (#1929)" |

Hello shut down in 2017 and those repositories may not outlive this document.
There are full mirrors, history included, in `github-backup/`.

Each patch has been verified to apply cleanly to a fresh checkout of its base
commit, not merely to the tree it was extracted from.

## Applying them

```bash
git clone https://github.com/hello/suripu-app && cd suripu-app
git checkout 1b1a1dd07e1da66ed893eb23bde35a44021f8f10
git apply /path/to/patches/suripu-app/*.patch

git clone https://github.com/hello/suripu && cd suripu
git checkout f0d1c139204eccd2c080d4c906f8afcc8c372055
git apply /path/to/patches/suripu/*.patch
```

## What each one is for

### suripu-app/0001-postgres-jdbc-driver.patch

Bumps the PostgreSQL JDBC driver from `9.2-1004-jdbc4` (2013) to `42.7.4`.

`oauth_tokens` stores `access_token` and `refresh_token` as Postgres **UUID**
columns, and the 2013 driver cannot infer a SQL type for a `java.util.UUID`
bind. Every token request died with *"Can't infer the SQL type to use for an
instance of java.util.UUID"*, so nobody could log in. 42.7.x still targets
Java 8 and talks to both old and new servers.

**Inert for orb-algo.** orb-algo holds no database connection, and
`org/postgresql/Driver.class` is shaded into the jar but never loaded. This
patch mattered only while `suripu-app` served the app API; orb replaced it on
2026-08-27. Keep it anyway: it costs nothing, and it is what makes `suripu-app`
usable again if the rollback path is ever wanted.

### suripu-app/0002-honour-local-aws-endpoints.patch

Two AWS clients ignored their configured endpoints and went to real AWS.

- **S3** was hardcoded to `us-east-1` while every other client honoured a
  configured endpoint. It loads the timeline models at startup, so against a
  local stack it failed the boot outright. Path-style access is set too, because
  a local endpoint cannot serve virtual-host style bucket names.
- **Kinesis** read `kinesis.endpoint` only for the stream *names*; the client
  itself defaulted to real AWS and was rejected with
  `UnrecognizedClientException`. That silently killed both loggers built from
  it, `activity_stream` and `logs`. The writes are async, so nothing failed
  visibly: it errored on a callback thread after every single request.

**Inert for orb-algo**, which never runs `SuripuApp` and constructs no AWS
client at all.

### suripu/0001-clock-skew-tolerance.patch

Raises `SenseProcessorUtils.CLOCK_SKEW_TOLERATED_IN_HOURS` from 2 to 4.

The Sense buffers up to three hours of samples while it cannot reach the server
and flushes them all on reconnect. At the stock two hours, the older half of
every flush was discarded as too far from the server clock. Four hours covers a
full buffer with headroom.

**This one is in the code orb-algo links against**, and it is compiled into the
shipped jar: `javap -c` on `SenseProcessorUtils` shows `iconst_4`, not
`iconst_2`. orb-algo does not call it (it imports only
`algorithmintegration`, `db` and `models`), so a pristine jar would behave
identically for timeline scoring today. Rebuild without it and any future use of
suripu-core's ingest path quietly starts dropping buffered data again.

### suripu/0002-short-night-floor.patch

Lowers `TimelineSafeguards.MINIMUM_SLEEP_DURATION_MINUTES` from 180 to 120.

**This one changes what the app shows, and it is the only patch here that
does.** `checkIfValidTimeline` discards any timeline whose measured sleep is at
or under this figure (the comparison is `<=`, so exactly 180 also failed). The
result is not a shorter night or a lower score: every algorithm in the chain
returns `NOT_ENOUGH_HOURS_OF_SLEEP`, orb-algo answers `status=NO_RESULT`, and
with nothing previously stored to rebuild from, orb writes no `timeline_events`
row at all. `writeTimeline` renders a missing night as an empty 200, which the
app draws as "no sleep data recorded". A night that was measured correctly end
to end reads as a night the device missed.

Found on the night of 2026-09-10, a 04:15 to 07:35 night. ONLINE_HMM placed
going to bed at 04:15, sleep at 04:30, waking at 07:25 and getting up at 07:35,
which is what actually happened, and then threw it away over 175 minutes of
sleep. VOTING fell through to the same error. The floor is the reference's own,
so stock Sense did this too.

Two hours rather than something lower on purpose: the guard is really there to
reject noise, and the motion-count thresholds in the same class
(`MINIMUM_MOTION_COUNT_DURING_SLEEP_*`) already screen out an empty bed
independently of duration.

Note the constant is `public static final int`, so javac inlines it at every
call site. `InstrumentedTimelineProcessor` reads it, which is why rebuilding
changes two class files rather than one. orb-algo does not use that class (it
runs the chain itself, mirroring it), but the recompiled copy stays consistent
rather than carrying a stale 180.

Verify in a built jar with
`javap -p -constants -cp <jar> com.hello.suripu.core.util.TimelineSafeguards`,
which must print `= 120`.

## Rebuilding the jar

Reconstructed and run end to end on 2026-09-11 to ship
`suripu/0002-short-night-floor.patch`. `mvn` is not needed on the host; every
step runs in a Java 8 container against one shared local repository.

```bash
docker volume create hello-m2

# 1. suripu, the libraries. Installs 0.8.0-SNAPSHOT.
docker run --rm -v "$PWD:/src" -v hello-m2:/root/.m2 -w /src/suripu \
  maven:3.9-eclipse-temurin-8 mvn -B -DskipTests clean install

# 2. suripu-app, the shaded jar.
docker run --rm -v "$PWD:/src" -v hello-m2:/root/.m2 -w /src/suripu-app \
  maven:3.9-eclipse-temurin-8 mvn -B -DskipTests \
    -Dsuripu.version=0.8.0-SNAPSHOT -Dgaibu.version=1.0-SNAPSHOT \
    -Dtcnative.classifier=linux-x86_64 clean package
```

`gaibu` (`github.com/hello/gaibu`, `mvn install`, no flags) must be in the same
local repository first: `suripu-app` needs `is.hello.gaibu:*`, which only ever
lived on the dead S3. So must the two private jars vendored in `suripu/repo/`
(`dropwizard-mikkusu`, `suripu-jodatime`); copy them into the volume.

Why each flag:

- **`clean` is not optional.** Building over a `target/` from an earlier run
  crashes javac 8 inside error-prone's annotation-processing rounds with
  `IllegalStateException: endPosTable already set`, reported against
  `suripu-core` as a bare "Compilation failure" with no file or line. It looks
  like the patch broke something and it is only a stale build directory.
- **`-Dsuripu.version` / `-Dgaibu.version`**: the poms pin exact versions
  (`0.8.6142` and similar) that no longer exist anywhere. Overriding to the
  SNAPSHOT tip is what makes resolution succeed.
- **`-Dtcnative.classifier=linux-x86_64`**: `suripu-app` derives the classifier
  from `${os.detected.classifier}`, and no arm64 build of
  `netty-tcnative-boringssl-static:1.1.33.Fork19` was ever published.
- **`-DskipTests`**: the suites reach for AWS and a database.

Other things worth knowing:

- `s3://hello-maven`, the release repository these poms point at, is long dead.
  `settings.xml` in each repo stubs out the credentials it wants.
- `repo/` inside `suripu-app` is a small in-tree Maven repository holding
  `java-lame 3.98.4`, which is not on Maven Central.
- The output is `target/suripu-app-0.6.0-SNAPSHOT.jar` (shaded, 77 MB)
  alongside `original-suripu-app-0.6.0-SNAPSHOT.jar` (633 KB, unshaded).
- `maven:3.9-eclipse-temurin-8` has a native arm64 image, so none of this runs
  under emulation.

The shaded jar already contains suripu-core and every transitive dependency,
which is why `orb-algo/Dockerfile` compiles against it with plain `javac` and
needs no Maven at all.

### Checking a rebuild before trusting it

The jar is not reproducible (timestamps in `MANIFEST.MF` and the generated
`pom.properties` move every build), so compare it by content rather than by
hash. Against the jar it replaces, expect the entry count to be **identical**
and the changed entries to be only the build metadata plus the classes the
patch actually touches:

```bash
unzip -Z1 old.jar | sort > old.list && unzip -Z1 new.jar | sort > new.list
comm -3 old.list new.list          # must be empty
```

For `0002` that came to eight changed entries out of 34,983: `MANIFEST.MF`, the
five `pom.properties`, `TimelineSafeguards.class`, and
`InstrumentedTimelineProcessor.class`. An entry appearing or disappearing means
the dependency closure moved and the build is not the one described here.

### Deploying it

The jar is not in the build context. `docker-compose.yml` reaches it through a
named context, so replacing it is a copy plus an image rebuild:

```bash
cp suripu-app-0.6.0-SNAPSHOT.jar "$SURIPU_JAR_DIR"/   # keep the old one alongside
docker compose build orb-algo && docker compose up -d orb-algo
```

Rolling back is putting the previous jar back and repeating those two commands.
Nothing else in the stack restarts, and orb tolerates orb-algo being briefly
absent: the timeline job logs the failure and retries on its next pass.
