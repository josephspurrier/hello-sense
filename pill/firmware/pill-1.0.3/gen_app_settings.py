#!/usr/bin/env python3
"""Generate the 12-byte nRF51 bootloader app-settings blob for a pill app image.

The kodobannin bootloader validates the application at 0x20000 with a CRC-16
stored in the bootloader-settings page at 0x3f000. This reproduces exactly what
the Makefile's `%.crc` recipe emits, but portably (no compiled crc16 tool, and
none of the BSD-vs-GNU `stat` breakage that dropped the size field on Linux).

Layout (12 bytes): 01 00 | CRC16(app) little-endian | ff ff ff ff | size LE(4)

Usage: gen_app_settings.py <app.bin> <app_settings.crc.bin>
"""
import sys


def crc16_nrf(data: bytes) -> int:
    """CRC-16-CCITT as implemented by nRF51 SDK crc16_compute (init 0xFFFF)."""
    crc = 0xFFFF
    for b in data:
        crc = ((crc >> 8) | (crc << 8)) & 0xFFFF
        crc ^= b
        crc ^= (crc & 0xFF) >> 4
        crc ^= (crc << 8) << 4 & 0xFFFF
        crc ^= ((crc & 0xFF) << 4) << 1 & 0xFFFF
        crc &= 0xFFFF
    return crc


def main() -> None:
    app, out = sys.argv[1], sys.argv[2]
    data = open(app, "rb").read()
    crc = crc16_nrf(data)
    blob = (
        bytes([0x01, 0x00])
        + bytes([crc & 0xFF, (crc >> 8) & 0xFF])  # CRC little-endian
        + bytes([0xFF, 0xFF, 0xFF, 0xFF])
        + len(data).to_bytes(4, "little")          # size little-endian
    )
    open(out, "wb").write(blob)
    print(f"{out}: {blob.hex()}  (crc=0x{crc:04x}, size={len(data)})")


if __name__ == "__main__":
    main()
