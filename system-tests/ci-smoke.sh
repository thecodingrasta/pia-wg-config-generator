#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "Usage: $0 <gluetun|wireguard-client> [--pf]"
  exit 2
fi

SCENARIO="$1"
EXPECT_PF="0"
if [[ "${2:-}" == "--pf" ]]; then
  EXPECT_PF="1"
elif [[ $# -eq 2 ]]; then
  echo "Unknown argument: $2"
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="${SCRIPT_DIR}/${SCENARIO}"
HOST_STATE_DIR="${SCRIPT_DIR}/.ci-state/${SCENARIO}"
TESTER_IMAGE="pia-wg-system-tester:${SCENARIO}"

case "${SCENARIO}" in
  gluetun|wireguard-client) ;;
  *)
    echo "Unknown scenario: ${SCENARIO}"
    exit 2
    ;;
esac

rm -rf "${HOST_STATE_DIR}"
mkdir -p "${HOST_STATE_DIR}"

cat > "${HOST_STATE_DIR}/wg0.conf" <<'WGCONF'
[Interface]
PrivateKey = 0000000000000000000000000000000000000000000=
Address = 10.0.0.2/32, fd00::2/128
DNS = 10.0.0.1
PostUp = ip6tables -P OUTPUT DROP
PostDown = ip6tables -P OUTPUT ACCEPT

[Peer]
PublicKey = 1111111111111111111111111111111111111111111=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.10:1337
PersistentKeepalive = 25
WGCONF

if [[ "${EXPECT_PF}" == "1" ]]; then
  printf '45678\n' > "${HOST_STATE_DIR}/forwarded_port"
fi

export PIA_USERNAME="ci-placeholder"
export PIA_PASSWORD="ci-placeholder"
export PIA_REGION="ca_toronto"
export PIA_PF="${EXPECT_PF}"
export IPV6_MODE="kill"
export TZ="UTC"
if [[ "${SCENARIO}" == "wireguard-client" ]]; then
  export STATE_DIR="/state"
else
  export STATE_DIR="/gluetun/wireguard"
fi
export WG_CONF_NAME="wg0.conf"
export PF_PORT_FILE="forwarded_port"
export IP_CHECK_URL="https://api.ipify.org"
export VPN_SERVICE_PROVIDER="custom"
export VPN_TYPE="wireguard"
export GLUETUN_WG_CONF="/gluetun/wireguard/wg0.conf"
export CURL_PATH="/usr/bin/curl"

echo "Validating Compose config for ${SCENARIO}"
docker compose -f "${COMPOSE_DIR}/docker-compose.yml" config >/dev/null

echo "Building project Docker services for ${SCENARIO}"
docker compose -f "${COMPOSE_DIR}/docker-compose.yml" build pia-wg-daemon tester

if [[ "${SCENARIO}" == "gluetun" ]]; then
  docker compose -f "${COMPOSE_DIR}/docker-compose.yml" build pia-wg-seed
fi

echo "Building standalone tester image"
docker build -f "${SCRIPT_DIR}/common/tester/Dockerfile" "${SCRIPT_DIR}" -t "${TESTER_IMAGE}"

echo "Running shared verifier against CI fixture"
VERIFY_ARGS=(--dir /state --wg wg0.conf --pf-file forwarded_port --ipv6 kill)
if [[ "${EXPECT_PF}" == "1" ]]; then
  VERIFY_ARGS+=(--pf)
fi

docker run --rm \
  -v "${HOST_STATE_DIR}:/state:ro" \
  --entrypoint /app/verify.sh \
  "${TESTER_IMAGE}" \
  "${VERIFY_ARGS[@]}"

echo "PASS: CI Docker smoke test completed for ${SCENARIO}"
