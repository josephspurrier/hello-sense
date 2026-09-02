# Running orb

How orb is started, supervised and switched into the device's path.

## The three states orb can be in

Which one you are in is decided entirely by how `sense_server.py` was started,
not by anything in orb.

| state | `sense_server.py` env | device acts on | suripu still fed |
|---|---|---|---|
| **shadow** (default) | `SENSE_SHADOW=http://127.0.0.1:8081` | suripu | yes |
| **primary** | `SENSE_UPSTREAM_SENSE=http://127.0.0.1:8081`<br>`SENSE_SHADOW=http://127.0.0.1:5555` | **orb** | yes, as shadow |
| suripu only | neither set | suripu | yes |

In shadow, orb ingests everything and **its responses are discarded**. That is
the trap that wasted an evening on 2026-08-15: orb computed an alarm perfectly,
replied into a void, and the Sense never rang because it was acting on suripu's
answer. Verifying a computation is not verifying an outcome.

The **primary** row reverses the shadow rather than removing it, so suripu keeps
receiving copies and the iOS app carries on updating from DynamoDB while orb
drives the hardware. Without that, the app's conditions tab goes stale the
moment you switch. This is the cutover rehearsal mode.

Switching either way is a restart of `sense_server.py` with different
environment, and rolling back is the same restart with the variables swapped.
It runs under `sudo` because it holds port 443.

## Supervision

`full-instructions/infrastructure/orb/deploy/install.sh` builds orb, installs it to `/usr/local/bin/orb`, and
loads `is.hello.orb` as a **LaunchDaemon**.

A daemon rather than an agent, deliberately: agents run only while a user is
logged in, and an alarm has to survive a logout and a reboot.

    ./orb/deploy/install.sh          # build, install, start
    tail -f /usr/local/var/log/orb.log
    sudo launchctl unload -w /Library/LaunchDaemons/is.hello.orb.plist

`KeepAlive` restarts orb whenever it exits. `ThrottleInterval` is 30 seconds
because orb exits immediately when Postgres or orb-algo are not up, which is the
normal case at boot while Docker is still starting; without the throttle launchd
would spin on it.

The install script unloads the service before replacing the binary. launchd will
otherwise keep running a deleted inode, and you will spend an afternoon
wondering why your fix did nothing.

**Installed and verified on 2026-08-16.** orb is no longer whatever `nohup` left
running. `sudo kill` of the daemon was answered by launchd in 33 milliseconds
with a new pid, a clean `last exit code = 0` (orb handles SIGTERM and shuts down
rather than being torn down), and the app and ingest both serving again. That
restart is the entire reason the daemon exists, so it is worth re-testing after
any plist change.

### The plist carries configuration, and a stale one fails quietly

**This bit off the first install.** The plist predated the same evening's work
and still said `-api-addr :8082` with no `-api-fallback` and no APNS variables.
Two failures followed, neither of which announced itself:

- The daemon crash-looped on `bind: address already in use`, because the old
  `nohup` process still held 8081. `launchctl print` said
  `state = spawn scheduled, runs = 4, last exit code = 1`, while `launchctl
  list | grep orb` printed **nothing at all**, which reads as "not installed".
- Had it started, it would have come up on 8082 with **nothing on 9999**, so the
  app would have stopped working while orb looked perfectly healthy, and push
  would have been off with only a single `push disabled; no signing key
  configured` line to say so.

So the plist is not boilerplate. It holds the app API port, the fallback
upstream, and all four APNS variables, and it must be updated in the same edit
as any change to how orb is invoked:

    -api-addr      :9999                    front door, NOT 8082
    -api-fallback  http://127.0.0.1:9997    proxy the rest to suripu-app
    ORB_APNS_KEY   /usr/local/etc/orb/AuthKey_XXXXXXXXXX.p8
    ORB_APNS_KEY_ID / ORB_APNS_TEAM / ORB_APNS_TOPIC

**The signing key lives in `/usr/local/etc/orb/`, not in a home directory.** The
daemon runs as root at boot before any user has logged in, so a path under
`/Users` may not be readable when it is needed. It is `0600 root:wheel`, which
means an ordinary shell cannot read it: `permission denied` when testing by hand
is the correct answer, and `push enabled` in the log is the proof that root can,
since that line only appears after the PKCS#8 parse and the ECDSA type check
both succeed.

**Two startup lines are the whole health check**, and their absence is the
failure mode:

    msg="orb app API listening" addr=:9999
    msg="app API fallback enabled" upstream=http://127.0.0.1:9997
    msg="push enabled" topic=... host=https://api.sandbox.push.apple.com

`apns-topic` is the **bundle id of the build on the phone**, currently
`com.example.sensetest` because Debug and Dev build `sensetest`. Switching
the app to Beta or Release makes it `com.example.sense`, and the plist has
to change with it or push fails with `DeviceTokenNotForTopic`.

## Order of operations for cutover

1. ~~Install the service, so orb survives a crash and a reboot.~~ **Done
   2026-08-16**, and the restart verified by killing it.
2. Leave orb in shadow for a day and compare. The sweep in `full-instructions/infrastructure/orb/cmd/apidiff`
   checks the app API; for ingest, compare orb's `sensor_samples` against the
   DynamoDB rows for the same window. **Note the ports moved**: orb answers the
   app API on 9999 and suripu-app is now on 9997.
3. Switch to **primary** with the reversed shadow, so suripu stays current and
   rollback is one restart.
4. Watch a night. Alarms are the thing that will be noticed first.

**The shadow/primary table above is about the device path only.** The app API is
a separate question and has already moved: orb is the front door on 9999
whichever way `sense_server.py` is pointed. So the phone is talking to orb today
while the Sense is still acting on suripu's answers, and those two facts are
independent.

## What orb serves, and what it does not

Ordinary alarms work and have rung on real hardware. **Smart wake** works and has
not: it brings a smart alarm forward only on the reference's own deterministic
awake test, never later than the set time, and it has unit tests but no live run
because this account has never had a smart alarm.

**OTA** is built and does nothing until told to. It is neither automatic nor
triggerable from the iOS app: an update is a row in `firmware_updates` naming the
device, and it is only offered once somebody sets `armed = true` by hand. No row
is the default and the normal state, and every gate in `internal/ota` can only
refuse. See the OTA section of CONSOLIDATION-PLAN.md for the gates, the reasoning
behind arming, and why the reference's `POST /v1/ota/request_ota` (which bypasses
the safety checks) has no orb equivalent.

    -- arm an update, having inserted the row first
    UPDATE firmware_updates SET armed = true WHERE id = ...;

Verified in three states against the live device (no row, unarmed row, armed row
outside the window). **No image has ever been offered to real hardware.**

Deliberately absent, with reasoning recorded in CONSOLIDATION-PLAN.md:

- **`aggstats-generator`.** `agg_stats` has no readers; the worker's only reads
  are checking whether it already wrote.

Genuinely still open:

- **`push`.** Sending works and has reached the phone; **nothing schedules a
  send** yet. See below.
- **`sense-last-seen`** is covered inline by `TouchSense` on ingest, which is
  what that worker exists for. Worth confirming nothing else depended on it.

## Push notifications: the producer was here all along

**Correction, 2026-08-16.** An earlier version of this section, and the comments
in `docker-compose.yml` and `configs/push.yml`, all said no producer for the
`push_notifications` stream existed in any backed-up repo and that it must have
lived in a service missing from the backup. **That was wrong, and the method was
the reason.** The grep behind it only covered checked-out working trees, and the
`infrastructure/` copies are single-commit snapshots: `infrastructure/suripu` has
1 commit where `github-backup/suripu` has 9,093 and 1,326 refs.

### What the exhaustive search covered

130 repositories (117 in `github-backup/`, 13 in `infrastructure/`), and for each:

- every ref, including `refs/remotes/*` and `refs/tags/*`, not just `HEAD`
- every commit in the graph, by pickaxe (`git log --all --reflog -G`)
- every **unreachable and dangling blob** (1,907 across the suripu repos alone),
  which is where a deleted branch's only copy would survive

The unreachable-blob detector was checked against a known positive first, because
a broken grep and a genuine absence produce identical output. That is the same
failure that hid the dead push worker for a day.

### Result: exactly two producers, both present

| producer | notification | deployed here? |
|---|---|---|
| `suripu-workers` `SavePillDataProcessor` | `PillBatteryLow` | **yes, running now** |
| `suripu-queue` `TimelineGenerator` | `NewSleepScore` | no, `suripu-queue` is not in the stack |

The pill worker is fully wired: `PillWorkerCommand` hands it a
`KinesisNotificationSender` aimed at this stream, and `pill_save_ddb.yml` already
maps `push_notifications`. **It is silent for an ordinary reason: the threshold is
`battery_notification_threshold: 10` and the Sleep Pill is at 63-78%.** Nothing is
broken and nothing is missing.

The sleep-score producer is additionally gated on the `PUSH_NOTIFICATIONS_ENABLED`
feature flag per account.

`push_notification.proto` declares seven notification types. Five of them
(`SenseOffline`, `PillOffline`, `RoomConditionAlert`, `NewInsight`,
`GeneralNotification`) have **no producer anywhere in any repo or any dead
branch**: they were designed and never built. `KinesisNotificationSender.send()`,
the generic single-event path, is itself half-written, carrying a literal
`// add all the fields` where the payload should be.

### The reference path, driven for real on 2026-08-16

`battery_notification_threshold` was raised from 10 to 100 for one heartbeat, so
the pill at 78% would qualify. It produced exactly what it should:

    RECORDS ON STREAM: 1, partition key 1
    account_id  1
    sense_id    0123456789ABCDEF
    pill_battery_low { pill_id 59B6229F7146D67A, battery_percent 78 }

The consumer then parsed it, passed the feature flag, and generated a
`HelloPushMessage`. **So the producer, the stream, the KCL consumer and the
message generation all work.** The claim that no producer existed was wrong twice
over: the code was there, and it runs.

It stopped one step further on, and that step is worth recording:

    error=Cannot do operations on a non-existent table
    key=2026-08-10 00:00:00|pill_battery  table_name=push_notification_event_2026
    ...
    action=duplicate-push-notification account_id=1 type=PILL_BATTERY

**`PushNotificationEventDynamoDB` writes to a table named per year**,
`push_notification_event_2026`, not the `push_notification_event` that was
created by hand to match the DAO. That alone is a fixable omission.

The part that matters more: after retrying with backoff and failing, the
processor concluded **`duplicate-push-notification`** and dropped the
notification. A dedupe store that cannot be reached is treated as "already sent".
It fails closed, so with a missing table **no push is ever delivered and the log
says the notification was a duplicate**, which is a sentence that sends you
looking for the first one. Something to remember if the reference stack is ever
made to deliver: create `push_notification_event_<year>` first, and a rollover on
1 January will silently produce the same symptom.

None of this blocks orb, which sends directly and shares none of this machinery.

### Push works. 2026-08-16, delivered to the phone.

**orb sends Apple push notifications directly, and one arrived on the handset.**
Nothing in the reference's Kinesis-to-SNS chain is involved.

| fact | value |
|---|---|
| bundle id / `apns-topic` | `com.example.sensetest` (the **Debug/Dev** value of `SENSE_APP_ID`) |
| team id | `YOURTEAMID` |
| key | `push-key/AuthKey_XXXXXXXXXX.p8`, key id `XXXXXXXXXX` |
| host | `api.sandbox.push.apple.com`, because the build is `development` |
| device | iPhone 15 Pro, iOS 26.6, app version 2.1.1 |

What was built in orb: `migrations/0010_push_tokens.sql`, `internal/push`
(APNS over HTTP/2, **no new dependencies**), `POST`/`DELETE
/v1/notifications/registration`, and `cmd/pushsend` for manual sends.

Apple issues a device token only to an app signed with a provisioning profile
whose App ID has push enabled. **Joe builds the app himself**, under his own team,
which is the only reason push is reachable at all: the original App Store
binary's token would be bound to Hello's Team ID, and Apple will not issue a key
for a bundle id you do not own.

### The four things that will waste an evening if forgotten

1. **The app never asks for permission on launch, and that is by design.** Only
   two places call `askForPermissionToSendPushNotifications`: the onboarding
   screen `HEMEnablePushViewController`, and the in-app Notification Settings
   screen. On every launch `HEMAppDelegate` calls `renewPushNotificationToken`,
   which registers **only if permission already exists**, so before it is granted
   the launch path does nothing and prompts for nothing. Grant it from the app's
   own Settings -> Notifications, or from iOS Settings. This is a 2017 app using
   the iOS 8 notification API; a modern one would call
   `UNUserNotificationCenter.requestAuthorization`.

2. **`currentUserNotificationSettings` still works on iOS 26**, which was not a
   given: it has been deprecated since iOS 10. If it ever returns nil the launch
   path silently stops registering, and the fix is to patch the app rather than
   to look at the server.

3. **ES256 signatures are raw R||S, not ASN.1 DER.** `ecdsa.SignASN1` produces
   DER, and APNS rejects it with a 403 `InvalidProviderToken` that is
   indistinguishable from a wrong key id. Pinned in `internal/push`.

4. **A fake token is the credential test.** Send to 64 zeroes: `BadDeviceToken`
   means the key, key id, team id, topic and host are all correct and only the
   token is wrong. `InvalidProviderToken` or `TopicDisallowed` means a credential
   is wrong and the token was never examined. This distinguishes the two halves
   of the problem without a phone.

### orb is now the app API's front door

**Since 2026-08-16, orb listens on 9999 and forwards what it does not implement
to suripu-app on 9997.** The app was built against `http://192.168.1.10:9999`,
so it needs no rebuild and does not know anything changed.

    iPhone -> :9999 (orb)
                |- /v1/notifications/*  orb answers
                |- /v2/timeline/*       orb answers
                `- everything else      proxied to suripu-app :9997

This is the **incremental cutover**. An endpoint moves the day a handler is
registered for it, and `cmd/apidiff` still compares the two beforehand. The
alternative, pointing `SENSE_API_URL` straight at orb, is all-or-nothing: every
screen served by orb at once, and anything unimplemented broken until it is not.

`api.Handler.ServeHTTP` decides by asking `mux.Handler(r)` for the matched
pattern and treating an empty pattern as "not ours". Checking the handler itself
would not work: `ServeMux` answers every request, so an unmatched path returns a
perfectly valid `NotFoundHandler`.

### A route ending in a bare slash steals its whole subtree

**The trap this arrangement adds, and it has already bitten.** In `net/http` a
pattern ending in `/` is a **subtree**, so `GET /v1/questions/` matched
`/v1/questions/more` too. Before the fallback existed that was invisible, because
the app only ever calls `/v1/questions/`. With the fallback it stopped being
cosmetic: a swallowed path is one orb answers *instead of* suripu, and it answers
with a plausible wrong body rather than an error.

    GET /v1/questions/more
      suripu:  "How refreshed do you feel?"      id 47
      orb:     "How was your sleep last night?"  id 22

The app's "show me another question" received today's list again. suripu serves
`/more`, `/skip` and `/{id}/skip` under that same prefix; orb implements none of
them.

Fixed by anchoring with Go 1.22's `{$}`, and registering the bare and
trailing-slash forms separately so `net/http`'s implicit redirect does not send
the bare form somewhere neither side chose:

    GET  /v1/questions/{$}        GET  /v1/questions
    POST /v1/questions/save/{$}   POST /v1/questions/save

`TestUnimplementedSubpathsAreNotClaimed` in `internal/api/routing_test.go` asserts
that every sibling suripu serves still reaches the fallback, and it asserts
against `mux.Handler` rather than a live server so it fails on the registration
mistake itself. **Adding a route that ends in `/` means adding its siblings to
that test.**

Only the GET was affected, incidentally: patterns carry the method, so
`PUT /v1/questions/skip` never matched orb's GET subtree and proxied correctly
all along. That is why the symptom was one wrong list rather than a broken
screen, and why it would have survived a casual look.

The first consequence is that push registration now lands in orb by itself, so
the device token is no longer lifted out of suripu-app's log by hand. The
stopgap that required is gone, and `com.hello.suripu.app` is back to INFO.

**The cost, stated plainly: orb is now in the critical path.** It was a shadow
whose death nobody would notice; it is now the app's front door, and orb being
down is the app being down. This is the argument for finally running
`full-instructions/infrastructure/orb/deploy/install.sh`, which is still not done.

To roll back: set suripu-app's port mapping to `9999:9999` in
`docker-compose.yml` and stop orb's `-api-addr :9999`.

### Notifications are scheduled, not just possible

The worker gained a `notifications` job on a 15 minute ticker, sending two kinds:

| kind | when | dedupe bucket |
|---|---|---|
| `sleep_score` | a night scored in the last 24h **and** dated last night or today | the night's date |
| `pill_battery` | a paired, active pill under 10% | the ISO week |

**Both bounds on the sleep score matter, and they catch different things.**
Without the `updated_at` bound, the first run against a database of history would
send one notification per night ever recorded. Without the `date_of_night` bound,
re-scoring an old night, which a timeline correction does routinely, announces
"you slept 76 last night" about a night three days ago. That second one was
caught by previewing the query before enabling the job, not by a test.

`push_log` holds the dedupe, and its unique constraint **is** the check: claiming
and deciding to send are one statement, so there is no window between them.
Claim-then-send rather than send-then-record, because a crash between the two
then costs a missed notification instead of one that repeats every 15 minutes.
A send that fails releases the claim, so the next tick retries it. A token Apple
reports as `Unregistered` is deleted rather than retried forever.

Verified live: on the first run after wiring, `push sent kind=sleep_score
account=1 key=2026-08-15 devices=1` and the notification arrived.

### The Xcode settings, since they are not where you would look

`Sense.entitlements` lives under `Extensions/RoomConditions/` but is referenced by
**all eight** `CODE_SIGN_ENTITLEMENTS` entries in `Sense.xcodeproj`, so it is the
main app's file as well as the widget's, and it already declared
`aps-environment: development`. The path misleads; the build settings decide. (An
earlier draft of this file said the main target had no entitlements file, on the
strength of a `find`. Wrong, and it would have sent Joe to add a capability that
was already there.)

Signing is Automatic with team `YOURTEAMID`, which matters: Xcode enables
capabilities on the App ID when it provisions, so the push capability was already
on. Had it not been, the build would have failed with "provisioning profile
doesn't include the aps-environment entitlement" rather than failing at send time.

Four configurations, two bundle ids. **Debug** and **Dev** build
`com.example.sensetest` against `http://192.168.1.10:9999`; **Beta** and
**Release** build `com.example.sense` against `https://api.hello.is`. Which
one is live is answerable from outside: port 9999 is suripu-app, and it is up.
`192.168.1.10` is a second address on the same Mac, which now also answers on
`192.168.1.11`.

Prefer an **APNs Auth Key (`.p8`)** over an SSL certificate: one key covers every
app and both environments and never expires, where a certificate is per app per
environment and stops working on its anniversary. There is no dev/prod choice to
make with a key, only a host to pick at send time. The key is a server credential
and never belongs in the app bundle.

### The stream had also been dead since 2026-08-15

`push_notifications` was collateral damage from the stream recreation (see
[LOCALSTACK-KINESIS.md](LOCALSTACK-KINESIS.md)), and the worker had been
crash-looping on `ResourceNotFoundException` ever since. Nobody noticed, because
**a worker that idles by design and a worker that is dead look identical from
outside.** Recreated 2026-08-16; it takes its lease and idles as intended. A
component whose correct behaviour is silence needs its liveness checked some
other way.

## Partners, and which Sense heard the pill (2026-09-01)

Pills broadcast over ANT and every Sense in range relays what it hears. The
Sense firmware queues every pill packet the top board hands it (kitsune
`ble_proto.c`, the PILL_DATA case, no paired-pill check) and orb routes each
sample by pill id to `account_pills`, never looking at the relaying Sense. So a
partner whose own Sense is unplugged still gets a score, as long as their pill
is within ANT range of any Sense on this backend. Room data does NOT travel
that way: `sensor_samples` for an account come only from its own active Sense.

Three things were added on top of that:

- **An explicit partner link**, `account_partners`, stored symmetrically. The
  reference inferred a partner as "the other account on my Sense"; here two
  people can share a bed and each keep their own Sense in the room. Set it by
  hand (no app screen calls it):

  ```
  curl -X PUT -H "Authorization: Bearer <app_id>.<token hex>" \
       -d '{"email":"partner@example.com"}' https://<host>/v1/account/partner
  ```

  `GET` shows it, `DELETE` unlinks both sides. `LoadNight` then loads the
  partner's pill samples over the same window and sends them as
  `partner_motion`; orb-algo turns minutes where the partner moved a lot and
  the sleeper did not into `PARTNER_MOTION` rows (the reference's
  `getPartnerMotionEvents` + `PartnerMotion.getPartnerData`). Display only: the
  score, sleep and wake times are unchanged by it (verified by replaying the
  same night with and without). The reference's partner FILTERS, which rewrite
  the sleeper's own motion before scoring, are not run.
- **`pill_samples.relayed_by`**: the Sense that uploaded the sample, so two
  Senses in one room can be compared on which pill each hears. Null on rows
  from before the column existed.
- **`cos_theta` and `motion_mask`** are now stored for v4 (1.5 pill) samples.
  orb decoded them all along and dropped them; the motion-mask partner filter
  needs them from both pills, and they cannot be backfilled.

A night with no real room reading inside the sleep window is scored on
duration alone (orb-algo `Timeline.environmentScore` returns absent, weighting
1.0 on duration), with no condition dots. Before this, the -1 fill for missing
minutes scored an unplugged Sense as an ALERT-cold, ALERT-dry room. The result
JSON carries `environment_score`, null in that case.

To replay a night against orb-algo without touching stored data, use
`~/replay_night.py <account> <date> [algorithm|-] [asof]` on the VM
(`NO_PARTNER=1` drops the partner motion). It rebuilds the request the way
`LoadNight` does and posts it to the service.

### Corrections on a night the algorithm now refuses (2026-09-02)

VOTING can score a night in the morning and then, once the day's motion has
arrived, return EVENTS_OUT_OF_ORDER on every later pass. A correction made on
such a night used to be stored, acknowledged by the app, and never drawn,
and the worker re-picked the night forever because its feedback stayed newer
than its timeline. Now the request carries `stored_events` (the night's four
main events as last stored) and orb-algo, when every algorithm fails, rebuilds
the night from them with the feedback applied (`algorithm = STORED`). The
learner still runs inside the chain; only the drawn answer changes.

### The Sense 1.5 (with voice) is not a Sense 1.0 (2026-09-02)

The reference converts the two generations differently
(`CalibratedDeviceData` picks `SenseOneFiveDataConversion` when the row has
the 1.5 extras): light comes from `lux_count / 5`, not from `light` (which on
the 1.5 is the sensor's clear channel: about 43 counts in a lit room, which the
1.0 formula turns into 0.16 lux, below the 1.1 lux darkness threshold, so the
room never registers as lit and LIGHTS_OUT is never found); temperature is
raw minus 6.00, not minus 3.89; humidity is raw, uncorrected. orb now stores the
1.5 extras (`pressure`, `tvoc`, `co2`, `ir`, `clear`, `lux_count`, `uv_count`,
columns that existed but were never written) and sends `hardware_version`
(from `senses.hw_version`, 4 for the 1.5) so orb-algo builds
`DeviceData.senseOneFive`. Rows stored before 2026-09-02 13:20 UTC have no
`lux_count`, so nights before then still show no lights-out for a 1.5.

Seen side by side in one room (2026-09-02 morning): voice Sense lux_count 184
(37 lux) vs orb light 1075 counts (4 lux); voice temperature converts about 2
degrees C warmer than the orb's; the orb's audio peak fields are all ZERO
(the voice Sense reports sound, the orb reports none); the voice Sense has no
dust calibration row so its particulates read WARNING.

### Room Conditions, trends and the LED read the 1.5 as a 1.5 (2026-09-02)

The app-facing sensor path (`internal/roomstate`, `api/sensors.go`,
`api/sensorseries.go`, and the LED colours in the sync response) had no
hardware version either. `roomstate/onefive.go` now carries the 1.5
conversions and the rows carry `hw_version` (from `senses`) so temperature,
humidity and light convert per device. Migration 0018 adds the colour
channels `r`, `g`, `b`. A Sense 1.5 now also gets the extra tiles the iOS
app already renders: CO2 (PPM), TVOC (VOC), PRESSURE (MILLIBAR), UV (RATIO)
and LIGHT_TEMP (KELVIN), with the reference's conversions and OUR bands (the
backend snapshot predates its classifiers for these; see the scales in
onefive.go). Sensor series answer the same names. The app groups
PARTICULATES, CO2 and TVOC into one air-quality tile.

### The orb Sense's microphone is server-gated (2026-09-02)

Every audio field from the orb Sense had been zero since 2026-08-29 21:10
UTC, the exact minute the first custom build (4514, from stock 4513)
completed, so it looked like a build defect. It was not. The 1.9.2 firmware
starts audio capture only when told to: a console command, or a sync
response carrying `audio_control` with capture ON (wifi_cmd.c,
AudioControlHelper). Nothing starts it at boot. The reference put that block
on EVERY sync response (ReceiveResource: capture ON, feature and raw saving
OFF); orb never did. Stock 4513 had simply kept capturing from before the
cutover until the 08-29 OTA rebooted it, and the timeline then showed no
noise events and a permanently ideal sound score. orb now sends
`audio_control{audio_capture_action: ON}` to a Sense 1.0 on every sync
(`edge.setAudioControl`); sound returned on the first minute after the
deploy. Deliberately NOT sent to the 1.5: its firmware captures continuously
for the wake word and manages its own feature uploads, and the reference's
save_features OFF would switch those off.

Note: `senses.firmware_version` for the orb Sense reads 4538, not the 4539
the orientation notes claim; the 4539 offer completed but the device reports
4538 since.

### Extras on the reference's own scales, and two classifier fixes (2026-09-02)

suripu-app (not suripu-core) carries the scales for the 1.5's extras
(`app/sensors/scales`: Co2Scale, VocScale, UvScale, PressureScale), so the
tiles now use those bands, names and units exactly: CO2 in PPM, VOC in MG_CM,
UV as the raw COUNT, and "Barometric pressure" whose condition is the CHANGE
over the last four hours on +/-20 and +/-40 mbar bands while the drawn scale
is those bands shifted around the current reading. Light temperature has no
reference scale and keeps ours. Gas sensors report 65021 in both fields for
a couple of minutes after a reboot (a not-ready sentinel; the reference would
clamp it to ALERT), shown as UNKNOWN instead; right after that they read at
the floor (CO2 400, VOC 0) until the sensor warms up.

Two classifier fixes found on the way. A value in the 0.1-wide gap between
two bands (49.95 on the particulates scale) used to fall through to the LAST
band and read "Hazardous"/ALERT; the reference's fromScale answers UNKNOWN
and orb now does too. The LED path keeps threshold semantics
(`ClassifyForLED`: a gap takes the band above, past the top stays in the top
band), which is what the reference's LED-side classifiers do.

The voice Sense's `dust_offset` was set to 639 on 2026-09-02 by matching its
raw count to the orb Sense's over 36 hours in the same room (orb 512, voice
830, both flat day and night, fan or no fan). Check-back note in CLAUDE.md
for 2026-09-09.
