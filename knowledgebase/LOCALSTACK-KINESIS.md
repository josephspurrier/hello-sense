# LocalStack Kinesis: it OOM-kills itself, and you must not hand-edit its state

Written 2026-08-13. Revised several times that day, because the first three
explanations were wrong and the first fix caused a second, worse outage. The
wrong turns are kept here on purpose.

## The symptom

The Room Conditions tab (far right in the iOS app) rendered axis labels like:

    Temperature   0deg  ..  6125082077234252000000000000000000000000deg
    Humidity      0%    ..  34028234663852886000000000000000000000000%
    Light         0.0lx ..  34028234663852886000000000000000000000000lx

with `- -` for every current value.

`3.4028234663852886E38` is **Float.MAX_VALUE**. The chart seeds its running
minimum at `Float.MAX_VALUE`, then folds in each sample. With zero samples the
seed is never displaced, so the sentinel is drawn. The temperature figure is the
same sentinel after the Celsius to Fahrenheit conversion.

The giant numbers are not a units bug, an overflow, or bad parsing. They mean
**the sensor query returned no rows**. Confirm server side rather than guessing:
`/v2/sensors` drops from ~2,050 bytes per response to **30 bytes**.

Note the symptom is identical whatever the underlying cause. It appeared twice
on 2026-08-13 for two completely different reasons, so seeing it tells you the
data stopped, not why.

## Why it dies

Kinesis in LocalStack is not LocalStack. It is a separate Node process,
`kinesis-local`, on port 58095 inside the container, which LocalStack proxies
to. It is [kinesis-mock](https://github.com/etspaceman/kinesis-mock).

It keeps the whole stream in memory and mirrors it to one file:

    /tmp/localstack/state/kinesis/000000000000.json

**Retention is decorative.** The streams declare 24 hours, and kinesis-mock
implements the retention *API* (`RetentionPeriodHours`,
`Increase`/`DecreaseStreamRetentionPeriod`, so `DescribeStream` reports a value)
but never expires a record. The only expiry-adjacent identifiers in its bundle
are `TRIM_HORIZON` / `AT_TRIM_HORIZON` / `FROM_TRIM_HORIZON`, which are shard
*iterator positions*, and `ExpiredIteratorException`, which is an iterator going
stale after minutes.

Do not rely on that bundle reading. The empirical proof is decisive: ours held
39,013 records spanning 2026-07-27 to 2026-08-13, **17 days under a 24 hour
policy**, 66 MB, nothing ever dropped.

On startup it parses that whole file into JavaScript objects. 66 MB of JSON,
mostly base64 payloads, inflates to about **2.1 GB resident**. The Docker VM has
7.65 GiB and the rest of the stack already commits 6.43 GiB, so the kernel OOM
killer takes the largest process, which is kinesis-local itself:

    Out of memory: Killed process 38750 (node)
      total-vm:2877916kB, anon-rss:2172892kB

LocalStack sees `received exit code -9` (SIGKILL) and restarts it. It reads the
same file, asks for 2.1 GB again, dies again. Loop.

It is a **kernel** OOM kill, not a Node heap error. Node heap exhaustion exits
134 with `FATAL ERROR: Reached heap limit`. Seeing -9 means the box ran out, not
that the program hit its own ceiling.

## Diagnosis traps

**The container reports healthy.** `docker ps` shows `Up 2 weeks (healthy)` and
`/_localstack/health` says `"kinesis": "running"`, because LocalStack reports
whether the provider is *enabled*, not whether the process is *alive*. Test it:
`aws kinesis list-streams`.

**`dmesg` lies by omission.** It showed 9 OOM kills, earliest three minutes old,
which reads as "this just started". The ring buffer had rolled and held about
two minutes. The authoritative record is the LocalStack log:

    docker logs hello-orb-localstack-1 | grep -c "Restarting process"

which showed **1,166 restarts, all exit -9, the first at 2026-08-10 23:34 UTC**.
Three days of flapping, not three minutes.

**Producers and consumers fail at different times.** The KCL needs
`DescribeStream` then `GetShardIterator` then `GetRecords` to succeed in
sequence; one failure triggers `ShutdownTask` and `SHUTDOWN: TERMINATE`, and it
does not come back on its own. `suripu-service` just retries a single
`PutRecord`, so it only needs one brief up-window. Ours: consumers died at
04:24 UTC, producers kept landing records until 11:37 UTC. The Orb went on
writing into a stream nothing was draining, leaving 128 sensor and 21 pill
records unconsumed.

## DO NOT hand-edit the state file under live consumers

This is the expensive lesson, and it cost more than the original outage.

The obvious fix looks safe: kill `kinesis-local`, swap in a state file with old
records removed, let it restart. It is not safe. **kinesis-mock does not
preserve its sequence-number high-water mark across a restart, and appears to
renumber records on load.** Every record written after the restart gets a
sequence number *below* what consumers have already checkpointed:

    checkpoint  : 49676810981906652374683055588626889042585179579366768642
    next record : 49676810981906652374683055569623784084063582794939367426
    delta       : -19003104958521596784427401216      <- NEGATIVE

Kinesis sequence numbers must increase monotonically forever. KCL asks for
everything after sequence X, receives records numbered below X, and refuses to
advance. The worker replays the old backlog, checkpoints back to the identical
value every time, then goes silent. Deleting the lease does not help: it replays
and stalls at the same boundary. Restarting the worker does not help either.

Symptoms of this specific corruption, none of which look like the real cause:

- The Orb is uploading fine, `POST /in/sense/batch` returning 200.
- Records are visibly arriving in the stream.
- `get-records` from the worker's own checkpoint returns records.
- The worker holds and renews its lease, `leaseCounter` incrementing.
- No exceptions anywhere.
- Nothing is written to DynamoDB, and the checkpoint never moves.

After three restarts the stream held several interleaved sequence generations
that were not even monotonic among themselves, and the two streams were in
opposite states relative to their checkpoints. There is no clean edit that
repairs it.

Ignore two red herrings that show up while chasing this:

- `SenseProcessorUtils: Querying dynamoDB. One or several timezones not found`
  fires on **every** message whenever `worker_kinesis_timezones` is off, even
  when the timezone was found. It only means "use the DynamoDB fallback", and
  that fallback reads `alarm_info`, which has `timezone_id: America/New_York`.
  Harmless noise.
- `Batch save failed, save N data using itemize insert` is a fallback path, not
  a failure to write.

## The safe procedure: full coordinated reset

The only reliable repair, and the only safe way to shrink the state. It works
because every consumer rebuilds its lease against a stream that starts empty,
so no stale checkpoint can outrank a new record.

    cd infrastructure/docker

    # 1. stop every consumer first, so nothing rewrites a lease mid-reset
    docker compose stop sense-save-worker pill-save-worker sense-last-seen-worker \
      smart-alarm-worker push-worker insights-generator-worker aggstats-generator-worker

    # 2. purge. TWO SEPARATE exec calls, see the pkill trap below
    docker exec hello-orb-localstack-1 pkill -9 -f kinesis-local/main.js
    docker exec hello-orb-localstack-1 rm -f /tmp/localstack/state/kinesis/000000000000.json
    # wait for it to come back with zero streams
    until aws kinesis list-streams --endpoint-url http://localhost:4566 >/dev/null 2>&1; do sleep 2; done

    # 3. recreate streams
    ./bootstrap-aws.sh
    # bootstrap does NOT create push_notifications; the push worker consumes it
    aws kinesis create-stream --stream-name push_notifications --shard-count 1 \
      --endpoint-url http://localhost:4566

    # 4. clear leases in ALL SEVEN tables, not just the two obvious ones:
    #    SenseSaveConsumerLocalDDB   PillSaveConsumerLocalDDB
    #    SenseLastSeenConsumerLocal  SmartAlarmWorkerLocal
    #    PushNotificationsWorkerLocal InsightsGeneratorLocal AggStatsGeneratorLocal
    #    delete each item by its leaseKey

    # 5. start the workers
    docker compose start sense-save-worker pill-save-worker ...

Cost: every unconsumed record. In our case four hours of daytime room
conditions.

**That hole is permanent and it reaches further than it looks.** The 2026-08-13
reset left `sense_data_2026_08` with nothing between 11:38 and 15:35 UTC, and
because DynamoDB is the source for everything downstream, the same four hours
are missing from every copy taken since, including `orb`'s mirror. It surfaced a
day later as a 238 minute gap that tripped `TimelineSafeguards` on the night of
2026-08-12, and the first guess was that the migration had dropped rows. It had
not: the source really is empty there. Before blaming a copy for missing data,
check the hourly counts in the dump:

    python3 -c "import json,collections;i=json.load(open('sense_data_2026_08.json'))['Items'];\
    print(collections.Counter(x['ts|dev']['S'][11:13] for x in i if '2026-08-13' in x['ts|dev']['S']))"

Nothing already in DynamoDB or Postgres is touched, and S3 survives
because only the kinesis *process* is killed, never the LocalStack container
(`normal4.base64`, `normal4ensemble.base64` and `hello-pvt.pem` all reported
"already exists" on re-bootstrap).

### The pkill self-match trap

    # WRONG: pkill -f matches the sh -c wrapper's own command line, kills the
    # shell, and the rm never runs. The state file survives and you conclude,
    # wrongly, that the purge did not take.
    docker exec c sh -c "pkill -9 -f kinesis-local/main.js; rm -f /tmp/.../state.json"

    # RIGHT: separate calls
    docker exec c pkill -9 -f kinesis-local/main.js
    docker exec c rm -f /tmp/.../state.json

Always verify the purge: `docker exec c ls /tmp/localstack/state/kinesis/`
should print nothing, and `list-streams` should return 0 streams.

## When it recurs

    68,320,907 bytes / 39,013 records over 17.3 days
      = 3.77 MB/day, 2,255 records/day
    fatal at ~66 MB (about 2.1 GB heap on load)

From an empty state that is roughly **17 days**, so expect trouble around
2026-08-30 and every couple of weeks after. Watch it:

    docker exec hello-orb-localstack-1 ls -la /tmp/localstack/state/kinesis/

Under ~10 MB is fine. Tens of MB means the clock is running.

Measured after the 2026-08-13 15:35 UTC reset: **2.2 MB at 17 hours**, which is
3.1 MB/day and tracks the estimate. The projection holds.

## Prevention: unsolved

`infrastructure/docker/trim-kinesis-state.sh` exists and **is unsafe**. Its
in-place edit is exactly the operation described above that corrupts sequence
numbers. It was written, tested, scheduled via a launchd agent, and then
withdrawn the same day when it caused the second outage. The launchd agent
(`com.example.hello-orb.trim-kinesis`) is unloaded. The script now
refuses to run without an explicit override. Do not re-enable it.

Why its test passed and the bug still shipped, worth remembering: the trim
dropped zero records and Kinesis was serving again in about a second, so it
looked clean. The regression only appears once *new* records arrive afterwards
and a consumer tries to advance past its checkpoint. **A test that does not
produce a new record after the restart cannot see this class of bug.**

Options, none implemented:

- Schedule the full coordinated reset above instead of an in-place trim. It is
  the procedure that actually works, at the cost of dropping unconsumed records,
  so it wants to run at a quiet hour and never mid-sleep-session.
- Give the VM more memory, or the stack less. 6.43 GiB of 7.65 GiB committed
  before Kinesis asks for anything is the real headroom problem.
- Replace kinesis-mock with something that honours retention.

Whatever is chosen, validate it by writing a record *after* the restart and
confirming a consumer advances past its old checkpoint. Nothing less proves it.

## Recreating a stream invalidates EVERY consumer's leases, not one

2026-08-16. `kinesis-local` had been crash-looping since 03:40 because its state
file was truncated to 0 bytes. The repair was to clear the state, restart
LocalStack and re-run `bootstrap-aws.sh` to recreate the streams.

That works, and it has a consequence which is easy to miss: **the recreated
stream has a fresh shard, and every KCL consumer still holds leases describing
the old one.** KCL then refuses to run, looping on

    KinesisClientLibIOException: Shard [shardId-000000000000] is not closed.
    SHUTDOWN: TERMINATE / Going to checkpoint / Checkpointed successfully

The fix is to delete the consumer's lease table in DynamoDB and restart it. The
lease table is named after the worker's KCL application name:

    sense-save            SenseSaveConsumerLocalDDB
    pill-save             PillSaveConsumerLocalDDB
    sense-last-seen       SenseLastSeenConsumerLocal
    insights-generator    InsightsGeneratorLocal
    aggstats-generator    AggStatsGeneratorLocal
    smart-alarm           SmartAlarmWorkerLocal
    push                  PushNotificationsWorkerLocal

**Do all seven.** The mistake made on the day was fixing only `sense-save`,
because that was the one whose failure was visible: the app's conditions tab had
gone blank, sense-save feeds it, fixing sense-save fixed the tab, and the
investigation stopped there. The other six stayed wedged for another hour, and
`pill-save` is the one that carries motion, so sleep data was still being
dropped while everything looked repaired.

The lesson generalises past Kinesis: **a shared dependency fails for every
consumer, so a fix verified through one symptom proves nothing about the
others.** Check the whole set, and check by asking each consumer whether it is
processing rather than by asking whether the visible symptom went away.

Quick health check across all of them:

    for w in sense-save pill-save sense-last-seen insights-generator \
             aggstats-generator smart-alarm push; do
      echo "$w $(docker logs --since 5m hello-orb-$w-worker-1 2>&1 \
                 | grep -c 'SHUTDOWN: TERMINATE')"
    done

Anything above zero is wedged.
