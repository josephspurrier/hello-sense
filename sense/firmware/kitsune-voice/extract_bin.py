#!/usr/bin/env python3
"""Turn a kitsune .out (ELF) into the flashable .bin via LOAD program headers.
Uses p_paddr (LMA) so RAM-run/flash-load sections (.ramcode) land at their flash
address; zero-fills gaps. Correct for images with .ramcode/.weights (voice)."""
import struct, sys, hashlib
def main():
    inp, outp = sys.argv[1], sys.argv[2]
    d = open(inp,'rb').read()
    assert d[:4]==b'\x7fELF' and d[4]==1
    e_phoff, = struct.unpack_from('<I', d, 0x1c)
    e_phentsize, e_phnum = struct.unpack_from('<HH', d, 0x2a)
    segs=[]
    for i in range(e_phnum):
        o=e_phoff+i*e_phentsize
        p_type,p_offset,p_vaddr,p_paddr,p_filesz,p_memsz,p_flags,p_align=struct.unpack_from('<8I',d,o)
        if p_type==1 and p_filesz>0:   # PT_LOAD with file content
            segs.append((p_paddr,p_offset,p_filesz))
    segs.sort()
    base=segs[0][0]; blob=bytearray()
    for paddr,off,fsz in segs:
        pos=paddr-base
        if pos>len(blob): blob+=b'\x00'*(pos-len(blob))
        assert pos==len(blob), f"overlap at 0x{paddr:x} (pos {pos} vs {len(blob)})"
        blob+=d[off:off+fsz]
        print(f"  LOAD paddr=0x{paddr:08x} filesz=0x{fsz:x}")
    open(outp,'wb').write(blob)
    print(f"wrote {outp}: {len(blob)} bytes sha1={hashlib.sha1(bytes(blob)).hexdigest()}")
if __name__=='__main__': main()
