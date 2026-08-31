# Sense with Voice, SD card contents

A byte-exact copy of the microSD card inside the **Sense with Voice**
(CC3220SF), recovered 2026-08-31 and verified against the device's own cached
hashes. This is the audio the firmware plays: sleep sounds, alarm ringtones,
and the voice-assistant UI clips. Hello is defunct and these files are the only
surviving copy of several of the sets.

## Layout

The card is FAT, and the firmware reads audio from fixed folder names. Names on
the card are FAT 8.3 short names (upper case, `~1` where a long name was
truncated), reproduced here verbatim.

```
/
├── SLPTONES/   12  sleep tones,         base rate   (ST001..ST012)
├── RINGTONE/   22  alarm ringtones,     base rate   (DIG/NEW/ORG/STAR/PINK/TONE)
├── SLPTON48/   12  sleep tones,         48 kHz      (ST101..ST112)
├── RINGTO48/   23  alarm ringtones,     48 kHz      (DIG/NEW/ORG/STAR/PINK/TONE 1xx)
├── VOICEUI/     5  voice-assistant UI,  base rate   (VUI001..VUI005)
├── VOICE48/     5  voice-assistant UI,  48 kHz      (VUI101..VUI105)
├── DG1__D~1        3072-byte factory bookkeeping blob (see below)
├── DG1__D~2        3072-byte factory bookkeeping blob (A/B copy of DG1__D~1)
├── USR/            empty
├── LOGS/           empty
└── TEST/           empty
```

81 files, ~195 MB. `SHA1SUMS.txt` lists every file; verify with
`shasum -a1 -c SHA1SUMS.txt` from this directory.

## Audio format

Every `.RAW` is **headerless signed 16-bit little-endian PCM, mono** (the Hello
convention, produced by kasetsu's `ConvertToPcm.sh`). There is no header, so the
sample rate is not stored in the file; you supply it on playback.

- The `*48` folders are **48 kHz**.
- The base `SLPTONES` / `RINGTONE` / `VOICEUI` folders are a separate,
  lower-rate set (the sleep tones measure ~16 kHz by size). The base and `*48`
  sets are different masters, not simple resamples, so their per-file sizes do
  not scale by a fixed ratio.

Play or convert a file with ffmpeg/ffplay, e.g.:

```sh
ffplay -f s16le -ar 48000 -ac 1 SLPTON48/ST101.RAW      # a 48 kHz master
ffplay -f s16le -ar 16000 -ac 1 SLPTONES/ST001.RAW      # a base sleep tone
ffmpeg -f s16le -ar 48000 -ac 1 -i RINGTO48/DIG101.RAW DIG101.wav
```

If a clip sounds too fast or slow, adjust `-ar` (try 16000 / 32000 / 48000).

## Sleep-tone names

The catalogued sleep tones map to the app's display names as follows (from
suripu's `file_info` seed; applies to the `SLPTONES` set, with the `SLPTON48`
`ST1xx` files as the 48 kHz counterparts):

| File   | Name         | File   | Name         |
|--------|--------------|--------|--------------|
| ST001  | Brown Noise  | ST007  | White Noise  |
| ST002  | Cosmos       | ST008  | Forest Creek |
| ST003  | Autumn Wind  | ST009  | Morpheus     |
| ST004  | Fireside     | ST010  | Aura         |
| ST005  | Ocean Waves* | ST011  | Horizon      |
| ST006  | Rainfall     | ST012  | Nocturne     |

\* ST005 exists on the card but was never offered in the app.

Ringtones are catalogued by numeric id: `DIG001..DIG005` = ids 4-8,
`NEW001..NEW006` = 9-14, `ORG001..ORG004` = 15-18. The `STAR*`, `PINK`, and
`TONE` files (and the extra `ORG005` in the 48 kHz set) are additional
ringtones present on the card.

## The DG1 blobs

`DG1__D~1` and `DG1__D~2` are **not audio**. Each is a 3072-byte, mostly
zero-padded binary record; the two are an A/B double-buffered pair (magic
`DsVh` / `DsDh`, with generation counters swapped between the copies, newest
wins). The payload is two 128-bit ids plus a 32-bit field, dated the same day
the audio was written, so they are almost certainly factory/provisioning
bookkeeping for the pre-loaded card. The writing tool is not in any surviving
Hello source, so the fields are not authoritatively decoded. They carry no
audio, key material, or personal data; they are kept here only for a complete
card image.

## How this was captured

The stock firmware's file-sync only scans `SLPTONES` and `RINGTONE`, so the
`*48` and `VOICEUI` folders never appear in the uploaded manifest. They were
pulled off over the on-device console with the `x` stream command
(`x $f<path> $i<http-post-url>`), landing each file on the backend, then
verified: each `.RAW` was checked against the device's own `<name>.SHA` cache
(a 20-byte binary SHA1 the firmware keeps beside each file). The console and the
`x`-export flush are enabled by the voice firmware build flags documented under
`sense/firmware/kitsune-voice`.
