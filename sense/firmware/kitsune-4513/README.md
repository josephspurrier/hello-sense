# Byte-exact rebuild of Sense firmware build 4513 (kitsune 1.9.2)

Rebuilds the Sense (non-voice, CC3200) firmware **byte-for-byte** from source:
`kitsune.bin`, SHA1 `0c5f639e1290df0e3a5f8641d670923ed71a5e63`, 146,864 bytes —
identical to the on-device flash dump and the official 1.9.2 release.

## Rebuild it

```bash
./rebuild.sh          # -> out/kitsune.bin  (verifies the SHA1 for you)
```

Needs Docker with 32-bit (386) emulation. On Apple Silicon / non-x86, once:

```bash
docker run --privileged --rm tonistiigi/binfmt --install 386
```

Everything is self-contained (toolchain and submodules are vendored here), so it
runs offline on any Docker host. First run builds the image (~5 min); the build
itself is ~30–90 min under emulation, far less on native x86-64.

## How it works (short version)

- **Toolchain:** TI CGT ARM **5.1.5** (the compiler) driven by GNU make, in a
  **linux/386** container. Only the compiler is vendored (`toolchain/cgt-5.1.5.tar.gz`,
  ~55 MB); the original CCS/Eclipse IDE isn't needed because the build runs the
  CCS-generated makefiles directly. See [PROCESS.md](PROCESS.md) for the licensing
  note on the TI compiler.
- **Source:** `hello/kitsune` tag `1.9.2`, `KIT_VER 4513`, with the `heap_6` and
  `tinyhttp` **submodules** populated from `submodules/` (their upstream
  `hello/heap_6` remote is gone; missing them silently builds a wrong, smaller
  binary).
- **The key fix:** under `-O4` the TI linker re-runs whole-program codegen, so
  **link module order** changes per-function padding. Building in the CI's exact
  order (subdir objects first, then root objects reverse-alphabetical; archives
  reverse-alphabetical) closes what was a stubborn 12-byte `.text` difference to
  zero. See `relink_reforder.sh`.

**Full write-up, root-cause analysis, and the forensic tools: [PROCESS.md](PROCESS.md).**
