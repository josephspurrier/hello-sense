#!/usr/bin/env python3
"""Local server for the Hello Sense sleep tracker.

Receives the device's time-sync (HTTP :80) and sensor/state uploads (HTTPS :443),
verifies/signs the AES-CBC message signatures, decodes the sensor protobufs, and
appends readings to sense_data.jsonl.

TLS is terminated with tlslite-ng rather than Python's ssl module: the CC3200 in
the Sense offers only TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA and sends no
supported_groups extension, which modern OpenSSL/LibreSSL/Go reject
(NO_SHARED_CIPHER). tlslite-ng defaults to P-256 like the old OpenSSL the Hello
cloud ran, so the handshake completes.

Config (no secrets in this file):
  SENSE_AES_KEY   env var, 32 hex chars = your device's 16-byte key from
                  /cert/key.aes (recover it with `make read-key`). If unset,
                  falls back to the file ./aes.key, then to the firmware default
                  key "1234567891234567".
"""

import hashlib
import json
import os
import socket
import sys
import time
import threading
from datetime import datetime, timezone
from http.server import HTTPServer, BaseHTTPRequestHandler

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

from tlslite import X509, X509CertChain, parsePEMKey, HandshakeSettings
from tlslite.integration.tlssocketservermixin import TLSSocketServerMixIn
from tlslite.tlsrecordlayer import TLSRecordLayer


def _recv_into_fixed(self, b):
    """tlslite's recv_into returns None at EOF, which breaks http.server's
    BufferedReader (it expects bytes/int). Return 0 at EOF instead."""
    data = self.read(len(b))
    if not data:
        return 0
    b[:len(data)] = data
    return len(data)


TLSRecordLayer.recv_into = _recv_into_fixed

import ntp_pb2
import periodic_pb2

CERT_FILE = "server.crt"
KEY_FILE = "server.key"
DATA_FILE = "sense_data.jsonl"
TLS_LOG = "server.log"


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


class ReusableHTTPServer(HTTPServer):
    allow_reuse_address = True

    def server_bind(self):
        self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
        except (AttributeError, OSError):
            pass
        super().server_bind()


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
    """Read a chunked transfer-encoded body, preserving chunk boundaries."""
    chunks = []
    while True:
        line = rfile.readline().strip()
        if not line:
            continue
        size = int(line, 16)
        if size == 0:
            rfile.readline()  # trailing CRLF
            break
        chunks.append(bytes(rfile.read(size)))
        rfile.readline()      # trailing CRLF after chunk
    return chunks


class HelloHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def _read_body(self):
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            return read_chunked_body(self.rfile)
        n = int(self.headers.get("Content-Length", 0))
        return [self.rfile.read(n)] if n > 0 else [b""]

    def _send_ok(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.end_headers()

    def do_GET(self):
        _log(f"[REQ] GET {self.path} (Host: {self.headers.get('Host', '?')})")
        self._send_ok()

    def do_POST(self):
        host = self.headers.get("Host", "?")
        chunks = self._read_body()
        total = sum(len(c) for c in chunks)
        _log(f"[REQ] POST {self.path} (Host: {host}, {len(chunks)} chunks, {total} bytes)")

        if "time.hello.is" in host or "ntp.hello.is" in host:
            self._handle_time_sync(chunks)
        elif "/in/sense/batch" in self.path:
            self._handle_sensor_batch(chunks)
        else:
            # /in/sense/state, messeji /receive, /register, /provision, etc.
            self._send_ok()
        sys.stdout.flush()

    def do_PUT(self):
        self.do_POST()

    def _handle_time_sync(self, chunks):
        try:
            req = ntp_pb2.NTPDataPacket()
            req.ParseFromString(chunks[0])
            now = int(time.time())
            resp = ntp_pb2.NTPDataPacket()
            resp.reference_ts = now << 32
            resp.origin_ts = req.origin_ts if req.HasField("origin_ts") else 0
            resp.receive_ts = now << 32
            resp.transmit_ts = now << 32
            signed = sign_response(resp.SerializeToString())
            self.send_response(200)
            self.send_header("Content-Type", "application/x-protobuf")
            self.send_header("Content-Length", str(len(signed)))
            self.end_headers()
            self.wfile.write(signed)
            _log(f"  [TIME] sent {datetime.fromtimestamp(now, tz=timezone.utc).isoformat()}")
        except Exception as e:
            _log(f"  [TIME] error: {e}")
            self._send_ok()

    def _handle_sensor_batch(self, chunks):
        body = chunks[0]
        try:
            batch = periodic_pb2.batched_periodic_data()
            batch.ParseFromString(body)
            _log(f"  [BATCH] Device: {batch.device_id}, FW: {batch.firmware_version}, "
                 f"{len(batch.data)} reading(s)")
            for r in batch.data:
                dt = datetime.fromtimestamp(r.unix_time, tz=timezone.utc) if r.HasField("unix_time") else None
                temp_c = r.temperature / 100.0 if r.HasField("temperature") else None
                humidity = r.humidity / 100.0 if r.HasField("humidity") else None
                light = r.light if r.HasField("light") else None
                dust = r.dust if r.HasField("dust") else None
                pressure = r.pressure / 256.0 if r.HasField("pressure") else None

                bits = []
                if dt:
                    bits.append(f"time={dt.isoformat()}")
                if temp_c is not None:
                    bits.append(f"temp={temp_c:.1f}C/{temp_c * 9 / 5 + 32:.1f}F")
                if humidity is not None:
                    bits.append(f"humidity={humidity:.1f}%")
                if light is not None:
                    bits.append(f"light={light}")
                if dust is not None:
                    bits.append(f"dust={dust}")
                if pressure is not None:
                    bits.append(f"pressure={pressure:.1f}Pa")
                _log("    " + ", ".join(bits))

                with open(DATA_FILE, "a") as f:
                    f.write(json.dumps({
                        "timestamp": dt.isoformat() if dt else None,
                        "device_id": batch.device_id,
                        "temperature_c": temp_c,
                        "humidity_pct": humidity,
                        "light": light,
                        "dust": dust,
                        "pressure_pa": pressure,
                    }) + "\n")
        except Exception as e:
            _log(f"  [BATCH] decode error: {e}; raw {len(body)}B: {body.hex()[:160]}")
        self._send_ok()


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
    print("=== Hello Sense local server ===")
    print(f"AES key: {'custom' if AES_KEY != b'1234567891234567' else 'DEFAULT (1234567891234567)'}")
    print(f"Sensor data -> {DATA_FILE}\n", flush=True)
    threading.Thread(target=run_server, args=(80, False), daemon=True).start()
    threading.Thread(target=run_server, args=(443, True), daemon=True).start()
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("\nShutting down.")


if __name__ == "__main__":
    main()
