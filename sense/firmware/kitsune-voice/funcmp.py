#!/usr/bin/env python3
"""Byte-compare a named function's body between two ELFs."""
import re, sys
from elfdiff import read_elf

def grab(path, want):
    d, secs, syms = read_elf(path)
    text = [s for s in secs if s['sname'] == '.text'][0]
    lo, hi = text['addr'], text['addr'] + text['size']
    named = sorted({(v & ~1, n) for (n, v, sz, t, sh) in syms
                    if lo <= v < hi and not n.startswith('$C$')})
    for i, (v, n) in enumerate(named):
        if re.sub(r'\$\d+$', '', n) == want:
            nxt = named[i+1][0] if i+1 < len(named) else hi
            off = text['offset'] + (v - text['addr'])
            return v, d[off:off + (nxt - v)]
    return None, None

want = sys.argv[3]
va, ba = grab(sys.argv[1], want)
vb, bb = grab(sys.argv[2], want)
print(f"{want}: A @0x{va:x} len={len(ba)}; B @0x{vb:x} len={len(bb)}")
m = min(len(ba), len(bb))
common_eq = ba[:m] == bb[:m]
print(f"first {m} bytes identical: {common_eq}")
if not common_eq:
    for i in range(m):
        if ba[i] != bb[i]:
            print(f"first diff at +{i}: A={ba[i:i+16].hex()} B={bb[i:i+16].hex()}")
            break
else:
    longer = ba if len(ba) > len(bb) else bb
    print(f"tail of longer: {longer[m:].hex()}")
