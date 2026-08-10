# Sleep Pill Recovery via SWD

Recover a bricked **Hello Sleep Pill** (the company is defunct) by flashing correct
firmware over SWD using a J-Link debug probe. This covers building from source,
flashing, and verifying the device.

> **There are two pill hardware variants with incompatible pin mappings.** Flashing the
> wrong variant's firmware is the most common cause of bricking. See
> [Identifying your pill variant](#identifying-your-pill-variant) before doing anything.

---

## Table of contents

- [Background](#background)
- [Identifying your pill variant](#identifying-your-pill-variant)
- [Hardware required](#hardware-required)
- [Flash memory map](#flash-memory-map)
- [Source repositories](#source-repositories)
- [Build environment](#build-environment)
- [Building from source](#building-from-source)
- [Preparing flash files](#preparing-flash-files)
- [SWD wiring](#swd-wiring)
- [Flashing](#flashing)
- [Verification](#verification)
- [AES key and pairing](#aes-key-and-pairing)
- [Factory images (no build required)](#factory-images-no-build-required)
- [BLE DFU (alternative to SWD)](#ble-dfu-alternative-to-swd)
- [Common pitfalls](#common-pitfalls)
- [References](#references)

---

## Background

The Sleep Pill is a motion-tracking accelerometer that pairs with the Hello Sense via
BLE and ANT. It runs on a Nordic nRF51422 (ARM Cortex-M0) with an ST LIS2DH12
accelerometer, powered by a coin cell battery, and communicates wirelessly.

Hello produced two pill hardware variants that share the same MCU but have different
board layouts and GPIO pin assignments. The firmware is built from the same source
repository (`kodobannin`) but with different platform headers, and the two builds are
**not interchangeable**.

If a pill is flashed with the wrong variant's firmware (or firmware that crashes before
BLE init), it becomes unrecoverable over the air because:

1. The single-bank bootloader only enters DFU mode if the app CRC fails or a
   `GPREGRET` flag is set by the running app.
2. A crashed app passes CRC validation (it was correctly signed), so the bootloader
   boots it every time.
3. The BLE DFU trigger (`0x08` written to GATT characteristic `DEED`) requires the
   app's BLE stack, which never initializes if the app crashes.

SWD is the only recovery path. It requires opening the pill case and connecting to
the debug pads on the PCB.

---

## Identifying your pill variant

This is the single most important step. **Getting this wrong bricks the device.**

### Pill v1 (original, ships with Sense)

| Property | Value |
|---|---|
| Board part number | `905-00007-05` or `-A` |
| Build target | `pill+pill_PVT1` |
| Platform header | `pill_PVT1/platform.h` |
| Last compiling tag | **1.2.1** |
| Hardware revision | 3 |
| Battery | Removable coin cell |
| Debug header | 6 holes (2x3 @ 1.27mm) |
| IMU | ST LIS2DH12 (SPI) |
| Version string in binary | `1.2.1` (no prefix) |

### Pill v1.5 (ships with Sense with Voice)

| Property | Value |
|---|---|
| Board part number | `905-00007-12` |
| Build target | `pill+pillx_DVT1` |
| Platform header | `pillx_DVT1/platform.h` |
| Last factory tag | **1.7.2** |
| Hardware revision | 4 |
| Battery | Sealed, non-replaceable |
| Debug header | 10 holes (2x5 @ 1.27mm) |
| IMU | ST LIS2DH12 (SPI) |
| Version string in binary | `1p5 1.7.2` |

### Pin mapping differences

The two variants use completely different GPIO assignments. This is why flashing the
wrong firmware bricks the device: SPI talks to wrong pins, UART is swapped, and IMU
power control differs.

| Function | v1 (pill_PVT1 @ 1.2.1) | v1.5 (pillx_DVT1) |
|---|---|---|
| SPI nCS | P0.16 | P0.13 |
| SPI SCLK | P0.14 | P0.15 |
| SPI MOSI | P0.12 | P0.09 |
| SPI MISO | P0.10 | P0.11 |
| IMU INT | P0.23 | P0.16 |
| IMU VDD_EN | P0.20 | P0.20 |
| UART TX | P0.18 | P0.19 |
| UART RX | P0.19 | P0.18 |

Both variants define `PLATFORM_HAS_IMU_VDD_CONTROL` at their respective build tags.

### How to tell which you have

- **Check the board markings.** Look for `905-00007-05` (v1) or `905-00007-12` (v1.5).
- **Count the debug holes.** 6 holes = v1, 10 holes = v1.5.
- **Check what Sense it came with.** Original Sense (no voice) = v1. Sense with Voice = v1.5.
- **Read the firmware version from flash.** Use `strings` on a flash dump. If it
  contains `1p5`, it has (or had) v1.5 firmware.

> **Warning:** the `905-00007` prefix is the same for both variants. The dash suffix
> (`-05`/`-A` vs `-12`) distinguishes them.

---

## Hardware required

| Part | Notes |
|---|---|
| **SEGGER J-Link** (any model supporting SWD) | J-Link EDU Mini V2 works. Must support nRF51422. |
| **Pogo pin adapter or probe wires** | For connecting to the PCB debug pads. Spring-loaded pogo pins at 1.27mm pitch recommended. |
| **Coin cell battery** (v1) or charged battery (v1.5) | The pill needs power during flashing. For v1, insert a fresh CR2032. |

### J-Link connection settings

- Device: `nrf51422`
- Interface: SWD
- Speed: **500 kHz** (higher speeds may be unreliable with pogo cables)
- VTref should read ~1.8V when connected (the nRF51422 runs at 1.8V)

If VTref reads 0V, the pogo cable has lost contact or the battery is dead.

---

## Flash memory map

The nRF51422 has 256KB flash (0x00000 to 0x3FFFF). The layout is:

| Region | Address range | Size | Contents |
|---|---|---|---|
| SoftDevice | 0x00000 - 0x1FFFF | 128KB | S310 v1.0.0 (BLE + ANT stack) |
| Application | 0x20000 - 0x35FFF | ~88KB | Pill firmware |
| Bootloader | 0x36000 - 0x3CFFF | ~28KB | Factory bootloader |
| App data | 0x3D000 - 0x3EFFF | 8KB | (reserved) |
| Boot settings | 0x3F000 - 0x3F00B | 12B | CRC + size for boot validation |
| Device info | 0x3F400 - 0x3F5FF | 512B | Encrypted device identity (auto-generated on first boot) |

Additionally, the UICR (User Information Configuration Registers) at 0x10001000
contains SoftDevice configuration and the bootloader address pointer at 0x10001014.

### SoftDevice

The S310 SoftDevice v1.0.0 is a precompiled binary blob from Nordic Semiconductor
that provides the BLE and ANT protocol stacks. It occupies the first 128KB of flash
and is loaded as two separate binaries:

- **Main binary** (`s310_nrf51422_1.0.0_softdevice_main.bin`): loaded at 0x00000
- **UICR binary** (`s310_nrf51422_1.0.0_softdevice_uicr.bin`): loaded at 0x10001000

The same SoftDevice binary is used for both pill variants.

### Boot settings format

The 12-byte boot settings page at 0x3F000 tells the bootloader whether the app is
valid:

```
Offset  Size  Field         Description
0x00    2     bank_0        0x0001 = valid app in bank 0
0x02    2     crc16         CRC-16 of the app binary (byte-swapped)
0x04    4     bank_1        0xFFFFFFFF (padding, single-bank bootloader)
0x08    4     bank_0_size   Size of the app binary in bytes (little-endian)
```

The bootloader reads this page, computes CRC-16 over the app region, and compares.
If they do not match, it enters DFU mode. If they match, it boots the app.

---

## Source repositories

All source code is from Hello's public GitHub organization (`github.com/hello`).

| Repository | Contents |
|---|---|
| [hello/kodobannin](https://github.com/hello/kodobannin) | Pill firmware (nRF51422, app + bootloader) |
| [hello/doraemon](https://github.com/hello/doraemon) | Factory flash tooling + prebuilt images for all pill/sense variants |
| [tdwebste/nRF51SDK](https://github.com/tdwebste/nRF51SDK) | nRF51 SDK v5.2.0 with S310 v1.0.0 headers (replacement for Hello's dead private fork) |
| [kmackay/micro-ecc](https://github.com/kmackay/micro-ecc) | ECC library (submodule, public) |

### SDK setup

The kodobannin repo's `.gitmodules` points `nRF51_SDK` and `SoftDevice` at Hello's
private GitHub forks, which are gone. Replace them:

1. Clone `tdwebste/nRF51SDK` alongside kodobannin.
2. Symlink the SDK's `Nordic` directory:
   ```bash
   cd kodobannin
   rm -rf nRF51_SDK      # remove the dead submodule directory
   ln -s ../nrf51sdk_src/Nordic nRF51_SDK
   ```
   Where `nrf51sdk_src` is your clone of `tdwebste/nRF51SDK`. The SDK's
   `Nordic/nrf51422/` maps to kodobannin's `nRF51_SDK/nrf51422/`.

3. The `SoftDevice` submodule is only needed for J-Link flash targets in the
   Makefile. For SWD flashing, use the prebuilt SoftDevice binaries from the
   `doraemon` repository instead.

4. The `micro-ecc` submodule is public and can be cloned normally:
   ```bash
   cd kodobannin
   git submodule update --init micro-ecc
   ```

---

## Build environment

The firmware requires a specific ARM cross-compiler: **GCC 4.7 2013q3** from the ARM
Launchpad archive.

### Option 1: macOS native (Apple Silicon or Intel)

The kodobannin repo bundles the toolchain at `tools/gcc-arm-none-eabi-4_7-2013q3/`.
These are **macOS Mach-O binaries**. On Apple Silicon Macs, they run under Rosetta 2.

```bash
cd kodobannin
make pill+pill_PVT1    # or pill+pillx_DVT1
```

The only post-build issue is the `crc16` tool (`tools/crc16`), which is a Linux ELF
binary and will not run on macOS. See [Preparing flash files](#preparing-flash-files)
for the workaround.

### Option 2: Docker (Linux amd64)

Build a Docker image with the correct toolchain:

```dockerfile
FROM --platform=linux/amd64 debian:bullseye

RUN dpkg --add-architecture i386 && apt-get update && apt-get install -y --no-install-recommends \
      wget bzip2 make git ca-certificates \
      python2.7 python3 \
      libc6:i386 libstdc++6:i386 libncurses5:i386 zlib1g:i386 \
 && ln -sf /usr/bin/python2.7 /usr/bin/python \
 && rm -rf /var/lib/apt/lists/*

RUN wget -q https://launchpadlibrarian.net/151487636/gcc-arm-none-eabi-4_7-2013q3-20130916-linux.tar.bz2 \
      -O /tmp/gcc.tar.bz2 \
 && tar xjf /tmp/gcc.tar.bz2 -C /opt \
 && rm /tmp/gcc.tar.bz2

RUN apt-get update && apt-get install -y --no-install-recommends \
      gcc libc6-dev xxd openssl \
 && rm -rf /var/lib/apt/lists/*

ENV PATH=/opt/gcc-arm-none-eabi-4_7-2013q3/bin:$PATH
WORKDIR /src
```

Build and tag it:
```bash
docker build --platform linux/amd64 -t nrf-build .
```

Run the build (mount the SDK separately since `nRF51_SDK` is a symlink):
```bash
docker run --rm --platform linux/amd64 \
  -v "$(pwd)/kodobannin:/src/kodobannin" \
  -v "$(pwd)/nrf51sdk_src/Nordic:/src/kodobannin/nRF51_SDK_real" \
  -w /src/kodobannin \
  nrf-build \
  sh -c "rm -f nRF51_SDK && ln -sf nRF51_SDK_real nRF51_SDK && make KODOBANNIN_GCC_ROOT=/opt/gcc-arm-none-eabi-4_7-2013q3 pill+pill_PVT1"
```

> **Warning:** The Docker bind mount can overwrite the `nRF51_SDK` symlink on the
> host filesystem. After a Docker build, verify and restore the symlink if needed:
> ```bash
> cd kodobannin
> rm -f nRF51_SDK && ln -s ../nrf51sdk_src/Nordic nRF51_SDK
> ```

### Verified: macOS and Linux produce identical binaries

The same source and toolchain version produce byte-for-byte identical output on both
macOS (bundled toolchain) and Linux (Docker). Either build method works.

---

## Building from source

### Pill v1 (pill_PVT1)

The last tag where `pill+pill_PVT1` compiles is **1.2.1**. Later tags (1.5.x, 1.7.x,
1.8.x) break because `pill/message_imu.c` uses IMU APIs that only exist in the
pillx_DVT1 driver. The shared code was not kept in sync after the Pill 1.5 was
introduced.

```bash
cd kodobannin
git checkout 1.2.1
make pill+pill_PVT1
make bootloader+pill_PVT1
```

Output:
- `build/pill+pill_PVT1.bin` (app, ~42KB)
- `build/bootloader+pill_PVT1.bin` (bootloader, ~25KB)

### Pill v1.5 (pillx_DVT1)

Build from tag **1.7.2** (the factory version):

```bash
cd kodobannin
git checkout 1.7.2
make pill+pillx_DVT1
make bootloader+pillx_DVT1
```

Output:
- `build/pill+pillx_DVT1.bin` (app, ~44KB)
- `build/bootloader+pillx_DVT1.bin` (bootloader, ~25KB)

The clean 1.7.2 build produces a byte-for-byte match with the factory image in
`doraemon/targets/pill1p5_dvt/pill+pillx_DVT1.bin`.

### Build artifacts

The Makefile appends an HMAC-SHA1 signature to the `.bin` file (keyed with
`HLO_SIGN_AES` from the Makefile). This signature is what
`bootloader_app_is_signed()` validates. The `.crc` file (12 bytes) is the boot
settings page, but its CRC field will be wrong on macOS because the `tools/crc16`
binary is Linux-only.

### Stale build artifacts

If you switch between tags or platforms, **delete the build directory** first:
```bash
rm -rf build/pill+pill_PVT1     # or whatever target you're switching to
```
Stale `.d` dependency files from a different tag can cause spurious "no rule to make
target" errors (e.g., referencing `pb_common.h` which only exists in later tags).

---

## Preparing flash files

After building, you need five files to flash:

1. **SoftDevice main** (`s310_nrf51422_1.0.0_softdevice_main.bin`) at 0x00000
2. **SoftDevice UICR** (`s310_nrf51422_1.0.0_softdevice_uicr.bin`) at 0x10001000
3. **App binary** (`pill+pill_PVT1.bin`) at 0x20000
4. **Bootloader** (`bootloader+pill_PVT1.bin`) at 0x36000
5. **Boot settings** (12 bytes) at 0x3F000

Get the SoftDevice binaries from `doraemon/targets/pill_pvt/` (v1) or
`doraemon/targets/pill1p5_dvt/` (v1.5). They are identical across variants.

### Computing the CRC and boot settings

The `tools/crc16` binary is a Linux ELF. On macOS, run it via Docker:

```bash
docker run --rm --platform linux/amd64 \
  -v "$(pwd)/build/pill+pill_PVT1.bin:/app.bin" \
  -v "$(pwd)/tools/crc16:/crc16" \
  nrf-build /crc16 /app.bin
```

This prints a 4-character hex CRC (e.g., `add8`).

Then create the 12-byte boot settings binary. Given CRC `add8` and app size 42512
(0xA610):

```bash
# Byte-swap the CRC: add8 -> d8ad
# Format: valid(0100) + crc_swapped + padding(ffffffff) + size_le
printf '\x01\x00\xd8\xad\xff\xff\xff\xff\x10\xa6\x00\x00' > app_settings.crc.bin
```

To compute the size in hex:
```bash
printf '%08x' $(wc -c < build/pill+pill_PVT1.bin)
# Then byte-reverse for little-endian: 0000A610 -> 10 A6 00 00
```

### Verify the boot settings

```bash
xxd app_settings.crc.bin
# Should show: 0100 XXXX ffff ffff XXXX XXXX  (12 bytes total)
```

---

## SWD wiring

### Minimum connections

| J-Link pin | Pill pad | Description |
|---|---|---|
| SWDIO | SWDIO | Bidirectional data |
| SWCLK | SWCLK | Clock |
| GND | GND | Common ground |

VTref on the J-Link should be connected to the pill's VCC (or the J-Link can sense
it from the target). The pill must be powered by its own battery during SWD operations.

### Pill v1 debug header (6 holes, 2x3 @ 1.27mm)

The v1 pill has a 6-hole debug cluster. Pin assignments (verify with a multimeter
against the schematic from `hello/morpheus-board-pill`):

```
Pin 1: TXD  (P0.18)     Pin 2: SWDIO
Pin 3: VMCU              Pin 4: GND
Pin 5: RXD  (P0.19)     Pin 6: SWDCLK
```

### Pill v1.5 debug header (10 holes, 2x5 @ 1.27mm)

The v1.5 has a 10-hole header at standard ARM Cortex-M 1.27mm pitch. This matches
the standard 10-pin SWD connector pinout. UART TX/RX are also broken out (P0.19 and
P0.18 respectively, note: swapped vs v1).

### Pogo cable reliability

Spring-loaded pogo pins at 1.27mm pitch are recommended but can be unreliable. If
`JLinkExe` reports `VTref=0.000V` or `Target voltage too low`, the cable has lost
contact. Re-seat the pogo pins and try again. Use 500 kHz SWD speed for reliability.

---

## Flashing

### The flash script

Create a J-Link command file (adjust paths to your binaries):

```jlink
w4 4001e504, 2
w4 4001e50c, 1
sleep 50
r
w4 4001e504, 1
loadbin softdevice_main.bin 0x0
loadbin softdevice_uicr.bin 0x10001000
loadbin pill_app.bin 0x20000
loadbin bootloader.bin 0x36000
loadbin app_settings.crc.bin 0x3f000
w4 10001014, 36000
verifybin softdevice_main.bin 0x0
verifybin pill_app.bin 0x20000
verifybin bootloader.bin 0x36000
verifybin app_settings.crc.bin 0x3f000
r
g
sleep 3000
r
savebin device.info 0x3f400 0x200
r
g
sleep 10000
halt
sleep 200
regs
mem32 20003FF0 4
go
q
```

#### What each section does

1. **Mass erase** (`w4 4001e504, 2` + `w4 4001e50c, 1`): Triggers NVMC ERASEALL.
   This erases all flash and UICR (except FICR, which is permanent). It also clears
   any readback protection (RBPCONF/APPROTECT), allowing SWD access. Safe because
   FICR (containing device ID and encryption root) survives.

2. **Enable write** (`w4 4001e504, 1`): Sets NVMC CONFIG to write-enable mode.

3. **Load binaries**: Writes the five regions to flash.

4. **Set bootloader address** (`w4 10001014, 36000`): Writes the bootloader start
   address to the UICR BOOTLOADERADDR register.

5. **Verify**: Reads back each region and compares against the source file.

6. **First boot** (`r` + `g` + `sleep 3000`): Resets and runs. The bootloader detects
   that device provisioning is needed (`factory_needs_provisioning()`), generates a
   new random AES key, stores it in flash at 0x3F400, and reboots.

7. **Save device info** (`savebin device.info 0x3f400 0x200`): Saves the 512-byte
   encrypted device info page for later analysis.

8. **Second boot + halt** (`r` + `g` + `sleep 10000` + `halt`): Lets the app run for
   10 seconds, then halts to check the CPU state.

9. **Check registers** (`regs` + `mem32 20003FF0 4`): Dumps CPU registers and the
   AES key at 0x20003FF0 (placed there by the bootloader for the app to use).

### Running the flash

```bash
JLinkExe -device nrf51422 -if swd -speed 500 -CommandFile flash.jlink
```

On macOS, JLinkExe may be at `/usr/local/bin/JLinkExe` or
`/Applications/SEGGER/JLink_V9xx/JLinkExe`.

### UICR verify "failure"

The SoftDevice UICR verify may report a failure at address 0x10001014. This is
expected: the UICR file contains 0xFF at that offset (erased state), but we wrote the
bootloader address (0x00036000) there. The app, bootloader, and boot settings
verifications should all pass.

---

## Verification

After flashing, check the halt output to determine if the pill booted successfully.

### Successful boot

The PC register should be in the **SoftDevice region** (0x0000xxxx), typically around
0x00003000-0x00004000. This is the SoftDevice's idle/WFE loop, meaning the app
initialized the SoftDevice and is now sleeping normally.

Example of a healthy halt:
```
PC = 0x0000318A    <-- SoftDevice idle loop
XPSR = 61000000   <-- IPSR = 000 (NoException)
```

### Failed boot (app error handler)

If the PC is in the **app region** (0x0002xxxx) and specifically in a WFE/branch
loop, the app crashed and is stuck in `app_error_handler`. Look for this pattern in
the disassembly:

```
2014c:  bf20    wfe
2014e:  e7fd    b.n  2014c
```

Common causes:
- **Wrong platform target** (the most common cause)
- IMU WHO_AM_I check failed (SPI talking to wrong pins)
- SoftDevice initialization error

### Failed boot (hard fault)

If IPSR is nonzero (e.g., 0x003 = HardFault), the CPU took a hardware exception.
This usually indicates a more fundamental problem like a bad SoftDevice version or
corrupt binary.

### BLE verification

After a successful boot, the pill should:
1. Advertise on BLE as "Pill" (visible in nRF Connect or similar BLE scanner apps)
2. Respond to connections with DIS (Device Information Service) showing manufacturer
   "Hello" and model number matching the firmware version

The pill is motion-activated: it only advertises after being shaken for several
seconds. Shake it vigorously near your BLE scanner and watch for it to appear.

---

## AES key and pairing

### Motion encryption key

Each pill encrypts motion data with a per-device AES-128 key derived from the
nRF51422's **FICR Encryption Root** (`NRF_FICR->ER`, 16 bytes at address
0x10000080). This is a hardware random value burned in at Nordic's factory. It is:

- **Permanent**: survives any flash erase (FICR is not erasable)
- **Per-device**: every nRF51422 has a unique value
- **Not derivable**: 128-bit hardware random, brute force is infeasible

The firmware reads it in `common/util.c`:
```c
const uint8_t *get_aes128_key(void) {
    return (uint8_t*)NRF_FICR->ER;
}
```

For the backend to decrypt motion data, this key must be stored in the
`pill_key_store` DynamoDB table (or equivalent) indexed by the pill's device ID.

### Reading FICR.ER via SWD

After a mass erase (which clears readback protection), you can read FICR.ER directly:

```
JLinkExe -device nrf51422 -if swd -speed 500
J-Link> connect
J-Link> mem32 10000080 4
```

This prints 4 words (16 bytes) = the AES motion key.

### Extracting the key from a factory-locked pill

Factory-flashed pills have readback protection enabled (RBPCONF at 0x10001004 set to
0x00). This blocks SWD reads of flash and FICR, so you cannot read FICR.ER while the
protection is active.

However, the key is still recoverable without rebuilding firmware:

1. **Mass erase** clears readback protection (along with all flash), but FICR survives
   because it is not erasable:
   ```
   J-Link> w4 4001e504, 2
   J-Link> w4 4001e50c, 1
   J-Link> sleep 50
   ```

2. **Read FICR.ER** immediately after the erase:
   ```
   J-Link> mem32 10000080 4
   ```
   Save these 16 bytes. This is the motion encryption key.

3. **Reflash the factory images** from `doraemon/targets/pill_pvt/` (v1) or
   `doraemon/targets/pill1p5_dvt/` (v1.5). The device ends up with the same
   firmware it had before, and the same permanent encryption key.

The pill is fully functional afterward. The only thing regenerated is the device info
page at 0x3F400, which the bootloader recreates automatically on first boot.

**Note:** if the factory flash script ends with `w4 0x10001004, 0` (setting readback
protection), you can omit that line to leave the pill unlocked for future SWD access.
The pill works the same either way; readback protection only prevents debug reads.

### Device info page

The bootloader auto-generates an encrypted device info page at 0x3F400 on first boot.
This page contains an encrypted copy of the entire FICR (256 bytes), encrypted with
`HLO_FACTORY_AES` (a known constant in `common/hlo_keys.h`). The device info page
can be decrypted to recover FICR.ER without reading FICR directly, but SWD access to
FICR is simpler.

### What 0x20003FF0 contains

The 16 bytes at RAM address 0x20003FF0 (shown in the flash script's `mem32` output)
are the `device_aes`, a random key generated during provisioning and used for
encrypting the device info page. This is NOT the motion encryption key.

### Alternative: replace the key

Instead of recovering the per-device key, you can patch the firmware to use a known
constant key:

1. Edit `common/util.c`, change `get_aes128_key()` to return a chosen constant
2. Rebuild and flash
3. Set the same constant in `pill_key_store`

This is simpler than key recovery and keeps the pill fully functional, but requires
a source modification and rebuild.

---

## Factory images (no build required)

The `hello/doraemon` repository contains prebuilt factory images for all pill
variants. These can be flashed directly without building from source.

### Pill v1

```
doraemon/targets/pill_pvt/
  pill+pill_PVT1.bin                         # app (firmware 0.9.3)
  bootloader+pill_PVT1.bin                   # bootloader
  pill+pill_PVT1.crc                         # boot settings
  s310_nrf51422_1.0.0_softdevice_main.bin    # SoftDevice
  s310_nrf51422_1.0.0_softdevice_uicr.bin    # SoftDevice UICR
  app+bootloader.prod.in                     # J-Link script template
```

Note: the doraemon factory image for v1 is firmware **0.9.3**, while the latest
buildable version from source is **1.2.1**.

### Pill v1.5

```
doraemon/targets/pill1p5_dvt/
  pill+pillx_DVT1.bin                        # app (firmware 1.7.2)
  bootloader+pillx_DVT1.bin                  # bootloader
  pill+pillx_DVT1.crc                        # boot settings
  s310_nrf51422_1.0.0_softdevice_main.bin    # SoftDevice
  s310_nrf51422_1.0.0_softdevice_uicr.bin    # SoftDevice UICR
```

### Using factory images

The factory J-Link script template (`app+bootloader.prod.in`) uses `$TEMP` and
`$CACHE` placeholders. Replace them with actual paths, or use the flash script format
from the [Flashing](#flashing) section with the factory binary filenames.

The factory script also enables **readback protection** at the end:
```
w4 0x10001004, 0    # UICR RBPCONF = 0 (protection enabled)
```

Omit this line if you want to keep SWD access for debugging. If you do set it, you
will need another mass erase to regain SWD access (which clears all flash).

---

## BLE DFU (alternative to SWD)

If the pill's BLE stack is running (i.e., it is NOT bricked), firmware can be updated
over the air using the Nordic legacy BLE DFU protocol.

### Triggering DFU mode

Write `0x08` to BLE GATT characteristic `DEED` (UUID `0000deed-...` on the Hello
service `0000e110-...`). This sets `PILL_COMMAND_WIPE_FIRMWARE` which calls
`REBOOT_TO_DFU()`, rebooting the pill into the Nordic DFU bootloader.

In DFU mode, the pill advertises as "PillDFU" with DFU service UUID `0x1530`.

### DFU protocol

The bootloader implements the **Nordic SDK 5.2 legacy BLE DFU** protocol
(unsigned, single-bank):

- Control point characteristic: `0x1531`
- Packet characteristic: `0x1532`
- Commands: START(1), PRN(8), RECV_FW(3), VALIDATE(4), ACTIVATE(5)
- Response format: `[0x10, opcode, 0x01]` for success

The DFU image is the `.bin` file (with HMAC-SHA1 signature appended). The boot
settings CRC and size are sent as the START packet data.

### Upload tools

- **nRF Connect for Mobile** (iOS/Android): free app, supports legacy DFU natively
- **Custom Python script using bleak**: implement the legacy DFU protocol over BLE

### Important notes

- The pill is motion-activated. It must be shaken vigorously for 60-80 seconds before
  it advertises on BLE. Keep it touching the device running the BLE scanner.
- Single-bank DFU overwrites the app in place. The bootloader and SoftDevice survive
  a bad flash, so a failed DFU re-enters DFU mode on the next reset.
- This path is only available when the pill's BLE stack is functional. A bricked pill
  (crashed app) cannot enter DFU mode.

---

## Common pitfalls

### 1. Wrong platform target (the most dangerous mistake)

Flashing `pillx_DVT1` firmware on a v1 pill (or vice versa) will brick it. The SPI
pins are completely different, so the IMU init crashes on WHO_AM_I (reads 0x00 instead
of 0x33), triggering `APP_ASSERT(0)` before BLE ever initializes.

**Always verify your pill variant before building or flashing.**

### 2. pill_PVT1 does not compile at recent tags

The v1 pill's platform code was not maintained after the Pill 1.5 was introduced.
`pill+pill_PVT1` only compiles at tag **1.2.1** and earlier. At 1.5.8 and later,
`pill/message_imu.c` calls functions that only exist in the pillx_DVT1 IMU driver.

### 3. crc16 tool is Linux-only

The `tools/crc16` binary in the kodobannin repo is a Linux ELF. On macOS, run it via
Docker:
```bash
docker run --rm --platform linux/amd64 \
  -v "$(pwd)/app.bin:/app.bin" \
  -v "$(pwd)/tools/crc16:/crc16" \
  nrf-build /crc16 /app.bin
```

### 4. nRF51_SDK symlink issues with Docker

The `nRF51_SDK` directory is a symlink that points outside the repo. Docker bind
mounts do not follow host symlinks, so mount the SDK separately and create the
symlink inside the container. Be aware that Docker can overwrite the symlink on the
host (bind mount side effect). Restore it after Docker builds:
```bash
cd kodobannin
rm -f nRF51_SDK && ln -s ../nrf51sdk_src/Nordic nRF51_SDK
```

### 5. Stale build directories

When switching git tags or platform targets, delete the build directory for that
target first. Stale `.d` dependency files reference files that may not exist at the
new tag, causing "no rule to make target" errors.

### 6. JLinkExe not in PATH

On macOS, JLinkExe may be installed at `/usr/local/bin/JLinkExe` or
`/Applications/SEGGER/JLink_V9xx/JLinkExe`. Use the full path if `JLinkExe` is not
found.

### 7. VTref = 0V (pogo cable contact)

If JLinkExe reports `VTref=0.000V` or `Target voltage too low`, the pogo cable has
lost contact with the PCB pads. Re-seat the pogo pins and ensure the battery is
installed (v1) or charged (v1.5). Expected VTref is ~1.8V.

### 8. Readback protection

Factory-programmed pills have UICR RBPCONF set (readback protection enabled). The
mass erase at the start of the flash script clears this. If you skip the mass erase
and the device has protection set, SWD reads will return all zeros.

### 9. HLO_FACTORY_AES vs the motion key

`HLO_FACTORY_AES` (found in `common/hlo_keys.h`) is used for encrypting the device
info page. It is NOT the motion encryption key. The motion key is `NRF_FICR->ER`,
unique per chip. Do not confuse the two.

---

## References

- Firmware source: [hello/kodobannin](https://github.com/hello/kodobannin)
- Factory flash tool: [hello/doraemon](https://github.com/hello/doraemon)
- nRF51 SDK (replacement): [tdwebste/nRF51SDK](https://github.com/tdwebste/nRF51SDK)
- Pill v1 board design: [hello/morpheus-board-pill](https://github.com/hello/morpheus-board-pill)
- Pill v1.5 BOM: [hello/morpheus-board-pill-bqle](https://github.com/hello/morpheus-board-pill-bqle)
- SEGGER J-Link: [segger.com/products/debug-probes/j-link](https://www.segger.com/products/debug-probes/j-link/)
- nRF51422 product page: [nordicsemi.com](https://www.nordicsemi.com/Products/nRF51422)
