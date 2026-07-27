#!/usr/bin/env python3
"""Generate a local CA and a *.hello.is server certificate for the Sense.

Two Sense-specific quirks are handled here:
  * The device's clock runs ~70 years behind (a firmware epoch bug: real 2026
    shows as ~1956), and it rejects certs whose validity hasn't started yet
    (sl_Connect error -461 = SL_ESECDATEERROR). So notBefore is set to 1950.
  * SANs cover every hello.is host the firmware contacts.

Outputs (PEM + the CA also in DER for writing to the device):
  ca.key ca.crt ca.der  server.key server.crt
"""

import datetime
from cryptography import x509
from cryptography.x509.oid import NameOID
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa

NOT_BEFORE = datetime.datetime(1950, 1, 1, tzinfo=datetime.timezone.utc)
NOT_AFTER = datetime.datetime(2046, 1, 1, tzinfo=datetime.timezone.utc)
SANS = ["*.hello.is", "hello.is", "time.hello.is", "ntp.hello.is",
        "sense-in.hello.is", "dev-in.hello.is",
        "messeji.hello.is", "messeji-dev.hello.is"]


def rsa_key():
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


def write_pem_key(path, key):
    with open(path, "wb") as f:
        f.write(key.private_bytes(serialization.Encoding.PEM,
                                  serialization.PrivateFormat.TraditionalOpenSSL,
                                  serialization.NoEncryption()))


def main():
    # --- Certificate Authority ---
    ca_key = rsa_key()
    ca_name = x509.Name([
        x509.NameAttribute(NameOID.COUNTRY_NAME, "US"),
        x509.NameAttribute(NameOID.ORGANIZATION_NAME, "Hello Local"),
        x509.NameAttribute(NameOID.COMMON_NAME, "Hello Local Root CA"),
    ])
    ca_cert = (x509.CertificateBuilder()
               .subject_name(ca_name).issuer_name(ca_name)
               .public_key(ca_key.public_key())
               .serial_number(x509.random_serial_number())
               .not_valid_before(NOT_BEFORE).not_valid_after(NOT_AFTER)
               .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
               .sign(ca_key, hashes.SHA256()))

    # --- Server cert signed by the CA ---
    srv_key = rsa_key()
    srv_name = x509.Name([
        x509.NameAttribute(NameOID.COUNTRY_NAME, "US"),
        x509.NameAttribute(NameOID.ORGANIZATION_NAME, "Hello Local"),
        x509.NameAttribute(NameOID.COMMON_NAME, "*.hello.is"),
    ])
    srv_cert = (x509.CertificateBuilder()
                .subject_name(srv_name).issuer_name(ca_name)
                .public_key(srv_key.public_key())
                .serial_number(x509.random_serial_number())
                .not_valid_before(NOT_BEFORE).not_valid_after(NOT_AFTER)
                .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
                .add_extension(x509.SubjectAlternativeName([x509.DNSName(n) for n in SANS]), critical=False)
                .sign(ca_key, hashes.SHA256()))

    write_pem_key("ca.key", ca_key)
    write_pem_key("server.key", srv_key)
    with open("ca.crt", "wb") as f:
        f.write(ca_cert.public_bytes(serialization.Encoding.PEM))
    with open("ca.der", "wb") as f:
        f.write(ca_cert.public_bytes(serialization.Encoding.DER))
    with open("server.crt", "wb") as f:
        f.write(srv_cert.public_bytes(serialization.Encoding.PEM))

    print("Wrote ca.key ca.crt ca.der server.key server.crt")
    print(f"Validity: {NOT_BEFORE.date()} .. {NOT_AFTER.date()} (1950 start clears the device's skewed clock)")
    print("Next: `make write-ca PORT=...` to install ca.der on the device.")


if __name__ == "__main__":
    main()
