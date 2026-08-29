#!/bin/bash
# Builds orb and installs it as a supervised launchd service.
#
# Run from the repo root. Needs sudo for /usr/local/bin and /Library/LaunchDaemons.
#
# Idempotent: re-run it to deploy a new build. The service is unloaded before
# the binary is replaced, because launchd will happily keep running a deleted
# inode and you will spend an afternoon wondering why your fix did nothing.
set -euo pipefail

PLIST=is.hello.orb.plist
DEST_PLIST=/Library/LaunchDaemons/$PLIST
DEST_BIN=/usr/local/bin/orb
LOG=/usr/local/var/log/orb.log

cd "$(dirname "$0")/.."   # orb/

echo "--- building ---"
go build -o /tmp/orb-build ./cmd/orb
echo "  ok"

echo "--- stopping the service if it is running ---"
if sudo launchctl list | grep -q is.hello.orb; then
  sudo launchctl unload "$DEST_PLIST" 2>/dev/null || true
  echo "  unloaded"
else
  echo "  not running"
fi

echo "--- installing ---"
sudo mkdir -p /usr/local/bin /usr/local/var/log
sudo cp /tmp/orb-build "$DEST_BIN"
sudo chmod 755 "$DEST_BIN"
sudo cp "deploy/$PLIST" "$DEST_PLIST"
sudo chown root:wheel "$DEST_PLIST"
sudo chmod 644 "$DEST_PLIST"
echo "  binary at $DEST_BIN"
echo "  plist  at $DEST_PLIST"

echo "--- starting ---"
sudo launchctl load -w "$DEST_PLIST"
sleep 3

if sudo launchctl list | grep -q is.hello.orb; then
  echo "  running"
else
  echo "  FAILED to start; see $LOG" >&2
  exit 1
fi

echo
echo "logs:    tail -f $LOG"
echo "stop:    sudo launchctl unload -w $DEST_PLIST"
echo
echo "NOTE: this does not switch the device over. orb stays a shadow until"
echo "sense_server.py is restarted with SENSE_UPSTREAM_SENSE=http://127.0.0.1:8081."
