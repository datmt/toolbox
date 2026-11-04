#!/bin/sh
# Fix BLE mouse/keyboard that is paired + trusted but not usable
# (bluetoothd: "input-hog profile accept failed").
# Usage: fix.sh [MAC]   (no MAC: list devices only)
set -u
MAC=${1:-}

if [ ! -d "/lib/modules/$(uname -r)" ]; then
  echo "Running kernel $(uname -r) has no modules on disk (kernel upgraded, not rebooted)."
  echo "Reboot, then run this again."
  exit 1
fi

command -v bluetoothctl >/dev/null || { echo "bluetoothctl missing: install bluez-utils (Arch) / bluez (Debian, Fedora)"; exit 1; }

sudo modprobe uhid && lsmod | grep -w uhid || { echo "uhid failed to load"; exit 1; }
sudo systemctl restart bluetooth
sleep 2

if [ -z "$MAC" ]; then
  bluetoothctl devices
  echo "Run again with the device MAC: $0 <MAC>"
  exit 0
fi

echo "Click/move the device now to wake it..."
bluetoothctl --timeout 15 connect "$MAC"
bluetoothctl info "$MAC" | grep -E 'Name|Connected|ServicesResolved'
journalctl -b -u bluetooth --no-pager | tail -5
