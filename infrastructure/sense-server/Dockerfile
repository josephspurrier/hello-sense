# syntax=docker/dockerfile:1
#
# sense-server: the TLS terminator the Sense can actually talk to.
#
# The CC3200 offers exactly one cipher suite, TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA
# (0xC014), and sends no supported_groups extension. OpenSSL 3.x, LibreSSL and
# Go all refuse that handshake, because a modern stack will not pick an ECDHE
# curve without the extension. tlslite-ng, pure Python, is the only thing that
# completes it. That is the entire reason this service exists and the reason it
# cannot sit behind nginx, Caddy, Cloudflare or a cloud load balancer.
#
# LINUX ONLY, in practice. On macOS, Docker Desktop runs a Linux VM and every
# packet reaches the container through a userland port forwarder. Putting a NAT
# hop in front of the pickiest client on the network buys nothing and this
# device has already been broken twice at exactly that layer. On macOS, run
# sense_server.py on the host instead. See README.md.
#
# Build context is the directory holding sense_server.py (SENSE_SERVER_SRC).

FROM python:3.13-slim

# tlslite-ng   the handshake, see above
# cryptography the AES used to sign device payloads
# protobuf     ntp_pb2, the clock exchange
RUN pip install --no-cache-dir \
        tlslite-ng==0.8.2 \
        cryptography \
        protobuf

WORKDIR /app
COPY sense_server.py ntp_pb2.py ./

# Runs as root because it binds :80 and :443. With network_mode: host on Linux
# those are the host's privileged ports, so there is no way around it short of
# CAP_NET_BIND_SERVICE, which is not simpler.
#
# server.crt and server.key are mounted at runtime from ./secrets, never baked
# into the image.
EXPOSE 80 443

ENTRYPOINT ["python", "-u", "sense_server.py"]
