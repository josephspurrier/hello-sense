# The suripu patches

`orb-algo` runs on `suripu-app-0.6.0-SNAPSHOT.jar`, a 77 MB shaded jar built
from Hello's own source. **That jar is not pristine.** Three fixes were applied
before it was built, and they are recorded here because nothing else records
them: they lived only as uncommitted working-tree edits in two scratch clones,
where a stray `git checkout .` would have destroyed them silently.

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

## Rebuilding the jar

Not fully recorded, and worth reconstructing carefully rather than guessing at.
What is known:

- `mvn` is not installed on the host; the build ran in a Java 8 container.
- `s3://hello-maven`, the release repository these poms point at, is long dead.
  `settings.xml` in each repo stubs out the credentials it wants.
- `repo/` inside `suripu-app` is a small in-tree Maven repository holding
  `java-lame 3.98.4`, which is not on Maven Central.
- The output is `target/suripu-app-0.6.0-SNAPSHOT.jar` (shaded, 77 MB)
  alongside `original-suripu-app-0.6.0-SNAPSHOT.jar` (633 KB, unshaded).

The shaded jar already contains suripu-core and every transitive dependency,
which is why `orb-algo/Dockerfile` compiles against it with plain `javac` and
needs no Maven at all.
