# Byte-exact reproduction of Sense firmware build 4513 — full process

**Result:** `kitsune.bin`, SHA1 `0c5f639e1290df0e3a5f8641d670923ed71a5e63`,
146,864 bytes, rebuilt entirely from source and **byte-identical** to the
firmware read off the device and to the official `hello/kitsune` **1.9.2**
release.

This document explains what the firmware is, how we identified the exact
toolchain, the two environment problems that had to be solved, the root cause of
the long-standing 12-byte difference, and how to reproduce the whole thing with
one command.

---

## 1. What we are reproducing

| | |
|---|---|
| Product | Sense (original, non-voice) — TI CC3200 "mid board" |
| Firmware repo | `hello/kitsune` |
| Release | tag **1.9.2**, commit `59d5c2ea08af23e21ff2bd6f076382eac058cc99` |
| Build label | **KIT_VER 4513** (the Travis build number, stamped into `kitsune_version.h`) |
| Artifact | `exe/kitsune.bin`, 146,864 bytes |
| SHA1 | `0c5f639e1290df0e3a5f8641d670923ed71a5e63` |

The `.bin` is the four loadable sections concatenated in address order:
`.intvecs (0x400) + .text (0x1fce8) + .const (0x38f8) + .cinit (0x3d0)`.
It carries **no** build-path strings or timestamps, which is what makes a
byte-exact match achievable at all.

We already held two byte-exact copies before this effort (the on-device flash
dump and the release binary); the goal here was to **regenerate it from source**,
proving we fully control the build.

---

## 2. Identifying the exact toolchain

The original build ran on Travis CI (`.travis.yml` → `travis-setup.sh` →
`build.sh`). `travis-setup.sh` fetched a now-dead Dropbox tarball
(`travisti.tar.gz`) containing a pre-installed TI Code Composer Studio, so the
exact versions were not written down anywhere. We recovered them forensically:

- **Compiler = TI CGT ARM 5.1.5.** The release ELF embeds
  `/home/travis/ti/ccsv6/tools/compiler/arm_5.1.5/…` in its metadata, and the
  repo's own `.cproject` files pin `OPT_CODEGEN_VERSION = 5.1.5` in the active
  build configurations. 5.1.5 is a single unique compiler build (the standalone
  update-site package and the CCS 6.0.0 bundle copy are byte-identical).
- **Builder = CCS 6.x headless.** `build.sh` drives
  `com.ti.ccstudio.apps.projectImport` and `…apps.projectBuild` — the Eclipse
  "apps" that read the `.project`/`.cproject` and generate + run the makefiles.

Two compiler flags mattered and were easy to get wrong:

- TI `armcl` defaults to **big-endian**; Cortex-M4 needs `-me` (`--little_endian`).
  Every build missing this was silently wrong-endian.
- With whole-program optimization, **`-O4` must be passed to the linker too**,
  not just the compiles — that is what triggers the whole-program codegen pass
  (below), and getting it onto the link line is what first shrank `.const` to an
  exact match.

---

## 3. Environment problem #1 — XPCOM only runs on a 32-bit kernel

CCS's project/build apps initialize a **32-bit XPCOM** native runtime (Mozilla's
component system). On Apple Silicon:

- An `amd64` container runs under **Rosetta**, which cannot execute the 32-bit
  x86 XPCOM code → `XPCOMInitializationException 0x80004005`, every time.
- A pure **`linux/386`** container runs under a single qemu-i386 layer with a
  true i386 kernel personality → XPCOM initializes cleanly.

So the entire build runs in a `--platform linux/386` Debian container. This is
baked into the reproducer's `Dockerfile`.

## 4. Environment problem #2 — cross-container contamination

Several build attempts bind-mounted the **same** `/src` source tree into
different containers (CCS 6.0.0 vs 6.0.1). Whichever container ran last
overwrote the CCS-**generated** makefiles in the source tree, so section-size
comparisons between "versions" were actually comparing whichever generator wrote
last. Fix: one image, one clean checkout per run (the reproducer clones into a
fresh temp dir every time). Also noted: CCS's generated rules pass `-g` on every
compile in **both** 6.0.0 and 6.0.1 — debug info was never the differentiator.

---

## 5. Root cause — link *module order*, not the compiler

With `-O4`, the TI linker **re-runs code generation over the whole program**.
In that mode the exact order in which object files appear on the link line, and
the order of members inside each `.a`, changes literal-pool and alignment
**padding** on a per-function basis.

Symptom: our build was `.text = 0x1fcf4` vs the reference's `0x1fce8` — **12
bytes** — with `.intvecs`, `.const`, `.cinit` already byte-exact. A symbol-level
diff (see `drift.py` / `gapsize.py`) showed the 12 bytes were **13 functions
each off by exactly ±4 bytes** (net +12). ±4 is the fingerprint of alignment
padding, i.e. *placement*, not different code.

**Recovering the reference's true link order.** TI stamps a `$NN` suffix on
static symbols where `NN` is the module's index in the whole-program build. We
read those suffixes straight out of the release ELF (`fileorder.py`) and
reconstructed the exact order the CI linker used:

- **subdirectory** objects first, in this group order:
  `common, debugutils, hashtable, nanopb, protobuf, tests, tinyhttp`
- then the **root-level** objects in **reverse-alphabetical** order
  (`wifi_cmd … aes`)
- and each **library archive** re-ordered **reverse-alphabetical** too
  (`timers.obj` first in `FreeRTOS.a`, `wdt.obj` first in `driverlib.a`,
  `wlan.obj` first in `simplelink.a`)

Our CCS 6.0.1's makefile generator emitted a *different* order (root objects
first, alphabetical). Same compiler, same code, 12 bytes of different padding.

**The fix** (`relink_reforder.sh`): re-archive the three multi-object libraries
reverse-alphabetically, then relink the freshly built app objects in the
reference order above. Result:

```
.intvecs 0x400    bytes_equal=True
.text    0x1fce8  bytes_equal=True   <- 12-byte gap closed
.const   0x38f8   bytes_equal=True
.cinit   0x3d0    bytes_equal=True
=> kitsune.bin SHA1 0c5f639e…  BYTE-EXACT
```

Things that turned out **not** to matter: CCS point release (6.0.0 vs 6.0.1;
6.0.0 actually produced a *worse*, different `.const`), `-g` debug info, and any
`.cproject` flag beyond the ones in §2.

---

## 6. Reproduce it — one command, any system

```bash
cd full-instructions/sense/firmware/kitsune-4513
./rebuild.sh
# -> ./out/kitsune.bin  sha1 0c5f639e1290df0e3a5f8641d670923ed71a5e63
```

`rebuild.sh`:
1. builds the `linux/386` toolchain image from `Dockerfile` + `toolchain/`
   (first run only, ~5 min),
2. clones kitsune into a fresh temp dir, checks out `1.9.2`, and populates the
   `heap_6` / `tinyhttp` submodules from the vendored bundles in `submodules/`,
3. runs `build_in_container.sh` inside the container: stamp `KIT_VER 4513` →
   lay down the CCS-generated makefiles → **`gmake` each project** (libs in CI
   order, then the app) → verify library object counts → `relink_reforder.sh` →
   `extract_bin.py` → **verify SHA1**,
4. copies `out/kitsune.bin` and `out/kitsune.out`.

The build scripts are also bind-mounted into the container at run time (not only
baked into the image), so edits to them take effect without rebuilding the image.

### Why gmake instead of Eclipse

The Travis CI drove the build with the Eclipse "apps"
(`com.ti.ccstudio.apps.projectImport` / `projectBuild`). Those work, but their
32-bit XPCOM runtime is **unreliable under qemu emulation** — it prints
`XPCOMInitializationException 0x80004005` and intermittently *wedges the whole
container* mid-import (the process table becomes unresponsive). So the reproducer
skips Eclipse entirely and runs the **CCS-generated makefiles** with `gmake`.
Those makefiles are the exact build orchestration Eclipse would run — the same
`armcl`/`armar` command lines — captured from a known-good import and vendored in
`generated_makefiles.tar.gz` (all of them, including the per-source-subdirectory
`subdir_rules.mk` files, and simplelink's `OS` config). This is faster and
deterministic, and it is what makes the rebuild reliable on an arbitrary host.

**Three non-obvious build traps the pipeline handles:**

- *Two sources are git submodules.* `heap_6`
  (`third_party/FreeRTOS/source/portable/MemMang`, upstream `hello/heap_6.git`)
  and `tinyhttp` (`kitsune/tinyhttp`, upstream `mendsley/tinyhttp`) are
  **submodules**. A plain `git clone` leaves them empty, and their absence
  *silently truncates the build*: `oslib` stops when `heap_6.c` is missing
  (`No rule to make target heap_6.c`), producing only 9 of 14 FreeRTOS objects,
  and the kitsune app link loses the tinyhttp objects. Because the
  `hello/heap_6` remote is gone, the exact pinned commits at 1.9.2
  (`d1f85e5` heap_6, `9abdeff` tinyhttp — the ones that build byte-exact) are
  **vendored here as git bundles** under `submodules/`. `rebuild.sh` populates
  them offline after checkout. *This was the single biggest reproducibility
  trap: a clean clone builds a smaller, wrong binary with no error unless you
  check the object count.* The pipeline hard-fails if `FreeRTOS.a` has fewer
  than 14 objects.
- *Per-subdirectory makefiles.* CCS emits a `subdir_rules.mk` + `subdir_vars.mk`
  in **each** source subdirectory of the app (`common/`, `protobuf/`,
  `hashtable/`, `nanopb/`, `tinyhttp/`, `debugutils/`, `tests/`). All of them
  must be vendored, or `gmake` compiles only the top-level sources (57 of 95
  objects) and the link dies with `unresolved symbols remain`.
- *Missing output directories.* A bare `gmake` (no Eclipse project setup) does
  not create the `exe/` archive/link output dirs, so the pipeline `mkdir -p`s
  `simplelink/ccs/exe`, `oslib/ccs/exe`, and `kitsune/main/ccs/exe` first, and
  pre-creates the app's object subdirectories.

**Requirements:** Docker with 32-bit (386) emulation. On Apple Silicon or any
non-x86 host, enable it once:

```bash
docker run --privileged --rm tonistiigi/binfmt --install 386
```

Under emulation the full compile+link takes roughly 30–90 min. On a native
x86-64 host it is far faster.

**Overrides** (environment variables): `KITSUNE_REPO`, `KITSUNE_TAG`, `KIT_VER`,
`EXPECT_SHA`, `IMAGE`, `PLATFORM`. Building a different tag/build number is just
`KITSUNE_TAG=… KIT_VER=… EXPECT_SHA=… ./rebuild.sh` (the reorder step is
version-independent; only the expected hash changes).

### Toolchain bundle

The only vendored TI binary is **`toolchain/cgt-5.1.5.tar.gz`** (~55 MB) — the
CGT ARM 5.1.5 **compiler** tree (`armcl`, `armar`, `armlnk`, headers, libs). The
`Dockerfile` unpacks it to `/opt/cgt-5.1.5` and recreates the one path the
generated makefiles expect — a symlink
`/root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5` → the compiler. Nothing else
from CCS is needed: the build is driven by the container's GNU **Make 4.3**,
which is byte-identical to the `gmake` CCS bundled.

(The original build used the full CCS 6.x IDE, but this reproducer drives the
generated makefiles directly, so the ~300 MB Eclipse/JRE tree was dropped —
that's what keeps every file here under GitHub's 100 MB limit.)

**Provenance and licensing.** The compiler is TI's proprietary CGT 5.1.5, *not*
built here — it's TI's redistributable, publicly downloadable from the TI codegen
archive (`software-dl.ti.com/codegen/non-esd/downloads/download_archive.htm`,
installer `ti_cgt_tms470_5.1.5_linux_installer_x86.bin`). Vendoring it keeps the
rebuild one-command and offline, but it is TI-licensed: if this repo is public,
prefer to **not** commit it — `.gitignore` `toolchain/` and either run that
installer once (`--prefix /opt/cgt-5.1.5`) or drop the extracted tree in place.
The tarball can be regenerated from a working container with
`tar czf cgt-5.1.5.tar.gz -C /opt cgt-5.1.5`.

---

## 7. Files in this directory

| File | Purpose |
|---|---|
| `rebuild.sh` | **Entry point** — one-command byte-exact rebuild on any Docker host |
| `Dockerfile` | `linux/386` build image: CGT 5.1.5 compiler + GNU make (no CCS/Eclipse) |
| `build_in_container.sh` | In-container pipeline: gmake the makefiles → reorder → extract → verify |
| `relink_reforder.sh` | Re-archive libs + relink app in the reference module order (the fix) |
| `extract_bin.py` | ELF → `.bin` by section concatenation (tiobj2bin equivalent) |
| `toolchain/cgt-5.1.5.tar.gz` | TI CGT ARM 5.1.5 compiler tree (the only vendored TI binary) |
| `submodules/*.bundle` | Vendored `heap_6` / `tinyhttp` submodules at their 1.9.2 pinned commits |
| `generated_makefiles.tar.gz` | The CCS-generated makefiles for all 5 projects (the build orchestration gmake runs) |
| `ci_exact_build.sh` | Legacy Eclipse `projectImport`/`projectBuild` driver — documents the original CI method; superseded by the gmake path because Eclipse/XPCOM wedges under emulation |
| `kitsune_reord.out/.bin` | The verified byte-exact artifacts from the first successful run |
| `elfdiff.py` | ELF section/symbol reader used by the analysis tools |
| `drift.py` | Locate where `.text` addresses start diverging between two builds |
| `gapsize.py` | Per-function size (via address gaps); lists the ±4-byte functions |
| `fileorder.py` | Recover whole-program module order from `$NN` static-symbol suffixes |
| `funcmp.py` | Byte-compare one named function between two ELFs |
| `README.md` | Short summary (this file is the long form) |
```
```
