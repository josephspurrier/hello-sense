What we flashed. The image we uploaded was pill+pillx_DVT1. That platform is exactly the board you're holding now: HW_REVISION 4, PILL_HW_TYPE HLO_ANT_DEVICE_TYPE_PILL1_5. So if the bricked pill is a v1 board (pill_PVT1,
HW_REVISION 3), we flashed v2 firmware onto v1 hardware.

One piece of my earlier reasoning was junk. I had recorded that pillx_DVT1/dfu_config.h setting DEVICE_NAME "PillDFU" confirmed the target. It confirms nothing: pill_PVT1, pillx_EVT1 and pillx_DVT1 all set the identical
string. The only real basis was tools/build_list.txt listing pill+pillx_DVT1 as the sole pill app build, and that file describes what Hello was building at HEAD (v1.8.2), not what shipped on an older pill running 0.5.6.1.

The platforms differ in ways that are fatal at boot. pill_PVT1 vs pillx_DVT1 move every IMU SPI pin:

┌──────┬───────────┬─────────────┐
│      │ PVT1 (v1) │ DVT1 (v1.5) │
├──────┼───────────┼─────────────┤
│ nCS  │ 29        │ 13          │
├──────┼───────────┼─────────────┤
│ SCLK │ 13        │ 15          │
├──────┼───────────┼─────────────┤
│ MOSI │ 15        │ 9           │
├──────┼───────────┼─────────────┤
│ MISO │ 25        │ 11          │
├──────┼───────────┼─────────────┤
│ INT  │ 23        │ 16          │
└──────┴───────────┴─────────────┘

And the boot path makes that unrecoverable. pill/main.c:146 calls _init_rf_modules(), which at line 60 runs pill_ble_load_modules() before hble_stack_init() at line 68. That loads the IMU module (pill/pill_ble.c:150), whose
_init() calls imu_init_low_power() with the DVT1 pin map. drivers/imu.c:517 then checks WHO_AM_I and, on mismatch, calls APP_ASSERT(0), which is APP_ERROR_CHECK(!0), a fatal error handler. The BLE stack and ANT never
initialize. That is precisely the symptom you saw: bootloader validates and activates the image, the app boots, and the radio never comes up.

There is a second, nastier layer. drivers/imu.c at HEAD includes lis2dh_registers.h and expects DEVICE_ID 0x33, which is an ST LIS2DH, not the InvenSense MPU-6500 that README_IMU.md describes. That README is stale relative to
HEAD. So even a correctly-targeted pill+pill_PVT1 build from HEAD would hit the same assert if the v1 pill carries an MPU-6500. The firmware-version gap (HEAD 1.8.2 vs the device's 0.5.6.1) is doing real damage here, not just
theoretical damage.

That also revises what I told you an hour ago about this new board: its small QFN is more likely an LIS2DH-family part than an MPU-6500, since the build that shipped on this platform drives a LIS2DH. The marking is unreadable
in the photos, so treat that as unsettled either way.

How to settle which board the bricked pill is. You have already found one hard difference, 6 holes versus 10. Two more checks on the old PCB:
- Silkscreen assembly number. This one is 905-00007-12. A different 905-000xx number means a different board.
- Look for a second SPI flash/FRAM chip and I2C parts. pillx_* defines PLATFORM_HAS_FSPI and PLATFORM_HAS_I2C; pill_PVT1 has neither, so the v1 should be visibly sparser.

What it changes for recovery. Not much, unfortunately: SWD was already the only route, and it still is. What it does change is what you flash once you are in. Target pill+pill_PVT1 if the old board is v1, and expect to have to
deal with the IMU driver mismatch, most likely by relaxing the WHO_AM_I assert in drivers/imu.c so a failed IMU init cannot take down the radio. With SWD attached you can see the assert fire directly instead of inferring it,
and you get FICR.ER in the same session.

I've updated both memory files with the corrected platform evidence and this failure mechanism.