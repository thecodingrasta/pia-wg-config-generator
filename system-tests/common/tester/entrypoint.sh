#!/usr/bin/env bash
set -euo pipefail

echo "Tester Starting"

STATE_DIR="${STATE_DIR:-/state}"
WG_CONF_NAME="${WG_CONF_NAME:-wg0.conf}"
PF_PORT_FILE="${PF_PORT_FILE:-forwarded_port}"
IP_CHECK_URL="${IP_CHECK_URL:-https://ipinfo.io/ip}"
PIA_PF="${PIA_PF:-0}"
IPV6_MODE="${IPV6_MODE:-auto}"

VERIFY_ARGS=(--dir "$STATE_DIR" --wg "$WG_CONF_NAME" --pf-file "$PF_PORT_FILE" --ipv6 "$IPV6_MODE")
if [[ "$PIA_PF" == "1" ]]; then
  VERIFY_ARGS+=(--pf)
fi

./verify.sh "${VERIFY_ARGS[@]}"

echo "Verifying Connectivity"
wget -qO- "$IP_CHECK_URL" >/dev/null 2>&1 || {
  echo "FAIL: Connectivity Check Failed"
  exit 1
}

echo "PASS: Connectivity Check Completed"