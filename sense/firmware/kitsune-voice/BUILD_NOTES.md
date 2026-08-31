# CC3220SF "Sense with Voice" byte-exact build — working notes

This directory is a fork of `../kitsune-4513` (the byte-exact CC3200 orb recipe)
retargeted at the **Sense with Voice (CC3220SF)** application firmware. This file
tracks what is done and what remains. The orb recipe's `PROCESS.md` is the
methodology reference; only the deltas are recorded here.

## Target

| | Value |
|---|---|
| Byte-exact reference | `reference/pvt-kitsune.bin` |
| Size | **360,984 bytes** |
| SHA1 | **`90453011b136916d3b67aa302e86b7526c074420`** |
| Source commit | **`fab199b305a8c270550969fd2b5bd1a19f62f881`** (tag `1p5_ext_rc7`, Travis build 6149) |
| Branch lineage | `origin/sensevoicelts` (voice LTS) is this + production tweaks; `master` = tag 2.0.9.1 |
| On device | `/ota/mcuimg1.bin`, loaded **plain/unsigned** by the signed boot manager |

The device's *running* `/ota/mcuimg1.bin` (md5 `107f0177…`) is a slightly LATER
build than this pvt snapshot (code region differs; trailing LSTM model + cinit
tail identical). We validate the recipe against the pvt snapshot, which has a
known commit, then build custom on top. `reference/pvt-kitsune.out` (the ELF) and
`pvt-kitsune.map` are the forensic references for link order.

## Delta from the CC3200 orb recipe

| Item | Orb (kitsune-4513) | Voice (here) | Status |
|---|---|---|---|
| Checkout | tag `1.9.2` | `1p5_ext_rc7` / `fab199b3` | rebuild.sh defaults updated ✅ |
| KIT_VER | 4513 | 6149 | ✅ |
| EXPECT_SHA | 0c5f639e… | 90453011… | ✅ |
| Compiler | CGT 5.1.5 | CGT 5.1.5 (map confirms) | reuse ✅ |
| Container / method | linux/386, make-driven CCS mk | same | reuse ✅ |
| Submodules | heap_6 d1f85e5, tinyhttp 9abdeff | **identical** on voice branch | bundles reused ✅ |
| Linker cmd file | `cc3200v1p32.cmd` | **`CC3220sf.cmd`** (flash 0x0100A000, 1MB, 256KB SRAM) | relink_reforder.sh TODO |
| Extra define | — | **`__SF_DEBUG__`** | TODO |
| Endpoints | DATA + MESSEJI | DATA + MESSEJI + **SPEECH** | rewrite TODO |
| OTA patch | fatfs_cmd.c boot-record | N/A (CC3220SF image format differs) | inert (flag unset) |
| Target size | 146,864 B | 360,984 B | — |
| Deploy | 3-slot mcuimgN + boot record | replace plain `/ota/mcuimg1.bin`, no signing | — |

## STATUS 2026-08-31 (session 2): ✅ BYTE-EXACT

`kitsune.bin` == the reference: 360,984 bytes, **SHA1
`90453011b136916d3b67aa302e86b7526c074420`**. The recipe compiles all 106 app objects
+ 4 SDK archives + a custom vfplib RTS lib and links a byte-identical image.

### The two final pieces (both READ from `.cproject`, not guessed)
1. **`tensor/` folder-level `-O2`.** `.cproject` has a `<folderInfo resourcePath="tensor">`
   that sets `OPT_LEVEL=2` for every file in `kitsune/tensor/`. `net_stats.c` and
   `keyword_net.c` have no per-file override, so they inherit the *folder's* −O2 — I had
   compiled them at the base −O1. (I'd only checked base + per-file settings, never the
   folder level.) −O2 changed their register allocation to match (e.g.
   `write_activations` goes from `{R5,R6,R7}` narrow to `{R6,R7,R12}` wide, exactly the
   reference). The `tinytensor_*` files already got −O2 via their own per-file overrides.
2. **`--minimize_trampolines=postorder`.** The `.cproject` linker options include
   `MINIMIZE_TRAMPOLINES=postorder`; my hand-built relink lacked it. This reorders `.text`
   function subsections in call-graph postorder (to keep callers/callees close and cut
   trampolines) — the reason the reference `.text` is interleaved across objects rather
   than grouped. Without it, my function order matched the reference in only 1/1360
   positions; with it, the whole image snapped to byte-exact. (Also confirmed present and
   already matched: `--trampolines=on`, `--rom_model`, `--unused_section_elimination=on`,
   `--compress_dwarf=on`, and the library order fatfs→FreeRTOS→simplelink→driverlib→rts.)

Lesson: the authoritative build spec is the `.cproject` — base folderInfo **and** any
per-folder `<folderInfo>` **and** per-file `<fileInfo>` for the compiler, plus the
`<tool ...linker...>` options for the link. Reading all three levels (not just base +
per-file) is what closed it.

**The `--opt_for_cache` breakthrough:** the `.cproject` base config sets
`OPT_FOR_CACHE value="true"` (I had missed it, like OPT_FOR_SPEED earlier). Adding
`--opt_for_cache` to every app compile made `tiny_tensor_get_descaling` (a 31-iteration
loop, the single +164 outlier) go **exactly byte-exact (332=332)** and dropped `.text`
from +328 to +172. The SDK projects driverlib/oslib/fatfs also carry
`OPT_FOR_CACHE=true` (simplelink does not) — added to their makefiles in the tar (a
no-op on output but faithful). Also learned: `-mt` in the `.cproject` is `THUMB_STATE`
(--thumb_state, enable 16-bit code), redundant with `--code_state=16` (not MULTITHREAD);
adding it is byte-neutral.

**What the remaining +172 B is:** NOT missing code — the app object `.text` total,
every SDK archive, and all trampolines match. It is **alignment-padding holes**: the
reference `.text` has 267 `--HOLE--` gaps (510 B) vs mine 178 (342 B), a +168 B
difference, almost all before APP functions. The holes cascade from a handful of
app functions whose code is off by ±2..4 B — the seeds are in `net_stats.c`
(`write_activations` +4, `encode_histogram_counts` +4, `net_stats_update_counts` +2,
all ref-bigger) and `keyword_net`/`map_protobuf_keywords` (−2..−4). Each early ±2 B
shifts every downstream function's offset, flipping many on/off a 4-byte boundary and
changing whether a padding hole is inserted. So closing these ~5 tiny function deltas
should collapse the whole cascade to byte-exact. They are at the correct source/opt
(`kitsune/tensor` per `.project`, −O1 default + opt_for_cache); the ±4 B is a
sub-function codegen nuance needing instruction-level disassembly (armofd/dis of, e.g.,
`net_stats.obj:write_activations` vs the reference) to identify — likely another
`.cproject` compiler option not yet translated, or a struct/enum-size header effect
in the protobuf-encoding path.

### What it took (each was a real blocker; full detail in the auto-memory
[[project-voice-sense-build-recipe]]):
1. **Everything is vfplib (software float), not FPv4SPD16.** Reference ELF has NO
   `Tag_ABI_VFP_args`/`Tag_ABI_FP_arch` (only `FP_number_model=1`). SDK makefiles must
   use `--float_support=vfplib`; the orb's used FPv4SPD16. A vfplib link needs a vfplib
   RTS lib — the linker auto-builds the STANDARD hard-float `rtsv7M4_T_le_eabi.lib`
   (VFP_args=1), incompatible → #16004 then unresolved memcpy/etc. Fix in
   `build_in_container.sh`: pre-build it with `mklib … --options="… --float_support=vfplib
   … --fp_mode=relaxed"` into `/src/rtslib` and name it explicitly on the link line
   (`-l/src/rtslib/…`); the `-i` search path is not enough.
2. **App object set = 106**, derived from `pvt-kitsune.map` (bare `foo.obj (.text…)`
   lines) and mapped to sources via the `.project` linkedResources (all duplicate
   basenames resolve to the `kitsune/` ROOT copies, NOT `main/ccs/{hlo,crypto,tensor}`;
   the 9 mad files come from `kitsune/mad/`). Saved as `reference/app_objs.json`.
   `voice_compile.inc` / `voice_relink_objs.inc` are generated from that map.
3. **App flags** (from `.cproject` base folderInfo): `-O1 --opt_for_speed=0
   --float_support=vfplib --fp_mode=relaxed --no_inlining --gcc` + the 9 defines
   (incl `__SF_DEBUG__`). `--no_inlining` is REQUIRED (else plain `inline`
   `pn_get_next_bit` isn't emitted → unresolved; and codegen differs everywhere).
   **Per-file overrides (both OPT_LEVEL and OPT_FOR_SPEED, from `.cproject` fileInfo):**
   commands/wifi_cmd = `--opt_level=off`; led_cmd/pcm_handler = `-O1 --opt_for_speed=5`;
   fft/tinytensor_lstm_layer/tinytensor_features = `-O2` (speed 0);
   tinytensor_{tensor,math,net,fullyconnected_layer} = `-O2 --opt_for_speed=5`. The
   `--opt_for_speed=5` overrides were the key fix that closed `.ramcode` (+240→−12)
   and most of `.text` (DMAPingPongCompleteAppCB_opt in pcm_handler etc.).
4. **SDK archive opt levels differ per project** (each `ccs/.cproject`, NOT the orb's
   -O4): driverlib `-O1` (defines `ccs`+`DRIVERLIB_APPS` only, NO `TARGET_IS_CC3200`,
   NO `--no_inlining`), simplelink `--opt_level=off`, oslib/FreeRTOS `-O1`, fatfs `-O1`,
   all vfplib+relaxed. `netutil.c` is missing from the orb's simplelink OS makefile
   (needed for `_SlNetUtilHandleAsync_Cmd`); added to subdir_vars.mk OBJS +
   subdir_rules.mk rule + the makefile's `ORDERED_OBJS` (the archive uses ORDERED_OBJS).
   These live in the vendored `sdk_makefiles.tar.gz`.
5. **`extract_bin.py` was broken for the voice** and is now rewritten to use ELF LOAD
   **program headers (p_paddr)** instead of a hardcoded `.intvecs/.text/.const/.cinit`
   list. The voice image also has `.resetVecs`, `.ramcode` (RAM-run/flash-load), and
   `.weights`; the old extractor missed `.ramcode` and zero-filled `.weights`.
   Validated: the new extractor on `reference/pvt-kitsune.out` reproduces 360,984 bytes
   and SHA1 `90453011…` exactly.
6. **Link order is irrelevant here** — CC3220sf.cmd places sections deterministically;
   reordering `voice_relink_objs.inc` produced byte-identical output. (Only 16 `$NN`
   suffixes exist = static-var collisions, NOT `-pm` whole-program ordering, so
   `fileorder.py` does not apply — unlike the orb's -O4 case.)

### Remaining work
None for byte-exactness — achieved. The register-allocation difference that earlier
looked like an "irreducible floor" (`net_stats:write_activations` picking `{R5,R6,R7}`
vs the reference's `{R6,R7,R12}`) was NOT a compiler quirk: it was `net_stats.c` being
compiled at −O1 instead of the `tensor/`-folder −O2. At −O2 the allocator makes the
reference's choice. Lesson logged: when a single file's codegen differs on unambiguous
source, check the **folder-level** `.cproject` config, not just base + per-file.

Custom builds (your server / own build number) reuse the same recipe; only the domain
substitution and `KIT_VER` change. Deploy is `mcuimg1.bin` replaced plain in `/ota/`
(no signing) — see the STATUS/DELTA sections above and [[project-voice-sense-cert-swap]].

## Build command (once makefiles exist)
```
cd full-instructions/sense/firmware/kitsune-voice
./rebuild.sh                 # -> out/kitsune.bin, verify SHA1 90453011…
# custom (your server, own build number):
KITSUNE_PROD_DOMAIN=example.com KIT_VER=<n> ./rebuild.sh
```
