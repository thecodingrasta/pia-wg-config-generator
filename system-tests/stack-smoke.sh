#!/usr/bin/env bash
# Smoke test for the documented four-container stack (docs/examples). It needs
# Docker and the network to pull images, but NO PIA credentials, and it never
# touches a container it did not create (everything is named pia-wg-smoke-*).
#
# It proves the claims the documentation makes that a unit test cannot:
#   1. the example compose file is valid and defines exactly the four services;
#   2. it refuses to start without credentials (required variables are enforced);
#   3. the restart-only proxy starts cleanly (no HAProxy warning), allows
#      POST /containers/<target>/restart, and answers 403 to everything else;
#   4. the image ships pia-wg-alert, reports its version, and its healthcheck
#      fails closed when there is no state.
#
# Usage: bash system-tests/stack-smoke.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
EXAMPLES_DIR="${REPO_ROOT}/docs/examples"
COMPOSE_FILE="${EXAMPLES_DIR}/docker-compose.yml"
ENV_FILE="${EXAMPLES_DIR}/.env.example"

PROJECT="pia-wg-smoke"
TARGET="pia-wg-smoke-target"
PROXY="pia-wg-smoke-proxy"
IMAGE="pia-wg-smoke-image:ci"
EXPECTED_SERVICES="gluetun
pia-wg-pf
pia-wg-refresh
pia-wg-refresh-proxy"

WORK_DIR="$(mktemp -d)"
FAILURES=0

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*" >&2; FAILURES=$((FAILURES + 1)); }

compose() {
  docker compose -p "${PROJECT}" --env-file "${ENV_FILE}" \
    -f "${COMPOSE_FILE}" -f "${WORK_DIR}/override.yml" "$@"
}

cleanup() {
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  docker rm -f "${TARGET}" >/dev/null 2>&1 || true
  docker image rm -f "${IMAGE}" >/dev/null 2>&1 || true
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

# Rename the proxy and point it at a throwaway container, so a developer's real
# stack is never involved. Everything else (image, hardening, template, networks)
# comes from the canonical compose file, which is the point.
cat > "${WORK_DIR}/override.yml" <<EOF
services:
  pia-wg-refresh-proxy:
    container_name: ${PROXY}
    environment:
      PIA_WG_RESTART_TARGET: ${TARGET}
EOF

# --- 1 and 2: the compose file itself ---------------------------------------
echo "== Compose file"
if docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" config --quiet; then
  pass "compose file validates with .env.example"
else
  fail "compose file does not validate"
fi

actual_services="$(docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" config --services | sort)"
if [ "${actual_services}" = "${EXPECTED_SERVICES}" ]; then
  pass "defines exactly the four documented services"
else
  fail "services differ from the documentation. Got: $(echo "${actual_services}" | tr '\n' ' ')"
fi

if (cd "${WORK_DIR}" && env -u PIA_USERNAME -u PIA_PASSWORD -u PIA_REGION \
      docker compose -f "${COMPOSE_FILE}" config --quiet >/dev/null 2>&1); then
  fail "compose file started without credentials; required variables are not enforced"
else
  pass "compose file refuses to start without credentials"
fi

# --- 3: the restart-only proxy ----------------------------------------------
echo "== Restart-only proxy"
docker run -d --name "${TARGET}" alpine:3.23 sleep 3600 >/dev/null
compose up -d --wait pia-wg-refresh-proxy >/dev/null

proxy_logs="$(docker logs "${PROXY}" 2>&1)"
if echo "${proxy_logs}" | grep -Eq '\[(WARNING|ALERT)\]'; then
  fail "proxy logged a HAProxy warning:"
  echo "${proxy_logs}" >&2
else
  pass "proxy starts without HAProxy warnings"
fi

# Status code of a request made from inside the proxy container (so no published
# port is needed). BusyBox wget prints the status line with -S, and the proxy
# answers errors with HTTP/1.0 or 1.1.
status() { # status <METHOD> <path>
  local method="$1" path="$2" out
  if [ "${method}" = "POST" ]; then
    out="$(docker exec "${PROXY}" sh -c "wget -q -S -O /dev/null --post-data='' 'http://127.0.0.1:2375${path}' 2>&1" || true)"
  else
    out="$(docker exec "${PROXY}" sh -c "wget -q -S -O /dev/null 'http://127.0.0.1:2375${path}' 2>&1" || true)"
  fi
  echo "${out}" | awk '/HTTP\//{print $2; exit}'
}

expect_status() { # expect_status <want> <METHOD> <path> <label>
  local want="$1" method="$2" path="$3" label="$4" got
  got="$(status "${method}" "${path}")"
  if [ "${got}" = "${want}" ]; then
    pass "${label} -> ${got}"
  else
    fail "${label}: want ${want}, got '${got}'"
  fi
}

started_before="$(docker inspect -f '{{.State.StartedAt}}' "${TARGET}")"
expect_status 204 POST "/containers/${TARGET}/restart?t=1" "POST restart of the configured target"
started_after="$(docker inspect -f '{{.State.StartedAt}}' "${TARGET}")"
if [ "${started_before}" != "${started_after}" ]; then
  pass "the target container was actually restarted"
else
  fail "proxy returned success but the target was not restarted"
fi

expect_status 403 POST "/containers/some-other-container/restart" "restart of a different container"
expect_status 403 POST "/containers/${TARGET}/stop" "stop of the target"
expect_status 403 POST "/containers/${TARGET}/kill" "kill of the target"
expect_status 403 POST "/containers/${TARGET}/exec" "exec create on the target"
expect_status 403 POST "/containers/create?name=evil" "container create"
expect_status 403 POST "/containers/other/../${TARGET}/restart" "dot-segment path trick"
expect_status 403 POST "/containers/${TARGET}/restart/extra" "extra path segment"
expect_status 403 GET "/containers/json" "list containers"
expect_status 403 GET "/containers/${TARGET}/json" "inspect the target"
expect_status 403 GET "/info" "daemon info"
expect_status 403 GET "/images/json" "list images"
expect_status 403 GET "/volumes" "list volumes"

# --- 4: the image ------------------------------------------------------------
echo "== Image"
docker build -q -t "${IMAGE}" "${REPO_ROOT}" >/dev/null
version_out="$(docker run --rm "${IMAGE}" --version)"
case "${version_out}" in
  *"pia-wg-config version"*) pass "image reports a version: ${version_out}" ;;
  *) fail "unexpected --version output: ${version_out}" ;;
esac

if docker run --rm --entrypoint sh "${IMAGE}" -c 'test -x "$(command -v pia-wg-alert)"'; then
  pass "pia-wg-alert is installed and executable"
else
  fail "pia-wg-alert is missing from the image"
fi

if docker run --rm -e PIA_WG_EVENT=failure --entrypoint pia-wg-alert "${IMAGE}"; then
  pass "pia-wg-alert is a silent no-op without PIA_WG_ALERT_WEBHOOK"
else
  fail "pia-wg-alert failed with no webhook configured"
fi

# The compose healthcheck must report unhealthy (exit 1) when there is no state.
set +e
docker run --rm "${IMAGE}" healthcheck --state-dir=/nonexistent >/dev/null 2>&1
health_exit=$?
set -e
if [ "${health_exit}" -eq 1 ]; then
  pass "healthcheck fails closed (exit 1) when wg0.conf is missing"
else
  fail "healthcheck exit code was ${health_exit}, want 1"
fi

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "FAILED: ${FAILURES} check(s) failed" >&2
  exit 1
fi
echo "PASS: four-container stack smoke test completed"
