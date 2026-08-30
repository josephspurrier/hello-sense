#!/usr/bin/env bash
#
# Stage an OTA image and arm it for one device.
#
#   scripts/arm-firmware.sh <device-id> <image.bin> <to-version> [host]
#
# Staging copies the image into ./firmware, which orb serves read-only at
# /firmware/<name> when ORB_FIRMWARE_DIR is set. Arming writes one row of
# firmware_updates. Nothing is offered until BOTH are true, and orb applies its
# own gates on top (see orb/internal/ota): armed, matching current version,
# 20 minutes of uptime, and a 02:00-05:00 local window.
#
# The values below are not free choices. They are what kitsune/fatfs_cmd.c
# requires:
#
#   serial_flash_filename   MUST be "mcuimgx.bin". The firmware overwrites
#                           index 6, the 'x', with the inactive slot number,
#                           giving /sys/mcuimg2.bin or /sys/mcuimg3.bin. It
#                           chooses; the server must not.
#   sd_card_*               MUST be present even though this is a serial-flash
#                           write. file_download_task reads them before it
#                           reaches the serial-flash branch, and an absent
#                           field leaves a NULL that strlen() walks into.
#                           sd_card_filename is "mcuimgx.bin", not "mcuimg.bin":
#                           three independent places in Hello's own code agree on
#                           it (suripu-service CreateDynamoDBTables, its
#                           ReceiveResourceIT, and the hello-admin UI defaults),
#                           so match the reference rather than reasoning about
#                           which fields "should" matter.
#   sha1 / file_size        MUST be right. sf_sha1_verify checks the written
#                           bytes and abandons the update on a mismatch WITHOUT
#                           touching the boot record, which is the difference
#                           between a failed download and a bad boot.
set -euo pipefail

cd "$(dirname "$0")/.."

DEVICE="${1:?usage: arm-firmware.sh <device-id> <image.bin> <to-version> [host]}"
IMAGE="${2:?missing image path}"
TO_VER="${3:?missing target version, e.g. 4514}"
HOST="${4:-${OTA_HOST:-sense-in.hello.is}}"

[ -f "$IMAGE" ] || { echo "no such image: $IMAGE" >&2; exit 1; }

# The device resolves this host ITSELF, with whatever resolver its network gives
# it. If it cannot, gethostbyname() fails inside download_file(), the update is
# abandoned before a single byte is fetched, and the only trace is the device
# logging "MCU image name converted to ..." and then nothing. The server sees an
# offer and no download, which looks exactly like a failed install.
#
# That cost 28 wasted attempts on 2026-08-29: the default was a hello.is name
# that the firmware no longer uses and the device's new network could not
# resolve. Check it here rather than infer it from silence.
if ! getent hosts "$HOST" >/dev/null 2>&1 && ! host "$HOST" >/dev/null 2>&1; then
  echo "REFUSING: '$HOST' does not resolve." >&2
  echo "  The device fetches the image from this name using its own resolver," >&2
  echo "  so it must resolve on the device's network, not just here." >&2
  echo "  Pass one as the 4th argument, or set OTA_HOST." >&2
  exit 1
fi

NAME="$(basename "$IMAGE")"
case "$NAME" in
  *.bin) ;;
  *) echo "image must be named *.bin (orb refuses anything else)" >&2; exit 1 ;;
esac

SHA=$(shasum "$IMAGE" 2>/dev/null | cut -d' ' -f1 || sha1sum "$IMAGE" | cut -d' ' -f1)
SIZE=$(wc -c < "$IMAGE" | tr -d ' ')

mkdir -p firmware
# The image may already be staged, in which case cp would refuse rather than
# no-op. Comparing resolved paths keeps re-arming the same image idempotent.
if [ "$(cd "$(dirname "$IMAGE")" && pwd)/$NAME" != "$(pwd)/firmware/$NAME" ]; then
  cp "$IMAGE" "firmware/$NAME"
fi

# Relabel for SELinux. The compose mount carries `z`, but that only relabels
# what exists when the container starts. A file staged afterwards keeps whatever
# label it arrived with, and `mv` preserves the source's label rather than
# inheriting the directory's. The result is a file that is plainly there, owned
# and readable, and completely invisible inside the container: the device gets a
# 404 and abandons the update, with nothing in any log saying why.
if command -v chcon >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
  chcon -t container_file_t "firmware/$NAME" 2>/dev/null \
    || sudo chcon -t container_file_t "firmware/$NAME"
  echo "relabelled firmware/$NAME for SELinux"
fi

PSQL_USER=$(. ./.env 2>/dev/null; echo "${POSTGRES_USER:-hello}")
PSQL_DB=$(. ./.env 2>/dev/null; echo "${POSTGRES_DB:-orb}")

echo "device   : $DEVICE"
echo "image    : firmware/$NAME"
echo "size     : $SIZE bytes"
echo "sha1     : $SHA"
echo "target   : $TO_VER"
echo "url      : http://$HOST/firmware/$NAME"
echo
read -r -p "Arm this update? Type the device id to confirm: " ans
[ "$ans" = "$DEVICE" ] || { echo "aborted"; exit 1; }

docker compose exec -T postgres psql -U "$PSQL_USER" -d "$PSQL_DB" -v ON_ERROR_STOP=1 <<SQL
-- One armed update per device at a time is enforced by a partial unique index,
-- so retire any previous attempt rather than colliding with it.
UPDATE firmware_updates SET armed = false
 WHERE device_id = '$DEVICE' AND armed AND completed_at IS NULL;

INSERT INTO firmware_updates (
    device_id, from_version, to_version, host, url, sha1, file_size,
    copy_to_serial_flash, reset_application_processor, reset_network_processor,
    serial_flash_filename, serial_flash_path,
    sd_card_filename, sd_card_path,
    armed, notes)
VALUES (
    '$DEVICE',
    (SELECT firmware_version FROM senses WHERE device_id = '$DEVICE'),
    $TO_VER,
    '$HOST',
    '/firmware/$NAME',
    decode('$SHA', 'hex'),
    $SIZE,
    true, true, false,
    'mcuimgx.bin', '/sys/',
    'mcuimgx.bin', '/',
    true,
    'armed by scripts/arm-firmware.sh');

SELECT id, device_id, from_version, to_version, file_size, armed FROM firmware_updates
 WHERE device_id = '$DEVICE' ORDER BY id DESC LIMIT 1;
SQL

echo
echo "Armed. Two things still have to be true before anything is offered:"
echo "  1. ORB_FIRMWARE_DIR=/firmware in .env, and orb restarted. Without it the"
echo "     image is not served and the device gets a 404."
echo "  2. The device must be inside orb's OTA window and up for 20 minutes."
echo "     The window defaults to 02:00-05:00 local; -ota-window overrides it."
