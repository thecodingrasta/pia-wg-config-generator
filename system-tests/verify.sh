#!/usr/bin/env bash
set -euo pipefail

# DRY Verifier For System Tests (Gluetun, WireGuard Client, wg-quick etc)
# Runs inside the "tester" container, reading from the shared state volume.
#
# Usage:
#   ./verify.sh --dir /state --wg wg0.conf --ipv6 auto
#   ./verify.sh --dir /state --wg wg0.conf --pf --pf-file forwarded_port --ipv6 auto

DIR=""
WG_NAME="wg0.conf"
PF_FILE_NAME="forwarded_port"
EXPECT_PF="0"
IPV6_MODE="auto"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DIR="$2"; shift 2 ;;
    --wg) WG_NAME="$2"; shift 2 ;;
    --pf-file) PF_FILE_NAME="$2"; shift 2 ;;
    --pf) EXPECT_PF="1"; shift ;;
    --ipv6) IPV6_MODE="$2"; shift 2 ;;
    *)
      echo "Unknown Arg: $1"
      exit 2
      ;;
  esac
done

if [[ -z "$DIR" ]]; then
  echo "Missing --dir"
  exit 2
fi

WG_CONF="${DIR%/}/${WG_NAME}"
PF_FILE="${DIR%/}/${PF_FILE_NAME}"

echo "Verifying System Test Output In: $DIR"
echo " - wg0.conf: $WG_CONF"
echo " - forwarded_port: $PF_FILE"
echo " - Expect PF: $EXPECT_PF"
echo " - IPv6 Mode: $IPV6_MODE"

if [[ ! -f "$WG_CONF" ]]; then
  echo "FAIL: Missing WireGuard Config File: $WG_CONF"
  exit 1
fi

grep -Eq '^\[Interface\]$' "$WG_CONF" || { echo "FAIL: Missing [Interface]"; exit 1; }
grep -Eq '^PrivateKey = .+$' "$WG_CONF" || { echo "FAIL: Missing PrivateKey"; exit 1; }
grep -Eq '^Address = .+$' "$WG_CONF" || { echo "FAIL: Missing Address"; exit 1; }
grep -Eq '^DNS = .+$' "$WG_CONF" || { echo "FAIL: Missing DNS"; exit 1; }

# Kill mode should include PostUp/PostDown
if [[ "$IPV6_MODE" == "kill" ]]; then
  grep -Eq '^PostUp = .+$' "$WG_CONF" || { echo "FAIL: Expected PostUp In Kill Mode"; exit 1; }
  grep -Eq '^PostDown = .+$' "$WG_CONF" || { echo "FAIL: Expected PostDown In Kill Mode"; exit 1; }
fi

grep -Eq '^\[Peer\]$' "$WG_CONF" || { echo "FAIL: Missing [Peer]"; exit 1; }
grep -Eq '^PublicKey = .+$' "$WG_CONF" || { echo "FAIL: Missing PublicKey"; exit 1; }
grep -Eq '^Endpoint = .+:[0-9]+$' "$WG_CONF" || { echo "FAIL: Missing Endpoint With Port"; exit 1; }
grep -Eq '^PersistentKeepalive = 25$' "$WG_CONF" || { echo "FAIL: Missing PersistentKeepalive"; exit 1; }

case "$IPV6_MODE" in
  off)
    grep -Eq '^AllowedIPs = 0\.0\.0\.0/0$' "$WG_CONF" || { echo "FAIL: Expected IPv4-Only AllowedIPs"; exit 1; }
    ;;
  auto|kill)
    grep -Eq '^AllowedIPs = 0\.0\.0\.0/0, ::/0$' "$WG_CONF" || { echo "FAIL: Expected IPv4+IPv6 AllowedIPs"; exit 1; }
    ;;
  *)
    echo "FAIL: Invalid --ipv6 value: $IPV6_MODE (expected auto|off|kill)"
    exit 2
    ;;
esac

PORT="$(grep -E '^Endpoint = ' "$WG_CONF" | sed -E 's/^Endpoint = .+:([0-9]+)$/\1/' | head -n 1)"
if ! [[ "$PORT" =~ ^[0-9]+$ ]]; then
  echo "FAIL: Could Not Parse Endpoint Port"
  exit 1
fi
if (( PORT < 1 || PORT > 65535 )); then
  echo "FAIL: Endpoint Port Out Of Range: $PORT"
  exit 1
fi

if [[ "$EXPECT_PF" == "1" ]]; then
  if [[ ! -f "$PF_FILE" ]]; then
    echo "FAIL: Expected forwarded_port File But It Does Not Exist: $PF_FILE"
    exit 1
  fi

  PF_PORT="$(cat "$PF_FILE" | tr -d '[:space:]')"
  if ! [[ "$PF_PORT" =~ ^[0-9]+$ ]]; then
    echo "FAIL: forwarded_port Not Numeric: $PF_PORT"
    exit 1
  fi
  if (( PF_PORT < 1 || PF_PORT > 65535 )); then
    echo "FAIL: forwarded_port Out Of Range: $PF_PORT"
    exit 1
  fi

  echo "PF Port Looks Valid: $PF_PORT"
else
  echo "PF Not Expected; Skipping forwarded_port Checks"
fi

echo "PASS: System Test Verification Completed"