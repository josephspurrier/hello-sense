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
  SENSE_CERT_PATH         TLS certificate, default ./server.crt
  SENSE_KEY_PATH          its private key,  default ./server.key
  SENSE_LOG_PATH          request log,      default ./server.log
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

# Paths, not just names, so this can run from anywhere. The container mounts
# the certificate onto ./server.crt so the defaults are what it uses; a host
# process points these at ../secrets/ instead.
CERT_FILE = os.environ.get("SENSE_CERT_PATH", "server.crt")
KEY_FILE = os.environ.get("SENSE_KEY_PATH", "server.key")
TLS_LOG = os.environ.get("SENSE_LOG_PATH", "server.log")

UPSTREAM_TIME = os.environ.get("SENSE_UPSTREAM_TIME", "http://127.0.0.1:1111")
UPSTREAM_SENSE = os.environ.get("SENSE_UPSTREAM_SENSE", "http://127.0.0.1:5555")
UPSTREAM_MESSEJI = os.environ.get("SENSE_UPSTREAM_MESSEJI", "http://127.0.0.1:10000")
TIME_MODE = os.environ.get("SENSE_TIME_MODE", "proxy").lower()

# Optional mirror of every device request to the orb Go edge, for validating it
# against live traffic before any cutover. Observational only: see _shadow.
SHADOW_URL = os.environ.get("SENSE_SHADOW", "").strip()
SHADOW_TIMEOUT = float(os.environ.get("SENSE_SHADOW_TIMEOUT", "5"))

# Directory to answer ACME http-01 challenges from. Empty means the feature is
# off, which is the default and the only behaviour this file had before.
#
# Why this lives here at all: Let's Encrypt validates http-01 on port 80 and
# nowhere else, and port 80 on this host belongs to the device. Certbot writes
# a token file under <webroot>/.well-known/acme-challenge/ and Let's Encrypt
# fetches it over plain HTTP. Serving that one prefix here is what lets a
# normal TLS terminator (Caddy, on another port, for the app API) hold a real
# certificate without taking port 80 away from the Sense.
#
# The Sense never requests this path. Nothing about the device path changes.
ACME_WEBROOT = os.environ.get("SENSE_ACME_WEBROOT", "").strip()

# Challenge tokens are base64url. Anything outside this set is not a token, and
# refusing early means no filename from the network ever reaches the filesystem.
ACME_TOKEN_CHARS = frozenset(
    "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
)
ACME_PREFIX = "/.well-known/acme-challenge/"

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


def _read_exact(rfile, n):
    """Read EXACTLY n bytes, looping over short reads.

    Load-bearing for the Sense with Voice audio upload. A batch upload is one
    small Content-Length body that arrives in a single TLS record, so a lone
    rfile.read(n) returns all of it. The voice utterance is a long chunked
    stream whose chunks straddle TLS record boundaries, and tlslite hands back
    one record at a time, so read(size) can return fewer bytes than asked. The
    old code trusted a single read to return the whole chunk; when it came up
    short the next readline() landed inside the audio and parsed ADPCM bytes as
    a hex chunk size ("invalid literal for int() with base 16"), killing the
    upload and lighting the device red."""
    buf = bytearray()
    while len(buf) < n:
        part = rfile.read(n - len(buf))
        if not part:
            raise ConnectionError("connection closed mid-chunk")
        buf.extend(part)
    return bytes(buf)


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
        # A chunk-size line may carry extensions after a ';' (RFC 7230); the
        # size is only the part before it.
        size = int(line.split(b";", 1)[0], 16)
        if size == 0:
            rfile.readline()  # trailing CRLF
            break
        chunks.append(_read_exact(rfile, size))
        rfile.readline()      # trailing CRLF after chunk
    return b"".join(chunks)


class HelloHandler(BaseHTTPRequestHandler):
    # HTTP/1.1, so responses are keep-alive rather than close-after-response.
    #
    # This overturns an earlier note here which assumed the Sense opens a new
    # TLS connection per request. Its own log says otherwise: it holds one
    # socket per host and reuses it ("using sock 85 85"). Answering 1.0 meant
    # that reused socket was already closed, so the device's next request hit a
    # dead socket, read zero bytes ("start recv error 0"), reported failure, and
    # only succeeded on the retry over a fresh connection. That is the
    # first-attempt failure behind the -12 the phone shows on pill pairing.
    #
    # The old worry was a handler left blocked on a socket the device resets.
    # ReusableHTTPServer already covers that: it is threaded with daemon
    # threads and puts a request_timeout deadline on every accepted socket, so
    # an idle kept-alive connection costs one daemon thread for at most that
    # long. Keep-alive also REQUIRES an accurate Content-Length on every
    # response; each path here sets one (0 on the error paths).
    #
    # To revert: delete this line to fall back to the HTTP/1.0 default.
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _read_body(self):
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            return read_chunked_body(self.rfile)
        n = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(n) if n > 0 else b""

    def _route(self):
        """Pick an upstream for this request. Returns (base_url, path, label).

        Routes on the FIRST LABEL of the Host, not on the full hello.is names it
        used to match. A firmware built for your own domain asks for
        time.example.com, and "time.hello.is" is not a substring of that, so the
        clock request quietly fell through to the sense upstream. It still
        worked, because orb recognises a clock request by its device-id header
        rather than its host, but working by accident is how the two-hour
        outage in this file's history happened.
        """
        host = (self.headers.get("Host") or "").lower().split(":")[0]
        label = host.split(".")[0]

        if label in ("time", "ntp"):
            # hello-time exposed exactly one route, TimeResource @Path("/"), so
            # the device's own path is logged but not forwarded.
            return UPSTREAM_TIME, "/", "hello-time"

        # startswith, not equality: the dev slot is "messeji-dev".
        if label.startswith("messeji") or self.path.startswith("/receive"):
            return UPSTREAM_MESSEJI, self.path, "messeji"

        # Everything else is the device endpoint: /in/sense/*, /register/*,
        # /audio/*, /logs, /check, /provision, /firmware/*.
        return UPSTREAM_SENSE, self.path, "suripu-service"

    def _finish_response(self, payload):
        """Send the already-queued headers and the body in ONE write.

        Sense reads a reply with a single recv() and only reads again if that
        first read completely filled its 2048-byte buffer (kitsune
        wifi_cmd.c:1720, SERVER_REPLY_BUFSZ). Our replies are a couple hundred
        bytes of headers plus a small protobuf, far short of that. Writing the
        headers and the body separately puts them in separate TLS records, so
        Sense's one recv() returns headers only, decodes a body-less buffer and
        reports "signature validation fail" -> ErrorType_NETWORK_ERROR. Every
        request then silently succeeded on the network task's retry, except pill
        pairing, which reports the first failure straight to the phone as -12.
        """
        self._headers_buffer.append(b"\r\n")
        head = b"".join(self._headers_buffer)
        self._headers_buffer = []
        self.wfile.write(head + payload)
        self.wfile.flush()

    def _shadow(self, body):
        """Send a copy of this request to the orb Go edge, fire and forget.

        Strictly observational. The reply is discarded and any failure is
        swallowed after logging, because the device's real answer comes from the
        Java stack and nothing about this copy may be allowed to affect it. It
        runs on a daemon thread so a slow or dead shadow cannot add latency to
        the request the device is waiting on.

        Enable with SENSE_SHADOW=http://127.0.0.1:8081. Unset by default, so
        this is inert unless deliberately switched on.
        """
        if not SHADOW_URL:
            return

        # Skip the messeji long-poll. It deliberately holds a request open for
        # ~10s, which is longer than SHADOW_TIMEOUT, so every copy times out and
        # fills the log with ignored failures. Shadowing it proves nothing
        # either: the useful comparison is what gets parsed and stored, and a
        # poll that returns no message stores nothing.
        if self.path.startswith("/receive"):
            return

        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in HOP_BY_HOP}
        headers["Content-Length"] = str(len(body))
        # Preserve the device's Host: the shadow routes on it exactly as the
        # real path does.
        headers["Host"] = self.headers.get("Host", "")
        path = self.path
        command = self.command

        def send():
            try:
                parts = urlsplit(SHADOW_URL)
                conn = http.client.HTTPConnection(
                    parts.hostname, parts.port or 80, timeout=SHADOW_TIMEOUT)
                conn.request(command, path, body=body, headers=headers)
                resp = conn.getresponse()
                resp.read()
                _log(f"  [shadow] {path} = HTTP {resp.status}")
                conn.close()
            except Exception as e:
                _log(f"  [shadow] FAILED (ignored): {type(e).__name__}: {e}")

        threading.Thread(target=send, daemon=True).start()

    def _proxy(self, body):
        self._shadow(body)
        base, path, label = self._route()
        parts = urlsplit(base)

        # Forward the device's headers untouched apart from this hop's own.
        # X-Hello-Sense-Id in particular is how the services look up the AES key.
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in HOP_BY_HOP}
        headers["Content-Length"] = str(len(body))

        # Preserve the DEVICE's Host, not this hop's upstream address.
        #
        # This used to be parts.netloc, which contradicted the comment above and
        # broke the clock the moment orb took over time.hello.is: orb routes
        # that hostname by Host header, saw "127.0.0.1:8081" instead, and 404'd
        # every request. hello-time had never looked at Host, so the rewrite was
        # invisible for as long as a Java service was answering. The device
        # retried every 35 seconds for nearly two hours before anyone noticed.
        #
        # orb now also routes a clock request on the device id, so this is no
        # longer the only thing standing between the Sense and its clock, but a
        # proxy should pass the origin host through regardless.
        headers["Host"] = self.headers.get("Host") or parts.netloc

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
        self._finish_response(payload)
        conn.close()

    def _serve_acme(self):
        """Answer an ACME http-01 challenge from ACME_WEBROOT.

        Returns True if this request was an ACME challenge and has been
        answered, False if the caller should carry on with normal routing.

        Deliberately narrow. It only ever reads, only under one path prefix,
        only when a webroot is configured, and only for names made entirely of
        base64url characters. A token that fails any of those is a 404 without
        the filesystem being touched, so there is no path this can be talked
        into reading something it should not.
        """
        if not ACME_WEBROOT or not self.path.startswith(ACME_PREFIX):
            return False

        token = self.path[len(ACME_PREFIX):].split("?", 1)[0]
        if not token or not set(token) <= ACME_TOKEN_CHARS:
            _log(f"[ACME] rejected token {token!r}")
            self._send_bytes(404, b"not found")
            return True

        full = os.path.join(ACME_WEBROOT, ".well-known", "acme-challenge", token)
        try:
            with open(full, "rb") as fh:
                payload = fh.read()
        except OSError:
            _log(f"[ACME] miss {token}")
            self._send_bytes(404, b"not found")
            return True

        _log(f"[ACME] served {token} ({len(payload)} bytes)")
        self._send_bytes(200, payload, "application/octet-stream")
        return True

    def _send_bytes(self, code, payload, ctype="text/plain"):
        """One small response, with the Content-Length keep-alive requires."""
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(payload)))
        self._finish_response(payload)

    def do_GET(self):
        _log(f"[REQ] GET {self.path} (Host: {self.headers.get('Host', '?')})")
        if self._serve_acme():
            sys.stdout.flush()
            return
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
            self._finish_response(signed)
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
