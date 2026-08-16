#!/usr/bin/env python3
"""Find where .text addresses drift between reference and our build."""
import re, sys
from elfdiff import textsyms

A, at, ad, asecs = textsyms(sys.argv[1])  # reference
B, bt, bd, bsecs = textsyms(sys.argv[2])  # ours

def base(n):
    return re.sub(r'\$\d+$', '', n)

# keep only real named funcs (skip compiler labels $C$...)
def clean(L):
    out = []
    for v, sz, n in L:
        if n.startswith('$C$'):
            continue
        out.append((v, base(n), n))
    out.sort()
    return out

CA, CB = clean(A), clean(B)
Bmap = {}
for v, bn, n in CB:
    Bmap.setdefault(bn, []).append(v)

print(f"named funcs: A={len(CA)} B={len(CB)}")
prev_delta = None
changes = 0
for v, bn, n in CA:
    if bn not in Bmap or not Bmap[bn]:
        print(f"MISSING in B: {n} @0x{v:x}")
        continue
    # take closest address occurrence
    w = min(Bmap[bn], key=lambda x: abs(x - v))
    Bmap[bn].remove(w)
    delta = w - v
    if delta != prev_delta:
        changes += 1
        if changes <= 60:
            print(f"delta {prev_delta} -> {delta:+d} at {n} A=0x{v:x} B=0x{w:x}")
        prev_delta = delta
print(f"total delta changes: {changes}")
extra = [bn for bn, vs in Bmap.items() if vs]
print(f"B leftover funcs: {sum(len(v) for k,v in Bmap.items() if v)}")
for bn, vs in Bmap.items():
    if vs:
        print(f"  EXTRA in B: {bn} {['0x%x'%x for x in vs]}")
