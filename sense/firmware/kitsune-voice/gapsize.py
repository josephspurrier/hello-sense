#!/usr/bin/env python3
"""Per-function size via address gaps; compare by name between two ELFs."""
import re, sys
from elfdiff import textsyms

def sizes(path):
    A, t, d, secs = textsyms(path)
    # all symbols (incl. $C$ labels are constants embedded in code; skip as
    # boundaries only if they'd split a function: keep only named funcs and
    # use next named-func addr as the end)
    named = sorted({(v & ~1, n) for v, sz, n in A if not n.startswith('$C$')})
    end = t['addr'] + t['size']
    out = {}
    for i, (v, n) in enumerate(named):
        nxt = named[i+1][0] if i+1 < len(named) else end
        out.setdefault(re.sub(r'\$\d+$', '', n), []).append((v, nxt - v))
    return out, t

SA, ta = sizes(sys.argv[1])
SB, tb = sizes(sys.argv[2])
tot_a = tot_b = 0
diffs = []
for n in SA:
    if n not in SB:
        continue
    a = sorted(sz for v, sz in SA[n])
    b = sorted(sz for v, sz in SB[n])
    tot_a += sum(a); tot_b += sum(b)
    if a != b:
        diffs.append((n, a, b, sum(b) - sum(a)))
diffs.sort(key=lambda x: -abs(x[3]))
print(f".text A=0x{ta['size']:x} B=0x{tb['size']:x} (B-A={tb['size']-ta['size']})")
print(f"sum(sizes) A={tot_a} B={tot_b} (B-A={tot_b-tot_a})")
print(f"functions with different size: {len(diffs)}")
for n, a, b, d in diffs:
    print(f"  {n}: ref={a} ours={b}  net={d:+d}")
