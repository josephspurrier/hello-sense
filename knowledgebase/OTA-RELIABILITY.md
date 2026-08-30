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

## Outcome (2026-08-30, ~02:40-02:55 EDT)

v3 works. Builds 4529 and 4530, both armed and flashed through a v3 image,
installed on the FIRST attempt, in 80 and 41 seconds respectively, with every
logged return clean: `WriteBootInfo ... w 88 c 0`, read-back confirming
TESTREADY on the record before the reset, `slstop rv 0`, and no
"error opening file, trying to create" (the no-delete path in use). Counting
4526 and 4528, the night ended with four consecutive one-shot installs against
a historical baseline of about one in five.

Because v3 changed two things at once (no-delete AND kept sl_Stop), and no v3
attempt has ever failed, the close-failure theory was never observed directly;
it remains the best explanation of the old behavior rather than a proven one.
If an OTA ever fails again, the w/c/slstop numbers in the device log are the
first thing to read.

The four hostname-clock builds (4527-4530) also settle the old red herring for
good: 4528-4530 run `time.orb.example.com` and sync time fine.
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
