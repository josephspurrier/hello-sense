# Where this project is, and what is left

Written 2026-08-16. **Start here**, then read what the section you care about
points at. The other files in `knowledgebase/` are deep and assume you already
know the shape of things; this one is the shape.

## Where the code lives

**Since 2026-08-28 all source is in `full-instructions/infrastructure/`**, which
is a git repository. `hello-orb/orb/`, `hello-orb/orb-algo/` and
`working-files/sense_server.py` no longer exist; they moved there so the whole
deployment is one committable tree.

    full-instructions/infrastructure/
      orb/            the Go module, Dockerfile at its root
      orb-algo/       the Java shim and the algorithm models
      sense-server/   sense_server.py and ntp_pb2.py
      secrets/        gitignored: the APNs key and the device certificates

**Do not confuse `full-instructions/infrastructure/` with
`hello-orb/infrastructure/`.** The second is the old Java stack: the suripu
repositories, the compose file that ran the sixteen containers, and the
`suripu-app` fat jar that orb-algo still builds against. Same word, unrelated
directories, and paths in these documents are written in full for that reason.

What stayed behind in `working-files/` is the CC3200 tooling, the device keys
and captured data. None of it is committable.

**The suripu jar orb-algo runs on is patched, not stock.** Three fixes, and
until 2026-08-28 they existed only as uncommitted working-tree edits in
`hello-orb/infrastructure/suripu-app` and `hello-orb/infrastructure/suripu`,
where a stray `git checkout .` would have destroyed them without a trace. They
are now captured as patch files, with their upstream base commits and what each
is for, in `full-instructions/infrastructure/orb-algo/patches/`. Two of the
three are inert for orb-algo; that README says which and why.

## The goal, and the distance to it

Consolidate the revived Hello Sense backend from **16 containers plus 2 host
processes (~4.4 GB, four languages)** into **3 (~350 MB, two languages)**,
rewriting in Go where sensible and keeping the sleep algorithms in Java.

The end state is `orb` (Go, one process), `orb-algo` (Java, the algorithms) and
Postgres. Everything else goes.

**Today: 2 containers plus orb and `sense_server.py`**, down from 16. The
target shape is reached: `orb` (Go, one process), `orb-algo` (Java, the
algorithms) and Postgres.

Everything else was stopped on **2026-08-28**. Nothing was deleted: every
container is `docker compose stop`, every volume is intact, and a full verified
backup was taken first. See CONSOLIDATION-PLAN.md, "The day the old system
went off".

## What is running right now

    iPhone ──> :9999  orb (app API front door)
                      └── everything the app asks for.

    Sense  ──> :80/:443  sense_server.py (TLS terminator, runs under sudo)
                      └── all three hostnames ──> :8081 orb
                            sense-in.hello.is   ingest, alarms, files, state
                            time.hello.is       clock sync
                            messeji.hello.is    the long-poll

                         orb ──> :8090 orb-algo (timelines, scoring)
                         orb ──> :5432 postgres

There is no shadow any more. `SENSE_SHADOW` is unset, so suripu-service
receives nothing; it is stopped.

**Since 2026-08-28, orb runs as a container**, not as a LaunchDaemon. The
`is.hello.orb` plist is unloaded with `-w`, so it stays down across reboots.
Logs are `docker compose logs orb`, no longer `/usr/local/var/log/orb.log`.

Everything is defined in `full-instructions/infrastructure/`, which is a git
repository and the place this is maintained from now on. `make` lists the
targets; `make verify` is the one worth knowing.

**One real regression on macOS.** Docker Desktop is a user application: it
cannot start before someone logs in, and `AutoStart` is currently **False**. A
launchd daemon started at boot with no session. So a reboot or a power cut now
leaves the whole backend down until somebody opens Docker Desktop, and the
alarm does not ring. Fixing it needs Docker Desktop's "start on sign in" AND
macOS automatic login. On Linux the problem does not exist, because Docker is a
systemd service that starts at boot. See that README's "Supervision" section.

**Both paths are orb's, and there is no longer a second implementation of
either.** The app moved first, the device followed on 2026-08-27, and the last
three device-facing Java services went on 2026-08-28.

## Read next

| file | for |
|---|---|
| [RUNNING-ORB.md](RUNNING-ORB.md) | how orb is started, supervised, and switched into the device path. The three states table. Push. The app API front door. |
| [CONSOLIDATION-PLAN.md](CONSOLIDATION-PLAN.md) | the long record: every phase, every decision, every defect found and why |
| [GOING-PUBLIC.md](GOING-PUBLIC.md) | the plan after consolidation: dropping suripu, moving to the OCI VM, reaching the Sense over the internet, and why the firmware step is avoidable. **Steps 1 and 2 done 2026-08-27/28; the OCI move onwards not started.** |
| [DEVICE-PROTOCOL.md](DEVICE-PROTOCOL.md) | the Sense wire protocol, verified against real hardware |
| [TIMELINE-ALGORITHMS.md](TIMELINE-ALGORITHMS.md) | what the Java algorithms do and where the seam is |
| [LOCALSTACK-KINESIS.md](LOCALSTACK-KINESIS.md) | the KCL lease-table trap, which has cost a day twice |
| [CUSTOM-FIRMWARE.md](CUSTOM-FIRMWARE.md) / [FLASH.md](FLASH.md) / [WIRING.md](WIRING.md) | firmware, flashing, and how a pill was bricked and recovered |
| `full-instructions/pill/PILL_RECOVERY.md` | the pill revival, already done |

## What is done and actually verified

Verified means driven against the real thing, not just tested.

- **Ingest.** The Sense's protobuf edge, in Go, byte-verified against live traffic.
- **The app API.** ~31 endpoints, each response diffed against suripu by
  `full-instructions/infrastructure/orb/cmd/apidiff` rather than by reading Java. As of 2026-08-27 this is
  everything the app asks for; the fallback to suripu-app is now unused.
- **Timelines and scoring**, through `orb-algo`.
- **Ordinary alarms, across a full night.** A three minute test rang on
  2026-08-16; a scheduled 07:15 alarm rang on 2026-08-28 with orb primary, after
  494 syncs and 468 alarm computations overnight, zero errors, and **zero
  re-serves after the ring passed**.
- **The device path.** orb has been primary since 2026-08-27 23:26, and since
  2026-08-28 it is the only implementation running.
- **Clock sync and the messeji long-poll**, live since 2026-08-28. Both were
  diffed against the Java services on the wire before those were stopped:
  `cmd/timecheck` for the clock (which is how the 1958 bug was found), and a
  real long-poll returning 204 at the ten second horizon for messeji.
- **Air quality**, matching suripu exactly at 50.33.
- **Push notifications**, scheduled ones included. orb signs a JWT with a `.p8`
  and posts to APNS over HTTP/2, no SNS and no new dependencies. The worker
  schedules sleep-score and pill-battery sends on a 15 minute ticker. Nine sends
  between 2026-08-16 and 2026-08-26, eight of them unattended.
- **The insights feed.** Two generators, WAKE_VARIANCE and SLEEP_DURATION, both
  of which have written real cards (2026-08-15, 08-22 and 08-26). The
  one-card-per-account-per-pass rule is proven on live data, not only on fakes:
  the 08-26 pass had both generators registered, WAKE_VARIANCE was inside its
  week, it yielded the turn and exactly one card was written.
- **The launchd daemon**, including the restart-on-kill it exists for. Retired
  2026-08-28 in favour of containers; kept here because the plist still exists
  and is the fallback if Docker Desktop's login dependency proves unacceptable.
- **The containerised stack**, 2026-08-28. Built and proven on a throwaway
  project first (own volume, own ports): the 13 migrations reproduce
  production's 32-table schema exactly, diffed by name. Then cut over live, with
  the device back in 6 seconds and a steady 60 second ingest cadence after.

## What is built but has never run for real

Say so when reporting on these. The habit of labelling them is load-bearing.

- **Smart wake.** Unit tested, never live: this account has never had a smart
  alarm set.
- **OTA.** Every refusal path verified against the device; **no image has ever
  been offered.** Armed by hand only, via a row in `firmware_updates`. There is
  deliberately no app-facing trigger.
- **`LAST_3_MONTHS` trends.**
- **The LED's room conditions.** orb has been driving the LED since the
  2026-08-27 cutover, so this is no longer untested code; what has NOT been done
  is anybody confirming the colour by eye. orb fixes the reference's
  unconverted-light bug, so the lights-ON colour is expected to differ from what
  suripu showed. See CONSOLIDATION-PLAN.md, "The Orb's LED".

Scheduled push used to sit on this list and no longer belongs on it: nine sends
between 2026-08-16 and 2026-08-26, eight of them scheduled by the daemon, and
the 11:00 gate has announced a settled score every morning it has had one.

## The gaps, in the order worth doing them

### 1. ~~The device cutover, then the shutdown~~ BOTH DONE

orb has been primary since 23:26 on 2026-08-27 and drove a full night including
a 07:15 alarm. On 2026-08-28 everything else was stopped, in this order, each
step verified before the next:

1. The five Kinesis/DynamoDB workers (`sense-save`, `sense-last-seen`,
   `pill-save`, `smart-alarm`, `aggstats-generator`). These are downstream of
   Kinesis and in no request path, so this could not affect the device.
2. `sense_server.py` restarted with all three hostnames pointing at orb's
   `:8081` and **`SENSE_SHADOW` unset**.
3. `suripu-service`, `hello-time`, `messeji`.
4. `dynamodb`, `localstack`, `redis`.

**A full backup was taken first, and restore-tested.** See
`backups/2026-08-28-precutover/README.md`. It is one copy on the same Mac;
getting a copy off this machine is still open.

**Step 2 nearly corrupted a night, and the check that caught it is worth
keeping.** Repointing `time.hello.is` at orb meant orb would hand the device its
clock for the first time. `cmd/timecheck` does one real signed round trip and
decodes the reply: orb's answer came back as **1958**. `toSigned64` subtracted
`1<<63`, which clears the sign bit, where Java's `TimeStamp.ntpValue()` holds a
two's-complement long (Go's `uint64`->`int64` conversion already does exactly
that). The firmware reads only `transmit_ts` and stamps every sample against it,
so this would have been a silent whole-night corruption, and the second such bug
in this one function. It survived because the unit test asserted only that the
reply was *signed*. Fixed, with `TestTimeResponseDecodesToNow` verified to fail
against the old code. Keep `cmd/timecheck` and run it after any edit to the time
path.

**Rollback**, should it ever be wanted: `docker compose start <name>` for the
containers, then restart `sense_server.py` with the old environment. The trap
below still applies and is now worse.

**The rollback trap.** orb is the only writer of alarms, so DynamoDB's
`alarm_info` is frozen at whatever it held when `suripu-app` stopped. Rolling
the device back to suripu-service restores connectivity and **silently loses the
alarm**. Set a phone alarm if it is overnight.

### 2. Drop suripu-app. DONE 2026-08-27, pending a deploy.

orb fronts 9999 and proxied the rest to 9997. The last four endpoints the app
actually used moved on 2026-08-27: `/v2/insights/info/{category}`,
`/v2/sharing/insight`, `/v2/alarms/sounds` and
`/v2/sleep_sounds/combined_state`. **Nothing the app asks for reaches the
fallback any more**, so `suripu-app` can be stopped once the new binary is
deployed. See GOING-PUBLIC.md for what each one involved and the two defects
`apidiff` caught. `cmd/apidiff` compares before each move.

**The trap here has already bitten once**: a route ending in a bare slash is a
subtree in `net/http` and silently claims paths orb does not implement, answering
with a plausible wrong body instead of forwarding. See RUNNING-ORB.md, and add
siblings to `internal/api/routing_test.go` whenever you register such a route.

### 3. ~~Assess `messeji` and `hello-time`~~ DONE 2026-08-28

Both replaced and stopped. `hello-time` served one route; `messeji` was a
Clojure service plus Redis for what is now one polled query.

### 4. ~~Retire the datastores~~ DONE 2026-08-28

`dynamodb`, `localstack` and `redis` are stopped. Nothing read them: orb's only
references to DynamoDB, Kinesis and Redis are in `cmd/migrate`, `cmd/compare`
and comments, which was checked before stopping them rather than assumed.

Their volumes still exist and are in the backup. **Do not `docker compose down
-v`**: that would delete the only remaining copy of the pre-migration history
apart from the backup.

### 5. One-sided feedback can collapse an ONLINE_HMM model, and nothing stops it

Demonstrated twice: SLEEP on 2026-08-13, BED on 2026-08-15. `LabelMaker` builds
labels per output and takes a one-sided branch when only one end of a pair is
corrected, which can train an all-one-state path. The symptom is
`transitions for <OUTPUT> are []`, then MISSING_KEY_EVENTS, then a silent
permanent fall through to VOTING. See TIMELINE-ALGORITHMS.md.

The 08-15 collapse was reverted by hand on 2026-08-17. **The hole is still
open**: correcting only "got in bed" in the app, with no matching "got out of
bed", is enough to do it again, and the only symptom is that timelines quietly
get worse. The obvious guard is in orb's correction endpoint, which already has
somewhere to put it (`internal/feedback` holds the ordering and window rules):
when a BED-family correction arrives without its partner for that night, record
the current opposite event as confirmed so the label set is two-sided. Same for
SLEEP and WAKE_UP.

### 6. Known unknowns

- **A 16 minute mirror divergence** earlier in the week, cause never found. Five
  clean days since. Watch for a recurrence rather than hunting it now.
- **`aggstats-generator` is deliberately not ported.** `agg_stats` has no
  readers; the worker's only reads check whether it already wrote. Do not
  "fix" this without finding a reader first.
- **The reference's push dedupe table is named per year**
  (`push_notification_event_<year>`), and when absent the processor concludes
  "duplicate" and delivers nothing. Only matters if the old push path is ever
  wanted again.

### 0. The clock outage of 2026-08-28, and what it should change

Between **21:11 and 23:07** the Sense could not get a clock. **225 consecutive
failures**, and nothing alerted.

`sense_server.py` set `Host` to its upstream's address before forwarding.
`hello-time` never looked at Host (its resource was `@Path("/")`), so the
rewrite was harmless for as long as Java answered. orb routes `time.hello.is`
*by* Host, so the moment orb took that hostname over it 404'd every clock
request the device made.

**It was verified, and the verification was wrong.** `cmd/timecheck` sets the
Host header itself, so it proved orb correct in isolation while the real path
through the proxy was broken. Testing a component instead of the path is how
this hid.

Both ends are fixed: orb routes a clock request on the device id whatever Host
says (`TestTimeSyncSurvivesAProxyRewritingHost` fails without it), and
`sense_server.py` passes the origin Host through. **`sense_server.py` must be
restarted for its half to take effect**; orb's half is already live and is what
restored service.

Three things worth carrying forward:

- **Verify through the whole path, not the component.** `cmd/timecheck -target
  http://127.0.0.1:80/` goes through the proxy. That is the one that counts.
- **`unrouted device request` at WARN was the only symptom**, and only because
  that log line happened to exist. A device failing silently for two hours with
  a clean `make verify` is the shape of the next incident too.
- **Nothing watches for absence.** Every check here proves something happened,
  never that something *stopped*. The device's three-hourly clock request is
  exactly the kind of thing whose absence should page.

## Things that will mislead you

- **The launchd plist is no longer how orb runs**, but it is still on disk and
  still carries configuration (ports, the dead fallback upstream, four APNS
  variables). Loading it now would collide with the container on 8081 and 9999.
- **`launchctl list | grep orb` prints nothing even when the service is
  installed.** Use `launchctl print system/is.hello.orb`. The first is how a
  crash-looping daemon looks identical to an absent one.
- **`lsof` without sudo cannot see root-owned sockets.** orb and
  `sense_server.py` both run as root, so their ports look closed. Curl them
  instead.
- **The editor's diagnostics lag.** `go build ./...` is the authority.
- **A worker that idles by design and a worker that is dead look identical.**
  The push worker crash-looped for a day unnoticed for exactly this reason.
- **`infrastructure/*` are single-commit snapshots.** Real history is in
  `github-backup/*` (`suripu` has 9,093 commits and 1,326 refs). A working-tree
  grep once produced a confident, written-down, wrong conclusion that no push
  producer existed; it was in a service already running.

## Rollback levers

| to undo | do |
|---|---|
| orb as app API front door | set suripu-app's port back to `9999:9999` in `docker-compose.yml`, stop orb's `-api-addr :9999` |
| the containers | `cd full-instructions/infrastructure && make down`. Volumes are kept. |
| containers, back to the daemon | `make down`, then `sudo launchctl load -w /Library/LaunchDaemons/is.hello.orb.plist`. Postgres must still be running; start it with `docker compose up -d postgres`. |
| the device cutover | `docker compose start suripu-service hello-time messeji dynamodb localstack redis`, then restart `sense_server.py` with the old environment. **Read the alarm trap in gap 1 first.** |
| a retired worker | `docker compose start <name>`, or `--profile retired up -d <name>` for the two that have that profile |

## Credentials and secrets

- **APNS key**: `/usr/local/etc/orb/AuthKey_XXXXXXXXXX.p8`, `0600 root:wheel`.
  Source copy in `push-key/`. Team `YOURTEAMID`, topic
  `com.example.sensetest` (the **Debug/Dev** bundle id; Beta and Release
  build `com.example.sense` and would need the topic changed).
- The sandbox APNS host is correct while the app is a development build.
- `hello-orb` is **not a git repository**, so nothing here is at risk of being
  committed, and nothing is version controlled either.
