# Custom firmware: what is possible, and the order to attempt it

A decision note, not a plan. Written 2026-08-16 after asking whether we could
build and flash our own firmware for the Sense and the pill.

## Short answer

**Already done once, for the pill.** It was bricked by a wrong-platform flash,
and then recovered over SWD. The full procedure is written up in
[`full-instructions/pill/PILL_RECOVERY.md`](../full-instructions/pill/PILL_RECOVERY.md):
variant identification, flash memory map, build environment, SWD wiring,
flashing, verification by PC register, AES key and pairing, factory images, and
BLE DFU as an alternative. **Read that before this file.** The pill is working
now and has been sending motion throughout.

So the question is not whether custom firmware is possible. It has been built,
flashed and verified. The question is what to attempt next, and the answer is
the Sense, which is a different risk entirely.

## What we have

Everything needed, which is unusual:

| repo | what it gives |
|---|---|
| `hello/kodobannin` | firmware source, platform headers, bootloader |
| `hello/doraemon` | factory release and recovery images, ready to flash, no build |
| `hello/morpheus-board-*` | Altium schematics and PCB for the boards |
| J-Link EDU Mini | SWD programmer, see WIRING.md |

`doraemon/targets/pill_pvt/` holds a complete v1 file set: app, bootloader,
softdevice, CRC and a J-Link Commander script. That is the known-good restore
path, and it is the reason a mistake on the pill is recoverable.

**Variant identification is the step that matters.** v1 is board `905-00007-05`,
target `pill+pill_PVT1`, last compiling tag **1.2.1**, hardware revision 3,
removable coin cell, 6-hole debug header. v1.5 is board `905-00007-12`, target
`pill+pillx_DVT1`, last factory tag **1.7.2**, hardware revision 4. The full
table is in `PILL_RECOVERY.md`.

## Why the first attempt bricked a pill

Recorded in full in [FLASH.md](FLASH.md). In short: `pill+pillx_DVT1` (v1.5
firmware) went onto a v1 board. The platforms move every IMU SPI pin, and the
boot path turns that into a hard stop rather than a degraded sensor.

`_init_rf_modules()` loads the IMU module **before** `hble_stack_init()`.
`imu_init_low_power()` reads `WHO_AM_I`, mismatches, and calls `APP_ASSERT(0)`.
The BLE stack and ANT never initialise. The bootloader validates and activates
the image, the app boots, and the radio never comes up, which is exactly the
symptom observed.

There is a second layer that matters more for any future build: `drivers/imu.c`
at HEAD expects `DEVICE_ID 0x33`, an **ST LIS2DH**, while `README_IMU.md`
describes an InvenSense MPU-6500 and is stale relative to HEAD. So even a
correctly targeted `pill_PVT1` build **from HEAD** would hit the same assert if
the board carries an MPU-6500. HEAD is 1.8.2; the device shipped 0.5.6.1.

**The lesson is not "be careful with pins".** It is that the source at HEAD has
drifted away from the hardware we own, so a build from HEAD is not a safer
starting point than a stock image. It is a more dangerous one.

## Order of attempt

1. ~~Revive the bricked pill.~~ **Done.** See `PILL_RECOVERY.md`. The toolchain
   is proven end to end: J-Link over SWD, build from `kodobannin`, verify by
   halting and reading the PC register.
2. **A modified pill build** is the natural next step, and the cheap one. The
   restore path is known-good, so the downside is a bench session rather than a
   loss. Verification is already understood: PC in the SoftDevice region
   (0x0000xxxx, the WFE idle loop) means it booted; PC in the app region stuck
   in `wfe / b.n` means it crashed into `app_error_handler`.
3. **Leave the Sense alone** until there is something custom firmware buys that
   the server cannot.

## Why point 3 keeps getting more true

Most of what custom firmware would be *for* has moved server-side. orb now
decides when the alarm rings, what the timeline says, what the app displays, and
what counts as a night. The firmware is largely a sensor and a radio.

The asymmetry is also stark: a bricked pill is a bench session with a J-Link. A
bricked Sense takes the whole system down and is the one unit everything else
depends on.

**Delivery is solved, though, which was the missing half.** orb now serves OTA,
so a built image has a way to reach the Sense without opening the case: a row in
`firmware_updates`, armed by hand, collected on the next sync in the small hours.
Nothing is automatic and the iOS app cannot trigger it. See the OTA section of
CONSOLIDATION-PLAN.md. That lowers the cost of *shipping* a firmware change; it
does not lower the cost of getting one wrong, so point 3 stands.
