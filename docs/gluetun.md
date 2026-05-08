# Docker + Gluetun Setup Guide

This guide walks through running `pia-wg-daemon` as a sidecar alongside [Gluetun](https://github.com/qdm12/gluetun) in Docker Compose.

The daemon generates and periodically refreshes a `wg0.conf` in a shared volume; Gluetun reads it to establish the WireGuard tunnel. Any container that routes through Gluetun's network namespace automatically benefits from the managed VPN session.

---

## Prerequisites

- Docker and Docker Compose v2
- A Private Internet Access account

---

## 1. Project Layout

```
your-stack/
├── docker-compose.yml
├── .env                   # your credentials (never commit this)
└── ...
```

---

## 2. Environment File

Copy the example and fill in your credentials:

```bash
cp system-tests/gluetun/.env.example .env
```

```ini
# .env
PIA_USERNAME=your_pia_username
PIA_PASSWORD=your_pia_password

# Region — run `pia-wg-config regions` to list available IDs
PIA_REGION=ca_toronto

# Set to 1 to enable port forwarding (only works on PF-capable regions)
PIA_PF=0

# IPv6 mode: auto (dual-stack), off (IPv4 only), kill (dual-stack + killswitch)
IPV6_MODE=auto

TZ=Europe/London
STATE_DIR=/state
WG_CONF_NAME=wg0.conf
PF_PORT_FILE=forwarded_port
IP_CHECK_URL=https://api.ipify.org

# Optional: path to an OpenSSL-linked curl inside the daemon container.
# Leave blank to use the container's system curl.
CURL_PATH=

# Gluetun custom provider settings (do not change these)
VPN_SERVICE_PROVIDER=custom
VPN_TYPE=wireguard
GLUETUN_WG_CONF=/gluetun/wireguard/wg0.conf
```

> **On regions and port forwarding:** not all PIA regions support port forwarding.
> Run `pia-wg-config regions --pf-only` to list the ones that do.
> `ca_toronto`, `ca_vancouver`, `netherlands`, and `sweden` are commonly available.

---

## 3. Basic Docker Compose

```yaml
# docker-compose.yml
services:

  # ── Sidecar daemon ───────────────────────────────────────────────────────
  pia-wg-daemon:
    build:
      context: .           # uses the Dockerfile in the project root
    environment:
      - PIA_USERNAME=${PIA_USERNAME}
      - PIA_PASSWORD=${PIA_PASSWORD}
      - CURL_PATH=${CURL_PATH}
      - TZ=${TZ}
    command: >
      daemon
        --region=${PIA_REGION}
        --state-dir=${STATE_DIR}
        --refresh-interval=12h
        --refresh-jitter=30m
        --verbose
    volumes:
      - state:${STATE_DIR}
    restart: unless-stopped

  # ── Gluetun VPN gateway ──────────────────────────────────────────────────
  gluetun:
    image: qmcgaw/gluetun:latest
    cap_add: [NET_ADMIN]
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      - TZ=${TZ}
      - VPN_SERVICE_PROVIDER=${VPN_SERVICE_PROVIDER}
      - VPN_TYPE=${VPN_TYPE}
      - WIREGUARD_CONF_FILE=${GLUETUN_WG_CONF}
      - UPDATER_PERIOD=0
    volumes:
      - state:${STATE_DIR}:ro    # daemon writes here
      - state:/gluetun:rw        # Gluetun reads wg0.conf from /gluetun/wireguard/
    depends_on:
      - pia-wg-daemon
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- ${IP_CHECK_URL} >/dev/null 2>&1 || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 40

  # ── Your application (routes through Gluetun) ────────────────────────────
  myapp:
    image: your-image:latest
    network_mode: "service:gluetun"   # <-- all traffic via VPN
    depends_on:
      gluetun:
        condition: service_healthy

volumes:
  state:
```

Start the stack:

```bash
docker compose up -d
docker compose logs -f pia-wg-daemon
```

---

## 4. Port Forwarding

Enable `--port-forwarding` to acquire and maintain a PIA port lease. The daemon writes the port to `forwarded_port` in the state directory and calls the `--on-port-change` hook whenever it changes.

```yaml
  pia-wg-daemon:
    command: >
      daemon
        --region=ca_toronto
        --state-dir=/state
        --port-forwarding
        --on-port-change="sh -c 'echo {port} > /state/forwarded_port && echo New port: {port}'"
        --refresh-interval=12h
        --verbose
```

The `{port}` placeholder in `--on-port-change` is replaced with the actual port number at runtime.

### Using the port in another container

Mount the state volume read-only and read the file:

```yaml
  qbittorrent:
    image: linuxserver/qbittorrent:latest
    network_mode: "service:gluetun"
    volumes:
      - state:/state:ro
    environment:
      - DOCKER_MODS=linuxserver/mods:qbittorrent-port-update
      - PORT_FILE=/state/forwarded_port
```

Or write a small hook script that calls your app's API when the port changes:

```bash
# /scripts/update-qbittorrent-port.sh
#!/usr/bin/env sh
curl -s -X POST "http://localhost:8080/api/v2/app/setPreferences" \
  --data-urlencode "json={\"listen_port\":$1}"
```

```yaml
    command: >
      daemon
        --region=ca_toronto
        --state-dir=/state
        --port-forwarding
        --on-port-change="/scripts/update-qbittorrent-port.sh {port}"
```

---

## 5. IPv6 Modes

Set `IPV6_MODE` in your `.env`:

| Value | Behaviour |
|-------|-----------|
| `auto` (or `on`) | Full dual-stack — routes IPv4 and IPv6 through the tunnel |
| `off` | IPv4 only — IPv6 traffic bypasses the tunnel |
| `kill` | Dual-stack + ip6tables rule that hard-blocks any IPv6 leaving outside the tunnel |

```yaml
  pia-wg-daemon:
    command: >
      daemon
        --ipv6-mode=${IPV6_MODE}
        ...
```

---

## 6. Config Refresh and Gluetun Reload

PIA sessions last up to ~24 hours. The daemon refreshes the config automatically (default: every 12 hours with up to 30 minutes of jitter). When it writes a new `wg0.conf`, Gluetun needs to be restarted to pick it up.

The cleanest approach is to send Gluetun a restart signal via the `--on-port-change` hook or a separate watchdog:

**Option A — restart via Docker socket (simple):**

Add a [Watchtower](https://containrrr.dev/watchtower/)-style signal or use the Docker CLI from within the daemon container:

```yaml
  pia-wg-daemon:
    volumes:
      - state:/state
      - /var/run/docker.sock:/var/run/docker.sock:ro   # allow restarting gluetun
    command: >
      daemon
        --region=ca_toronto
        --state-dir=/state
        --on-port-change="docker restart gluetun"
```

**Option B — Gluetun file-watch (future):**

Gluetun has a planned file-watch feature for `wg0.conf`. Check the [Gluetun changelog](https://github.com/qdm12/gluetun/releases) for availability.

**Option C — short refresh interval:**

Set a short interval so Gluetun's own restart cycle picks up changes quickly:

```yaml
    command: >
      daemon --refresh-interval=1h --refresh-jitter=5m ...
```

---

## 7. Complete Port-Forwarding Example

The system-test docker-compose is the reference implementation:

```bash
cat system-tests/gluetun/docker-compose.yml
```

To run it locally:

```bash
cp system-tests/gluetun/.env.example system-tests/gluetun/.env
# Edit .env with your credentials and PIA_PF=1
docker compose -f system-tests/gluetun/docker-compose.yml \
  --env-file system-tests/gluetun/.env \
  up --build
```

---

## 8. Troubleshooting

**Daemon starts but Gluetun never becomes healthy**

Check that the `wg0.conf` has been written to the shared volume:
```bash
docker compose exec gluetun cat /gluetun/wireguard/wg0.conf
```
If empty, the daemon may still be generating it on first run. Watch its logs:
```bash
docker compose logs -f pia-wg-daemon
```

**Token errors on first run**

PIA's token endpoint occasionally rate-limits. The daemon will fall back to the metadata server automatically. Set `--verbose` to see which path it took.

**`AddKey` fails with connection refused**

This usually means the selected region's WireGuard server is temporarily unavailable. Try a different `--region`.

**Port forwarding returns `status: ERROR`**

PIA only supports port forwarding on a subset of regions. Run:
```bash
pia-wg-config regions --pf-only
```
and switch to one of those regions.

**IPv6 leaks through**

Use `--ipv6-mode kill` to add an ip6tables rule that hard-blocks any IPv6 not routed through the tunnel.
