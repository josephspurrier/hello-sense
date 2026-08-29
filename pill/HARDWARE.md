# Sleep Pill hardware

*Part of [Reviving the Hello Sense Sleep System](../README.md).*

The Sleep Pill is a Nordic nRF51422 with a BLE and ANT radio. This is the
physical side: which variant you have, where the debug pads are, what to buy,
and how pairing actually works. For recovering a bricked pill, see
[PILL_RECOVERY.md](PILL_RECOVERY.md).

**There are two variants with incompatible GPIO pin mappings. Flashing the wrong
one is the most common cause of bricking.**

## What you need


| Part | Purpose | Example |
|---|---|---|
| **SEGGER J-Link** (SWD debug probe) | Read/write the nRF51422 flash over SWD | J-Link EDU Mini V2 |
| **1.27mm pogo pin adapter** or probe wires | Contact the pill's debug pads without soldering | Spring-loaded pogo pins at 1.27mm pitch |
| Coin cell battery (CR2032) | Powers the v1 pill during SWD operations | Any CR2032 |

## The two variants

Incompatible GPIO pin mappings, so the wrong firmware bricks the pill. See
[PILL_RECOVERY.md](PILL_RECOVERY.md) for recovery.

### Pill v1 (original, ships with Sense)

- Board: `905-00007-05` / `-A`
- MCU: Nordic nRF51422-QFAA
- IMU: ST LIS2DH12 (SPI)
- Battery: removable CR2032 coin cell
- Debug header: **6 holes** (2x3 @ 1.27mm pitch, active-low reed switch on P0.30)
- Build target: `pill+pill_PVT1`
- Last compiling firmware tag: **1.2.1**
- Factory image in doraemon: `targets/pill_pvt/` (firmware 0.9.3)

#### Pill v1 debug header pinout (P2, 2x3 @ 1.27mm)

From the schematic (`hello/morpheus-board-pill`, HDR127P2x3):

```
         +-------+
  Pin 1  | TXD   |  Pin 2  SWDIO
  (P0.18)|       |
         |       |
  Pin 3  | VMCU  |  Pin 4  GND
         |       |
         |       |
  Pin 5  | RXD   |  Pin 6  SWDCLK
  (P0.19)|       |
         +-------+
```

> **Note:** Physical orientation on your specific board may differ. Verify pin 4 (GND)
> with a multimeter against the battery negative terminal before connecting.

### Pill v1.5 (ships with Sense with Voice)

- Board: `905-00007-12`
- MCU: Nordic nRF51422-QFAA
- IMU: ST LIS2DH12 (SPI)
- Battery: sealed, non-replaceable
- Debug header: **10 holes** (2x5 @ 1.27mm pitch, standard ARM Cortex-M SWD pinout)
- Build target: `pill+pillx_DVT1`
- Factory firmware tag: **1.7.2**
- Factory image in doraemon: `targets/pill1p5_dvt/` (firmware 1.7.2)

#### Pill v1.5 debug header pinout (2x5 @ 1.27mm, standard ARM SWD)

```
         +---------+
  Pin 1  | VTref   |  Pin 2   SWDIO
         |         |
  Pin 3  | GND     |  Pin 4   SWDCLK
         |         |
  Pin 5  | GND     |  Pin 6   SWO
         |         |
  Pin 7  | (key)   |  Pin 8   TDI
         |         |
  Pin 9  | GND     |  Pin 10  nRESET
         +---------+
```

UART TX (P0.19) and RX (P0.18) are also available on the 10-pin header
(note: TX/RX pin numbers are swapped compared to v1).

### GPIO pin mapping comparison

| Function | v1 (pill_PVT1 @ 1.2.1) | v1.5 (pillx_DVT1 @ 1.7.2) |
|---|---|---|
| SPI nCS | P0.16 | P0.13 |
| SPI SCLK | P0.14 | P0.15 |
| SPI MOSI | P0.12 | P0.09 |
| SPI MISO | P0.10 | P0.11 |
| IMU VDD_EN | P0.20 | P0.20 |
| IMU INT | P0.23 | P0.16 |
| UART TX | P0.18 | P0.19 |
| UART RX | P0.19 | P0.18 |
| Reed switch | P0.30 | P0.30 |

---

## Pairing

**The Sense finds the pill over ANT, not BLE.** This is the single most misleading thing
about debugging pill pairing. A BLE scan from a laptop showing `Pill-XX` advertising
proves only that the pill's BLE stack is alive; it says nothing about whether pairing can
work. The pill's BLE radio is used for DFU and for the app's own firmware updates.

Pairing flow: **phone -> Sense -> cloud**. The phone sends BLE message type 12
(`PAIR_PILL`) with the account token. The Sense's nRF then:

1. Starts a **30 second** timer (`APP_PILL_PAIRING_TIMEOUT_INTERVAL`, kodobannin
   `morpheus/app.h`) and calls `ANT_UserSetPairing(1)`.
2. Waits for an `ANT_PILL_SHAKING` packet, which the pill sends only when its IMU
   detects the shake gesture (`_on_pill_pairing_guesture_detected` ->  `_send_shake()`,
   kodobannin `pill/message_imu.c`).
3. On receiving it, dispatches `MSG_BLE_ACK_DEVICE_ADDED` with the pill's 64-bit UID and
   the CC3200 calls `POST /register/pill` on suripu-service.

**Only the shake packet completes pairing.** Heartbeats and motion data arrive on
different branches of the same switch in `morpheus/ant_user.c` and do nothing for
pairing. It is entirely possible (and normal) to see `POST /in/pill` succeeding every
60 seconds while pairing still times out: the ANT link is healthy, the Sense just never
got a shake inside its window.

So, in practice:

- **Shake hard and continuously from before you tap, through the entire 30 second
  window.** A shake beforehand does not count. This is the most common cause of failure.
- Keep the pill **next to the Sense**. Range is measured from the orb, not the phone.
- The pill is motion-activated to conserve its coin cell and sleeps quickly; expect to
  shake for a long time if it has been idle.

If pairing fails, read the error code from the app logs (grep the app's own log for
`ble response has an error with device code`):

- **-4 (Timeout, ~30.5s)**: the ANT pairing timer expired without a shake packet. Either
  the pill is asleep, out of ANT range of the orb, or its ANT channel is dead.
- **-12 (SenseNetworkError)**: the Sense's HTTP call failed. Note this fires on the
  Sense's *first* attempt; if the proxy is answering HTTP/1.0 the first attempt always
  fails on a reused socket, so see the keep-alive note in [../sense/SENSE_SETUP.md](../sense/SENSE_SETUP.md).
- **-5 (SenseAlreadyPaired)**: pill is paired to a different account.

A successful pairing resolves in **under 7 seconds**. Anything taking the full 30
seconds is a `-4`.

Getting the app's log off the phone does not need USB:

```bash
xcrun devicectl device copy from --device <UDID> \
  --domain-type appDataContainer --domain-identifier <bundle-id> \
  --source Library/Caches/Logs --destination ./applogs
```

---
