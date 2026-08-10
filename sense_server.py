#!/usr/bin/env python3
"""TLS front-end for the Hello Sense, proxying to the local suripu backend.

The Sense speaks an ancient TLS handshake that modern stacks refuse, and it
validates the server certificate against a clock that starts ~70 years in the
past. Both problems are solved here and nowhere else: TLS is terminated with
tlslite-ng using a certificate dated from 1950. Everything past the handshake
is forwarded verbatim to the Java services running under docker compose, which
own the data.

    Sense --WiFi--> DNS (*.hello.is -> this host) --> :80 / :443 (this file)
                                                          |
                            time.hello.is       -> hello-time      :1111
                            sense-in.hello.is   -> suripu-service  :5555
                            messeji.hello.is    -> messeji         :10000

Bodies are forwarded byte-for-byte. Each request is AES-CBC signed with the
device's own key, and the Java services verify that signature against the key
in the DynamoDB key_store table, so any rewriting here would break them.

Config (no secrets in this file):
  SENSE_UPSTREAM_TIME     default http://127.0.0.1:1111
  SENSE_UPSTREAM_SENSE    default http://127.0.0.1:5555
  SENSE_UPSTREAM_MESSEJI  default http://127.0.0.1:10000
  SENSE_TIME_MODE         "proxy" (default) or "local", see _handle_time_local
  SENSE_AES_KEY           32 hex chars, only needed for SENSE_TIME_MODE=local.
                          Falls back to ./aes.key, then the firmware default.
"""

import hashlib
import http.client
import json
import os
import socket
import sys
import time
import threading
from datetime import datetime, timezone
from http.server import HTTPServer, BaseHTTPRequestHandler
from socketserver import ThreadingMixIn
from urllib.parse import urlsplit

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

from tlslite import X509, X509CertChain, parsePEMKey, HandshakeSettings
from tlslite.integration.tlssocketservermixin import TLSSocketServerMixIn
from tlslite.tlsrecordlayer import TLSRecordLayer


def _recv_into_fixed(self, b):
    """tlslite's recv_into returns None at EOF, which breaks http.server's
    BufferedReader (it expects bytes/int). Return 0 at EOF instead.

    Also treats an abrupt close as EOF. The Sense opens a fresh TLS connection
    per request and drops the previous one with an RST rather than a clean
    shutdown, which otherwise surfaces as an unhandled ConnectionResetError
    traceback from socketserver."""
    try:
        data = self.read(len(b))
    except (ConnectionResetError, BrokenPipeError, OSError):
        return 0
    if not data:
        return 0
    b[:len(data)] = data
    return len(data)


TLSRecordLayer.recv_into = _recv_into_fixed

import ntp_pb2

CERT_FILE = "server.crt"
KEY_FILE = "server.key"
TLS_LOG = "server.log"

UPSTREAM_TIME = os.environ.get("SENSE_UPSTREAM_TIME", "http://127.0.0.1:1111")
UPSTREAM_SENSE = os.environ.get("SENSE_UPSTREAM_SENSE", "http://127.0.0.1:5555")
UPSTREAM_MESSEJI = os.environ.get("SENSE_UPSTREAM_MESSEJI", "http://127.0.0.1:10000")
TIME_MODE = os.environ.get("SENSE_TIME_MODE", "proxy").lower()

# Seconds between the NTP epoch (1900-01-01) and the Unix epoch (1970-01-01).
# The Sense expects NTP-style timestamps. Sending it Unix seconds is what made
# it report 1956 instead of 2026, which in turn made suripu-workers discard
# every sample as more than two hours out of sync.
NTP_EPOCH_OFFSET = 2208988800


def _to_signed64(value):
    """Wrap a 64-bit unsigned value into the signed range.

    NTP timestamps are unsigned 64-bit fixed point, but the protobuf field is
    int64 and Java's TimeStamp.ntpValue() hands back a signed long that has
    already wrapped negative for any present-day date. Protobuf rejects
    anything above 2**63-1, so match Java's representation.
    """
    return value - (1 << 64) if value >= (1 << 63) else value

# Headers that describe this specific hop and must not be relayed onward.
HOP_BY_HOP = {
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "te", "trailers", "transfer-encoding", "upgrade", "content-length", "host",
}


def load_aes_key():
    hexkey = os.environ.get("SENSE_AES_KEY", "").strip()
    if not hexkey and os.path.exists("aes.key"):
        hexkey = open("aes.key").read().strip()
    if hexkey:
        key = bytes.fromhex(hexkey)
        if len(key) != 16:
            raise SystemExit("SENSE_AES_KEY must be 16 bytes (32 hex chars)")
        return key
    # Firmware default, used only if a device has no provisioned key.
    return b"1234567891234567"


AES_KEY = load_aes_key()


def sign_response(pb_bytes):
    """Sign a protobuf response: IV(16) + AES-CBC(SHA1(body) padded to 32B) + body."""
    iv = os.urandom(16)
    padded_hash = hashlib.sha1(pb_bytes).digest() + b"\x00" * 12  # 20 -> 32 bytes
    encryptor = Cipher(algorithms.AES(AES_KEY), modes.CBC(iv)).encryptor()
    encrypted_sig = encryptor.update(padded_hash) + encryptor.finalize()
    return iv + encrypted_sig + pb_bytes


def _log(msg):
    line = f"[{time.strftime('%H:%M:%S')}] {msg}"
    print(line)
    try:
        with open(TLS_LOG, "a") as f:
            f.write(line + "\n")
    except OSError:
        pass
    sys.stdout.flush()


class ReusableHTTPServer(ThreadingMixIn, HTTPServer):
    # Threaded, with a deadline on every accepted socket. Served serially and
    # without timeouts, a single connection the Sense abandons mid-request
    # (it resets them often) blocks the accept loop forever: the device keeps
    # connecting, the backlog fills, and every upload is lost until restart.
    allow_reuse_address = True
    daemon_threads = True
    request_timeout = 60  # comfortably above the ~10s messeji long-poll

    def server_bind(self):
        self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
        except (AttributeError, OSError):
            pass
        super().server_bind()

    def get_request(self):
        sock, addr = super().get_request()
        sock.settimeout(self.request_timeout)
        return sock, addr

    def handle_error(self, request, client_address):
        exc = sys.exc_info()[1]
        _log(f"[CONN] {client_address[0]} dropped: {type(exc).__name__}: {exc}")


class TLSHelloServer(TLSSocketServerMixIn, ReusableHTTPServer):
    """HTTPS server that terminates TLS with tlslite-ng (see module docstring)."""
    cert_chain = None
    private_key = None
    tls_settings = None

    def handshake(self, connection):
        try:
            connection.handshakeServer(certChain=self.cert_chain,
                                       privateKey=self.private_key,
                                       settings=self.tls_settings)
            connection.ignoreAbruptClose = True
            _log(f"[TLS] handshake OK (cipher={connection.session.cipherSuite:#06x})")
            return True
        except Exception as e:
            _log(f"[TLS] handshake FAILED: {type(e).__name__}: {e}")
            return False


def read_chunked_body(rfile):
    """Read a chunked transfer-encoded body and return the reassembled bytes."""
    chunks = []
    while True:
        raw = rfile.readline()
        if not raw:
            # EOF mid-body. Without this the loop spins on b'' forever.
            raise ConnectionError("connection closed mid-chunked-body")
        line = raw.strip()
        if not line:
            continue
        size = int(line, 16)
        if size == 0:
            rfile.readline()  # trailing CRLF
            break
        chunks.append(bytes(rfile.read(size)))
        rfile.readline()      # trailing CRLF after chunk
    return b"".join(chunks)


class HelloHandler(BaseHTTPRequestHandler):
    # Deliberately left at the HTTP/1.0 default. The Sense sends HTTP/1.1
    # chunked requests but opens a new TLS connection for each one, so
    # advertising 1.1 here only enables a keep-alive the device never uses,
    # leaving the handler blocked on a socket the device is about to reset.

    def log_message(self, fmt, *args):
        pass

    def _read_body(self):
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            return read_chunked_body(self.rfile)
        n = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(n) if n > 0 else b""

    def _route(self):
        """Pick an upstream for this request. Returns (base_url, path, label)."""
        host = (self.headers.get("Host") or "").lower()

        if "time.hello.is" in host or "ntp.hello.is" in host:
            # hello-time exposes exactly one route, TimeResource @Path("/"),
            # so the device's own path is logged but not forwarded.
            return UPSTREAM_TIME, "/", "hello-time"

        if "messeji" in host or self.path.startswith("/receive"):
            return UPSTREAM_MESSEJI, self.path, "messeji"

        # Everything else is suripu-service: /in/sense/*, /register/*, /audio/*,
        # /logs, /check, /provision.
        return UPSTREAM_SENSE, self.path, "suripu-service"

    def _proxy(self, body):
        base, path, label = self._route()
        parts = urlsplit(base)

        # Forward the device's headers untouched apart from this hop's own.
        # X-Hello-Sense-Id in particular is how the services look up the AES key.
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in HOP_BY_HOP}
        headers["Content-Length"] = str(len(body))
        headers["Host"] = parts.netloc

        try:
            conn = http.client.HTTPConnection(parts.hostname, parts.port or 80, timeout=30)
            conn.request(self.command, path, body=body, headers=headers)
            resp = conn.getresponse()
            payload = resp.read()
        except Exception as e:
            _log(f"  -> {label} UNREACHABLE: {type(e).__name__}: {e}")
            self.send_response(502)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        _log(f"  -> {label} {path} = HTTP {resp.status}, {len(payload)}B")

        self.send_response(resp.status)
        for k, v in resp.getheaders():
            if k.lower() not in HOP_BY_HOP:
                self.send_header(k, v)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if payload:
            self.wfile.write(payload)
        conn.close()

    def do_GET(self):
        _log(f"[REQ] GET {self.path} (Host: {self.headers.get('Host', '?')})")
        self._proxy(b"")

    def do_POST(self):
        host = self.headers.get("Host", "?")
        body = self._read_body()
        _log(f"[REQ] POST {self.path} (Host: {host}, {len(body)} bytes)")

        is_time = "time.hello.is" in host.lower() or "ntp.hello.is" in host.lower()
        if is_time and TIME_MODE == "local":
            self._handle_time_local(body)
        else:
            self._proxy(body)
        sys.stdout.flush()

    def do_PUT(self):
        self.do_POST()

    def _handle_time_local(self, body):
        """Answer time sync here instead of proxying to hello-time.

        Only used when SENSE_TIME_MODE=local, as a fallback for running without
        the Java stack. Signing uses AES_KEY, so it only works for the one
        device that key belongs to, whereas hello-time looks the key up per
        device in key_store.
        """
        try:
            # Device -> server layout is [PB][IV(16)][Sig(32)], so the protobuf
            # is everything except the trailing 48 bytes. This mirrors
            # SignedMessage.parse in suripu-core.
            if len(body) < 48:
                raise ValueError(f"body too short to be signed ({len(body)}B)")
            req = ntp_pb2.NTPDataPacket()
            req.ParseFromString(body[:-48])
            # NTP timestamps are 64-bit fixed point: seconds since 1900 in the
            # high half, fraction in the low half. The seconds must be offset
            # from the Unix epoch or the device lands 70 years in the past.
            ntp_seconds = int(time.time()) + NTP_EPOCH_OFFSET
            ts = _to_signed64(ntp_seconds << 32)
            resp = ntp_pb2.NTPDataPacket()
            resp.reference_ts = ts
            resp.origin_ts = req.origin_ts if req.HasField("origin_ts") else 0
            resp.receive_ts = ts
            resp.transmit_ts = ts
            signed = sign_response(resp.SerializeToString())
            self.send_response(200)
            self.send_header("Content-Type", "application/x-protobuf")
            self.send_header("Content-Length", str(len(signed)))
            self.end_headers()
            self.wfile.write(signed)
            shown = datetime.fromtimestamp(ntp_seconds - NTP_EPOCH_OFFSET, tz=timezone.utc)
            _log(f"  [TIME local] sent {shown.isoformat()}")
        except Exception as e:
            _log(f"  [TIME local] error: {e}")
            self.send_response(500)
            self.send_header("Content-Length", "0")
            self.end_headers()


def _build_tls_settings():
    s = HandshakeSettings()
    s.minVersion = (3, 1)   # TLS 1.0
    s.maxVersion = (3, 3)   # TLS 1.2 (the CC3200 does not do 1.3)
    s.cipherNames = ["aes256", "aes128"]
    s.macNames = ["sha", "sha256"]
    s.keyExchangeNames = ["ecdhe_rsa"]
    s.eccCurves = ["secp256r1", "x25519"]
    s.keyShares = []
    return s


def run_server(port, use_ssl):
    if use_ssl:
        server = TLSHelloServer(("0.0.0.0", port), HelloHandler)
        x509 = X509()
        x509.parse(open(CERT_FILE).read())
        server.cert_chain = X509CertChain([x509])
        server.private_key = parsePEMKey(open(KEY_FILE).read(), private=True)
        server.tls_settings = _build_tls_settings()
        label = "HTTPS (tlslite-ng)"
    else:
        server = ReusableHTTPServer(("0.0.0.0", port), HelloHandler)
        label = "HTTP"
    print(f"{label} listening on :{port}", flush=True)
    server.serve_forever()


def main():
    print("=== Hello Sense TLS front-end (proxy mode) ===")
    print(f"  time.hello.is     -> {UPSTREAM_TIME}"
          f"{'  [OVERRIDDEN: answering locally]' if TIME_MODE == 'local' else ''}")
    print(f"  sense-in.hello.is -> {UPSTREAM_SENSE}")
    print(f"  messeji.hello.is  -> {UPSTREAM_MESSEJI}")
    if TIME_MODE == "local":
        print(f"  AES key: {'custom' if AES_KEY != b'1234567891234567' else 'DEFAULT'}")
    print(flush=True)
    threading.Thread(target=run_server, args=(80, False), daemon=True).start()
    threading.Thread(target=run_server, args=(443, True), daemon=True).start()
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("\nShutting down.")


if __name__ == "__main__":
    main()
