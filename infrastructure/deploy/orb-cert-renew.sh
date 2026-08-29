#!/usr/bin/env bash
#
# Renew the app API's Let's Encrypt certificate and restart Caddy only if it
# actually changed.
#
# Renewal goes through the same route as the first issuance: certbot writes a
# challenge token into ./acme and sense-server serves it on port 80. Nothing
# takes port 80 away from the device, not even for a moment.
#
# Installed by:
#   sudo install -m 755 deploy/orb-cert-renew.sh /usr/local/bin/orb-cert-renew.sh
#   sudo install -m 644 deploy/orb-cert-renew.{service,timer} /etc/systemd/system/
#   sudo systemctl enable --now orb-cert-renew.timer
set -euo pipefail

INFRA="${INFRA:-/home/opc/hello-sense/infrastructure}"
cd "$INFRA"

# The overlay list has to be spelled out: this runs from systemd, not from the
# Makefile that normally derives it.
export COMPOSE_FILE=docker-compose.yml:docker-compose.linux.yml:docker-compose.public.yml

DOMAIN="$(sed -n 's/^APP_DOMAIN=//p' .env | tr -d '"' | head -1)"
if [ -z "$DOMAIN" ]; then
  echo "APP_DOMAIN is not set in $INFRA/.env" >&2
  exit 1
fi
LIVE="/etc/letsencrypt/live/$DOMAIN/fullchain.pem"

fingerprint() { sha256sum "$LIVE" 2>/dev/null | cut -d' ' -f1 || echo none; }

before="$(fingerprint)"

docker run --rm --network host \
  -v "$INFRA/acme:/acme:z" \
  -v /etc/letsencrypt:/etc/letsencrypt:z \
  -v /var/lib/letsencrypt:/var/lib/letsencrypt:z \
  certbot/certbot renew --webroot -w /acme --quiet

after="$(fingerprint)"

if [ "$before" = "$after" ]; then
  echo "certificate unchanged ($DOMAIN)"
  exit 0
fi

# Caddy reads the certificate once at startup, so a renewed file on disk is not
# a renewed certificate on the wire until it restarts.
echo "certificate renewed ($DOMAIN), restarting caddy"
docker compose restart caddy
