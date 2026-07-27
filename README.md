# Self-hosting the Hello Sense

Make a **Hello Sense** sleep tracker (the company is defunct, cloud shut down) upload its
sensor data — temperature, humidity, light, dust — to a **local server** over its own
WiFi + HTTPS, exactly the way it once talked to Hello's cloud.

Confirmed working on firmware **1.3.0 / build 4513**.

> ⚠️ **Your Sense is irreplaceable.** Nothing here is guaranteed non-destructive. The
> flash steps (installing a CA, etc.) modify the device. **Run `make backup` first** so
> you can restore. Proceed at your own risk.

---

## What this does

```
   Sense ──WiFi──> your DNS (*.hello.is -> your PC) ──> your server (:80 HTTP, :443 HTTPS)
                                                             │
                                                   decodes + logs sensor_data.jsonl
```

Four problems had to be solved, all handled by the code here:

1. **Message signing** — every request is AES-128-CBC signed with a per-device key stored
   on the device. You recover that key over UART.
2. **DNS** — the endpoints (`sense-in.hello.is`, `messeji.hello.is`, `time.hello.is`) are
   hardcoded in firmware, so a small DNS server redirects `*.hello.is` to your PC.
3. **TLS** — the device speaks an ancient handshake modern TLS libraries reject. We
   terminate TLS with **tlslite-ng** (pure Python).
4. **Cert trust + a clock bug** — you install your own CA on the device, and the server
   cert must be dated from **1950** because the device's clock runs ~70 years behind.

See **[How it works](#how-it-works)** for the details.

---

## Hardware

| Part | Example |
|------|---------|
| Micro-USB **male breakout board** (breaks out VBUS/D-/D+/ID/GND) | Treedix USB MicroB Plug Breakout Board — Amazon `B09JC7JPGN` |
| **3.3V-capable USB-UART adapter** (FT232R with a 3V3/5V jumper) | FT232 USB-UART board (FT232RL, 5V/3.3V) — Amazon `B0CSYVXH8L` |

### Physical access
The Sense's micro-USB port is recessed, so to seat the breakout you must partially
disassemble the sphere:
1. Remove the **4 screws on the bottom**.
2. Remove the screws holding the **light ring** and the **internal board**, so the board
   lifts enough for the breakout to plug into the micro-USB port.

### Wiring (UART adapter ↔ micro-USB breakout)
Set the adapter's voltage jumper to **3.3V**. The CC3200 is **not** 5V-tolerant on its
I/O, and 5V also silently breaks the bootloader handoff.

| Breakout pin | Adapter pin | Notes |
|---|---|---|
| `VBUS` | **5V** | raw USB 5V — powers the Sense |
| `GND`  | `GND` | common ground |
| `D+`   | `TXD` | **D+/D- are swapped on this breakout — if no console output, swap D+ and D-** |
| `D-`   | `RXD` | |
| `ID`   | `GND` **or** `RTS` | mode select (below) |

### Two modes (the `ID` pin)
| `ID` connected to | Mode | Use |
|---|---|---|
| `GND` | **debug console** — device boots normally, 115200 8N1 | watch boot log, `connect` to WiFi |
| adapter `RTS` | **CC3200 bootloader** — flash read/write via `cc3200tool` | `make backup`, `read-key`, `write-ca` |

Power-cycle after changing the `ID` pin. (`RTS` idles high, which is what puts the CC3200
into the bootloader; `cc3200tool` is invoked with `--sop2 '~rts'`.)

---

## Setup

```bash
make setup        # venv + deps + cc3200tool + generate *_pb2.py from proto/
```

Requires Python 3.9+ and (for `gen-certs`) nothing but the venv — tlslite-ng and the
`cryptography` cert generator are pure/portable, so the host OpenSSL version doesn't
matter.

---

## Step by step

Set `PORT` to your adapter's serial device (find it: `ls /dev/tty.usbserial-*` on macOS,
`/dev/ttyUSB*` on Linux).

### 1. Back up the device (bootloader mode: `ID→RTS`, power-cycle)
```bash
make backup PORT=/dev/tty.usbserial-XXXX
```

### 2. Recover the AES key (bootloader mode)
```bash
make read-key PORT=/dev/tty.usbserial-XXXX
xxd -p key.aes | tr -d '\n' > aes.key      # 32 hex chars -> ./aes.key
```
`aes.key` is what the server signs with. (If the device has no key, it falls back to the
firmware default `1234567891234567`; leave `aes.key` absent to use that.)

### 3. Generate certificates
```bash
make gen-certs        # -> ca.crt ca.der ca.key server.crt server.key (valid 1950–2046)
```

### 4. Install your CA on the device (bootloader mode)
```bash
make write-ca PORT=/dev/tty.usbserial-XXXX
```

### 5. Point the Sense's DNS at your PC
Either set your **router's DHCP** to hand out your PC's IP as the DNS server on the
Sense's network, or use the firmware's `setDns` command over the debug console. Then, in
**terminal 1**:
```bash
make dns REDIRECT_IP=192.168.x.y      # your PC's LAN IP
```

### 6. Run the server (terminal 2)
```bash
make run
```
Switch the device to **debug mode** (`ID→GND`) and power-cycle so the app boots and joins
WiFi. Within a minute you should see, in the `make run` output and `sense_data.jsonl`:
```
[REQ] POST /in/sense/batch (Host: sense-in.hello.is ...)
  [BATCH] Device: ..., 1 reading(s)
    time=..., temp=24.9C/76.8F, humidity=40.5%, light=89, dust=1543
```

### (Optional) Connect the Sense to a WPA2 network
Over the debug console: `connect <ssid> <key> 2` (`2` = WPA/WPA2). The profile persists
across reboots.

---

## How it works

**Message signing.** Requests are `protobuf + IV(16) + AES-CBC(SHA1(body) padded to 32B)`;
responses mirror it. The key lives at `/cert/key.aes` on the CC3200 flash. `sign_response()`
in `sense_server.py` produces valid time-sync responses (which is what sets the device
clock and unblocks everything else).

**UART bootloader — the 3.3V gotcha.** `cc3200tool` can read/write the SimpleLink flash,
but only after `SWITCH_2_APPS` hands control to the APPS bootloader. At 5V logic levels
that handoff silently fails; at **3.3V** it works. (Power VBUS at 5V, drive signals at 3.3V.)

**TLS — the hard part.** The Sense's ClientHello offers only
`TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA` (0xC014) and sends **no `supported_groups`
extension**. OpenSSL 3.x, LibreSSL, and Go all reject this (`NO_SHARED_CIPHER`) because
they won't choose an ECDHE curve without the extension. The original Hello servers ran old
OpenSSL 1.0.x, which defaulted to P-256. We reproduce that with **tlslite-ng** (pure
Python), configured for `ecdhe_rsa` / `aes256` / `sha` / `secp256r1`. One tlslite quirk is
patched in `sense_server.py`: its `recv_into` returns `None` at EOF, which crashes
`http.server`; we make it return `0`.

**Cert trust + the 1950 date.** The device validates the server cert against
`/cert/ca.der`, so you install your own CA there. And its clock runs **~70 years behind**
(a firmware epoch bug: real 2026 shows as ~1956), so cert date validation fails
(`sl_Connect` error **-461 = `SL_ESECDATEERROR`**) unless the cert's `notBefore` predates
~1956. `gen_certs.py` dates the certs from **1950**, which satisfies both the skewed clock
and real time.

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `cc3200tool` times out at "Switching UART to APPS" | Signals at 5V — set the jumper to **3.3V** |
| No debug console output | Swap **D+ and D-** |
| `zsh: no matches found: ~rts` | Quote it: `--sop2 '~rts'` (the Makefile already does) |
| TLS `NO_SHARED_CIPHER` / `-340` | You're using OpenSSL, not tlslite — use this server |
| `sl_Connect` **-461** | Cert `notBefore` after the device's (skewed) clock — use `gen_certs.py` (1950) |
| Device ignores responses, resends same reading | See "Known limitations" |

---

## Known limitations

- **Readings repeat.** The server acks `/in/sense/batch` with a plain `200`; the firmware
  wants a **signed** response and resends until it gets one, so the same reading loops
  rather than advancing. Applying `sign_response()` to the batch/state responses is the fix
  (not yet done here).
- **Timestamps read ~1956** due to the device's epoch bug. The sensor values are correct;
  correct the year server-side (add 70 years, or stamp with server receive time).

---

## References / credits

- Firmware source: [hello/kitsune](https://github.com/hello/kitsune)
- Self-hosted server project + write-up: [oaeide/hello-sense-server](https://github.com/oaeide/hello-sense-server)
- Encryption discussion: [owarz/sense](https://github.com/owarz/sense)
- CC3200 flashing tool: [toniebox-reverse-engineering/cc3200tool](https://github.com/toniebox-reverse-engineering/cc3200tool)
