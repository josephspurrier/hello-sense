#!/usr/bin/env python3
"""Turn a kitsune .out (ELF) into the flashable .bin by concatenating the four
loadable sections in address order: .intvecs + .text + .const + .cinit.

This is the tiobj2bin equivalent; tiobj2bin itself is missing/broken in the
container, and sections are placed at their address-derived offsets with
alignment gaps zero-filled (see the comment in main).

Usage: extract_bin.py <in.out> <out.bin>
"""
import struct, sys

LOADABLE = ('.intvecs', '.text', '.const', '.cinit')

def read_sections(path):
    d = open(path, 'rb').read()
    assert d[:4] == b'\x7fELF', "not an ELF"
    assert d[4] == 1, "expected 32-bit ELF"
    e_shoff, = struct.unpack_from('<I', d, 0x20)
    e_shentsize, e_shnum, e_shstrndx = struct.unpack_from('<HHH', d, 0x2e)
    secs = []
    for i in range(e_shnum):
        off = e_shoff + i * e_shentsize
        name, typ, flags, addr, offset, size = struct.unpack_from('<6I', d, off)
        secs.append(dict(name=name, addr=addr, offset=offset, size=size))
    shstr = secs[e_shstrndx]
    def nm(n):
        s = d[shstr['offset'] + n:]
        return s[:s.index(b'\0')].decode()
    for s in secs:
        s['sname'] = nm(s['name'])
    return d, secs

def main():
    inp, outp = sys.argv[1], sys.argv[2]
    d, secs = read_sections(inp)
    load = sorted((s for s in secs if s['sname'] in LOADABLE),
                  key=lambda s: s['addr'])
    # Place each section at its ADDRESS-derived offset, zero-filling any
    # alignment gap between sections. Naive concatenation shipped a subtly
    # broken image whenever the linker aligned .cinit 4 bytes past the end of
    # .const: the gap was dropped, so .cinit sat 4 bytes below its linked
    # address in the .bin, boot-time C initialization read garbage, and the
    # image crashed before main(). SHA verification cannot catch it (the file
    # is self-consistent), and the bootloader silently reverts, which mimicked
    # a boot-record failure for a full day of diagnosis on 2026-08-30. Stock
    # 4513 is gap-free, so this changes nothing for the byte-exact build.
    base = load[0]['addr']
    blob = bytearray()
    for s in load:
        pos = s['addr'] - base
        if pos > len(blob):
            print(f"  (zero-fill 0x{pos - len(blob):x} gap before {s['sname']})")
            blob += b'\x00' * (pos - len(blob))
        assert pos == len(blob), f"{s['sname']} overlaps previous section"
        blob += d[s['offset']:s['offset'] + s['size']]
        print(f"  {s['sname']:9s} @0x{s['addr']:08x} size 0x{s['size']:x}")
    blob = bytes(blob)
    open(outp, 'wb').write(blob)
    import hashlib
    print(f"wrote {outp}: {len(blob)} bytes  sha1={hashlib.sha1(blob).hexdigest()}")

if __name__ == '__main__':
    main()
