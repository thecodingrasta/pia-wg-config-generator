# Docker + Gluetun Setup

This guide shows the recommended setup: run `pia-wg-config` as a sidecar daemon, let it generate and refresh the PIA WireGuard config, and let Gluetun consume that generated config.

The simple version:

1. Build the daemon image once.
2. Run the daemon and Gluetun against the same `./config/gluetun` directory.
3. Make Gluetun wait until `wg0.conf` exists.
4. Restart Gluetun after every successful config refresh.

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

## 3. Compose File

```yaml
services:
  pia-wg-daemon:
    image: pia-wg-config-generator:local
    container_name: pia-wg-daemon
    environment:
      PIA_USERNAME: ${PIA_USERNAME}
      PIA_PASSWORD: ${PIA_PASSWORD}
      TZ: ${TZ}
      GLUETUN_CONTAINER: gluetun
    command: >
      daemon
        --region=${PIA_REGION}
        --state-dir=/gluetun/wireguard
        --port-forwarding
        --refresh-interval=12h
        --refresh-jitter=30m
        --retry-delay=5m
        --ipv6-mode=${IPV6_MODE}
        --on-config-change="restart-gluetun"
        --verbose
    volumes:
      - ./config/gluetun:/gluetun
      - /var/run/docker.sock:/var/run/docker.sock:ro
    healthcheck:
      test: ["CMD-SHELL", "test -s /gluetun/wireguard/wg0.conf"]
      interval: 5s
      timeout: 3s
      retries: 60
      start_period: 5s
    restart: unless-stopped

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
    depends_on:
      pia-wg-daemon:
        condition: service_healthy
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- https://api.ipify.org >/dev/null 2>&1 || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 10
      start_period: 60s
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

On the host, these are under:

```text
./config/gluetun/wireguard/
```

## Why the Healthcheck Matters

Plain `depends_on` only starts containers in order. It does not wait until the first container is ready.

This healthcheck:

```yaml
test: ["CMD-SHELL", "test -s /gluetun/wireguard/wg0.conf"]
```

makes Compose wait until the daemon has written a non-empty WireGuard config before starting Gluetun.

## Why `--on-config-change` Matters

PIA sessions are refreshed over time. A refreshed `wg0.conf` can change even when the forwarded port stays the same.

Use:

```bash
--on-config-change="restart-gluetun"
```

That restarts Gluetun after each successful config refresh. The image includes:

- `docker-cli`
- `/usr/local/bin/restart-gluetun`

For this to work, the daemon needs:

```yaml
environment:
  GLUETUN_CONTAINER: gluetun
volumes:
  - /var/run/docker.sock:/var/run/docker.sock:ro
```

If your Gluetun container has a different name, set `GLUETUN_CONTAINER` to that exact container name.

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

`--on-port-change` is separate from `--on-config-change`. Use it only when another application needs to be told that the port changed:

```bash
--on-port-change="/scripts/update-qbittorrent-port.sh {port}"
```

## Using an Existing Stack Prefix

If your Gluetun container is named dynamically, for example `media-gluetun`, configure:

```yaml
environment:
  GLUETUN_CONTAINER: media-gluetun
```

or:

```yaml
environment:
  GLUETUN_CONTAINER: ${STACK_PREFIX}-gluetun
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
2. `GLUETUN_CONTAINER` matches the actual Gluetun container name.
3. `restart-gluetun` is present in the daemon image.

You can test the helper manually:

```bash
docker compose exec pia-wg-daemon restart-gluetun
```

If `forwarded_port` is missing, check:

1. `--port-forwarding` is present.
2. `PIA_REGION` supports port forwarding.
3. `status.json` does not contain a port-forwarding error.
