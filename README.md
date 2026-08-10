# Reviving the Hello Sense Sleep System

Bring a **Hello Sense** sleep tracking system (company defunct, cloud shut down) back
to life with local infrastructure. This covers the Sense hub, the Sleep Pill
accelerometer, the backend services, and the iOS app.

---

## Guides

| Guide | What it covers |
|---|---|
| [Sense Setup](SENSE_SETUP.md) | Self-hosting the Sense hub: UART access, AES key recovery, TLS/cert workarounds, local server, DNS redirect, WiFi data flow |
| [Pill Recovery](PILL_RECOVERY.md) | Recovering a bricked Sleep Pill via SWD: identifying your pill variant, building firmware from source, flashing with J-Link, AES key extraction |

---

## System overview

The Hello Sense sleep system consists of:

- **Sense hub** (the orb): a TI CC3200 WiFi SoC that collects temperature, humidity,
  light, and dust readings and uploads them over HTTPS to Hello's cloud.
- **Sleep Pill**: a Nordic nRF51422 BLE/ANT accelerometer placed on the pillow to
  track motion during sleep. It sends encrypted motion data to the Sense hub, which
  relays it to the cloud.
- **Backend services** (Java, built from [hello/suripu](https://github.com/hello/suripu)):
  five services that process sensor data, run sleep scoring algorithms, manage device
  pairing, alarms, and timelines.
- **iOS app** (built from [hello/suripu-ios](https://github.com/hello/suripu-ios)):
  pairs devices, displays sleep data, and controls alarms.

With Hello's cloud gone, the system needs local replacements for DNS, TLS termination,
time sync, and all five backend services. This project provides those replacements.

---

## Hardware and tools

### For the Sense hub (UART access and flash operations)

| Part | Purpose | Example |
|---|---|---|
| Micro-USB **male breakout board** | Breaks out VBUS/D-/D+/ID/GND from the Sense's recessed USB port | Treedix USB MicroB Plug Breakout Board (Amazon `B09JC7JPGN`) |
| **3.3V-capable USB-UART adapter** (FT232R) | UART serial to the Sense. **Must have a 3V3/5V jumper.** Set to 3.3V. | FT232 USB-UART board, FT232RL (Amazon `B0CSYVXH8L`) |
| Jumper wires | Connect breakout to UART adapter | Standard dupont wires |

### For the Sleep Pill (SWD debug and flash)

| Part | Purpose | Example |
|---|---|---|
| **SEGGER J-Link** (SWD debug probe) | Read/write the nRF51422 flash over SWD | J-Link EDU Mini V2 |
| **1.27mm pogo pin adapter** or probe wires | Contact the pill's debug pads without soldering | Spring-loaded pogo pins at 1.27mm pitch |
| Coin cell battery (CR2032) | Powers the v1 pill during SWD operations | Any CR2032 |

### Software tools

| Tool | Purpose | Source |
|---|---|---|
| `cc3200tool` | Read/write the Sense's CC3200 SimpleLink flash (backup, key recovery, CA install) | [toniebox-reverse-engineering/cc3200tool](https://github.com/toniebox-reverse-engineering/cc3200tool) |
| `JLinkExe` | SEGGER J-Link commander for SWD flash operations on the pill | [segger.com](https://www.segger.com/downloads/jlink/) |
| `nRF Connect` (mobile app) | BLE scanner for verifying pill advertisement, and legacy DFU firmware upload | Nordic Semiconductor (free, iOS/Android) |
| Docker | Build environment for nRF51 firmware (Linux amd64 container with GCC 4.7) | [docker.com](https://www.docker.com/) |
| Python 3.9+ | Runs sense_server.py, DNS server, cert generator, and BLE tools | |

---

## Source repositories

All source code is from Hello's public GitHub organization or community mirrors.

### Device firmware

| Repository | Contents |
|---|---|
| [hello/kitsune](https://github.com/hello/kitsune) | Sense hub firmware (CC3200) |
| [hello/kodobannin](https://github.com/hello/kodobannin) | Sleep Pill firmware (nRF51422, app + bootloader) |
| [hello/doraemon](https://github.com/hello/doraemon) | Factory flash tooling + prebuilt firmware images for all device variants |

### Backend services

| Repository | Contents |
|---|---|
| [hello/suripu](https://github.com/hello/suripu) | All five backend Java services (suripu-service, suripu-admin, suripu-workers, suripu-app, messeji) |
| [hello/hello-time](https://github.com/hello/hello-time) | NTP-epoch time sync service (handles the 1900-vs-1970 epoch translation) |

### Mobile apps

| Repository | Contents |
|---|---|
| [hello/suripu-ios](https://github.com/hello/suripu-ios) | iOS app (Swift 3 era, requires migration for modern Xcode) |
| [hello/suripu-android](https://github.com/hello/suripu-android) | Android app |

### SDK and board design

| Repository | Contents |
|---|---|
| [tdwebste/nRF51SDK](https://github.com/tdwebste/nRF51SDK) | nRF51 SDK v5.2.0 with S310 v1.0.0 headers (replacement for Hello's dead private fork) |
| [kmackay/micro-ecc](https://github.com/kmackay/micro-ecc) | ECC library (kodobannin submodule, public) |
| [hello/morpheus-board-pill](https://github.com/hello/morpheus-board-pill) | Pill v1 Altium PCB design (schematic, BOM, Gerbers) |
| [hello/morpheus-board-pill-bqle](https://github.com/hello/morpheus-board-pill-bqle) | Pill v1.5 board design (2016, LIS2DH12 IMU) |

### Community resources

| Repository | Contents |
|---|---|
| [oaeide/hello-sense-server](https://github.com/oaeide/hello-sense-server) | Early self-hosted server project and write-up |
| [owarz/sense](https://github.com/owarz/sense) | Encryption protocol discussion |
| [0ff/long-live-sense](https://github.com/0ff/long-live-sense) | Community reverse engineering (UART console discovery) |

---

## Sleep Pill hardware variants

There are two pill hardware variants with **incompatible GPIO pin mappings**. Flashing the
wrong variant's firmware is the most common cause of bricking. See
[Pill Recovery](PILL_RECOVERY.md) for full details.

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

## Sense hub hardware

### UART pinout (micro-USB connector)

The Sense's micro-USB port carries UART serial, not USB data.

```
Pin 1 (VBUS) = 5V power
Pin 2 (D-)   = UART TX (Sense -> host)
Pin 3 (D+)   = UART RX (host -> Sense)
Pin 4 (ID)   = mode select: GND = debug console, RTS = CC3200 bootloader
Pin 5 (GND)  = ground
```

Baud rate: **115200, 8N1**. Set the UART adapter to **3.3V** (the CC3200 is not
5V-tolerant on I/O, and 5V breaks the bootloader handoff).

See [Sense Setup](SENSE_SETUP.md) for wiring details, key recovery, and server setup.

---

## Pairing and BLE

### Sense BLE pairing

The Sense (orb) whitelists BLE peers. In normal mode it only responds to previously
bonded devices. To pair a new phone:

1. **Press and hold the button on the Sense** to enter pairing mode.
2. Open the iOS app's pairing screen within the pairing window.

Without pressing the button first, scan responses never arrive and connections time out.

The Sense advertises as `Sense-XX` where `XX` is the leading byte of its device ID
(e.g., device `49F277D951568DF3` advertises as `Sense-49`).

### Sleep Pill pairing

The pill is motion-activated to conserve its coin cell. The app says "shake quickly for
three seconds," but in practice:

- **Shake hard and continuously for 60-80 seconds** before the pill starts advertising.
- Keep shaking through the pairing attempt. The Sense's scan window is only 30 seconds.
- The pill advertises as `Pill-XX` (same leading-byte naming as the Sense).

Pairing flow: **phone -> Sense -> cloud**. The phone sends a BLE PAIR_PILL command to
the Sense, which scans for the pill and registers it via `POST /register/pill` on
suripu-service.

If pairing fails, read the error code from the app logs:
- **-4 (Timeout)**: Sense never saw the pill. Pill is asleep, shake harder/longer.
- **-12 (SenseNetworkError)**: Sense found the pill but the server call failed. Check
  suripu-service logs.
- **-5 (SenseAlreadyPaired)**: Pill is paired to a different account.

---

## Quick reference: device identification

| Property | Sense hub | Pill v1 | Pill v1.5 |
|---|---|---|---|
| MCU | TI CC3200 | Nordic nRF51422 | Nordic nRF51422 |
| Connectivity | WiFi + BLE | BLE + ANT | BLE + ANT |
| SoftDevice | n/a | S310 v1.0.0 | S310 v1.0.0 |
| Firmware repo | hello/kitsune | hello/kodobannin | hello/kodobannin |
| Debug interface | UART via USB | SWD (6-pin header) | SWD (10-pin header) |
| BLE name | `Sense-XX` | `Pill-XX` | `Pill-XX` |
| Key storage | `/cert/key.aes` on CC3200 flash | FICR.ER (hardware, permanent) | FICR.ER (hardware, permanent) |
| Key recovery | `cc3200tool` read file | SWD read of 0x10000080 | SWD read of 0x10000080 |
