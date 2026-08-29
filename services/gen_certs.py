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
import os
from cryptography import x509
from cryptography.x509.oid import NameOID
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa

NOT_BEFORE = datetime.datetime(1950, 1, 1, tzinfo=datetime.timezone.utc)
NOT_AFTER = datetime.datetime(2046, 1, 1, tzinfo=datetime.timezone.utc)
SANS = ["*.hello.is", "hello.is", "time.hello.is", "ntp.hello.is",
        "sense-in.hello.is", "dev-in.hello.is",
        "messeji.hello.is", "messeji-dev.hello.is"]

# Additional names, for a firmware built with KITSUNE_DEV_DOMAIN pointing its DEV
# endpoint slots at a domain you control:
#
#     SENSE_EXTRA_DOMAIN=example.com python3 gen_certs.py
#
# Both sets end up in one certificate on purpose. The device switches between
# them with the console command `dev 1` / `dev 0`, and a certificate that only
# covered one set would make that switch fail the handshake instead of just
# changing servers.
EXTRA_DOMAIN = os.environ.get("SENSE_EXTRA_DOMAIN", "").strip()
if EXTRA_DOMAIN:
    SANS += ["*." + EXTRA_DOMAIN, EXTRA_DOMAIN,
             "sense-in." + EXTRA_DOMAIN,
             "messeji." + EXTRA_DOMAIN,
             "time." + EXTRA_DOMAIN]


def rsa_key():
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


def write_pem_key(path, key):
    with open(path, "wb") as f:
        f.write(key.private_bytes(serialization.Encoding.PEM,
                                  serialization.PrivateFormat.TraditionalOpenSSL,
                                  serialization.NoEncryption()))


def load_ca():
    """Reuse ca.key and ca.crt if they are already here.

    THIS MATTERS MORE THAN IT LOOKS. The CA is what the device trusts, and it
    lives in /cert/ca.der on the CC3200's serial flash, reachable only over UART
    in bootloader mode. Minting a fresh CA and deploying the server certificate
    signed by it means the device rejects every handshake, and the only way back
    is a cable. Reissuing the SERVER certificate from the SAME CA, which is what
    happens when the files below already exist, requires nothing on the device.

    Pass SENSE_NEW_CA=1 to generate a new one anyway. You then have to run
    `make write-ca` before the device will talk to you again.
    """
    if os.path.exists("ca.key") and os.path.exists("ca.crt") and not os.environ.get("SENSE_NEW_CA"):
        with open("ca.key", "rb") as f:
            ca_key = serialization.load_pem_private_key(f.read(), password=None)
        with open("ca.crt", "rb") as f:
            ca_cert = x509.load_pem_x509_certificate(f.read())
        print("Reusing the existing CA (ca.key, ca.crt). The device already trusts it.")
        return ca_key, ca_cert, ca_cert.subject, False

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
    print("Generated a NEW CA. Run `make write-ca` or the device will reject every handshake.")
    return ca_key, ca_cert, ca_name, True


def main():
    ca_key, ca_cert, ca_name, ca_is_new = load_ca()

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

    if ca_is_new:
        write_pem_key("ca.key", ca_key)
        with open("ca.crt", "wb") as f:
            f.write(ca_cert.public_bytes(serialization.Encoding.PEM))
        with open("ca.der", "wb") as f:
            f.write(ca_cert.public_bytes(serialization.Encoding.DER))
    write_pem_key("server.key", srv_key)
    with open("server.crt", "wb") as f:
        f.write(srv_cert.public_bytes(serialization.Encoding.PEM))

    print("Wrote server.key server.crt" + (" ca.key ca.crt ca.der" if ca_is_new else " (CA untouched)"))
    print("SANs: " + ", ".join(SANS))
    print(f"Validity: {NOT_BEFORE.date()} .. {NOT_AFTER.date()} (1950 start clears the device's skewed clock)")
    if ca_is_new:
        print("Next: `make write-ca PORT=...` to install ca.der on the device.")
    else:
        print("Nothing to install on the device: it already trusts this CA.")


if __name__ == "__main__":
    main()
