#!/usr/bin/env python3
"""Turn a kitsune .out (ELF) into the flashable .bin by concatenating the four
loadable sections in address order: .intvecs + .text + .const + .cinit.

This is the tiobj2bin equivalent; tiobj2bin itself is missing/broken in the
container, but the result is identical because these sections are contiguous
PT_LOAD content with no gaps.

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
    blob = b''
    for s in load:
        blob += d[s['offset']:s['offset'] + s['size']]
        print(f"  {s['sname']:9s} @0x{s['addr']:08x} size 0x{s['size']:x}")
    open(outp, 'wb').write(blob)
    import hashlib
    print(f"wrote {outp}: {len(blob)} bytes  sha1={hashlib.sha1(blob).hexdigest()}")

if __name__ == '__main__':
    main()
