#!/usr/bin/env python3
"""Tiny DNS server that answers *.hello.is with this machine's IP and forwards
everything else upstream. Point the Sense's DNS at this host and it will send
its cloud traffic to your local server instead.

Redirect IP resolution order:
  1. SENSE_REDIRECT_IP env var
  2. argv[1]
  3. auto-detect (the interface used to reach the internet)

Auto-detect can pick the wrong interface on multi-homed machines (VPN, etc.),
so set SENSE_REDIRECT_IP explicitly to your LAN IP if in doubt.
"""

import os
import socket
import struct
import sys
import time

LISTEN_IP = "0.0.0.0"
LISTEN_PORT = 53
UPSTREAM_DNS = "8.8.8.8"


def auto_ip():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("8.8.8.8", 80))
        return s.getsockname()[0]
    finally:
        s.close()


def parse_name(data, offset):
    labels = []
    while True:
        length = data[offset]
        if length == 0:
            offset += 1
            break
        if (length & 0xC0) == 0xC0:
            ptr = struct.unpack("!H", data[offset:offset + 2])[0] & 0x3FFF
            labels.extend(parse_name(data, ptr)[0])
            offset += 2
            return labels, offset
        offset += 1
        labels.append(data[offset:offset + length].decode(errors="replace"))
        offset += length
    return labels, offset


def build_a_response(query, ip):
    resp = bytearray(query)
    resp[2] = 0x81  # QR=1, RD=1
    resp[3] = 0x80  # RA=1
    resp[6:8] = struct.pack("!H", 1)  # one answer
    _, offset = parse_name(query, 12)
    qtype = struct.unpack("!H", query[offset:offset + 2])[0]
    offset += 4  # skip qtype + qclass
    if qtype != 1:  # only A records
        return None
    answer = b"\xc0\x0c" + struct.pack("!HHI", 1, 1, 60) + struct.pack("!H", 4) + socket.inet_aton(ip)
    return bytes(resp[:offset]) + answer


def forward(query):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3)
    try:
        s.sendto(query, (UPSTREAM_DNS, 53))
        return s.recvfrom(4096)[0]
    except socket.timeout:
        return None
    finally:
        s.close()


def main():
    redirect_ip = os.environ.get("SENSE_REDIRECT_IP") or (sys.argv[1] if len(sys.argv) > 1 else None) or auto_ip()
    print(f"DNS on {LISTEN_IP}:{LISTEN_PORT}  |  *.hello.is -> {redirect_ip}  |  else -> {UPSTREAM_DNS}\n", flush=True)

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind((LISTEN_IP, LISTEN_PORT))

    while True:
        data, addr = sock.recvfrom(4096)
        try:
            labels, offset = parse_name(data, 12)
            name = ".".join(labels)
            ts = time.strftime("%H:%M:%S")
            if name == "hello.is" or name.endswith(".hello.is"):
                print(f"[{ts}] {name} -> INTERCEPT {redirect_ip}", flush=True)
                resp = build_a_response(data, redirect_ip)
                if resp:
                    sock.sendto(resp, addr)
            else:
                resp = forward(data)
                if resp:
                    sock.sendto(resp, addr)
        except Exception as e:
            print(f"parse error: {e}", flush=True)


if __name__ == "__main__":
    main()
