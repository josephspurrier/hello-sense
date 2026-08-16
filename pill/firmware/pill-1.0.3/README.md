# Sleep Pill v1 firmware 1.0.3 (working ANT)

Source-built firmware for the **original Sleep Pill v1** (`pill_PVT1`, nRF51422 —
the pill that ships with the non-Voice Sense). Built at kodobannin tag **1.0.3**,
which is the build whose **ANT radio actually pairs** with the Sense.

`out/pill+pill_PVT1.bin` — version `1.0.3`, 42,568 bytes, SHA1 `e8475ef0…`.

## Why 1.0.3 and not a newer tag

The pill talks to the Sense over **ANT**, not BLE. The ANT transmit scheme changed
from asynchronous to synchronous at tag **1.1.1**, and the Sense's own nRF51 board
(built from the same code) is on the *async* scheme — so a newer pill build (e.g.
1.2.1) has working BLE but **dead ANT** and never pairs. 1.0.3 is the newest tag
that is still async, still builds for the v1 pill, and keys correctly. Full
analysis, rebuild steps, and the byte-exact toolchain validation are in
**[PROCESS.md](PROCESS.md)**.

## Build

```bash
NRF51_SDK=/path/to/nRF51SDK/Nordic ./rebuild.sh   # -> out/
```

Needs Docker and the nRF51 SDK v5.2.0 (S310 v1.0.0 headers) — Nordic-licensed, not
vendored here; get it from https://github.com/tdwebste/nRF51SDK. See PROCESS.md.

## Flash

Supply the Nordic S310 SoftDevice, then run the J-Link script — see
**[flash/README.md](flash/README.md)**.

Pill hardware identification, SWD wiring, and recovery: **[../../PILL_RECOVERY.md](../../PILL_RECOVERY.md)**.
