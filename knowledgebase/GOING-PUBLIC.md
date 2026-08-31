# Going public: dropping suripu, moving to OCI, and reaching the Sense

Written 2026-08-26, updated 2026-08-29. **Steps 1 and 2 are done (2026-08-27
and 2026-08-28) and the containerisation that step 3 depends on landed
2026-08-28. The OCI move itself has not been started.** This is the plan and the
evidence behind it, so the decisions are already made when the work begins.

The 2026-08-29 update adds three things the original draft did not cover: the
port collision between the two TLS surfaces, the fact that the suripu jar is not
in the public repository, and a corrected, much less alarming account of what
flashing a new URL actually involves. See "What changed since this was written".

The goal, in the user's words: drop the old infrastructure, run orb and its
components on a static machine (the OCI VM), and push firmware that points the
Sense at a new domain, so the sleep tracking is reachable from anywhere.

The short version of what follows: **the goal is reachable without touching the
firmware**, the firmware step is the riskiest and least necessary part, and the
thing that actually shapes the architecture is a TLS cipher.

## Is the old system droppable? Yes, with one night of proof

Audited 2026-08-26 against the running system, not from memory.

### The app side: DONE, 2026-08-27

All four moved. `suripu-app` now serves nothing the app asks for.

| Endpoint | How it was handled |
|---|---|
| `GET /v2/insights/info/{CATEGORY}` | 38 rows of editorial copy migrated from the `insights` database into `insight_info`. Diffs clean. |
| `POST /v2/sharing/insight` | Rebuilt. Stores a snapshot and serves a real page from orb at `/share/insight/{id}`; the reference returned a link to the dead `share.hello.is`. |
| `GET /v2/alarms/sounds` | 15 ringtones, ids and names. Audio URL omitted. |
| `GET /v2/sleep_sounds/combined_state` | 11 tones, 6 durations, status. Preview URLs omitted. |

The two sound endpoints differ from suripu **only** in the audio URL field, and
that is deliberate: the audio is gone (see below). The ids are the entire
functional payload, since the id is what gets written onto the alarm and the
Sense plays the tone from its own SD card.

Migration `0013_insight_info_and_shares.sql`. Routes and their siblings pinned in
`internal/api/routing_test.go`, because the bare-slash subtree trap has bitten
here before.

Two defects `apidiff` caught that tests would not have:

- The ringtone list was transcribed from a truncated view of the Java source and
  had **12 entries instead of 15**. Highlights, Ripple and Sway were missing.
- The first load of `insight_info` collapsed NULL and empty-string `image_url`
  into NULL. suripu returns `""` for one and `null` for the other, and the app is
  sent whichever is stored. Reloaded preserving the distinction: 21 NULL, 16
  empty, 1 surviving URL.

Both are the reason the discipline is "diff before moving", not "read the Java".

### The audio: recovered 2026-08-31, and the search that preceded it

**RECOVERED.** All 27 SD card audio files were extracted on 2026-08-31
(working files, outside the public repo): 12 sleep tones (ST001 to ST012),
15 ringtones (DIG, NEW, ORG), plus PINK/TONE/STAR startup sounds. Every one
of the 12 SLPTONES files verifies byte-exact against the SHA1s in
`file_info_one_five`, including the never-offered Ocean Waves (ST005).
The format is headerless s16le PCM, mono, 32 kHz (`ConvertToPcm.sh` in
kasetsu and `AUDIO_SAMPLE_RATE` in the firmware agree, and every file's byte
count divides into a clean duration at 64,000 bytes per second). Curiosity:
the recovered DIG005 is the same length as kasetsu's surviving copy but
differs in nearly every byte, a different master of the same tone.

orb now serves previews from them: mp3s embedded in the binary by
`internal/api/soundpreview` (ringtones full length, sleep tones cut to 30 s
because the device loops them), served unauthenticated at
`/v1/sounds/previews/`, with `url` on `/v2/alarms/sounds` and `preview_url`
on `/v2/sleep_sounds/combined_state` built from the request origin the way
the insight art already is. The ringtone preview file is derived from
`alarm.SoundPath`, so the phone can only preview what the device will ring.

Play followed the same day: POST /v2/sleep_sounds/play and /stop are messeji
PlayAudio/StopAudio commands (hand-encoded in `internal/messeji`, signed,
queued on `device_messages`), and /v2/sleep_sounds/status now reads the
device's own recorded SenseState instead of a hardcoded not-playing.
Verified end to end on the no-voice Sense 2026-08-31: play queued, device
opened /SLPTONES/ST010.RAW two seconds later, stop stopped it.

Two catalogue generations exist and they matter. The recovered files above
are the voice-era set (`file_info_one_five`, 32 kHz masters, played by the
CC3220 firmware at 32 kHz). The no-voice Sense's card holds the 2016 set
(`file_info` in the precutover common.dump backup, 48 kHz masters, played
by 1.9.2 at 48 kHz), all 11 sleep tones verified against those SHA1s via a
captured debug manifest (kept privately with the working files). Both
generations map every name to the same ST file, so paths and previews are
right for both. The no-voice card's 16 ringtones plus PINK/STAR/TONE, and
an uncatalogued ORG005.RAW, are a second audio set not yet exported.

The search below is kept so nobody repeats it; it was searched 2026-08-26
before committing to a teardown.

**Every blob in all 135 repositories, reachable and unreachable, content-hashed
against the SHA1s the old `file_info` table records for the 11 sleep tones:
24,631 candidate blobs, zero matches.** Also nothing by path (`ST0xx.RAW`,
`SLPTONES`, `RINGTONE`), nothing by any audio extension, nothing by content grep,
nothing in `kitsune/resources/`, nothing in `Nonsense.apk` or the Java jars.

Three repos with promising names, all tooling rather than content: `hello-audio`
is an ADPCM encoder library, `store-audio` is a Go uploader, and `suripu-ios`
carries `Aria/Ballad/Bells.m4a` which match no tone in either list.

What survives is worth knowing:

- **`kasetsu/audio/server/raw/DIG005.raw`** (1.0 MB) with a `.wav` sibling in
  history. One real ringtone in both encoded and decoded form, which is exactly
  the worked example needed to identify the format of anything recovered.
- **The full catalogue**: 11 names, their SD paths (`/SLPTONES/ST010.RAW`), and
  their SHA1s. Anything recovered can be verified rather than hoped at.

**It cannot be pulled over the wire.** `cat` exists in the 1.9.2 firmware (a
later revision dropped it, so the working-tree checkout misleads) but prints with
`LOGF("%s", ...)` and stops at the first null byte, which makes it useless for
binary. `fsrd` reads serial flash, not the SD card. The file-sync protocol only
ever pushes files to the device. So it is a teardown, and it is not blocking
anything: the audio affects one optional field on two endpoints.

orb now logs the device's own manifest at DEBUG, one line per file with its
SHA1. That is the only catalogue of what is actually on the card, including the
ringtones, which `file_info` does not cover. Turn the level up, capture one
manifest, turn it back down.

### The device side is one restart, and better than STATE.md used to say

orb's edge replaces **all three** device-facing services, not just
`suripu-service`:

- `suripu-service`: ingest, alarms, file manifest, sense state
- `hello-time`: `byHost` routes `time.hello.is` and `ntp.hello.is` to
  `h.timeSync`, with `ntpEpochOffset = 2208988800` handled correctly
- `messeji`: `POST /receive`, the long-poll

So they fall together, and with them the five workers that only exist to feed
them. That is seven of the thirteen running containers.

### What is genuinely unproven

Written 2026-08-26; the first item has since been settled.

- ~~**A full night on orb's device path.**~~ Done 2026-08-27/28: 494 syncs, a
  07:15 alarm that rang within a second, zero errors.
- **The LED will visibly change colour.** orb fixes the reference's
  unconverted-light bug. Expect it; do not read it as a regression. Still not
  confirmed by eye.
- **OTA has never delivered an image.** Irrelevant to dropping suripu. Very
  relevant to the firmware step below.
- **Smart wake** stays unproven, and stays moot: this account has never set one.
  On the motion actually recorded before the 2026-08-28 alarm it would have
  changed nothing, so a "successful" test of it would prove very little.

## The constraint that shapes the whole architecture

**The Sense offers exactly one cipher, `TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA`
(0xC014), and sends no `supported_groups` extension.** OpenSSL 3.x, LibreSSL and
Go all refuse that handshake, because modern stacks will not pick an ECDHE curve
without the extension. Only `tlslite-ng`, pure Python, will talk to it.

The consequence is not a detail. **The device endpoint cannot sit behind nginx,
Caddy, Cloudflare, or an OCI load balancer.** Every one of them rejects the
Sense. See [DEVICE-PROTOCOL.md](DEVICE-PROTOCOL.md) and `sense_server.py`'s
`TLSHelloServer` for how each TLS
gate was cleared originally.

### So split the surfaces, and never let them merge

| Surface | Terminator | Exposure |
|---|---|---|
| App API | anything modern, Let's Encrypt | genuinely public |
| Device endpoint | `tlslite-ng`, its own hostname | one device, one home IP |

The app API is the "reachable from anywhere" goal and **has no CC3200 anywhere
near it**. The device endpoint serves exactly one device from one address, so
firewall it to the home IP rather than exposing an unaudited pure-Python TLS
stack to the internet. Letting these two share a terminator is how the app ends
up stuck on TLS 1.2 with a CBC cipher for the sake of one sleep tracker.

## The firmware change is probably avoidable, and that is the recommendation

The stated plan is to change the URL and the CA. Take them apart.

### The CA does not need to change at all

The CA on the device is **ours**. We generated it and wrote it to
`/cert/ca.der`. For a new domain, issue a new *server* cert from that same CA
with the new SANs. The device already trusts the issuer. Nothing on the device
changes.

That matters more than it first sounds, because **the CA cannot be updated over
the air under any circumstances**:

- `/cert/ca.der` lives on the CC3200's **serial flash**, reached through
  `sl_FsOpen` (SimpleLink).
- The file-sync endpoint `/in/sense/files` writes to the **SD card**, through
  `hello_fs_*` (FatFS). `filedownloadmanager.c` takes `sd_card_path` and
  `sd_card_filename`. Different filesystem, no overlap.

Updating the CA means bootloader mode over UART, ID pin to RTS, `cc3200tool`.
Physical access, every time. That path is proven (`make write-ca`,
`make restore-ca`) but it is never remote.

### The URL only needs to change if you refuse a DNS override

The hostnames are compile-time constants, but not quite plain `#define`s, and
the difference turns out to be useful. `kitsune/endpoints.h` defines two names
per endpoint and `endpoints.h` resolves `DATA_SERVER` through a function:

    #define DATA_SERVER    get_server()
    #define PROD_DATA_SERVER    "sense-in.hello.is"
    #define DEV_DATA_SERVER     "dev-in.hello.is"
    #define PROD_MESSEJI_SERVER "messeji.hello.is"
    #define DEV_MESSEJI_SERVER  "messeji-dev.hello.is"

`get_server()` (`kitsune/wifi_cmd.c:1860`) returns one of the two based on a
`use_dev_server` flag, which `load_data_server()` reads at boot from
`SERVER_SELECTION_FILE` on the device's own flash. The console command `dev 1`
writes that file (`Cmd_setDev`, `wifi_cmd.c:299`) and `dev 0` clears it.

So there is a persistent, runtime, no-reflash switch between two endpoint sets.
It cannot point at an arbitrary name (both are baked in), but it means a
firmware carrying your domain in the **DEV** slots can be toggled on and off
from the serial console. See "Flashing a new URL" below, which is where that
becomes the whole plan.

Two related dead ends, so nobody re-searches them:

- `MORPHEUS_COMMAND_SET_SERVER_IP = 33` exists in `morpheus_ble.pb.h` and is
  **handled nowhere in the firmware**. It is an enum value and nothing else.
- There is no `setserver` console command. The command table
  (`commands.c:1970-2100`) has `dev`, `dns`, `aes`, `mac` and `set-time`, and
  that is the whole of the relevant surface.

`TIME_HOST` is different again: `kitsune/sys_time.c:172` hardcodes
`"time.hello.is"` with **no dev/prod variant at all**. Any scheme that toggles
the other two leaves time sync pointing at `hello.is` unless you add the variant
(a three-line edit following the existing pattern).

None of this is needed to reach the goal. The device only needs to resolve a
name **you** control if nothing at home is answering DNS for it. Point
`sense-in.hello.is` at the VM's public address instead:

- `dns_server.py` already does exactly this, and only its `REDIRECT_IP` changes.
- Better, **a static DNS entry in the router removes the Mac entirely**, with
  zero firmware work.

### Why to leave firmware until last

OTA has never delivered an image. A first real OTA, carrying a domain change, to
a device whose only recovery path is physical, is two unproven things stacked in
one step. Nothing about the goal requires taking that risk early, and step 4
below reaches the goal without it.

**That argument is about OTA specifically.** A UART flash is a different risk
profile entirely, and the next section is the evidence for why.

The 2026-08-29 investigation refines "never delivered an image" considerably:
the mechanism is complete on both sides and has real safety layers. What has
never happened is anyone arming it. See "OTA is implemented on both sides"
below. The recommendation is unchanged, but the reason narrows from "the
mechanism is unproven" to "the first use of a mechanism should not also be the
risky payload".

### Flashing a new URL: the mechanics, and why it is recoverable

Established 2026-08-29 from the bootloader source and the device's own flash
dump. The short version: the CC3200 runs a three-slot fail-safe scheme, a bad
image falls back instead of bricking, and **rollback is an 88-byte file**.

`boot/application_bootloader/main.c` names the slots:

    #define IMG_BOOT_INFO           "/sys/mcubootinfo.bin"
    #define IMG_FACTORY_DEFAULT     "/sys/mcuimg1.bin"
    #define IMG_USER_1              "/sys/mcuimg2.bin"
    #define IMG_USER_2              "/sys/mcuimg3.bin"

and the boot info is

    typedef struct sBootInfo {
      _u8  ucActiveImg;
      _u32 ulImgStatus;
      unsigned char sha[NUM_IMAGES][SHA1_SIZE];
    } sBootInfo_t;

Our dump of this device (`working-files/sense_fs_backup/sys/`) reads
`ucActiveImg = 1` (`IMG_ACT_USER1`) and `ulImgStatus = 0xABCDDCBA`
(`IMG_STATUS_NOTEST`), with `sha[1] = 0c5f639e1290df0e3a5f8641d670923ed71a5e63`.

**That is the same SHA1 as our byte-exact rebuild.** The running firmware is
`/sys/mcuimg2.bin`, and we can reproduce it bit for bit, so a modified image
differs only where we intend it to.

The safety comes from `Test()` (`main.c:322`), which SHA1s the image it just
loaded into SRAM and compares it against `sBootInfo.sha[img]`. On mismatch the
bootloader runs `LoadAndExecute(IMG_FACTORY_DEFAULT)`, so **a corrupt or
unrecorded image boots the factory image rather than nothing**. There is also a
try-once path: `ulImgStatus = IMG_STATUS_TESTREADY` boots the non-active slot,
marks it `TESTING`, and a subsequent boot that still reads `TESTING` reverts to
`NOTEST` and the active slot. A crash loop therefore self-reverts.

The procedure that follows from this:

1. Rebuild with the new names, byte-exact toolchain, one-line change.
2. `write_file` the new image to the **inactive** slot, `/sys/mcuimg3.bin`,
   leaving the known-good `mcuimg2.bin` untouched.
3. Write a `mcubootinfo.bin` with `ucActiveImg = 2` and `sha[2]` set to the new
   image's SHA1.
4. Reboot and watch the console.

Rollback is writing the original 88-byte `mcubootinfo.bin` back, which we have.
No reflash of any image is needed to undo it. Combined with the DEV-slot trick
above, a flashed device can also be toggled between the old and new endpoints
with `dev 1` / `dev 0` and no cable at all.

Both writes use the same `cc3200tool ... write_file <local> <device-path>`
mechanism that `make write-ca` has already used successfully on this device, and
the CC3200's ROM bootloader is in **ROM**, reachable over UART with the SOP2/RTS
jumper regardless of what is in flash. There is no state of the serial flash
that removes the recovery path.

**The one unverified step** is whether writing under `/sys/` needs file tokens
or flags that `/cert/` did not. Reading them worked, and `write_file` is the
same call, but this has not been exercised. Test it on `/sys/mcuimg3.bin`, the
slot nothing boots from, before touching the boot info.

### OTA is implemented on both sides, and the blocker is not the device

Established 2026-08-29 by reading `kitsune/fatfs_cmd.c`, `boot/application_bootloader/main.c`
and orb's own `internal/ota`. The earlier note that OTA "has never delivered an
image" is true and was misleading: nothing is missing from the mechanism, and
nobody has ever armed it.

**The device path is complete.** `file_download_task` (`fatfs_cmd.c:1424`) acts
on a `SyncResponse.FileDownload` carrying `copy_to_serial_flash`, a `sha1` and
`reset_application_processor`:

1. If the serial flash filename contains `mcuimgx`, the firmware rewrites
   character 7 itself: `serial_flash_name[6] = _McuImageGetNewIndex() + '1'`.
   **The server never chooses the slot**, so it cannot be told to overwrite the
   running image.
2. It downloads, then `sf_sha1_verify` checks the written bytes against the
   digest the server sent. A mismatch aborts with the boot info untouched.
3. Only on a match does it set `ulImgStatus = IMG_STATUS_TESTREADY`, write
   `sha[newIndex]`, and reset.
4. The bootloader boots the new slot under `TESTING` and SHA1-checks it again,
   falling back to the factory image on failure.
5. `boot_commit_ota()` (`fatfs_cmd.c:1339`) in the NEW firmware is what commits:
   it flips `ucActiveImg` and clears the status. A firmware that boots but never
   reaches that line leaves `TESTING` set, and the next boot reverts.

Three independent checks: the digest before commit, the digest at boot, and
auto-revert on a firmware that starts but does not run.

**The download is plain HTTP on port 80.** `CreateConnection` (`fatfs_cmd.c:616`)
hardcodes `sl_Htons(80)` and opens an ordinary socket, with a `//todo secure
socket` comment nobody ever actioned. The OTA path therefore does not touch
tlslite-ng, the CC3200's TLS quirks, or any certificate. The most fragile part
of this system is not involved.

**orb has the server side already.** `internal/ota/ota.go` treats an update as a
row in `firmware_updates` (migration 0009) naming one device, which must be
explicitly armed; the reference's feature-flag-and-cohort model was rejected as
wrong for a single irreplaceable unit. `Decide()` has twelve gates that can only
refuse, including a 20 minute minimum uptime and a 02:00 to 05:00 local window.
`edge.go:958` wires it into the sync response.

**So what is actually missing is two things, neither on the device:**

- `select * from firmware_updates` returns **0 rows**. Nothing has ever asked
  for an update.
- **Nothing serves the image bytes.** orb's edge mux routes `/in/sense/batch`,
  `/in/pill`, `/in/sense/state`, `/in/sense/files` and `/receive`, and a GET for
  a firmware file falls through to `default:` and 404s. Either add a static
  route to orb or serve it from `sense_server.py`, which already owns :80.

**A trap to avoid when arming the first row.** The firmware reads the SD card
fields even on a serial flash download:

    if( strlen(filename) == 0 && strlen(serial_flash_name) == 0 ) { ... }
    ...
    if (filename && url && host && path) {   // the serial flash branch is INSIDE this

Those `.arg` pointers initialise to `NULL` (`fatfs_cmd.c:1643`) and are only
allocated when the field is present on the wire, while orb's `strPtrOrNil`
omits empty strings by design. A row with blank `sd_card_filename` or
`sd_card_path` therefore gets a silently skipped download at best and a
`strlen(NULL)` at worst. Populate all four path fields even though only the
serial flash pair matters.

**Recommended first use.** Ship an image that is byte-identical to 4513 except
for the build number, and prove the mechanism on a payload whose failure costs
nothing. Use OTA for the domain change only after it has worked once. The
failure mode that argues for this is specific: an OTA that successfully installs
a firmware pointing at a domain that turns out to be wrong leaves a device that
no longer talks to you, and the fix is the cable you were trying to avoid.

## One constraint that has already lifted

Certs used to need `notBefore` earlier than ~1956, because the device's clock
read 70 years behind. **That was our time server sending Unix epoch where the
device expected NTP**, fixed and verified 2026-07-27, and orb's own handler
carries the correct offset.

Time sync is plain **HTTP on :80**, so a cold boot gets the right clock before
any TLS handshake happens. Normally-dated certs should now work.

**Verify rather than assume.** The current `server.crt` is still 1950-dated, so
this has not actually been exercised with a modern cert. Test it at home, on the
existing CA, before it matters.

## The app rebuild is unavoidable

The app runs with `NSAllowsArbitraryLoads` today, set by a build phase for
Debug/Dev/Beta. Over the internet that has to go, ATS comes back on, and the
base URL becomes the new domain. That is a rebuild regardless of what happens to
the firmware.

While in there: the base URL is a DHCP address in four build configurations plus
`HEMSelectHostPresenter.m`. Moving to a hostname is what stops that going stale,
and it is the same edit.

## The OCI VM

    Host oci -> 203.0.113.10, user opc

`aarch64`, **2 cores, 10 GB RAM**, 183 GB disk, Oracle Linux 9.8, no Docker
installed yet.

**The consolidation is a prerequisite for the move, not a parallel effort.** The
three-component target (~350 MB) fits comfortably. The current sixteen-container
stack (~4.4 GB across four languages) does not, on two cores.

ARM matters in exactly two places: `orb-algo` needs an arm64 image, and orb is a
`GOARCH=arm64` build. Postgres has arm64 images. `tlslite-ng` is pure Python.

## What changed since this was written

Recorded 2026-08-29. The prerequisite is done and three practical obstacles
surfaced that the original draft did not anticipate.

### The prerequisite landed

`full-instructions/infrastructure/` is now a complete, committable deployment:
`docker-compose.yml` for the three components, a cross-platform `Makefile`, and
`scripts/migrate.sh` with a `schema_migrations` ledger. Every image the Mac is
running is already `linux/arm64`, the same architecture as the VM, so the ARM
question is settled rather than merely expected to work.

`docker-compose.linux.yml` was written for this move specifically. It adds
`sense-server` as a container with `network_mode: host`, which is the mode the
device path actually wants and cannot have on Docker Desktop. On Linux the
Makefile selects it automatically, so on the VM the TLS terminator stops being a
host process babysat by hand.

The data is smaller than the tooling around it: **21 MB**, a 900 KB `pg_dump`.
`make backup` and `make restore` already exist.

### Obstacle 1: both TLS surfaces want :80 and :443

`sense_server.py` binds `0.0.0.0:80` and `0.0.0.0:443` (`sense_server.py:473`).
The app API needs a modern terminator with Let's Encrypt on the same two ports,
and the whole architecture rests on those two never sharing a terminator. On one
host with one address they collide. Three ways out, in preference order:

1. **A second public IP on the VM.** sense-server binds one, Caddy the other,
   both on 443. Costs a small change, since `sense_server.py` hardcodes
   `0.0.0.0` and would need a bind-address environment variable.
2. **The app API on its own port** (8443, say). Zero infrastructure work,
   because the app's base URL is ours to set. The wrinkle is orb's public share
   page at `/share/insight/{id}`, where a port in the URL is ugly.
3. **SNI passthrough** (HAProxy in TCP mode, default backend to sense-server).
   One IP, one port, no termination on the device path. Rejected as the default
   because it adds a hop in front of the path that has already broken twice at
   the transport layer, and it is unconfirmed whether the CC3200 sends SNI at
   all.

### Obstacle 2: the suripu jar is not in the repository

`orb-algo` builds with `COPY --from=jars`, a named build context pointing at
`infrastructure/suripu-app/target/suripu-app-0.6.0-SNAPSHOT.jar`, **81 MB**,
deliberately outside the public tree. Cloning the repo on the VM is not enough
to build it. Copy the jar across and set `SURIPU_JAR_DIR`, rather than shipping
a built image, so the VM can rebuild on its own.

### Obstacle 3: DNS cannot move to the VM

Whatever else moves, something on the home LAN has to answer `*.hello.is` for
the Sense, because the names are compiled in. A static entry in the router is
the clean version and removes the Mac entirely. A Pi running `dns_server.py` is
the fallback if the router cannot do it. The only way to remove this piece is to
flash the device, which is now a documented and recoverable operation rather
than a leap: see "Flashing a new URL" above.

## The order

Each step is independently useful and independently reversible.

1. ~~**Finish the four endpoints**~~ DONE 2026-08-27. `suripu-app` can be
   stopped: nothing the app asks for reaches it any more.
2. ~~**Device cutover at home. Watch a full night.** Then drop
   `suripu-service`, the five workers, `hello-time`, `messeji`.~~ DONE
   2026-08-27 and 2026-08-28. Three components remain: orb, orb-algo, Postgres.
   The order actually used, and the clock bug that nearly came with it, are in
   STATE.md gap 1.
3. **Stand up OCI**: Postgres, orb, orb-algo on arm64. Restore data. Run it in
   parallel with home before cutting anything over. Concretely:

   1. Docker on Oracle Linux 9.8. It ships podman, so this is docker-ce from the
      CentOS repo plus the compose plugin.
   2. Clone the repo. `scp` the 81 MB suripu jar and `secrets/` (the APNs
      `.p8`, `server.crt`, `server.key`). **`ca.key` stays at home**; it is only
      needed to issue certs, and there is no reason for the signing key to live
      on a public host.
   3. `make init && make build && make up`, then `make restore DUMP=...` from a
      fresh `make backup`.
   4. Leave the Mac serving the device while the VM has the data and answers
      `/ping`. Nothing has cut over yet.

   Two Oracle Linux specifics that cost an evening if unanticipated: firewalld
   blocks ports even after the OCI security list allows them, and SELinux needs
   `:z` on the bind mounts.
4. **App to the internet**: real domain, Let's Encrypt, rebuild the app for
   HTTPS. **The goal is met here.** Sleep tracking is reachable from anywhere,
   and the Sense has not been touched.
5. **Then the device**: point DNS at OCI, keep the same CA, issue a server cert
   with the new SANs. Flipping the router's DNS entry is the entire cutover, and
   flipping it back is the entire rollback.
6. **Firmware only if** step 5's dependency on a DNS override is genuinely
   unacceptable. This is no longer the cliff the original draft treated it as
   (see "Flashing a new URL"), but it is still the only step that needs a cable,
   and it buys one thing: deleting the home DNS entry.

The shape of that order is deliberate: the stated goal lands at step 4, and the
irreversible work is last and optional.

## Before step 4, settle these

- **The OAuth token endpoint becomes public.** There is one account and no
  reason for anyone to register. Read what the rate limiting actually does
  before it faces the internet rather than after. Note the known trap: a shared
  empty-IP bucket has silently broken app screens before.
- **Decide whether registration is reachable at all.** The cheapest control is
  not serving it.
- **Backups.** Today the only copy of the sleep history is a container on a Mac.
  A public host with 183 GB and no backup policy is a worse place for it, not a
  better one.
- **How the device endpoint is firewalled**, and what happens to it when the
  home IP changes. It is a DHCP address too. Either a dynamic-DNS updater
  driving the OCI security list, or WireGuard, or a decision to accept public
  exposure and rely on the per-message AES signing. That signing is real, but
  the pure-Python TLS stack in front of it is unaudited.
- **Which way the port collision is resolved**, because it decides whether the
  app lives on 443 and therefore whether the share links are presentable.
