# The Sense wire protocol

Written 2026-08-13 while reimplementing the device edge in Go. Everything here
was verified against a real Orb. Three items were got wrong first and only
caught by live traffic, each one layer deeper than the last:

| Mistake | Consequence | Caught by |
|---|---|---|
| compared 32 signature bytes, not 20 | every upload rejected | shadowing live traffic |
| stored audio raw instead of converting | all audio ~2.4% high, forever | diffing against the Java stack |
| stored absent counts as NULL, not 0 | "unknown" where the truth is "none" | diffing against the Java stack |

The unit tests were green throughout. **A self-signed test proves
self-consistency; a shadow proves you can parse; only a field-by-field diff
against the system you are replacing proves you interpret the same way.**

## Where the protos actually live

`github-backup/proto/` is the canonical repository: about 40 files including
`periodic.proto`, `morpheus_ble.proto`, `state.proto`, `file_manifest.proto`,
`sync_response.proto` and `ntp.proto`.

This matters because `suripu-api` does **not** contain them. Its `proto.sh`
generates from only four files, none of which define `batched_periodic_data`,
yet the Java references `DataInputProtos.batched_periodic_data`. Copies in
`working-files/` are reconstructions; they turned out to be byte-identical for
everything except a comment, but the canonical repo is the one to vendor from.
`orb/proto/` now does.

## Endpoints

The device addresses three hostnames that all resolve to the same host. Routing
is by `Host` header, not by path, because `hello-time` exposes only `/`.

| Host | Path | Purpose |
|---|---|---|
| `sense-in.hello.is` | `POST /in/sense/batch` | sensor samples, ~1/minute |
| `sense-in.hello.is` | `POST /in/pill` | relayed pill motion |
| `sense-in.hello.is` | `POST /logs` | device's own logs, ~2KB |
| `time.hello.is` | `POST /` | clock sync |
| `messeji.hello.is` | `POST /receive` | command long-poll, ~10s, ~1/11s |

## Message signing

Every request and response is signed with the device's per-unit AES-128 key from
`key_store`. The two directions use **opposite layouts**, which is the single
easiest thing to get backwards:

    device -> server:   [protobuf][IV(16)][sig(32)]     trailer APPENDED
    server -> device:   [IV(16)][sig(32)][protobuf]     trailer PREPENDED

The signature is `AES-CBC(key, IV)` over `SHA1(payload)` placed in a 32-byte
block. SHA1 is 20 bytes; AES needs a multiple of 16; the firmware pads to two
blocks. This is not PKCS#7 and using a standard padding scheme breaks it.

### The trap: verify by DECRYPTING, and compare only 20 bytes

`SignedMessage.validateWithKey` in suripu-core does this:

    final byte[] decryptedBytes = cipher.doFinal(sig);   // DECRYPT
    for (int i = 0; i < 20; i++) {
        if (decryptedBytes[i] != output[i]) return error;
    }

It decrypts the signature and compares **only the first 20 bytes** to the SHA1.
The trailing 12 bytes are never checked, because **the Sense does not zero
them**: it sends whatever happened to be in that buffer.

So the intuitive implementation, re-encrypt a zero-padded hash and compare the
ciphertexts, is wrong. It demands 12 bytes the device never promised and
rejects every genuine upload with a signature mismatch.

The failure mode is nasty because it is invisible to unit tests. A test that
signs its own payload uses its own padding, so it agrees with itself and
passes. Only a real device disagrees. In `orb` this appeared as HTTP 401 on the
first live request after a full green test suite:

    edge: verify signature err="device 0123456789ABCDEF: signature does not match body"

`internal/sense/signature.go` now decrypts and compares 20 bytes, and
`TestVerifyIgnoresPaddingBytes` builds a signature with deliberately non-zero
padding so it cannot regress.

**General lesson worth keeping: a self-signed test proves self-consistency, not
conformance.** Anything that has to interoperate with a device needs either
captured real traffic or a shadow run.

## Sensor values are NOT stored as sent

Two transformations happen between the wire and storage. Both were found by
diffing a Go reimplementation against the Java stack on live traffic, and
neither is discoverable from the proto.

### Audio is converted to millidecibels

`DeviceData.Builder` runs all three audio fields through `DataUtils`:

    decibels = raw / 1024.0f          convertAudioRawToDB
    stored   = (int)(decibels * 1000) floatToDbIntAudioDecibels

so the stored unit is millidecibels, and readers divide by 1000 again. Verified
on a live upload: raw `45369` becomes `44305`, exactly what Java wrote.

Storing the wire value instead makes every audio reading about 2.4% high,
forever, and silently disagrees with every historical row. Applies to
`audio_peak_energy_db`, `audio_peak_background_db` and
`audio_peak_disturbances_db`.

Temperature, humidity, light and dust are stored as sent (centi-units for
temperature and humidity).

### Absent counts mean zero, not unknown

The device omits `wave_count`, `hold_count` and `audio_num_disturbances` when
they are zero. suripu coerces with `hasX() ? getX() : 0`
(`SenseProcessorUtils.java:155`), so the stored value is `0`.

A protobuf library that reports "field not present" tempts you into storing
NULL, which claims the value is unknown when it is really none. Use the getter's
zero value.

## Responses must be written in ONE write

The Sense reads a reply with a single `recv()` and only reads again if that read
completely filled its 2048-byte buffer (kitsune `wifi_cmd.c:1720`,
`SERVER_REPLY_BUFSZ`). Replies are a couple of hundred bytes of headers plus a
small protobuf, far short of that.

Writing headers and body separately puts them in separate TLS records, so the
device's single read returns headers only, it decodes a body-less buffer, and
reports "signature validation fail". Every request then silently succeeds on
retry, except pill pairing, which reports the first failure to the phone as -12.

## Pill motion needs TWO keys

`/in/pill` carries `batched_pill_data`. The batch's `device_id` is the **Sense**
that relayed it, and the request signature uses the **Sense's** key. Each
`pill_data` inside carries `motion_data_entrypted` (typo original) encrypted
with the **pill's** key. The Sense cannot read what it forwards.

Motion payload:

    [nonce(8)][AES-CTR ciphertext...]

The nonce is zero-padded to a full 16-byte counter block, not a truncated IV. A
trailing CRC exists in some versions and suripu never validated it.

Decoded by payload version, which is field 5. suripu-api calls that field
`firmware_version` and the newer proto calls it `protocol_version`; it is
neither. It is the **payload format version**. The pill's actual firmware build
is field 8.

| Version | Layout (little endian) |
|---|---|
| 0, 1 | `uint32` amplitude |
| 2, 3 | `uint32` amplitude, `uint16` range, `uint8` kickoffs, `uint8` duration |
| 4 | adds cosTheta and motion mask; not implemented in orb |

Amplitude converts to milli-m/s^2 as `sqrt(raw) * (4.0 * 9.81 / 65536.0) - 9.81`,
truncated to an integer. Those constants are part of the stored value's meaning:
change one and every historical sample silently means something different.

### The magic-byte quirk, matched on purpose

`checkForMagicBytes` rejects only when **both** trailing bytes differ from
`0x5A`:

    if (decryptedRawMotion[len-1] != 0x5A && decryptedRawMotion[len-2] != 0x5A)

That reads like it meant `||`. orb keeps the `&&` deliberately: tightening it
would reject payloads the old system accepted and stored, and the new code has
to agree with 20,000 existing rows. `TestMagicByteQuirk` documents it.

## Clocks

The Orb's clock starts about 70 years in the past after a reboot and only
corrects once time sync succeeds. Time sync answers with **NTP-epoch**
timestamps (seconds since 1900, in the high half of a 64-bit fixed point value),
not Unix seconds. Feeding it Unix seconds puts it in 1956, and everything
downstream then discards its samples as more than two hours out of sync.

orb drops samples whose timestamp the clock cannot account for at ingest, with
a warning, rather than storing them for something else to discard later. The
bounds are **asymmetric**, and the asymmetry is the point:

    ahead of the server clock    2 hours
    behind the server clock      7 days

A sample from the future cannot be anything but a broken clock, so that side
stays tight. A sample from the past may simply be **late**: the Sense and the
pill buffer readings while they cannot upload and flush the backlog when
service returns, which is normal behaviour and not a fault.

The original bound was a symmetric two hours. That number was never derived
from anything: it was chosen to catch the 1956 case above, which any bound
catches. It also rejected healthy backlogs. On 2026-08-16 LocalStack's Kinesis
died at 23:40 local, the device received 500s for nine hours and buffered, and
on recovery re-sent samples up to two and a half hours old. The symmetric window
logged 492 "unsynced clock" warnings across the night. Little was lost that
time, because most of those samples had already been stored on an earlier
attempt, but a longer outage would have discarded a whole night of good data.

Seven days covers any plausible outage, including a bug noticed only after a
second night, while still rejecting a device that thinks it is 1956.
`TestClockGuardKeepsBacklogAndRejectsBrokenClocks` pins both directions.

## TLS

Go's `crypto/tls` cannot serve this device. The Sense offers exactly one cipher
(`0xC014`, ECDHE-RSA-AES256-SHA) and sends no `supported_groups` extension. RFC
4492 section 4 permits that and lets the server pick any curve; Go does not
implement the fallback, so it eliminates the only offered cipher and sends
handshake_failure. `CurvePreferences` does not help. See
[CONSOLIDATION-PLAN.md](CONSOLIDATION-PLAN.md) phase 0 for the experiment.

TLS therefore stays terminated by `sense_server.py` (tlslite-ng), with a
certificate dated 1950 so the device's backwards clock still accepts it.
