# Self-hosting the Hello Sense

Make a **Hello Sense** sleep tracker (the company is defunct, cloud shut down) upload its
sensor data to a **local server** over its own WiFi + HTTPS, exactly the way it once
talked to Hello's cloud.

Confirmed working on firmware **1.3.0 / build 4513**.

> **Your Sense is irreplaceable.** Nothing here is guaranteed non-destructive. The
> flash steps (installing a CA, etc.) modify the device. **Run `make backup` first** so
> you can restore. Proceed at your own risk.

---

## What this does

`sense_server.py` is a TLS front-end. It handles the two things only it can (the
device's ancient TLS handshake and its antique certificate clock), then forwards every
request untouched to Hello's own backend services, rebuilt from source and running
under docker compose in `../infrastructure`.

```
   Sense --WiFi--> your DNS (*.hello.is -> your PC) --> sense_server.py (:80, :443)
                                                             |  terminates TLS, proxies
                                    time.hello.is     ------>|--> hello-time      :1111
                                    sense-in.hello.is ------>|--> suripu-service  :5555
                                    messeji.hello.is  ------>|--> messeji         :10000
```

Bodies are relayed byte-for-byte. Every request is AES-CBC signed with the device's own
key, and the Java services verify that signature against the key in DynamoDB's
`key_store` table, so nothing here may rewrite a payload.

Five problems had to be solved, all handled by the code here:

1. **Message signing** -- every request is AES-128-CBC signed with a per-device key stored
   on the device. You recover that key over UART, then seed it into `key_store` so the
   backend can verify requests (`infrastructure/docker/seed-device.sh`).
2. **DNS** -- the endpoints (`sense-in.hello.is`, `messeji.hello.is`, `time.hello.is`) are
   hardcoded in firmware, so a small DNS server redirects `*.hello.is` to your PC.
3. **TLS** -- the device speaks an ancient handshake modern TLS libraries reject. We
   terminate TLS with **tlslite-ng** (pure Python).
4. **Cert trust + a clock bug** -- you install your own CA on the device, and the server
   cert must be dated from **1950** because the device's clock starts ~70 years behind.
5. **The NTP epoch** -- the Sense expects timestamps counted from **1900**, not the Unix
   1970 epoch. Feed it Unix seconds and it sits exactly 70 years in the past (reporting
   1956), and the backend then discards every sensor sample as more than two hours out
   of sync. `hello-time` gets this right, which is why time sync is proxied to it.

See **[How it works](#how-it-works)** for the details.

### Running against the backend

Start the stack first (see `../infrastructure/docker`), then `make run`. Upstreams and
behaviour are configurable by environment variable:

| Variable | Default | Purpose |
|---|---|---|
| `SENSE_UPSTREAM_TIME` | `http://127.0.0.1:1111` | hello-time |
| `SENSE_UPSTREAM_SENSE` | `http://127.0.0.1:5555` | suripu-service |
| `SENSE_UPSTREAM_MESSEJI` | `http://127.0.0.1:10000` | messeji |
| `SENSE_TIME_MODE` | `proxy` | set `local` to answer time sync in Python instead, for running with no backend |
| `SENSE_AES_KEY` | -- | 32 hex chars, only needed for `SENSE_TIME_MODE=local` |

---

## Hardware

### Sense UART access

The Sense's micro-USB port carries UART serial (not real USB) on the D+/D- pins.

| Part | Example |
|------|---------|
| Micro-USB **male breakout board** (breaks out VBUS/D-/D+/ID/GND) | Treedix USB MicroB Plug Breakout Board (Amazon `B09JC7JPGN`) |
| **3.3V-capable USB-UART adapter** (FT232R with a 3V3/5V jumper) | FT232 USB-UART board (FT232RL, 5V/3.3V) (Amazon `B0CSYVXH8L`) |

### Physical access

The Sense's micro-USB port is recessed, so to seat the breakout you must partially
disassemble the sphere:
1. Remove the **4 screws on the bottom**.
2. Remove the screws holding the **light ring** and the **internal board**, so the board
   lifts enough for the breakout to plug into the micro-USB port.

### UART pinout (micro-USB connector)

```
Pin 1 (VBUS) = 5V power
Pin 2 (D-)   = UART TX (Sense -> host)
Pin 3 (D+)   = UART RX (host -> Sense)
Pin 4 (ID)   = mode select (see below)
Pin 5 (GND)  = ground
```

Baud rate: **115200, 8N1**

### Wiring (UART adapter to micro-USB breakout)

Set the adapter's voltage jumper to **3.3V**. The CC3200 is **not** 5V-tolerant on its
I/O, and 5V also silently breaks the bootloader handoff (see Troubleshooting).

| Breakout pin | Adapter pin | Notes |
|---|---|---|
| `VBUS` | **5V** | raw USB 5V, powers the Sense |
| `GND`  | `GND` | common ground |
| `D+`   | `TXD` | **D+/D- are swapped on this breakout. If no console output, swap D+ and D-.** |
| `D-`   | `RXD` | |
| `ID`   | `GND` **or** `RTS` | mode select (below) |

### Two modes (the `ID` pin)

| `ID` connected to | Mode | Use |
|---|---|---|
| `GND` | **debug console** -- device boots normally, 115200 8N1 | watch boot log, `connect` to WiFi |
| adapter `RTS` | **CC3200 bootloader** -- flash read/write via `cc3200tool` | `make backup`, `read-key`, `write-ca` |

Power-cycle after changing the `ID` pin. (`RTS` idles high, which is what puts the CC3200
into the bootloader; `cc3200tool` is invoked with `--sop2 '~rts'`.)

### Console commands

Once connected in debug mode (ID to GND), these commands are available:

| Command | Description |
|---|---|
| `temp` | Read temperature |
| `humid` | Read humidity |
| `dust` | Read dust/particulate level |
| `light` | Read light level |
| `prox` | Read proximity sensor |
| `set-time <unix_ts>` | Set device clock (bypasses network time sync) |
| `connect <ssid> <key> 2` | Connect to WPA2 WiFi (profile persists across reboots) |
| `factory_reset` | Factory reset the device |
| `led stop` | Stop LED animation |
| `nwp` | Network processor info |

---

## Setup

```bash
make setup        # venv + deps + cc3200tool + generate *_pb2.py from proto/
```

Requires Python 3.9+ and (for `gen-certs`) nothing but the venv. tlslite-ng and the
`cryptography` cert generator are pure/portable, so the host OpenSSL version doesn't
matter.

---

## Step by step

Set `PORT` to your adapter's serial device (find it: `ls /dev/tty.usbserial-*` on macOS,
`/dev/ttyUSB*` on Linux).

### 1. Back up the device (bootloader mode: `ID` to `RTS`, power-cycle)

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

> **Critical: use 3.3V signal levels.** At 5V the `SWITCH_2_APPS` bootloader handoff
> silently fails. Set the FT232 jumper to 3V3 so TXD/RXD/RTS all swing 3.3V. Power
> the Sense's VBUS from the board's separate 5V pin (USB VBUS), not VCCIO.

### 3. Generate certificates

```bash
make gen-certs        # -> ca.crt ca.der ca.key server.crt server.key (valid 1950-2046)
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

Switch the device to **debug mode** (`ID` to `GND`) and power-cycle so the app boots and
joins WiFi. Within a minute you should see, in the `make run` output and `sense_data.jsonl`:

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

**UART bootloader -- the 3.3V gotcha.** `cc3200tool` can read/write the SimpleLink flash,
but only after `SWITCH_2_APPS` hands control to the APPS bootloader. At 5V logic levels
that handoff silently fails; at **3.3V** it works. (Power VBUS at 5V, drive signals at 3.3V.)

**TLS -- the hard part.** The Sense's ClientHello offers only
`TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA` (0xC014) and sends **no `supported_groups`
extension**. OpenSSL 3.x, LibreSSL, and Go all reject this (`NO_SHARED_CIPHER`) because
they won't choose an ECDHE curve without the extension. The original Hello servers ran old
OpenSSL 1.0.x, which defaulted to P-256. We reproduce that with **tlslite-ng** (pure
Python), configured for `ecdhe_rsa` / `aes256` / `sha` / `secp256r1`. One tlslite quirk is
patched in `sense_server.py`: its `recv_into` returns `None` at EOF, which crashes
`http.server`; we make it return `0`.

**HTTP/1.1 keep-alive is required, not optional.** The Sense holds **one socket per host
and reuses it** across requests (its log shows `using sock 85 85` on consecutive
requests). If the server answers `HTTP/1.0`, that means close-after-response, so the
device's next request goes out on a socket we already closed: `recv` returns zero bytes
(`start recv error 0`), the request fails, and it only succeeds when the network task
retries over a fresh connection.

The result is that **every** request silently fails on its first attempt and succeeds on
the second, roughly 2-3 seconds later. On uploads this is invisible. On pill pairing it is
not: `_on_pair_failure` (kitsune `ble_proto.c`) replies to the phone immediately with
`ErrorType_NETWORK_ERROR`, which the app shows as **-12 "pairing failed"** — even though
the retry then succeeds server-side and the pill really does get paired.

`sense_server.py` therefore sets `protocol_version = "HTTP/1.1"` on the handler. This
needs three things to be true, and all are:

- an accurate `Content-Length` on every response (all paths set one, `0` on errors);
- `HOP_BY_HOP` stripping upstream `content-length` / `transfer-encoding` / `connection`,
  so framing headers can't duplicate or leak a `Connection: close`;
- a deadline on idle connections — `ReusableHTTPServer` is threaded with `daemon_threads`
  and sets `request_timeout` on every accepted socket, so a kept-alive connection the
  device abandons costs one daemon thread briefly rather than wedging the server.

After the change, TLS handshakes drop from roughly one per request to almost none, and
round trips that took 4-5 seconds complete in under one. To revert, delete the
`protocol_version` line.

**Cert trust + the 1950 date.** The device validates the server cert against
`/cert/ca.der`, so you install your own CA there. At power-on, before time sync
completes, the device's clock starts at ~1956 (NTP epoch offset). The TLS handshake
happens before the time sync response corrects the clock, so cert date validation fails
(`sl_Connect` error **-461 = `SL_ESECDATEERROR`**) unless the cert's `notBefore` predates
~1956. `gen_certs.py` dates the certs from **1950**, which satisfies both the pre-sync
clock and real time. Once hello-time responds, the clock is correct.

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `cc3200tool` times out at "Switching UART to APPS" | Signals at 5V. Set the jumper to **3.3V**. |
| No debug console output | Swap **D+ and D-** |
| `zsh: no matches found: ~rts` | Quote it: `--sop2 '~rts'` (the Makefile already does) |
| TLS `NO_SHARED_CIPHER` / `-340` | You're using OpenSSL, not tlslite. Use this server. |
| `sl_Connect` **-461** | Cert `notBefore` after the device's (skewed) clock. Use `gen_certs.py` (1950). |
| Device ignores responses, resends same reading | See "Known limitations" |
| Every request seems to take 3-5s; app shows `-12` on pill pairing | Server answering HTTP/1.0 while the Sense reuses sockets. See the keep-alive note above. |
| A request never appears in the proxy log at all, but the device reports failure | It was written to a socket the server had already closed. Same cause as above. |

### Read the Sense's own logs

The single most useful debugging move, and easy to miss: the Sense uploads its internal
`LOGF`/`LOGI` output via `POST /logs`. `LogsResource` only records the device id in the
service log — the actual text is published to the **Kinesis `logs` stream**. With the
local stack that is localstack on `http://localhost:4566`, shard
`shardId-000000000000`.

This is what tells you, in the device's own words, things the proxy log cannot show:
`signature validation fail`, `using sock 85 85`, `start recv error 0`, `NT <host><path> --
0` / `-- 1` attempt numbers, and `status: http/1.0 200 ok`.

```bash
export AWS_ACCESS_KEY_ID=x AWS_SECRET_ACCESS_KEY=x AWS_DEFAULT_REGION=us-east-1
IT=$(aws kinesis get-shard-iterator --stream-name logs \
       --shard-id shardId-000000000000 --shard-iterator-type LATEST \
       --endpoint-url http://localhost:4566 --query ShardIterator --output text)
aws kinesis get-records --shard-iterator "$IT" --limit 20 \
    --endpoint-url http://localhost:4566 \
  | python3 -c 'import sys,json,base64
for r in json.load(sys.stdin).get("Records",[]):
    d=base64.b64decode(r["Data"])
    print("".join(chr(c) if 32<=c<127 or c==10 else "." for c in d))'
```

Two gotchas: localstack stamps records with stale `ApproximateArrivalTimestamp` values, so
judge recency by whether records arrive **during** your watch rather than by that field;
and dump whole records rather than grepping for a keyword, since the mechanism usually
shows up in the surrounding lines.

---

## Known limitations

- **Readings repeat in standalone mode.** When running with `SENSE_TIME_MODE=local`
  (no backend), the server acks `/in/sense/batch` with a plain `200`. The firmware
  wants a **signed** response and resends until it gets one, so the same reading loops
  rather than advancing. When running against the full backend (the default `make run`
  mode), suripu-service signs the response and this does not happen.

---

## References / credits

- Firmware source: [hello/kitsune](https://github.com/hello/kitsune)
- Self-hosted server project + write-up: [oaeide/hello-sense-server](https://github.com/oaeide/hello-sense-server)
- Encryption discussion: [owarz/sense](https://github.com/owarz/sense)
- CC3200 flashing tool: [toniebox-reverse-engineering/cc3200tool](https://github.com/toniebox-reverse-engineering/cc3200tool)
- UART console discovery: [0ff/long-live-sense#2](https://github.com/0ff/long-live-sense/issues/2)
