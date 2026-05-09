# Docker + Gluetun Setup

This guide shows the recommended setup: build the `pia-wg-config` image locally, seed the first WireGuard config once, then run the daemon inside Gluetun's network namespace so it can refresh the config and maintain PIA port forwarding.

The simple version:

1. Build the daemon image once.
2. Generate the first `wg0.conf` once if this is a fresh install.
3. Start Gluetun from that config.
4. Run the long-running daemon inside Gluetun's network namespace.
5. On every refresh, the daemon writes `wg0.conf`, restarts Gluetun, exits, and is restarted by Docker into Gluetun's new network namespace.
6. The restarted daemon waits for the PIA gateway, then writes/renews `forwarded_port`.

## 1. Build the Image

From this repository:

```bash
docker build -t pia-wg-config-generator:local .
```

If you run Docker on another machine, build the image on that machine or push it to a registry.

## 2. Create `.env`

```ini
PIA_USERNAME=your_pia_username
PIA_PASSWORD=your_pia_password

# Use a region that supports port forwarding if --port-forwarding is enabled.
# Run: pia-wg-config regions --pf-only
PIA_REGION=ca_toronto

# on = dual stack, off = IPv4 only, kill = dual stack with an IPv6 block rule
IPV6_MODE=kill

TZ=Europe/London
```

## 3. Seed the First Config

Gluetun cannot start without an initial WireGuard config. Generate it once before the first `docker compose up`:

```bash
docker run --rm --env-file .env -v ./config/gluetun:/gluetun pia-wg-config-generator:local generate --region=${PIA_REGION} --port-forwarding --ipv6-mode=${IPV6_MODE} --outfile=/gluetun/wireguard/wg0.conf --verbose
```

After that, the daemon keeps the file refreshed.

This seed step replaces a Compose init container. It is required only when `./config/gluetun/wireguard/wg0.conf` does not exist yet.

Startup order after seeding:

1. Gluetun starts from the existing `wg0.conf`.
2. Gluetun passes its healthcheck.
3. `pia-wg-daemon` starts because it depends on `gluetun: service_healthy`.
4. On future refreshes, the daemon writes a new config, restarts Gluetun, exits, and is restarted by Docker into Gluetun's new network namespace.
5. The restarted daemon uses the pending metadata to update `forwarded_port` without generating another config first.

## 4. Compose File

```yaml
services:
  gluetun:
    image: qmcgaw/gluetun:latest
    container_name: gluetun
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      TZ: ${TZ}
      VPN_SERVICE_PROVIDER: custom
      VPN_TYPE: wireguard
      UPDATER_PERIOD: 0
    volumes:
      - ./config/gluetun:/gluetun
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- https://api.ipify.org >/dev/null 2>&1 || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 10
      start_period: 60s

  pia-wg-daemon:
    image: pia-wg-config-generator:local
    container_name: pia-wg-daemon
    network_mode: "service:gluetun"
    environment:
      PIA_USERNAME: ${PIA_USERNAME}
      PIA_PASSWORD: ${PIA_PASSWORD}
      TZ: ${TZ}
    command: >
      daemon
        --region=${PIA_REGION}
        --state-dir=/gluetun/wireguard
        --port-forwarding
        --refresh-interval=12h
        --refresh-jitter=30m
        --retry-delay=5m
        --ipv6-mode=${IPV6_MODE}
        --wait-for-gateway
        --restart-container=gluetun
        --verbose
    volumes:
      - ./config/gluetun:/gluetun
      - /var/run/docker.sock:/var/run/docker.sock:ro
    depends_on:
      gluetun:
        condition: service_healthy
    restart: unless-stopped
```

Start it:

```bash
docker compose up -d
docker compose logs -f pia-wg-daemon
docker compose logs -f gluetun
```

## What Gets Written

With `--state-dir=/gluetun/wireguard`, the daemon writes:

| File | Purpose |
|------|---------|
| `/gluetun/wireguard/wg0.conf` | WireGuard config consumed by Gluetun |
| `/gluetun/wireguard/status.json` | Last daemon status and errors |
| `/gluetun/wireguard/forwarded_port` | Current forwarded port, when port forwarding is enabled |
| `/gluetun/wireguard/pending_port_forward.json` | Temporary metadata used across a Gluetun restart |

On the host, these are under:

```text
./config/gluetun/wireguard/
```

## Why the First Config Is Separate

PIA port forwarding is only available through the active VPN tunnel, but Gluetun needs `wg0.conf` before it can create that tunnel. That means a completely empty install needs one seed command.

After the first file exists, the daemon can run inside Gluetun's network namespace and handle refreshes continuously.

If you want `docker compose up` to bootstrap a completely empty directory without running the seed command first, Compose needs an extra one-shot seed/init service. That is the piece we removed to keep the long-running setup to one PIA container plus Gluetun.

## Why `--restart-container` Matters

PIA sessions are refreshed over time. A refreshed `wg0.conf` can change even when the forwarded port stays the same.

Use:

```bash
--restart-container=gluetun
```

That restarts Gluetun after each config refresh through Docker's API.

Because the daemon uses:

```yaml
network_mode: "service:gluetun"
```

it must exit after restarting Gluetun. Docker then restarts the daemon into Gluetun's new network namespace. That is required for PIA port forwarding because the PIA gateway endpoint, usually `10.100.0.1:19999`, is reachable only through the active VPN tunnel.

For this to work, the daemon needs:

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock:ro
restart: unless-stopped
```

If your Gluetun container has a different name, set `--restart-container` to that exact container name.

## Port Forwarding

Keep `--port-forwarding` enabled only on PIA regions that support port forwarding.

To list compatible regions:

```bash
pia-wg-config regions --username YOU --password SECRET --pf-only
```

When port forwarding succeeds, the daemon writes:

```text
/gluetun/wireguard/forwarded_port
```

Use that file from qBittorrent, scripts, or another sidecar if you need to update an application with the current port.

`--on-port-change` is separate from `--restart-container`. Use it only when another application needs to be told that the port changed:

```bash
--on-port-change="/scripts/update-qbittorrent-port.sh {port}"
```

## Using an Existing Stack Prefix

If your Gluetun container is named dynamically, for example `media-gluetun`, configure:

```yaml
command: >
  daemon
    --restart-container=${STACK_PREFIX}-gluetun
```

## IPv6 Mode

Recommended values:

| Value | Behavior |
|-------|----------|
| `on` | Route IPv4 and IPv6 through the tunnel |
| `off` | IPv4 only |
| `kill` | Route IPv4 and IPv6, plus add an ip6tables rule to block IPv6 outside the tunnel |

If you do not want to think about IPv6, start with:

```ini
IPV6_MODE=kill
```

## Troubleshooting

Check generated files:

```bash
ls -la ./config/gluetun/wireguard
cat ./config/gluetun/wireguard/status.json
```

Check daemon logs:

```bash
docker compose logs -f pia-wg-daemon
```

Check Gluetun logs:

```bash
docker compose logs -f gluetun
```

If Gluetun does not restart after refresh, check:

1. `/var/run/docker.sock` is mounted into `pia-wg-daemon`.
2. `--restart-container` matches the actual Gluetun container name.
3. The daemon image was rebuilt and the container was recreated after updating the binary.

If `forwarded_port` is missing, check:

1. `--port-forwarding` is present.
2. `PIA_REGION` supports port forwarding.
3. `status.json` does not contain a port-forwarding error.
