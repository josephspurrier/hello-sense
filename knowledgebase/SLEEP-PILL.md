# Hello Sleep Pill: Hardware and Flashing Reference

Last updated: 2026-08-02

Covers both Sleep Pill generations: identifying which one you have, what is on each
board, how the firmware platforms map to them, why the v1 unit in this project is
currently dark, and the exact SWD flashing procedure to restore either one.

## Confidence key

Claims in this document are marked so you know what to re-check:

- **[V]** Verified from a primary source (schematic, BOM, source code, factory script).
- **[M]** Measured or read off photographs. Good but not authoritative.
- **[I]** Inferred. Plausible, corroborated, not proven.
- **[?]** Unknown or unverified. Do not build on it without checking.

---

## 1. Which pill am I holding?

| | **Pill v1** | **Pill v1.5** (sold as the voice-Sense pill) |
|---|---|---|
| Board part number | `905-00007-05`, MP rev `-A` **[V]** | `905-00007-12` **[V]** |
| Silkscreen | `Hello Inc.`, 2014/2015 era | `PB15226 / 905-00007-12 / (C) 2016 Hello Inc.` **[V]** |
| Debug header holes | **6**, 2x3 **[V]** | **10**, 2x5 **[M]** |
| Battery | Coin cell on a contact spring, replaceable **[V]** | Sealed, non-replaceable |
| IMU | InvenSense **MPU-6500** **[V]** | ST **LIS2DH12** **[I]** |
| kodobannin platform | `pill_PVT1` **[V]** | `pillx_DVT1` **[V]** |
| doraemon target | `pill_pvt` **[V]** | `pill1p5_dvt` **[V]** |

`905-00007` is the board part number for **both** generations, so the base number does
not distinguish them. The dash suffix is the revision. The reliable visual tell is the
**6 versus 10 hole** debug header.

Other doraemon targets exist for pre-production boards (`pill_evt` = `pill_EVT2`,
`pill_dvt` = `pill_DVT1`, `pill1p5_evt` = `pillx_EVT1`) and for a later `pill1p9_evt`
= `pillp_EVT1`, a platform that is not present in the public kodobannin HEAD. **[V]**

---

## 2. Hardware

### 2.1 Common to both generations

- MCU: **Nordic nRF51422-QFAA**, QFN48. 256 KB flash, 16 KB RAM, ANT + BLE. **[V]**
  - v2 die marking observed: `N51422 / QFAAF0 / 1631DI` = rev 3, week 31 of 2016. **[M]**
- SoftDevice: **S310 v1.0.0** (`s310_nrf51422_1.0.0`), the combined ANT + BLE stack. **[V]**
- Rail voltage: **1.8 V**. `U11` is a `TPS79718`, a fixed 1.8 V LDO, and the v1
  schematic annotates VMCU as 1.8 V. **[V]** This matters for probe selection: many
  cheap SWD dongles are 3.3 V only.
- 16 MHz crystal `X1` = `ILCX19`, 32.768 kHz `X2` = Epson `FC-135`. **[V]**
- Magnetic reed switch (`PLATFORM_HAS_REED`). **[V]**
- Device identity is `NRF_FICR->DEVICEID`, burned into the die at Nordic.
  `common/util.h:52` defines `GET_UUID_64()` as `*(uint64_t*)NRF_FICR->DEVICEID`. **[V]**

### 2.2 Pill v1

Source: `github.com/hello/morpheus-board-pill`, Altium project `pill.pvt`, titled
"Pill Motion Detector", PVT sheet dated 12/30/2014, MP release 01/12/2015. The
schematic PDF extracts as text cleanly with `pypdf`, no OCR needed. **[V]**

| Ref | Part | Notes |
|---|---|---|
| `U1` | nRF51422-QFAA | |
| `U5` | InvenSense **MPU-6500** | SPI. WHO_AM_I returns `0x70`. |
| `U11` | TPS79718 | 1.8 V LDO |
| `X1` / `X2` | ILCX19 / FC-135 | 16 MHz / 32.768 kHz |
| `S1` | reed switch | |
| `P1` | Contact Spring | coin cell terminal |
| `B1` | 2450BM14E0003 | balun |
| `P2` | **HDR127P2x3, DNP** | the 6-hole debug header |
| `U4` | FM25FN10-G FRAM | **DNP**, "test pads only" |
| `D4` | CLV1A-FKB RGB LED | **DNP** |
| `U12` | TPS61220 boost | **DNP** |
| `RTC0` | AB-RTCMC-32.768kHz-AIGZ | **DNP**, matches `drivers/aigz.c` |

### 2.3 Pill v1.5

No schematic for `905-00007-12` has been located in the Hello org. The closest design
artifact is `github.com/hello/morpheus-board-pill-bqle` (2016), an energy-harvesting
variant (BQ25570 + TPS61098) whose BOM lists `U5 ST LIS2DH12` and `U1 nRF51422-QFAA`.
That BOM plus the HEAD firmware driver (below) is the basis for calling the v1.5 IMU a
LIS2DH12. **[I]**

Observed on the physical board:

- Populated: nRF51422, 4-pad ceramic 16 MHz crystal beside the MCU, a 2-pad gold-ceramic
  part marked `AS26N` (LF 32.768 kHz crystal, since `platform.h` reserves XTL_CLK and
  XTL_OUT), a small ~3x3 QFN (the IMU), a glass reed switch, and a SOT-23-6 marked
  `CA3D6`. **[M]**
  - `CA3D6` is **unidentified**. Marking-code database searches returned nothing. **[?]**
- **Unpopulated** footprints: a bare QFN land and a bare PLCC-4 land. These match
  `pillx_DVT1/platform.h`, which comments out both `PLATFORM_HAS_PROX` and
  `PLATFORM_HAS_VLED`. So the shipped v1.5 has **no RGB LED and no proximity sensor**,
  despite the layout supporting them. **[V]**
  - For reference, `drivers/prox_i2c.c` is a TI **FDC1004** capacitive sensor
    (manufacturer id `0x5449` = "TI", device id `0x1004`). **[V]**
- DataMatrix label decodes to **`905000071201165207450`**. **[V]**
  Reads as `905-00007-12` + `01` + `1652` (week 52, 2016) + serial `07450`. The split
  after the part number is inferred. **[I]** It is **not** the BLE/ANT device id.
  - Decode with `pylibdmtx`; on macOS it needs `DYLD_LIBRARY_PATH=/opt/homebrew/lib`.
- A ring of roughly 60 small plated holes runs around the board perimeter
  (test points or stitching). **[M]**

### 2.4 Debug header pinouts

**Pill v1, `P2`, 2x3 at 1.27 mm.** Derived from the schematic netlist. **[V]** for the
signal set; **[?]** for which physical hole is pin 1, because that comes from the PCB
layout, not the netlist.

| Pin | Signal | nRF51422 pin |
|---|---|---|
| 1 | TXD | 26 (P0.18) |
| 2 | SWDIO | 23 (SWDIO/nRESET) |
| 3 | VMCU | VDD |
| 4 | GND | VSS |
| 5 | RXD | 27 (P0.19) |
| 6 | SWDCLK | 24 |

**Pill v1.5, 2x5 at ~1.27 mm.** Pitch is measured from photographs (roughly 1.2 to
1.35 mm across a 25 to 28 mm board, which brackets 1.27 mm). **[M]** The **pinout is
completely undocumented** and must be buzzed out. **[?]** A 2x5 at 1.27 mm is the
standard ARM Cortex-M debug footprint, so the standard pinout is a reasonable first
hypothesis, but Hello may have folded in the UART instead. Verify before connecting.

**How to identify the pins on either board, battery removed:**

- `SWDIO` is nRF51422 QFN48 **pin 23**, `SWDCLK` is **pin 24**. Buzz each header hole
  against those two chip pins. This is definitive and beats trusting the table above.
- `GND` is continuity to the ground plane and the battery negative terminal.
- `VMCU` is continuity to the TPS79718 output.

---

## 3. Firmware platforms (kodobannin)

Source: `github.com/hello/kodobannin`, HEAD = tag `1.8.2`. Local clone lives at
`working-files/kodobannin`.

### 3.1 Platform table

| Platform dir | `HW_REVISION` | `PILL_HW_TYPE` | Board |
|---|---|---|---|
| `pill_PVT1` | 3 | `..._PILL` | v1 |
| `pillx_EVT1` | 3 | `..._PILL1_5` | v1.5 EVT |
| `pillx_DVT1` | 4 | `..._PILL1_5` | v1.5 shipping |

`tools/build_list.txt` lists `pill+pillx_DVT1` as the only pill app build at HEAD. That
describes what Hello was building **last**, not what shipped on older hardware. Do not
use it to infer which platform a given device runs. **[V]**

**`DEVICE_NAME "PillDFU"` is set identically in all three platforms' `dfu_config.h`,
so the DFU advertising name proves nothing about which board you are looking at.** **[V]**

### 3.2 IMU pin maps differ between v1 and v1.5

| Signal | `pill_PVT1` (v1) | `pillx_DVT1` (v1.5) |
|---|---|---|
| nCS | 29 | 13 |
| SCLK | 13 | 15 |
| MOSI | 15 | 9 |
| MISO | 25 | 11 |
| INT | 23 | 16 |

`pillx_DVT1` also defines `PLATFORM_HAS_IMU_VDD_CONTROL` (IMU_VDD_EN = 20),
`PLATFORM_HAS_I2C` and `PLATFORM_HAS_FSPI`; `pill_PVT1` has none of those. **[V]**

### 3.3 The IMU driver split, and why it is the dangerous part

`drivers/imu.c` at HEAD includes `lis2dh_registers.h` and requires
`DEVICE_ID 0x33` (ST LIS2DH). On mismatch it calls `APP_ASSERT(0)`, which is
`APP_ERROR_CHECK(!0)`, a fatal handler. See `drivers/imu.c:517`. **[V]**

`README_IMU.md` in the same repo describes an InvenSense MPU-6500 and is **stale**
relative to HEAD. **[V]** MPU-6500 answers WHO_AM_I with `0x70`, not `0x33`.

Consequence: **a HEAD build of any pill platform will hard-assert on a v1 board**,
because the v1 carries an MPU-6500. Choosing the right platform is necessary but not
sufficient.

---

## 4. Postmortem: why the v1 unit in this project is dark

On 2026-07-28 a `pill+pillx_DVT1` image built from HEAD was flashed to the v1 pill over
BLE legacy DFU. The upload succeeded (CRC and HMAC signature accepted, activate, reset)
and the device has never advertised BLE or sent ANT data since.

### 4.1 Boot call chain

1. `pill/main.c:146` calls `_init_rf_modules()`.
2. Inside it, `pill_ble_load_modules()` runs at line 60, commented "MUST load before
   everything else is initialized", **before** `hble_stack_init()` at line 68 and long
   before `hble_advertising_init()` at line 95.
3. `pill/pill_ble.c:150` loads the IMU module via `central->loadmod(MSG_IMU_Init(central))`.
4. `common/message_app.c:58` calls `mod->init()` on load.
5. `pill/message_imu.c` `_init()` calls `imu_init_low_power()` with the **DVT1** pin map.
6. `drivers/imu.c:517` reads WHO_AM_I, does not get `0x33`, and calls `APP_ASSERT(0)`.

The radio never initializes. This exactly matches the observed symptom: the bootloader
validates and activates the image, the app boots, and nothing ever comes up.

**Two independent causes**, either of which alone is fatal:

- Wrong platform: the DVT1 SPI pin map does not reach the v1's IMU.
- Wrong driver: HEAD's LIS2DH-only driver rejects the v1's MPU-6500 even with correct pins.

### 4.2 Why there is no over-the-air recovery

`bootloader/main.c` enters DFU only if:

- `bootloader_app_is_valid()` fails the CRC check at line 78. **Our image passes**, which
  is precisely why it boots the broken app every time.
- The `GPREGRET_FORCE_DFU_ON_BOOT_MASK` bit is set at line 85. Only the running app can
  set it (that is what writing `0x08` to the `deed` characteristic does), and a power-on
  reset clears it.

Line 62 checks `GPREGRET_APP_CRASHED_MASK`, so the bootloader **knows** the app crashed,
but it only prints a debug string. It does not force DFU. There is no button, reed
switch, or magnet entry condition anywhere in the bootloader. **[V]**

**SWD is the only route.**

---

## 5. Stock firmware images (doraemon)

`github.com/hello/doraemon` is Hello's factory release and recovery repo. Each
`targets/<name>/` directory holds complete, ready-to-flash production binaries. No
source build required.

`targets/pill_pvt/` (v1):

```
pill+pill_PVT1.bin
bootloader+pill_PVT1.bin
pill+pill_PVT1.crc
s310_nrf51422_1.0.0_softdevice_main.bin
s310_nrf51422_1.0.0_softdevice_uicr.bin
app+bootloader.prod.in      <- J-Link Commander script
target.sh
```

`targets/pill1p5_dvt/` (v1.5): identical file set with `pillx_DVT1` in place of
`pill_PVT1`.

Download with `gh`:

```sh
gh api "repos/hello/doraemon/contents/targets/pill_pvt/<file>" -q .download_url \
  | xargs curl -sL -o <file>
```

---

## 6. Flashing procedure

### 6.1 Bench setup

Probe: **SEGGER J-Link EDU Mini**. Chosen because the doraemon scripts are already
J-Link Commander format and because `nrfjprog --recover` handles a readback-protected
nRF51 cleanly. Its VTref range (1.2 V to 5 V) also covers the pill's 1.8 V rail.

Chain:

```
J-Link EDU Mini (0.05" 10-pin male)
  -> 0.05" 10-pin ribbon cable
  -> 1.27mm-to-0.1" adapter (Adafruit 2743 breakout, or Adafruit 2094 JTAG/SWD adapter)
  -> 0.1" female Dupont leads
  -> 2x3 1.27 mm pogo adapter (P50-E2 conical tips, which self-center in plated holes)
  -> pill P2
```

Signals to connect. Only four are needed:

| J-Link 10-pin | 20-pin JTAG equivalent | Signal | Pill |
|---|---|---|---|
| 1 | 1 | VTref | VMCU |
| 2 | 7 | SWDIO | SWDIO |
| 4 | 9 | SWCLK | SWDCLK |
| 3 or 5 | 4, or any even pin 6-20 | GND | GND |

Leave `nRESET` unconnected: nRF51422 pin 23 is SWDIO/nRESET combined and the pill header
exposes no separate reset line. Leave SWO, TXD and RXD unconnected.

On the 20-pin ARM JTAG standard, **pin 2 is Vsupply, not GND**. Use pin 4 or higher.

### 6.2 Electrical cautions

- **The target is 1.8 V.** Do not connect a 3.3 V-only probe.
- **Do not power the pill from the probe.** Fresh coin cell in the holder; VTref is a
  sense input only.
- SEGGER's bundled 9-pin 0.05" cable may be keyed at position 7, while the Adafruit
  adapters populate all ten. If the cable will not seat, that is why. Either pull pin 7
  from the adapter or use an unkeyed 10-pin cable.

### 6.3 Pre-flight

Before the pill ever sees a probe, buzz the **complete chain**, from each J-Link
connector pin through cable, adapter, jumper, and out to the pogo tip. Also confirm the
adapter board's internal mapping (0.05" pin 1 to JTAG pin 1, 0.05" pin 2 to JTAG pin 7,
0.05" pin 4 to JTAG pin 9).

On the pogo adapter itself, buzz each Dupont lead to its pogo pin position. **Ignore the
AVR ISP silkscreen labels**, that pinout does not match the pill.

### 6.4 Connect and unlock

The factory sets readback protection at the end of every production flash
(`w4 0x10001004, 0` writes UICR RBPCONF). So expect the part to be **locked**, and
expect flash reads to fail until you erase. **[V]**

```sh
JLinkExe -device nRF51422_xxAA -if SWD -speed 1000 -autoconnect 1
```

Unlock via mass erase. This is what the factory script does in its first four lines:

```
w4 4001e504, 2      // NVMC.CONFIG = EEN (erase enable)
w4 4001e50c, 1      // NVMC.ERASEALL = 1
sleep 50
r
```

`nrfjprog --family NRF51 --recover` does the same thing in one command.

ERASEALL wipes flash and UICR. It does **not** touch FICR.

### 6.5 Grab FICR.ER while you are in there

Once unlocked, before flashing anything, read the encryption root. This is the per-die
AES key the firmware uses for motion payloads (`common/util.c get_aes128_key()` returns
`NRF_FICR->ER`):

```
mem32 10000080, 4
```

Save those 16 bytes. This is the whole reason the SWD route was worth doing. Each chip
has its own value, so the v1 and v1.5 pills have different keys and each needs its own
`pill_key_store` row.

### 6.6 Flash

The factory script, with `$TEMP` replaced by your working directory. **v1 shown**:

```
w4 4001e504, 1
loadbin $TEMP/s310_nrf51422_1.0.0_softdevice_main.bin 0x0
loadbin $TEMP/s310_nrf51422_1.0.0_softdevice_uicr.bin 0x10001000
loadbin $TEMP/pill+pill_PVT1.bin 0x20000
loadbin $TEMP/bootloader+pill_PVT1.bin 0x36000
loadbin $TEMP/pill+pill_PVT1.crc.bin 0x3f000
w4 10001014, 36000
verifybin $TEMP/s310_nrf51422_1.0.0_softdevice_main.bin 0x0
verifybin $TEMP/s310_nrf51422_1.0.0_softdevice_uicr.bin 0x10001000
verifybin $TEMP/pill+pill_PVT1.bin 0x20000
verifybin $TEMP/bootloader+pill_PVT1.bin 0x36000
verifybin $TEMP/pill+pill_PVT1.crc.bin 0x3f000
r
g
sleep 3000
r
savebin $CACHE/device.info 0x3f400 0x200
r
```

Memory map:

| Address | Contents |
|---|---|
| `0x00000` | S310 SoftDevice |
| `0x20000` | application |
| `0x36000` | bootloader |
| `0x3f000` | bootloader settings page (the 12-byte `.crc`) |
| `0x3f400` | device info page, regenerated on first boot |
| `0x10001000` | SoftDevice UICR |
| `0x10001014` | UICR bootloader address, set to `0x36000` |

**Gotcha:** the repo file is named `pill+pill_PVT1.crc` but the script references
`pill+pill_PVT1.crc.bin`. Rename it or edit the script.

**Note on `nrfjprog`:** its `--program` wants Intel hex, while doraemon ships raw `.bin`
with implied load addresses. Either use J-Link Commander's `loadbin` as above, or
convert first:

```sh
arm-none-eabi-objcopy -I binary -O ihex --change-addresses 0x20000 app.bin app.hex
```

### 6.7 The only differences between v1 and v2 flashing

This is the short answer to "what changes between the two":

| | v1 | v1.5 |
|---|---|---|
| doraemon target dir | `targets/pill_pvt` | `targets/pill1p5_dvt` |
| App image | `pill+pill_PVT1.bin` | `pill+pillx_DVT1.bin` |
| Bootloader image | `bootloader+pill_PVT1.bin` | `bootloader+pillx_DVT1.bin` |
| CRC file | `pill+pill_PVT1.crc` | `pill+pillx_DVT1.crc` |
| SoftDevice | identical S310 v1.0.0 | identical S310 v1.0.0 |
| All load addresses | identical | identical |
| Unlock sequence | identical | identical |
| Physical header | 2x3, 1.27 mm | 2x5, 1.27 mm |
| Pogo adapter | 2x3 1.27 mm | 2x5 1.27 mm (e.g. Adafruit 5434 clip) |

**The two `app+bootloader.prod.in` scripts are structurally byte-identical apart from
the three filenames.** **[V]** Everything that actually differs is either the target
directory or the physical connector.

**Do not cross-flash.** A v1.5 image on a v1 board is exactly what caused the current
brick.

### 6.8 Re-protecting (optional)

The factory script ends by re-enabling readback protection:

```
w4 4001e504, 1
w4 0x10001004, 0
```

Skip this. Leaving the part unprotected costs nothing here and keeps SWD available if
you need to go back in.

---

## 7. What survives a mass erase

| Item | Survives? | Notes |
|---|---|---|
| `FICR.DEVICEID` | **Yes** | Not erasable. Device id is unchanged, so existing pairing and DynamoDB rows stay valid. |
| `FICR.ER` | **Yes** | The motion AES key. Permanent, per-die. |
| `FICR.DEVICEADDR` | **Yes** | BLE address. |
| Flash (app, bootloader, SoftDevice) | No | Restored from doraemon images. |
| UICR | No | Restored by the script. |
| Device info page `0x3f400` | No | **Auto-regenerated.** `bootloader/main.c:92` calls `factory_needs_provisioning()`, which runs `factory_provision_start()` on next boot. |
| `device_aes` inside the device info page | No | Freshly randomized. See below. |

So the pill comes back with the same identity and the same motion key. The only thing
not restored byte-for-byte is a random 16-byte value that does not affect the motion
path.

---

## 8. Keys

Two separate keys live on the pill and they are easy to confuse.

**`FICR.ER` at `0x10000080`** is the motion payload key. `common/util.c
get_aes128_key()` returns it, and `message_ant.c:190` encrypts every motion payload with
`aes128_ctr_encrypt_inplace(payload, len, get_aes128_key(), nonce)`. Per-die, random,
burned in at Nordic, unreadable except by on-chip code or SWD. **[V]**

CTR construction, confirmed against the server: `iv = nonce(8) || 0x00 * 8`, AES-128-CTR,
blob = `nonce(8) || ciphertext`. The fw2 payload is 8 data bytes plus trailing magic
`5A 5A`, so 10 ciphertext bytes, a single block, counter never advances.

**`device_aes`** is a *different*, randomly generated 16-byte value stored in the device
info page. `bootloader/main.c:137` copies it to RAM at `0x20003FF0` for the app. It is
Hello's "identity reported to HQ", not the motion key.

**The device info page is a second route to `FICR.ER`.** `common/device_info.c
generate_new_device()` fills `device_encrypted_info_t` with `device_id`,
`device_address`, the random `device_aes`, **`ficr[256]` (a verbatim copy of the entire
FICR block, so `ER` sits at offset `0x80` within it)**, and a SHA1. The blob is encrypted
with `HLO_FACTORY_AES` = `<redacted; see common/hlo_keys.h>` (`common/hlo_keys.h`), which
we have. **[V]** So `savebin` of `0x3f400` and an offline decrypt yields the motion key
without reading FICR directly. Worth remembering if a non-SWD path to that page ever
turns up on a live pill.

---

## 9. Open items

- **v1.5 2x5 header pinout is unknown.** Buzz it out against nRF51422 pins 23 and 24
  before connecting anything.
- **Which physical hole is pin 1** on the v1 `P2`. Netlist gives the signal set, not the
  layout order.
- **`CA3D6`** SOT-23-6 on the v1.5 board is unidentified.
- **v1.5 schematic** has not been found. `morpheus-board-pill-bqle` is a related 2016
  design but is an energy-harvesting variant, not necessarily this board.
- **Post-recovery firmware choice.** Stock doraemon images restore function but leave the
  motion key as `FICR.ER`. If you would rather bake in a known key, patch
  `get_aes128_key()` in `common/util.c` and build `pill+pill_PVT1` (v1) rather than
  reusing the HEAD DVT1 build. If building from HEAD for a v1, you must also deal with
  the LIS2DH-only IMU driver, most safely by relaxing the WHO_AM_I assert so a failed IMU
  init cannot take down the radio.

---

## 10. Sources

Hello GitHub org repos used (all still public as of 2026-08-02):

| Repo | What it gave us |
|---|---|
| `hello/morpheus-board-pill` | v1 Altium schematic and PCB, `905-00007-05`/`-A` |
| `hello/morpheus-board-pill-bqle` | 2016 pill BOM showing `U5 ST LIS2DH12` |
| `hello/pill-board` | earliest prototype, described as "nRF51822+MPU6500" |
| `hello/morpheus-board-pill-flex` | battery and ID flex |
| `hello/kodobannin` | firmware source, platform headers, bootloader |
| `hello/doraemon` | factory production images and J-Link scripts |
| `hello/hello-hardware-components` | Altium component libraries |

Related project memory files: `project_pill_aes_key.md`,
`project_pill_1_5_hardware.md`, `project_sleep_pill_pairing.md`.

Note: `knowledgebase/FLASH.md` in this folder is an earlier chat transcript covering the
same postmortem. Sections 3 and 4 here supersede it, in particular the IMU question it
left open, which the bqle BOM has since settled.
