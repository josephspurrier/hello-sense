# Sleep Pill v1 firmware — the ANT fix, from source

**Result:** a source-built `pill+pill_PVT1` firmware (tag **1.0.3**) whose **ANT
radio works**, so the pill can pair with and report to a Sense. This is the fix
for a subtle regression where a naively-chosen build (tag 1.2.1) has working BLE
but **dead ANT** and silently never pairs.

`out/pill+pill_PVT1.bin` — version `1.0.3`, 42,568 bytes, SHA1
`e8475ef0cb027e4521a143d88d833a8eca3fbf6c`.

This is for the **original Sleep Pill v1** (`pill_PVT1`, Nordic nRF51422, the pill
that ships with the non-Voice Sense). It is *not* for the Pill 1.5 (`pillx_DVT1`,
the Voice-Sense pill) — the two have incompatible GPIO maps; see
[`../../PILL_RECOVERY.md`](../../PILL_RECOVERY.md).

---

## 1. The symptom

Recover a v1 pill, build `pill+pill_PVT1` at the "obvious" tag (1.2.1, the last
one that still compiles for this target), flash it — and BLE comes up (a scan
shows `Pill-XX`) but the pill **never pairs** with the Sense. Every pairing
attempt ends in a ~30 s ANT timeout, and no `POST /in/pill` data ever arrives.
Meanwhile the *factory* image pairs fine.

The trap: **the Sense finds and talks to the pill over ANT, not BLE.** A live BLE
scan proves only that the BLE stack runs; it says nothing about ANT. So a build
with working BLE and broken ANT looks "mostly alive" while being useless for its
actual job.

## 2. Root cause — an ANT transmit-scheme change

The pill's ANT transmit path was rewritten from **asynchronous** to
**synchronous** mode in kodobannin commit `cb4becff` ("changed from async to
synchronous transmit mode", 2016-03-07), which first ships in tag **1.1.1**:

- **Async** (tags ≤ 1.0.3): `ant/ant_driver.c` transmits with
  `CHANNEL_TYPE_MASTER_TX_ONLY` + `EXT_PARAM_ASYNC_TX_MODE` and never opens a
  periodic channel — a background broadcast.
- **Sync** (tags ≥ 1.1.1, including 1.2.1): `CHANNEL_TYPE_MASTER` +
  `sd_ant_channel_open`, a periodic channel with a real ANT period.

The decisive fact: **the pill and the Sense's own nRF51 "morpheus" board are
built from the same kodobannin `ant/ant_driver.c`**, so the two ends must use the
same scheme. A factory pill on the *async* scheme pairs with our Sense; a
source-built *1.2.1* pill on the *sync* scheme does not. That means the Sense's
morpheus is on the async scheme — so the pill must be too.

**It is not the ANT network key.** The real key `A8 AC 20 7A 1D 72 E3 4D` is
compiled into the 1.2.1 build (via `USE_HLO_ANT_NETWORK` in `pill_PVT1/platform.h`)
exactly as in the working image; verified present in both binaries. The scheme,
not the key, is the difference.

## 3. The fix — build tag 1.0.3

Tag **1.0.3** is the sweet spot: it is the newest tag that is simultaneously

1. **async** (`CHANNEL_TYPE_MASTER_TX_ONLY`) — matches the Sense's morpheus,
2. still **builds for `pill_PVT1`** — the `imu_handle_fifo_read` /
   `IMU_FIFO_CAPACITY_WORDS` API break that stops the v1 pill compiling starts at
   1.3.0, not here, and
3. keys correctly — `pill_PVT1/platform.h` defines `USE_HLO_ANT_NETWORK`.

## 4. Toolchain validation — byte-exact against a known-good image

To prove the GCC-4.7 pipeline faithfully reproduces a working-ANT pill, a source
build of tag **0.9.3** `pill+pill_PVT1` was compared to the **factory** 0.9.3
image (`doraemon/targets/pill_pvt/pill+pill_PVT1.bin`): **byte-identical**, both
40,908 bytes, SHA1 `c9b58fa501975a97c306caab12c096951bfa8e4c`. Since that factory
0.9.3 image is precisely the one that restores ANT on a real device, the
toolchain reproduces a known-working-ANT pill exactly — so the 1.0.3 build (same
toolchain, same async scheme) is sound, not merely plausible.

The one remaining confirmation is an over-the-air pairing test on hardware.

---

## 5. Rebuild it

```bash
cd full-instructions/pill/firmware/pill-1.0.3
NRF51_SDK=/path/to/nRF51SDK/Nordic ./rebuild.sh
# -> out/pill+pill_PVT1.bin  (version 1.0.3), out/app_settings.crc.bin, bootloader
```

`rebuild.sh`:
1. builds a `linux/amd64` image with the exact **ARM GCC 4.7 (2013q3)** kodobannin
   pins (downloaded from launchpad in the `Dockerfile`), first run only;
2. clones kodobannin, checks out `1.0.3`, inits the public `micro-ecc` submodule;
3. compiles `pill+pill_PVT1` inside the container (SDK mounted at a non-nested
   path — see gotcha below);
4. generates `out/app_settings.crc.bin` with `gen_app_settings.py`;
5. verifies the version string and the ANT key, prints the SHA1.

**Requirements:** Docker, and the **nRF51 SDK v5.2.0 (with S310 v1.0.0 headers)**.
The SDK is Nordic-licensed and is *not* vendored here; point `NRF51_SDK` at a
local copy or fetch the public mirror
[`tdwebste/nRF51SDK`](https://github.com/tdwebste/nRF51SDK). The build needs only
the SDK's *headers* (including `Include/s310/`), not the SoftDevice binary.

**Build gotchas** (all handled by `rebuild.sh`, worth knowing if you build by hand):
- **Non-nested SDK mount.** Mounting the SDK *inside* the source mount
  (`.../nRF51_SDK_real` under `/src/kodobannin`) is unreliable on Docker Desktop /
  Rosetta and yields "`nrf51.h`: No such file or directory". Mount it at a
  separate path (`-v <sdk>:/sdk:ro`) and `ln -s /sdk nRF51_SDK`.
- **Symlink write-back.** That `ln -s` inside the container writes back through
  the bind mount to your host tree; a fresh checkout each run (as `rebuild.sh`
  does) avoids leaving a dangling `nRF51_SDK` symlink behind.
- **Clean between tags.** `make` is incremental; remove `build/pill+pill_PVT1*`
  when switching tags. Older tags also leave `nRF51_SDK` as a real directory on
  checkout — clear it with `rm -rf`, not `rm -f`.

## 6. Flash it

See [`flash/README.md`](flash/README.md). Short version: supply the Nordic S310
SoftDevice binaries (not shipped — read them off a device or get them from
Nordic), then

```bash
JLinkExe -device nrf51422 -if swd -speed 4000 -autoconnect 1 \
         -CommanderScript flash/flash_pill_1.0.3.jlink
```

The bootloader validates the app by **CRC-16 only** (no signature), which is what
`app_settings.crc.bin` at `0x3f000` carries.

---

## 7. Files

| Path | Purpose |
|---|---|
| `rebuild.sh` | One-command source rebuild of `pill+pill_PVT1` @ 1.0.3 |
| `Dockerfile` | `linux/amd64` image with ARM GCC 4.7 (2013q3) |
| `gen_app_settings.py` | Portable nRF51 bootloader app-settings (CRC-16) generator |
| `out/pill+pill_PVT1.bin` | The firmware (version 1.0.3, working ANT) |
| `out/app_settings.crc.bin` | Boot-settings CRC blob for `0x3f000` |
| `out/bootloader+pill_PVT1.bin` | Matching bootloader |
| `out/*.hex`, `out/*.elf`, `out/SHA1SUMS` | Hex/ELF and checksums |
| `flash/flash_pill_1.0.3.jlink` | J-Link flash script (relative paths) |
| `flash/README.md` | Flash layout + how to obtain the SoftDevice |

## 8. Provenance and licensing

- **Firmware / bootloader** here are built from the open-source
  [`hello/kodobannin`](https://github.com/hello/kodobannin) (GCC 4.7) and contain
  no SoftDevice code.
- **ARM GCC 4.7 2013q3** is downloaded from launchpad by the `Dockerfile`.
- **nRF51 SDK** and the **S310 SoftDevice** are Nordic-licensed and are **not**
  redistributed here — supply your own (SDK for building, SoftDevice binary for
  flashing).
