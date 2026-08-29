# Infrastructure for the consolidated Hello Sense backend

*Part of [Reviving the Hello Sense Sleep System](../README.md).*

Three services where there used to be sixteen containers across four languages.

| Service | Language | What it is |
|---|---|---|
| **orb** | Go | The whole application in one static binary: the device edge, the app API, the background worker, and Apple push |
| **orb-algo** | Java 8 | A thin HTTP shim over Hello's original sleep algorithms, kept because reimplementing timeline scoring is not worth doing twice |
| **postgres** | C | The only datastore |

A fourth piece, `sense-server`, terminates TLS for the Sense itself. On Linux it
is a container here. On macOS it is not, and the reason is in
[The platform split](#the-platform-split) below.

## Quick start

```bash
make init          # creates .env and secrets/, overwrites neither
                   # then edit .env: POSTGRES_PASSWORD, and the APNS block
make build
make up
make migrate       # NEW database. Adopting an existing one? See below.
make verify
```

`make` on its own lists every target.

## Layout

Everything Docker needs is in this directory. Each service is a folder with its
Dockerfile at the root of the source it builds.

    infrastructure/
    |- Makefile  README.md  .gitignore  .env.example
    |- docker-compose.yml            postgres, orb, orb-algo
    |- docker-compose.linux.yml      adds sense-server on Linux
    |- scripts/migrate.sh
    |- orb/                          the Go module: cmd/ internal/ migrations/
    |  `- Dockerfile
    |- orb-algo/                     src/*.java, models/
    |  `- Dockerfile
    |- sense-server/                 sense_server.py, ntp_pb2.py
    |  `- Dockerfile
    |- secrets/                      gitignored: APNs key, device certs
    `- backups/                      gitignored

The only thing not here is `suripu-app-*.jar`. See below.

Not committed, and deliberately: `orb/migrations/dump/` is 25 MB of DynamoDB
scan output holding real sleep data, `orb-algo/build/` and `orb/migrate` are
build output. All are gitignored. `make check-secrets` will tell you if any of
it, or a key, becomes visible to git.

## Requirements

- Docker 24 or newer with Compose v2.17 or newer. The build uses named build
  contexts, which is what keeps a 77 MB jar out of this repository.
- `suripu-app-*.jar`. See below.
- Nothing else. All three services build from source in this directory.

## The suripu jar

`orb-algo` compiles against, and runs on, `suripu-app-0.6.0-SNAPSHOT.jar`. That
file is **not in this repository**: it is 77 MB of Hello's own build output and
not ours to redistribute. It is `.gitignore`d so it cannot be added by accident.

Point `SURIPU_JAR_DIR` in `.env` at wherever you have it. Compose passes that
directory to the build as a named context called `jars`, so the jar is read from
its real location and never copied into a build context. It is the one thing you
must supply yourself.

There is no Maven step. The shaded jar already contains suripu-core and every
transitive dependency, so `javac` needs nothing else, which avoids resolving
2016-era artifacts from repositories that no longer exist.

**The jar is not a stock upstream build.** Three fixes were applied before it
was built: a PostgreSQL JDBC driver bump (the 2013 driver cannot bind a
`java.util.UUID`, which broke every OAuth token request), two AWS clients that
ignored their configured endpoints and went to real AWS, and a clock-skew
tolerance that was discarding half of every buffered flush from the Sense.

They are in [`orb-algo/patches/`](orb-algo/patches/), with the exact upstream
commits they apply to and what each is for. Each has been verified to apply
cleanly to a fresh checkout of its base commit. **If you rebuild the jar from
upstream without them, apply them first**, or read that README to decide which
you actually need: two of the three are inert for orb-algo, and it says which.

## The platform split

The Makefile detects your OS and picks the right shape. You do not have to think
about it, but here is why it differs.

**On Linux**, `sense-server` runs as a fourth container with
`network_mode: host`. The container shares the host's network stack directly:
no port publishing, no NAT, no userland forwarder. The bytes the Sense sends are
the bytes tlslite-ng sees.

**On macOS**, it does not run in Docker at all. Run `sense_server.py` on the
host instead:

```bash
cd sense-server
sudo SENSE_UPSTREAM_SENSE=http://127.0.0.1:8081 \
     SENSE_UPSTREAM_TIME=http://127.0.0.1:8081 \
     SENSE_UPSTREAM_MESSEJI=http://127.0.0.1:8081 \
     SENSE_CERT_PATH=../secrets/server.crt \
     SENSE_KEY_PATH=../secrets/server.key \
     <python-with-tlslite-ng> sense_server.py
```

It needs `tlslite-ng`, `cryptography` and `protobuf`. Any interpreter with
those will do; the Dockerfile pins the same three.

The reason is the device. The CC3200 offers exactly one cipher suite,
`TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA` (0xC014), and sends no `supported_groups`
extension. OpenSSL 3.x, LibreSSL and Go all refuse that handshake; tlslite-ng,
pure Python, is the only thing that completes it. The same fragility means the
device endpoint **cannot sit behind nginx, Caddy, Cloudflare or a cloud load
balancer** either.

Docker Desktop on macOS runs a Linux VM, so every packet reaches a container
through a userland proxy. Putting a NAT hop in front of the one client on the
network that cannot tolerate surprises buys nothing, and this device has already
been broken twice at exactly that layer. On Linux the concern does not exist,
so there it is a container like anything else.

Nothing about orb changes either way: it publishes `:8081` to the host, and
`sense_server.py` reaches it at `127.0.0.1:8081` whether it is a host process or
a host-networked container.

## Secrets

Everything sensitive lives in `./secrets/`, which is gitignored along with
`.env` and every `*.p8`, `*.key`, `*.pem`, `*.crt` and database dump. The
patterns are deliberately broad. Forcing an occasional `git add -f` for a
harmless file is cheaper than leaking a private key once.

`make check-secrets` fails if anything matching those patterns is visible to
git. Worth wiring into CI.

**The APNs signing key.** Put the `.p8` in `secrets/`, set `APNS_KEY_FILE` to
its filename (not its path), and make it readable by the container's user:

```bash
sudo chown 10001 secrets/AuthKey_XXXXXXXXXX.p8
sudo chmod 600 secrets/AuthKey_XXXXXXXXXX.p8
```

Leave `APNS_KEY_FILE` empty and push is simply off. orb logs `push disabled` at
startup and everything else works.

**On Linux, the directory matters as much as the file.** orb runs as uid 10001
and needs to traverse `secrets/` to reach the key. If the directory is `700`
and owned by you, it cannot, and orb crash-loops with

```
level=ERROR msg=apns err="push: read key: open /secrets/AuthKey_XXXXXXXXXX.p8: permission denied"
```

on a file whose own permissions look fine. `make init` now creates it `711`, so
any uid can open a file it knows the name of while nobody can list the
directory. If you created `secrets/` before this change, run `chmod 711 secrets`.

This never happens on macOS, because Docker Desktop rewrites ownership on bind
mounts. It happens immediately on the Linux host you deploy to.

**The device certificates.** `server.crt` and `server.key` also go in
`secrets/`, and are only used by `sense-server` on Linux.

## A certificate for the app API

The app API and the device endpoint cannot share a TLS terminator. The Sense
offers one cipher and sends no `supported_groups`, so Caddy, nginx and every
cloud load balancer refuse its handshake; only `tlslite-ng` completes it. The
device's port is not negotiable either, since the firmware hardcodes 443
(`kitsune/wifi_cmd.c:1193` sets the port bytes to `0x01BB`). So `sense-server`
holds 80 and 443, and the app API goes somewhere else, typically Caddy on 8443.

That leaves Caddy unable to get its own certificate: Let's Encrypt validates
http-01 on **port 80 only**, and tls-alpn-01 on **443 only**. Both belong to the
device.

The way out is to let `sense-server` answer the challenge and nothing else:

1. `make init` creates `acme/`. Point certbot at it as a webroot.
2. Set `SENSE_ACME_WEBROOT=/acme` in `.env` and restart `sense-server`.
3. Issue the certificate:

```bash
sudo certbot certonly --webroot -w ./acme -d sense.example.com
```

4. Point Caddy at the resulting `fullchain.pem` and `privkey.pem`, and have it
   reverse-proxy to orb's API port.

`sense_server.py` serves exactly one path prefix,
`/.well-known/acme-challenge/`, read-only, and only when `SENSE_ACME_WEBROOT` is
set. Token names must be entirely base64url characters, so nothing from the
network reaches the filesystem unchecked. With the variable unset the code path
does not exist, which is the default and how it behaves on macOS.

The Sense never requests that prefix, so the device path is unaffected.

**Why not give Caddy port 80 and put `sense-server` behind it?** Renewals would
then handle themselves, which is tempting. But the device's port 80 carries time
sync, and a device with a wrong clock has every sample it uploads discarded as
out of range. Putting a second proxy in front of that path to save a cron job is
not a good trade, and this file has already caused one two-hour outage.

## Adopting an existing deployment

If your database already has the schema because the migrations were applied by
hand, do **not** run `make migrate`. It would try `0001_init.sql` against a
database that already has those tables and fail on a duplicate object.

```bash
make migrate-baseline    # records all 13 as applied, runs none of them
```

Then `make migrate` behaves normally from that point on.

Two other things when adopting:

- **Keep `COMPOSE_PROJECT_NAME=hello-orb`.** The volume name derives from it, so
  the default reuses the existing `hello-orb_pgdata`. Changing it starts from an
  empty database, silently.
- **`make up` will recreate the `postgres` container** with this file's
  configuration. The volume is reused, so no data moves. The old configuration
  forced MD5 password encryption for a 2016 JDBC driver in services that no
  longer exist; that is gone here, and an existing data directory keeps working
  because its `pg_hba.conf` is already written.

## Operations

| Command | What it does |
|---|---|
| `make up` / `make down` | start, stop. `down` keeps volumes |
| `make ps` / `make logs` / `make logs-orb` | status and tails |
| `make verify` | proves the stack works, not just that it started |
| `make migrate` / `make migrate-status` | schema |
| `make psql` | a shell on the orb database |
| `make backup` | timestamped dump into `./backups` |
| `make restore DUMP=<file>` | destructive, asks for confirmation |
| `make config` | the fully resolved Compose file, for when something is not what you think |
| `make nuke` | deletes the database volume, asks for confirmation |

`make verify` checks container health, orb's `/ping`, orb-algo's `/health`, the
table and migration counts, and that the zone database is present in the orb
image. That last one matters more than it sounds; see below.

## Ports

| Port | Service | Who talks to it |
|---|---|---|
| 8081 | orb edge | `sense_server.py` |
| 9999 | orb app API | the iPhone, or Caddy |
| 5432 | postgres | bound to `127.0.0.1` by default |
| 8090 | orb-algo | orb only, not published |
| 80, 443 | sense-server | the Sense. Linux only |
| 8443 | Caddy | the app, over the internet. `ORB_PUBLIC=1` only |

Each orb port is configured as a port plus an optional bind address:
`ORB_EDGE_PORT` with `ORB_EDGE_BIND_IP`, and `ORB_API_PORT` with
`ORB_API_BIND_IP`. Empty bind addresses publish on all interfaces, which is what
a LAN deployment wants.

**On an internet-facing host, set both bind addresses to `127.0.0.1`.** Caddy
reaches orb by service name over the Compose network, and `sense-server` reaches
the edge over the host's loopback, so neither port needs a public interface and
neither depends on the firewall being configured correctly.

The port and the address are separate variables for a reason. `ORB_EDGE_PORT` is
also interpolated into `sense-server`'s upstream URL, where only a bare number is
valid. These were one variable each until 2026-08-29, and putting an address in
the edge one produced `http://127.0.0.1:127.0.0.1:8081`: `sense-server` started,
logged its upstreams, bound 80 and 443, reported itself healthy, and failed every
device request at proxy time.

## Supervision, and a real macOS caveat

`restart: unless-stopped` brings every container back if it crashes or if the
Docker daemon restarts. That is equivalent to what a systemd unit or a launchd
daemon gives you, with one exception that matters on macOS.

**On Linux**, Docker runs as a systemd service and starts at boot, before and
independently of any login. Containers with `restart: unless-stopped` come back
on their own after a reboot or a power cut. There is nothing to think about.

**On macOS**, Docker Desktop is a user application. It cannot start before
someone logs in, and by default it does not start on login either. So a reboot
leaves the entire backend down until a human opens Docker Desktop.

That is a genuine downgrade from a launchd daemon, which starts at boot with no
session at all. If you run this on a Mac and care about an alarm ringing after a
power cut, you need **both**:

1. Docker Desktop: Settings, General, "Start Docker Desktop when you sign in".
2. macOS: System Settings, Users & Groups, automatic login for that user.

Check the current state of the first with:

```bash
python3 -c "import json;print(json.load(open('$HOME/Library/Group Containers/group.com.docker/settings-store.json')).get('AutoStart'))"
```

If neither is acceptable, keeping orb as a launchd daemon on macOS and using
these containers only on Linux is a perfectly reasonable split. The images and
the compose file do not care which you choose.

## Things that will bite you

**Verify through the proxy, not just against orb.** orb routes `time.hello.is`
by Host header. `sense_server.py` used to rewrite Host to its upstream's
address, which 404'd every clock request the device made for two hours on
2026-08-28. It hid because `cmd/timecheck` sets the Host header itself, so it
proved orb correct in isolation while the real path was broken. Both ends are
fixed, but the habit is the lesson: test the path, not the component.

```bash
go run ./cmd/timecheck -target http://127.0.0.1:80/ -host time.hello.is
```

**Nothing here watches for absence.** `make verify` proves things respond. It
cannot tell you the device stopped asking for something. The Sense requests a
clock every three hours; if that stops, no check in this repository notices.

**tzdata is not optional in the orb image.** orb calls `time.LoadLocation` in
three places, including the alarm path and the sleeper's own timezone. Without
the zone database those calls fail and the code falls back to a fixed numeric
offset, **silently**. The symptom is an alarm exactly one hour wrong after a
daylight saving change. This is why the orb image is Alpine and not `scratch` or
`distroless-static`, and why `make verify` checks for `/usr/share/zoneinfo`.
Nothing fails at startup to warn you.

**`0001_init.sql` opens its own transaction.** `scripts/migrate.sh` detects that
and does not wrap it in a second one. If you add a migration that manages its
own transaction, the script handles it, but the ledger row is then written
separately: a crash in that window leaves the migration applied but unrecorded,
and the next run fails loudly on a duplicate object rather than corrupting
anything.

**orb exits immediately if Postgres or orb-algo are not up.** That is
deliberate: a backend that pretends to be healthy without a database is worse
than one that is visibly down. `depends_on` with health conditions makes it a
non-event rather than a restart loop.

**There is no `-api-fallback`.** It used to forward unimplemented app API paths
to `suripu-app`. orb answers everything now, so a fallback would only mask a
missing route.

**JAVA_OPTS is empty by default.** orb-algo's JVM sits around 400 MB RSS.
`-Xmx256m` or `-XX:MaxRAMPercentage=25` will cut that, but neither has been
tested against a real overnight timeline computation, so capping the heap is
left as a deliberate choice rather than a default that fails at 3am.
