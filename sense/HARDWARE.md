# Sense hub hardware

*Part of [Reviving the Hello Sense Sleep System](../README.md).*

The Sense (the orb) is a TI CC3200 WiFi SoC. This is the physical side of
working with one: how to get a serial console, what to buy, and how BLE pairing
behaves. For the software side, see [SENSE_SETUP.md](SENSE_SETUP.md).

## What you need


| Part | Purpose | Example |
|---|---|---|
| Micro-USB **male breakout board** | Breaks out VBUS/D-/D+/ID/GND from the Sense's recessed USB port | Treedix USB MicroB Plug Breakout Board (Amazon `B09JC7JPGN`) |
| **3.3V-capable USB-UART adapter** (FT232R) | UART serial to the Sense. **Must have a 3V3/5V jumper.** Set to 3.3V. | FT232 USB-UART board, FT232RL (Amazon `B0CSYVXH8L`) |
| Jumper wires | Connect breakout to UART adapter | Standard dupont wires |


## UART pinout (micro-USB connector)

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

See [SENSE_SETUP.md](SENSE_SETUP.md) for wiring details, key recovery, and server setup.

## BLE pairing

The Sense (orb) whitelists BLE peers. In normal mode it only responds to previously
bonded devices. To pair a new phone:

1. **Press and hold the button on the Sense** to enter pairing mode.
2. Open the iOS app's pairing screen within the pairing window.

Without pressing the button first, scan responses never arrive and connections time out.

The Sense advertises as `Sense-XX` where `XX` is the leading byte of its device ID
(e.g., device `49F277D951568DF3` advertises as `Sense-49`).
