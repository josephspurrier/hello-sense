#!/usr/bin/env python3
"""Diff two TI ARM ELFs at the symbol level to find where .text diverges."""
import struct, sys

def read_elf(path):
    d = open(path, 'rb').read()
    assert d[:4] == b'\x7fELF'
    is64 = d[4] == 2
    assert not is64
    e_shoff, = struct.unpack_from('<I', d, 0x20)
    e_shentsize, e_shnum, e_shstrndx = struct.unpack_from('<HHH', d, 0x2e)
    secs = []
    for i in range(e_shnum):
        off = e_shoff + i * e_shentsize
        name, typ, flags, addr, offset, size, link, info, align, entsize = struct.unpack_from('<10I', d, off)
        secs.append(dict(name=name, type=typ, addr=addr, offset=offset, size=size, link=link, entsize=entsize))
    shstr = secs[e_shstrndx]
    def sname(n):
        s = d[shstr['offset']+n:]
        return s[:s.index(b'\0')].decode()
    for s in secs:
        s['sname'] = sname(s['name'])
    syms = []
    for s in secs:
        if s['type'] == 2:  # SYMTAB
            strtab = secs[s['link']]
            n = s['size'] // 16
            for i in range(n):
                off = s['offset'] + i*16
                nm, val, sz, info, other, shndx = struct.unpack_from('<IIIBBH', d, off)
                st = d[strtab['offset']+nm:]
                name = st[:st.index(b'\0')].decode(errors='replace')
                typ = info & 0xf
                syms.append((name, val, sz, typ, shndx))
    return d, secs, syms

def textsyms(path):
    d, secs, syms = read_elf(path)
    text = [s for s in secs if s['sname'] == '.text'][0]
    lo, hi = text['addr'], text['addr'] + text['size']
    # FUNC symbols in .text with a size
    out = [(v, sz, n) for (n, v, sz, t, sh) in syms if t == 2 and lo <= v < hi]
    out.sort()
    return out, text, d, secs

if __name__ == '__main__':
    a_path, b_path = sys.argv[1], sys.argv[2]
    A, at, ad, asecs = textsyms(a_path)
    B, bt, bd, bsecs = textsyms(b_path)
    print(f"A ({a_path}): .text addr=0x{at['addr']:x} size=0x{at['size']:x}, {len(A)} func syms")
    print(f"B ({b_path}): .text addr=0x{bt['addr']:x} size=0x{bt['size']:x}, {len(B)} func syms")
