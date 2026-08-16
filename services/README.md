# services — the local cloud replacement for the Sense

These are the Python services that stand in for Hello's shut-down cloud so a
**Sense** hub can talk to a server on your LAN over its own WiFi + HTTPS.

| File | Role |
|---|---|
| `sense_server.py` | TLS front-end (:80/:443): terminates the Sense's ancient TLS, then proxies requests to the backend |
| `dns_server.py` | Redirects `*.hello.is` to your PC so the Sense reaches this server |
| `gen_certs.py` | Generates a local CA + server cert (dated from 1950 to satisfy the device's pre-sync clock) |
| `Makefile` | Orchestrates everything — run `make help` |
| `proto/` | Protobuf definitions; `make setup` generates the `*_pb2.py` |
| `requirements.txt` | Python deps (installed into a local `venv` by `make setup`) |

## Quick start

```bash
make setup                          # venv + deps + cc3200tool + generated protobufs
make gen-certs                      # local CA + server cert
make dns  REDIRECT_IP=192.168.x.y   # terminal 1: point *.hello.is at your PC
make run                            # terminal 2: TLS front-end on :80/:443
make help                           # all targets (device backup, AES-key recovery, CA install, ...)
```

**The full walkthrough** — UART/key recovery, DNS, TLS, the epoch/cert quirks, and
reading the Sense's own logs — is in **[../sense/SENSE_SETUP.md](../sense/SENSE_SETUP.md)**.

## Backend dependency

`make run` proxies to Hello's five Java backend services (suripu, hello-time,
messeji), rebuilt from source and run under docker compose. That stack lives in
**`../../infrastructure/`** (a separate part of the project, not in this repo) and
must be up first. To run without a backend for a quick smoke test, use
`make run-standalone` (answers time sync in Python; note sensor readings will
loop because the standalone path can't sign responses — see SENSE_SETUP.md).
