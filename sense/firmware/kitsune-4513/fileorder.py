#!/usr/bin/env python3
"""Recover -pm whole-program file ordering from $NN static suffixes."""
import re, sys
from collections import defaultdict
from elfdiff import read_elf

def order(path):
    d, secs, syms = read_elf(path)
    idx = defaultdict(set)
    for (n, v, sz, t, sh) in syms:
        m = re.match(r'^(.+)\$(\d+)$', n)
        if m and not n.startswith('$C$'):
            idx[int(m.group(2))].add(m.group(1))
    return idx

A = order(sys.argv[1])
B = order(sys.argv[2])
print(f"file indices: A has {len(A)} (max {max(A)}), B has {len(B)} (max {max(B)})")
allidx = sorted(set(A) | set(B))
for i in allidx:
    a = sorted(A.get(i, []))[:4]
    b = sorted(B.get(i, []))[:4]
    mark = '' if A.get(i) == B.get(i) else '   <-- DIFF'
    print(f"{i:4d}: A={a} B={b}{mark}")
