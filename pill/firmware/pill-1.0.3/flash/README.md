# Flashing the pill 1.0.3 image

`flash_pill_1.0.3.jlink` writes the full pill_PVT1 stack over SWD (SEGGER
J-Link). Memory map:

| Region | Address | File | Provenance |
|---|---|---|---|
| SoftDevice | `0x0` | `flash/softdevice_main.bin` | **you supply** (Nordic S310 v1.0.0) |
| SoftDevice UICR | `0x10001000` | `flash/softdevice_uicr.bin` | **you supply** |
| Application | `0x20000` | `out/pill+pill_PVT1.bin` | built here (open source) |
| Bootloader | `0x36000` | `out/bootloader+pill_PVT1.bin` | built here (open source) |
| Boot settings | `0x3f000` | `out/app_settings.crc.bin` | generated here (CRC-16 of the app) |

## The SoftDevice (not shipped here)

The Nordic **S310 v1.0.0** SoftDevice is proprietary and is not redistributed in
this repo. It is only needed to *flash* (the app is built without it). Obtain it
one of two ways:

- **Read it off a working device** over SWD (it lives at `0x0`, ~102 KB) and
  save the dump as `flash/softdevice_main.bin`, plus its UICR at `0x10001000`
  (`flash/softdevice_uicr.bin`). This is what most people already have from a
  factory dump.
- **Download S310 v1.0.0 from Nordic** (nRF5 SDK / SoftDevice archive) under
  their license and convert the `.hex` to the two `.bin` regions.

If your pill already has the correct SoftDevice flashed and you only want to
swap the app, you can skip the two `loadbin`/`verifybin softdevice*` lines and
flash just the app + bootloader + settings.

## Run it

```bash
cd ..            # the pill-1.0.3 bundle root
JLinkExe -device nrf51422 -if swd -speed 4000 -autoconnect 1 \
         -CommanderScript flash/flash_pill_1.0.3.jlink
```

The pill's per-device AES motion key lives in hardware FICR (`NRF_FICR->ER`,
`0x10000080`) and is untouched by flashing — see `../../../PILL_RECOVERY.md`.
