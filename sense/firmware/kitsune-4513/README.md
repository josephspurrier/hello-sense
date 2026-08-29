# Byte-exact rebuild of Sense firmware build 4513 (kitsune 1.9.2)

Rebuilds the Sense (non-voice, CC3200) firmware **byte-for-byte** from source:
`kitsune.bin`, SHA1 `0c5f639e1290df0e3a5f8641d670923ed71a5e63`, 146,864 bytes,
identical to the on-device flash dump and the official 1.9.2 release.

## Rebuild it

```bash
./rebuild.sh          # -> out/kitsune.bin  (verifies the SHA1 for you)
```

Needs Docker with 32-bit (386) emulation. On Apple Silicon / non-x86, once:

```bash
docker run --privileged --rm tonistiigi/binfmt --install 386
```

Everything is self-contained (toolchain and submodules are vendored here), so it
runs offline on any Docker host. First run builds the image (~5 min); the build
itself is ~30–90 min under emulation, far less on native x86-64.

## Building it for your own domain

The firmware hardcodes `sense-in.hello.is`, `messeji.hello.is` and
`time.hello.is`, which is why a self-hosted setup needs something on the LAN
answering DNS for a domain that no longer exists. Building your own endpoints in
removes that dependency:

```bash
KITSUNE_DEV_DOMAIN=example.com ./rebuild.sh     # -> out/kitsune-custom.bin
```

The result is **not** byte-exact, obviously, so the SHA1 check is skipped and the
output goes to a different filename rather than overwriting the 4513 reference.

**It rewrites the DEV slots, not the PROD ones, and that is the whole point.**
The firmware carries two sets of endpoints and chooses between them at boot from
a file on its own flash (`load_data_server`, `kitsune/wifi_cmd.c`). The console
commands `dev 1` and `dev 0` write and clear that file. So a device flashed this
way can be switched between your server and the original names over the serial
console, with no reflash:

```
dev 1     # use sense-in.example.com, messeji.example.com, time.example.com
dev 0     # back to the hello.is names
```

That turns "the new domain does not work" from a disassembly into a one-line
command.

`TIME_HOST` has no DEV twin upstream, so the rewrite adds one. Without it `dev 1`
would switch two endpoints out of three and leave the clock talking to a domain
that no longer answers, and a Sense with a wrong clock has every sample it
uploads discarded as out of range.

### Before you flash

1. **DNS.** `sense-in`, `messeji` and `time` under your domain must resolve to
   your server. These are public A records; nothing on the LAN is involved any
   more, which is the point of doing this at all.
2. **A certificate covering both name sets.** The device switches with `dev`, and
   a certificate covering only one set turns the other into a failed handshake:

   ```bash
   SENSE_EXTRA_DOMAIN=example.com python3 ../../../services/gen_certs.py
   ```

   Run it where `ca.key` already is. It reuses the existing CA, so **nothing has
   to change on the device.** Minting a new CA instead means the device trusts
   nothing until you reinstall it over UART.
3. **Deploy that certificate and confirm the device still works on the old
   names.** Prove the certificate swap on its own, before adding a firmware
   change to the same experiment.

### Flashing it

The CC3200 keeps three application images and a boot record
(`boot/application_bootloader/main.c`):

```
/sys/mcuimg1.bin   factory fallback
/sys/mcuimg2.bin   IMG_USER_1
/sys/mcuimg3.bin   IMG_USER_2
/sys/mcubootinfo.bin   { ucActiveImg, ulImgStatus, sha[3][20] }
```

The bootloader SHA1s the image it loads and compares it against `sha[]` in the
boot record. On a mismatch it runs the factory image instead, so a bad write
falls back rather than bricking. Write to the slot that is **not** active:

```bash
# with the device in bootloader mode (ID pin to RTS), from services/
cc3200tool -p /dev/tty.usbserial-XXXX --reset none --sop2 '~rts' \
  write_file ../sense/firmware/kitsune-4513/out/kitsune-custom.bin /sys/mcuimg3.bin
```

then update `/sys/mcubootinfo.bin` so `ucActiveImg` selects that slot and
`sha[2]` holds the new image's SHA1.

**Rollback is writing the original 88-byte `mcubootinfo.bin` back.** No image
needs rewriting to undo it. Back up the whole filesystem first
(`make backup` in `services/`) and keep it.

**One step here is unverified**: `write_file` has been used successfully on
`/cert/`, but not on `/sys/`, which may carry file tokens that `/cert/` does not.
Try it on `/sys/mcuimg3.bin`, the slot nothing boots from, before touching the
boot record.

## How it works (short version)

- **Toolchain:** TI CGT ARM **5.1.5** (the compiler) driven by GNU make, in a
  **linux/386** container. Only the compiler is vendored (`toolchain/cgt-5.1.5.tar.gz`,
  ~55 MB); the original CCS/Eclipse IDE isn't needed because the build runs the
  CCS-generated makefiles directly. See [PROCESS.md](PROCESS.md) for the licensing
  note on the TI compiler.
- **Source:** `hello/kitsune` tag `1.9.2`, `KIT_VER 4513`, with the `heap_6` and
  `tinyhttp` **submodules** populated from `submodules/` (their upstream
  `hello/heap_6` remote is gone; missing them silently builds a wrong, smaller
  binary).
- **The key fix:** under `-O4` the TI linker re-runs whole-program codegen, so
  **link module order** changes per-function padding. Building in the CI's exact
  order (subdir objects first, then root objects reverse-alphabetical; archives
  reverse-alphabetical) closes what was a stubborn 12-byte `.text` difference to
  zero. See `relink_reforder.sh`.

**Full write-up, root-cause analysis, and the forensic tools: [PROCESS.md](PROCESS.md).**
