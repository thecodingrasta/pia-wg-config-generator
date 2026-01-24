#!/usr/bin/env bash
set -euo pipefail

CONF="${1:-wg0.conf}"

if [ ! -f "${CONF}" ]; then
  echo "Config not found: ${CONF}"
  exit 1
fi

echo "[linux] Bringing interface up"
sudo wg-quick up "${CONF}"

echo "[linux] Checking connectivity"
curl -fsSL https://api.ipify.org >/dev/null

echo "[linux] Showing wg status"
sudo wg show

echo "[linux] Tearing interface down"
sudo wg-quick down "${CONF}"

echo "[linux] PASS"