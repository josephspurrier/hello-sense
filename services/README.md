# services: the local cloud replacement for the Sense

These are the Python services that stand in for Hello's shut-down cloud so a
**Sense** hub can talk to a server on your LAN over its own WiFi + HTTPS.

| File | Role |
|---|---|
| `dns_server.py` | Redirects `*.hello.is` to your PC so the Sense reaches this server |
| `gen_certs.py` | Generates a local CA + server cert (dated from 1950 to satisfy the device's pre-sync clock) |
| `Makefile` | Orchestrates everything, including `make run`. Run `make help` |
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

**The full walkthrough** (UART/key recovery, DNS, TLS, the epoch/cert quirks, and
reading the Sense's own logs) is in **[../sense/SENSE_SETUP.md](../sense/SENSE_SETUP.md)**.

## The TLS front-end is not in this directory

`sense_server.py` lives at
**[`../infrastructure/sense-server/sense_server.py`](../infrastructure/sense-server/sense_server.py)**,
with the rest of the deployment. `make run` invokes it from there.

There used to be a second copy here. The two drifted apart, and the stale one
still contained a bug that had already cost a two-hour clock outage on the
device, so the copy was deleted rather than resynchronised. One file, one place.

## Backend dependency

`make run` proxies to **orb**, which must be up first:

```bash
cd ../infrastructure && make up
```

orb replaced Hello's Java stack entirely: it answers the device edge, the app
API, clock sync and the messeji long-poll in one Go binary. The five Java
services this used to point at are retired.

To run without a backend for a quick smoke test, use `make run-standalone`
(answers time sync in Python; note sensor readings will loop because the
standalone path cannot sign responses, see SENSE_SETUP.md).
