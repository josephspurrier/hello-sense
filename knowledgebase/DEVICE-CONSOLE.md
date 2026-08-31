# The on-device console and SD-card file transfer

How to get an interactive shell on a Sense and move files on and off its
microSD card, without opening the case for a UART adapter. This is how the
full SD card image in `sense/sd-card/` was recovered. It needs a firmware
build with the console servers enabled (below); stock production firmware has
them off.

## Two consoles: UART and telnet

The firmware carries an interactive command console. Normally it is reached
over **UART** (the pogo-pin serial header inside the case, see
`sense/HARDWARE.md`). The firmware also has a **telnet server on TCP port 224**,
but it is compiled out in production, guarded by `BUILD_SERVERS` /
`BUILD_TELNET_SERVER` with a source comment "todo PVT disable!". The telnet
console is far more convenient because it needs no adapter, just the LAN.

Enable it with the voice build flag:

```sh
KITSUNE_ENABLE_SERVERS=1 KITSUNE_PROD_DOMAIN=example.com KIT_VER=<n> ./rebuild.sh
```

in `sense/firmware/kitsune-voice` (applies `patches/enable-telnet-console.patch`).
After flashing, connect with `nc <device-ip> 224`. There is **no
authentication** and it is plaintext, so this is a LAN-only debug capability:
build it when you need it, and reflash to a clean build to close port 224 when
done.

## The five fixes that made the telnet console usable

The `BUILD_SERVERS` code had bit-rotted since it was last built. The patch does
five things:

1. **Force-include** the console-server code (`#if 1` the two telnet guards)
   without defining `BUILD_SERVERS` (which would also disable the UART logger
   and analytics, and reroute logging).
2. **SDK bit-rot**: `SlSockNonblocking_t.NonBlockingEnabled` (was
   `NonblockingEnabled`); `sl_Accept(...)` in place of the undefined
   `sl_AcceptNoneThreadSafe`; drop the HTTP status-page task, whose
   `get_temp`/`get_humid`/`get_light`/`get_prox` are unimplemented and fail to
   link (dead-code elimination then drops them).
3. **16 KB console-task stack** (was 5 KB, which overflows on the first FatFS
   `f_open`, since the FAT layer is stack-heavy).
4. **Mirror console output to telnet** (`telnetPrint` in the echo path), so the
   `device log` output you would see on UART also comes over the socket.
5. **`x` read-once**: `open_stream_from_path(argv[1], 1)` instead of `2`; input
   mode `2` opens SD files in repeating mode and replays forever.

Two related fixes shipped separately: the `x` HTTP-export flush
(commit a95b7d1, `patches/http-export-flush.patch`) so the last buffered chunk
and terminator are actually sent, and the sense-server firmware-stream fix
(chunked, flushed writes) so large downloads do not stall on the device's 1s
receive timeout.

## The `x` stream command (read AND write)

`x IN OUT [rate] [filter]` builds a byte pipe from IN to OUT. Each endpoint is
named by a sigil (`open_stream_from_path` in `fatfs_cmd.c`):

| Sigil       | As IN (source)                | As OUT (sink)                     |
|-------------|-------------------------------|-----------------------------------|
| `$f<path>`  | SD card file, read            | SD card file, **write**           |
| `$~<path>`  | serial-flash file, read       | serial-flash file, write          |
| `$i<url>`   | HTTP GET (body is the source) | HTTP POST (body is the sink)      |
| `$a[rate]`  | microphone                    | speaker (playback)                |
| `$o`        | UART                          | UART                              |
| `$0`        | zeros                         | -                                 |

So the console reads **and** writes the card:

- **Read a file off the card** (to the backend):
  `x $f/SLPTON48/ST101.RAW $ihttp://sense-in.example.com/export/ST101.RAW`
  The landing zone is orb's `/export` route, off unless `ORB_EXPORT_DIR` is set.
- **Write a file onto the card** (from a server):
  `x $ihttp://<server-ip>:8000/newtone.raw $f/SLPTONES/ST013.RAW`
  The device GETs the URL and writes the body to the SD file. `$f` as a sink
  opens `f_open(path, FA_WRITE|FA_OPEN_ALWAYS)`: it creates the file or opens an
  existing one and writes from offset 0, but it does **not** truncate. Replacing
  a file with a shorter one leaves the old trailing bytes in place; for a clean
  overwrite, delete first (`rm <path>`) or use a create-always path. There is
  also `write <path> <text>` (`Cmd_write`) for tiny text files.

**Hazard**: FatFS here is non-reentrant (`_FS_REENTRANT=0`) and the wake-word
detector touches the card from an ISR, so any transfer can be corrupted
mid-operation if audio fires. Always verify (below) and retry.

## Enumerating the card

The firmware's file-sync (`fs`) scans only `SLPTONES` and `RINGTONE` (a
hardcoded `folders[]`), so its uploaded manifest hides `SLPTON48`, `RINGTO48`,
`VOICEUI`, `VOICE48`, and the root blobs. Use the console `ls`:

```
cd /SLPTON48
ls
```

The `ls <path>` argument is ignored (it always lists the current directory), so
`cd` first. `cwd` is a firmware global that persists across telnet connections.
The full card layout is documented in `sense/sd-card/README.md`.

## Recovery procedure (how the 48 kHz masters and VOICEUI came off)

1. Build and flash a telnet build (`KITSUNE_ENABLE_SERVERS=1`).
2. On the backend: `ORB_EXPORT_DIR=/export` in `.env`, `mkdir -p export &&
   sudo chown 10001 export` (orb runs as uid 10001), `docker compose up -d orb`.
3. For each file: `x $f<folder>/<name>
   $ihttp://sense-in.example.com/export/<name>`. Size the wait to the
   file (~60 KB/s), and retry on a short read.
4. **Verify each file** against the device's own cache: pull `<base>.SHA` (a
   20-byte **binary** SHA1 the firmware keeps beside each file), `xxd -p` it, and
   compare to `sha1sum` of the transferred `.RAW`. For a file with no `.SHA`
   cache, double-read it and require the two independent reads to agree (the
   corruption hazard above is random per read, so agreement is trustworthy).
5. Copy verified files out of `export/` (which is owned by the orb container),
   then clear `ORB_EXPORT_DIR` and restart orb.

This recovered all 81 card files, each verified three ways (device cache == VM
== local). The result is committed in `sense/sd-card/`.

## Cleanup and security

`/export` and the telnet console are both debug surfaces. When finished: clear
`ORB_EXPORT_DIR` and empty `export/`; and reflash the device to a clean build
(no `KITSUNE_ENABLE_SERVERS`) to close the unauthenticated port 224. A failed
flash always reverts to the running image, so this cannot brick the device.
