# Why Sense OTA updates failed three quarters of the time

Written 2026-08-29, rewritten 2026-08-30 after reading the device's own logs
for the whole night: 34 armed attempts, 6 testing-mode boots, 26 boot records
lost. What is settled: the boot record silently reverts to its previous
contents across the arm-then-reset, content and image size are irrelevant, and
neither a delay nor sl_Stop() before the reset changes the odds. What is not
yet proven is the final mechanism; the standing suspect is the unchecked
sl_FsClose() commit (see the fix-attempts section at the bottom).

## Check this first: did the device actually download anything?

An offer that never becomes a download looks exactly like a failed install, and
it is a completely different fault. On the server:

    docker compose logs orb | grep "SERVING FIRMWARE"

No line means the device never fetched the image, so nothing about the boot
record or the bootloader is involved. In the device's own logs the tell is:

    "MCU image name converted to mcuimg2.bin"     <- and then nothing

A real attempt continues "Start downloading the file", "SHA Match!",
"change image status to IMG_STATUS_TESTREADY".

**The usual cause is that the download host does not resolve ON THE DEVICE'S
NETWORK.** `download_file()` calls `gethostbyname()` with the host from the
update row, using whatever resolver the device's network gave it. If that fails
the update is abandoned silently.

This cost 28 wasted attempts and about an hour on 2026-08-29. The Sense had moved
to a network with ordinary DNS, and `arm-firmware.sh` still defaulted the
download host to `sense-in.hello.is`, a name only the Mac's `dns_server.py` ever
answered. The script now refuses to arm against a host that does not resolve.

Note the asymmetry that makes this easy to miss: the host must resolve for the
DEVICE, not for the server. Checking it from the VM proves nothing if the device
is on a different network.

## The symptom

An update is offered, the device downloads it, verifies it, reboots, and comes
back running the firmware it already had. Nothing is damaged and nothing is
logged as an error. The server sees a successful download and then a device that
never changed version.

## What the device logs proved (2026-08-29 19:02 to 2026-08-30 01:40)

Run orb with `-debug` and the device's `/logs` uploads land in the server log
as `device log` lines, including everything it stored to SD across reboots.
That stream, captured in `~/ota-evidence.log` on the VM, is a working UART
console substitute and is what finally settled this. The night's totals:

- 34 attempts wrote `IMG_STATUS_TESTREADY` after a verified download.
- 26 of them read back `NOTEST` about 6 seconds later, on the next boot, with
  `ucActiveImg` unchanged and no testing boot in between. The boot record
  REVERTED. The 6 second gap matters: the running firmware carried the three
  second delay fix, and the write still lost.
- 6 booted in testing mode. At least 2 of those were NOT the new image: the
  post-boot log shows the OLD firmware's time host, meaning the bootloader
  wrote TESTING, loaded the new image, failed its SHA check against internal
  flash, and silently fell back (`ImageLoader` in
  `boot/application_bootloader/main.c` checks `Test()` but not `Load()`, and
  the fallback has no log). The remainder were real installs.
- The failures were content-independent. `land4522.log`: build 4522 failed 8
  consecutive attempts and landed on a later one. That build is byte-identical
  in kind to every "working" configuration; the earlier belief that builds
  with a long time host were the ones failing was a streak in a ~1-in-5
  lottery, kept alive by retry loops that hammered exactly those builds.

## The cause

`_WriteBootInfo()` in `kitsune/fatfs_cmd.c` begins by DELETING the boot record:

    sl_FsDel((unsigned char *)IMG_BOOT_INFO, ulBootInfoToken);
    ...
    ulBootInfoCreateFlag = FS_MODE_OPEN_CREATE(256,
        _FS_FILE_OPEN_FLAG_COMMIT|_FS_FILE_PUBLIC_WRITE);

then recreates it as a fail-safe file, writes and closes. All three of its
callers then call `mcu_reset()`, which is not a soft reset: it sends "bounce"
to the top board, which cuts power within about a second, and `sl_Stop()` is
never called. The authors' own note sits right there:

    //TODO make flush work on reset...

Per TI, a fail-safe file whose new version never commits keeps the PREVIOUS
version, which is why the record reads back as a clean `NOTEST` rather than
garbage: that is the last committed content. The open question is WHY the new
version so often fails to commit. The write-back-latency reading (NWP still
busy flushing the 146KB image) predicted that a delay or an sl_Stop() before
the reset would fix it; both were tried (v1, v2 below) and neither did, so
the commit is most likely failing at `sl_FsClose()` time, whose return the
code throws away. The torn images behind the SHA-fallback boots have the same
shape: an image file whose own close/commit did not take.

## A second, quieter bug: the fallback mis-commits the boot record

When the bootloader falls back after a failed SHA check, it leaves the status
at `TESTING` and boots the OLD image. `boot_commit_ota()` in the old image then
sees `TESTING`, assumes it is the freshly tested image, and "commits": it flips
`ucActiveImg` to the slot holding the BAD image and writes `NOTEST`. Nothing
breaks at runtime, because normal boots execute whatever is already in internal
flash and never reload by `ucActiveImg`, but the slot bookkeeping is now wrong:
the next OTA writes into the slot holding the last GOOD image. This is why slot
numbers alternated in confusing ways during the night. It also means the
firmware has no defense against committing an image that never ran; only the
bootloader knows which image it loaded, and it does not say.

## How the logs distinguish the outcomes

Lost boot record (26 of 34):

    WriteBootInfo: ucActiveImg=1, ulImgStatus=0x56788765   (TESTREADY)
    ... ~6s, reboot ...
    ReadBootInfo:  ucActiveImg=1, ulImgStatus=0xabcddcba   (NOTEST, reverted)

Testing boot, which is EITHER a real install or an SHA fallback:

    ReadBootInfo: ucActiveImg=1, ulImgStatus=0x12344321    (TESTING)
    Booted in testing mode
    WriteBootInfo: ucActiveImg=2, ulImgStatus=0xabcddcba   (committed + flip)

These two are byte-identical in the logs. The discriminator is which firmware
is running afterwards: check the version the device reports on its next sync,
or a host/behavior string unique to the new build. Do not trust the flip.

Both outcomes log `error opening file, trying to create` first. That is not a
fault: `_WriteBootInfo` deletes the file every time, so opening it for write
always fails and it always recreates it.

## The fix attempts, honestly labeled

v1 (`vTaskDelay(3000)`): present in the running builds on 2026-08-29, lost 26
of 34 boot records anyway. Refuted.

v2 (`vTaskDelay(1000); sl_Stop(30000)`): shipped in build 4526. TI's 1.9.2
driver waits forever for the NWP's stop acknowledgment before powering it
down, so if latency were the mechanism this had to work. Every 4527 arm made
through it failed the same two ways as before. Refuted, and with it the whole
timing-race framing.

That refutation points at write time instead: `_WriteBootInfo` checks NEITHER
`sl_FsWrite` fully NOR `sl_FsClose` at all, and for a fail-safe file the close
IS the commit; a failed close keeps the previous version by design, which is
exactly the observed clean revert, and no delay or shutdown ordering would
ever change it. `sl_FsClose` is a synchronous command whose return is the
NWP's own status, so the failure was observable all along and simply thrown
away.

v3 (current, shipped as `patches/ota-reliability.patch`, which `rebuild.sh`
applies with `git apply` when `KITSUNE_OTA_FLUSH_FIX=1`; first in build 4528):
- removes the `sl_FsDel` so the record is updated in place through the
  fail-safe machinery (Hello's own later master also dropped the delete,
  which reads like their fix for this same bug);
- logs `sl_FsWrite` and `sl_FsClose` returns, reads the record back at each
  reset site, and logs `sl_Stop`'s return;
- keeps the `sl_Stop` since it is correct hygiene regardless.

The generational trap applies to every version of this fix: the arm path that
matters is the one in the RUNNING image, so a fix only takes effect one flash
AFTER it lands, and landing it uses the broken path (retry until lucky).

## Outcome (2026-08-30)

v3 is a real improvement but is NOT reliable, and the earlier claim here that
it "works" was premature. Two windows tell the story:

- ~02:40-02:55 EDT: builds 4526, 4528, 4529, 4530 all installed on the FIRST
  attempt, four in a row against a ~1-in-5 baseline. That looked solved.
- ~15:20-20:05 EDT: build 4531 (4530 arming it, a v3 image) failed **14 of 14**
  attempts and never once entered testing mode. A user power cycle mid-run
  changed nothing.

The 4531 failures are the decisive data. Every attempt logged a CLEAN write:
`WriteBootInfo ... w 88 c 0` (close returned 0), the read-back confirmed
TESTREADY was on the record (`armchk rd 0 st 56788765`), and `slstop rv 0`
reported a clean NWP shutdown. Then the reset reverted the record anyway and
the bootloader booted the old image. So **a clean `sl_FsClose` AND a clean
`sl_Stop` still do not guarantee the fail-safe record commits to physical
flash before the power cut.** The close-failure theory is therefore also
incomplete: here the close did not fail and the record was lost regardless.

That it went 0-of-14 deterministically (not intermittently) suggests a
state, not a race: most likely the specific fail-safe mirror or the target
slot (every 4531 attempt targeted mcuimg3 / USER2 with ucActiveImg=1) is in a
degraded state after a night of dozens of boot-record writes. 4529/4530 may
have landed only because they happened to target the other slot. Unproven.

What is proven: the failure is NOT caused by the patch-file conversion (4531
is byte-identical to its Python build) nor by the commit-guard fix (which never
runs, because 4531 never boots). The device is safe on 4530 throughout.

The `-debug` /logs channel ALREADY answers the last question, no UART needed
(an earlier draft of this section wrongly reached for UART). The two hypotheses
leave different app-side fingerprints, and the logs show one of them cleanly.
Per attempt:

    WriteBootInfo: ucActiveImg=1, ulImgStatus=0x56788765 w 88 c 0   (TESTREADY, close=0)
    armchk rd 0 st 56788765 img 1                                   (read-back sees TESTREADY)
    ...reset...
    Start polling                                                   (rebooted)
    ReadBootInfo: ucActiveImg=1, ulImgStatus=0xabcddcba             (NOTEST)

`ucActiveImg` never moves and "Booted in testing mode" never appears, so the
bootloader booted normally: it read NOTEST. That is the REVERT case, not the
torn-image case (which would show a testing boot). Confirmed 14/14. So the
record is confirmed-written and confirmed-read-back as TESTREADY, close returns
0, sl_Stop returns 0, and it STILL reverts across the power cut. `close == 0`
is a false success signal for the fail-safe commit.

## Leading suspect, and why it is not conclusive

`_WriteBootInfo` opens the record like this (the patch removed the `sl_FsDel`
above it but left this untouched):

    if (sl_FsOpen(IMG_BOOT_INFO, FS_MODE_OPEN_WRITE, ...))     // open existing, NO commit flag
        ... sl_FsOpen(IMG_BOOT_INFO, FS_MODE_OPEN_CREATE(256, _FS_FILE_OPEN_FLAG_COMMIT|...))  // WITH commit flag

Originally the `sl_FsDel` deleted the file first, so the open-existing always
FAILED and the create-WITH-commit path always ran. Removing the delete means
the file persists, the open-existing SUCCEEDS, and the write goes through
`FS_MODE_OPEN_WRITE` with no commit flag. That is a plausible reason a close
returns 0 while the fail-safe mirror never swaps.

The hole in that theory: if the missing commit flag categorically broke the
commit, v3 would be 0-for-everything, and it went 4-for-4 first (4526, 4528,
4529, 4530). So the flag is not a clean on/off switch. The deterministic 0/14
came only after hours of writes to the same fail-safe file, which points more
at a degraded mirror state (or the specific mcuimg3/USER2 slot every 4531
attempt targeted) than at the open flag alone. Do not assert a single
mechanism here; three have already been wrong this project.

## How to continue (in order)

1. Let the device rest and arm ONE 4531 attempt in a clean window (no retry
   loop). Every success came early in a fresh window; the 0/14 came after
   hammering. This may just be transient fail-safe/slot state that settles,
   and it costs one reboot to find out.
2. If that fails, build a variant that FORCES the commit path (open the
   existing record with `_FS_FILE_OPEN_FLAG_COMMIT`, or create-with-commit
   without the delete) and land one to compare head-to-head. This needs one
   successful flash to break the chicken-and-egg, so do it after 1.
3. Only if both stall: reset the boot record / reprovision to clear a degraded
   mirror or corrupted slot. Needs care and probably UART or a console command.

The device is safe on 4530 (a working v3 image) throughout. Nothing here can
brick it: a failed OTA always falls back to the running image.

## Update: two clean single-shot flashes (2026-08-30 ~20:40 EDT)

After the 4531 0/14 run, reproduced the last known-good image (4530: v3
write-fix, hostname clock, NO commit guard, 147,416 bytes) as 4532 and 4533,
each a version-only bump, and flashed them with a GENTLE single-shot arm (arm
once, wait ~10 min, no rapid re-arming). Both installed on the FIRST attempt
(~90s each). So the flash pipeline is reliable, and the 0/14 was specific to the
4531 run, not an environmental block.

Two variables changed between 4531 (failed) and 4532/4533 (worked), so this does
not fully isolate the cause: 4531 carried the commit guard (+~300 bytes) AND was
flashed with a rapid loop (re-arm every 3 min) that may have degraded the
fail-safe/slot state. 4532/4533 removed the guard AND used gentle pacing. To
isolate: flash a guard-carrying image single-shot; if it lands, the hammering
was the culprit. Operationally: prefer the single-shot arm (~/flash-once.sh on
the VM) over the hammering loop.

Tally: installs 4526, 4528, 4529, 4530, 4532, 4533; the lone failure is 4531.

## Update: guard image fails single-shot, isolating SIZE/layout (2026-08-30 ~21:12 EDT)

Flashed 4534 = guard-carrying (full patch), 147,716 bytes (same as 4531),
single-shot gentle pacing. It FAILED the same as 4531: clean write `w 88 c 0`,
armchk TESTREADY, slstop 0, then NOTEST after reboot, ZERO testing boots. The
no-guard 4532/4533 (147,416 B) landed first-try under identical pacing.

Perfect size correlation: 147,416 installs (4530/4532/4533), 147,716 reverts
(4531/4534). This EXONERATES the guard logic: `boot_commit_ota` runs only after
the image boots, and these images never boot (0 testing boots), so the guard
code never executes. The differentiator is the image itself, ~300 bytes larger
and relaid-out. Pacing is also exonerated (single-shot failed).

Open question: raw byte count vs the guard build layout. Decisive test: build a
NO-GUARD image padded to 147,716 and flash single-shot. Fails => raw size
threshold near 147.5KB; installs => the guard build layout specifically. If it
is size, the practical fix is to keep images under ~147,416 (shrink the guard,
or find the boundary).

## Update: padding test kills the SIZE theory (2026-08-30 ~21:36 EDT)

Built no-guard 4535 (147,416 B), padded it with 300 zero bytes to exactly
147,716 (the failing size), flashed single-shot. It INSTALLED on the first arm.
Same size as the failing 4531/4534, no guard code -> raw byte size is NOT the
cause. The earlier "size correlation" was two variables moving together.

Standings after all controls:
- Pacing: RULED OUT (single-shot 4534 failed; single-shot 4532/4533/4535 passed).
- Guard runtime logic: RULED OUT (boot_commit_ota runs only post-boot; the
  failing images never boot, 0 testing boots).
- Raw image size: RULED OUT (padded 147,716 no-guard installed).
- Remaining: the guard BUILD specifically. Tally, guard image 0 installs in ~18
  attempts (4531 14/14 hammered + 4534 ~4 single-shot); no-guard write-fix 7/7
  first-try (4526/4528/4529/4530/4532/4533 + 4535-padded). 0-of-18 vs 7-of-7 is
  not plausibly probabilistic noise, so something about the guard build is bad,
  most likely the byte-exact reorder/link LAYOUT (this project has a history of
  link-module-order effects under -O4), by a mechanism not yet explained. Do NOT
  assert one; several mechanism guesses have already been wrong here.

## Practical conclusion

- The important fix (v3 boot-record write) flashes reliably; ship no-guard
  images (KITSUNE_OTA_PATCH=patches/ota-write-fix-only.patch).
- The commit guard fixes only a BENIGN bookkeeping bug (a fallback mis-commit
  that never causes a runtime fault) and its build somehow will not OTA-install.
  Not worth the trouble: drop or defer it until the layout mechanism is
  understood. Device currently on 4535 (no-guard, padded, healthy).
- Padding a .bin with trailing zeros is a safe, working way to hit a target
  size (the padded 4535 boots and runs normally).

## Practical consequences

- **A failed update costs a reboot and nothing else.** The device always comes
  back on working firmware.
- **Retrying is the remedy** on unfixed firmware. Measured: 6 testing boots in
  34 attempts, of which at least 2 were fallbacks, so under 1 in 5 installs.
- **Run orb with `-debug` during any OTA work.** The device explains itself in
  its `/logs` uploads; without `-debug` that explanation goes to `io.Discard`.
- **`ORB_OTA_MIN_UPTIME`** shortens the retry loop for supervised testing. The
  20 minute default is right for unattended updates.
- **Image size and content do not matter** to the failure rate beyond loading
  the NWP a little longer. Every content theory tested on 2026-08-29 (size
  cliff, slot, version ordering, added code, time-host string) died against
  the evidence; the writeup that once blamed the time host is wrong.
