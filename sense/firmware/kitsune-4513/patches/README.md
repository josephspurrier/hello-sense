# Firmware patches

Source-level changes applied to the pristine `hello/kitsune` **tag 1.9.2**
checkout before the byte-exact build. `rebuild.sh` applies these with
`git apply` when the matching environment variable is set; with none set the
build stays byte-identical to the official 4513 image.

These are real unified diffs, authored by editing the actual 1.9.2 source and
capturing `git diff`, so they are compiler-checkable at author time and review
like any code change. That is the whole reason they are files and not string
substitutions in the build script: the earlier regex approach could not be
compiled while being written, and mistakes (a wrong struct field, a mangled
backreference, tab-exact whitespace) only surfaced inside the build container.

## `ota-reliability.patch`

Applied when `KITSUNE_OTA_FLUSH_FIX=1`. Two independent OTA correctness fixes in
`kitsune/fatfs_cmd.c`, both explained in full in
`knowledgebase/OTA-RELIABILITY.md`:

1. **Boot-record write fix.** `_WriteBootInfo()` no longer deletes and recreates
   the fail-safe boot record on every write; it updates it in place, and its
   `sl_FsWrite`/`sl_FsClose` returns are logged. This is what made flashing
   reliable (builds before it installed roughly one attempt in five). Each of
   the three reset sites also reads the record back and calls `sl_Stop()` with
   its return logged before `mcu_reset()`.

2. **Commit guard.** `boot_commit_ota()` promotes the new slot only when the
   image actually running in internal flash matches the SHA recorded for that
   slot, mirroring the bootloader's own `Test()`. This stops the old firmware
   from "committing" a slot after the bootloader rejected a torn image and fell
   back, which used to corrupt the active-slot bookkeeping.

## `ota-write-fix-only.patch`

The boot-record write fix WITHOUT the commit guard. Historical: it was cut to
isolate why guard builds would not install; the real culprit turned out to be
extract_bin.py dropping section alignment gaps (fixed 2026-08-30, see
OTA-RELIABILITY.md), and the guard is fine, first shipped working in build
4539. Kept because a smaller variant is occasionally useful near the link
limit. Select with `KITSUNE_OTA_PATCH=patches/ota-write-fix-only.patch`.

`rebuild.sh` applies `${KITSUNE_OTA_PATCH:-patches/ota-reliability.patch}`, so
the default build gets both fixes and this variable overrides which patch runs.

## `ota-guard-inert.patch`, `ota-write-fix-durable.patch`

Diagnostic variants from the 2026-08-30 investigation. `ota-guard-inert` keeps
the guard's code linked but unreachable (layout probe). `ota-write-fix-durable`
replaces the reset-site check with `_commit_boot_record_durably()`: write the
record, `sl_Stop` -> `sl_Start` (a fresh NWP reads real flash, not cache),
re-read, rewrite until it sticks (logs `armchk2 ...`). Proven in build 4538.
Not folded into the default patch: the verification costs several seconds per
reset and the images are a few hundred bytes from the link limit.

## What is NOT a patch

The endpoint and time-host rewrites (`KITSUNE_PROD_DOMAIN` /
`KITSUNE_DEV_DOMAIN` / `KITSUNE_TIME_HOST`) stay as runtime string
substitutions in `rebuild.sh`, not patches, on purpose: they inject a private
domain that must not be committed, and the value differs per user. A patch
would bake one person's hostnames into a tracked file.

## Re-generating or editing a patch

```sh
git clone <kitsune> src && git -C src checkout 1.9.2
# edit src/kitsune/*.c, compile-check it
git -C src diff > path/to/patch
```

`rebuild.sh` applies with `git apply --whitespace=nowarn` (the 1.9.2 tree has
some space-before-tab indentation the reset-site edits inherit; that is
faithful to the shipped firmware and not worth normalizing).
