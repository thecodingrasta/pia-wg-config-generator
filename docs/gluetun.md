# Docker + Gluetun Setup

This guide explains the recommended setup service by service: a **self-recovering four-container stack** that keeps a PIA WireGuard tunnel and its forwarded port alive for [Gluetun](https://github.com/qdm12/gluetun) without manual intervention. The overview and diagrams are in the [README](../README.md#how-the-containers-fit-together); this page covers the reasoning, the details and the failure modes.

The complete, tested compose file is [`docs/examples/docker-compose.yml`](examples/docker-compose.yml). It is the single source of truth: this guide refers to it instead of repeating it, so it cannot drift.

> [!IMPORTANT]
> **Why not one daemon inside the tunnel?** PIA can evict a WireGuard registration server-side *before* the 24h token TTL (observed at ~11h45m). Recovering means calling PIA's public API to register a fresh key. A single daemon running inside Gluetun's namespace cannot do that once the tunnel is down: it has no egress, so it retries `network is unreachable` forever. The `refresh` daemon runs on the **LAN** precisely so recovery always has a route; `pf` stays inside the namespace because only the port-forward gateway needs the tunnel.

## The Four Containers

| Container | Image | Runs | Network |
| --------- | ----- | ---- | ------- |
| `gluetun` | `qmcgaw/gluetun` | The WireGuard tunnel | Its own namespace |
| `pia-wg-refresh-proxy` | `tecnativa/docker-socket-proxy` + the [restart-only template](examples/restart-only-docker-proxy.cfg.template) | A Docker API filter that allows one call | `refresh_net` (internal) |
| `pia-wg-refresh` | this project, `refresh` | The config daemon | `default` + `refresh_net` |
| `pia-wg-pf` | this project, `pf` | The port-forward daemon | Gluetun's namespace |

`pia-wg-refresh` and `pia-wg-pf` run the same image with different subcommands, so there are four containers but three images.

### `gluetun`: the VPN client

Gluetun runs the tunnel from `./config/gluetun/wireguard/wg0.conf` (`VPN_SERVICE_PROVIDER=custom`, `VPN_TYPE=wireguard`) and serves a control server on port 8000. `pia-wg-refresh` reads that control server to decide whether the tunnel is alive. The example does **not** override Gluetun's built-in Docker healthcheck: it reports Gluetun's own verdict, and `pia-wg-pf` waits for it (`depends_on: gluetun: service_healthy`).

### `pia-wg-refresh`: the config daemon

It keeps `wg0.conf` valid. It:

1. asks Gluetun's control server every `--health-check-interval` whether the VPN status is `running` **and** a public IP is present;
2. regenerates `wg0.conf` on a lifetime-aware schedule, and immediately (after `--unhealthy-threshold` consecutive bad checks) when the tunnel is dead;
3. writes `pending_port_forward.json` for `pia-wg-pf` **before** restarting Gluetun;
4. restarts Gluetun through the proxy (`--restart-container=gluetun`, `DOCKER_HOST=tcp://pia-wg-refresh-proxy:2375`);
5. rate-limits itself with single-flight, exponential backoff and an escalating cooldown, so a wedged tunnel never becomes a registration storm;
6. runs `--on-failure` once per outage, after `--alert-after` consecutive failed recoveries, and `--on-recovered` when the tunnel is healthy again.

It runs on the LAN so step 2 still works with the tunnel dead. It does not exit when Gluetun restarts, because it is not in the namespace that gets replaced. Its Docker healthcheck (`pia-wg-config healthcheck`) reports unhealthy only for a sustained problem and never restarts anything.

A tunnel is judged dead when Gluetun's control server reports a status other than `running` **or** reports `running` with no public IP. The second case matters: after a silent eviction Gluetun keeps reporting `running` while it restarts its VPN loop against the dead config. See the [README](../README.md#how-a-dead-tunnel-is-detected) for the measurements.

### `pia-wg-refresh-proxy`: the restart-only Docker proxy

`pia-wg-refresh` holds your PIA credentials, so it must not be handed the raw Docker socket, which is root on the host. The proxy sits between them and answers exactly one request:

```text
POST /containers/<PIA_WG_RESTART_TARGET>/restart
```

Everything else is `403`: listing, inspecting, exec, create, start, stop, kill, images, volumes, networks, any other container name, any other method. The path is matched as an exact string after URL decoding, so encoded or dot-segment tricks cannot widen it.

The stock `tecnativa/docker-socket-proxy` switches are too broad for this. Measured on that image, `CONTAINERS=1` with `POST=1` also allows `POST /containers/create` and exec creation, and `ALLOW_RESTARTS=1` allows stop, kill and restart of **every** container. The [template](examples/restart-only-docker-proxy.cfg.template) replaces the image's config to pin it to one container.

**Why it runs as root.** The proxy must open the Docker socket, which is `root:root` on Docker Desktop and `root:docker` on most Linux hosts, and the right group id differs per host. The compose service also runs with `cap_drop: ALL`, which removes the capabilities that HAProxy's `user`/`group` (CAP_SETUID/SETGID) and `chroot` (CAP_SYS_CHROOT) directives need. So the template declares `uid 0` explicitly, which is what HAProxy asks for when running as root is intentional, and silences its start-up warning (`HAProxy was started as the root user and does not make use of 'user' nor 'uid' global options`). The compensating controls are in the compose service: no capabilities, a read-only root filesystem, `no-new-privileges`, an internal network shared only with `pia-wg-refresh`, and the single-call ACL.

**Checking it yourself.** From inside the proxy container:

```bash
docker exec pia-wg-refresh-proxy sh -c "wget -q -S -O /dev/null --post-data='' http://127.0.0.1:2375/containers/gluetun/restart 2>&1 | head -1"   # 204 (this restarts Gluetun)
docker exec pia-wg-refresh-proxy sh -c "wget -q -S -O /dev/null http://127.0.0.1:2375/containers/json 2>&1 | head -1"                              # 403
```

`system-tests/stack-smoke.sh` runs these checks (against a throwaway container) in CI.

### `pia-wg-pf`: the port-forward daemon

PIA's port-forward gateway (usually `10.x.0.1:19999`) answers only through the tunnel, so this is the one job that must run in Gluetun's namespace (`network_mode: "service:gluetun"`). It polls `pending_port_forward.json`; for each **new** registration it waits for the gateway (`--wait-for-gateway`), requests a signature once, binds the port, then re-binds every 14 minutes and writes `forwarded_port`. It never generates configs and never touches Docker.

## Keeping `pia-wg-pf` Attached (gluetun-monitor)

A Gluetun restart builds a **new network namespace**. A container joined with `network_mode: service:gluetun` keeps the old one and is left with only a loopback interface until it is restarted. `pia-wg-pf` does not exit when that happens, so Docker's `restart: unless-stopped` never fires, and none of the four containers restart it. Until something does, it cannot reach the PIA gateway and the forwarded port stops being renewed. You can see it:

```bash
docker exec pia-wg-pf ls /sys/class/net     # only "lo" means it is stranded
docker restart pia-wg-pf                    # rejoins Gluetun's current namespace
```

[`gluetun-monitor`](https://github.com/csmarshall/gluetun-monitor) v2 automates exactly this. Every cycle it checks each container that shares Gluetun's namespace; one that is loopback-only is restarted (or recreated, with its volumes preserved, if Gluetun was replaced rather than restarted). Add it to the compose file:

```yaml
  # Third-party (MIT). Restarts pia-wg-pf after Gluetun is restarted or replaced.
  gluetun-monitor-proxy:
    image: tecnativa/docker-socket-proxy:latest   # pin by digest in real use
    container_name: gluetun-monitor-proxy
    environment:
      CONTAINERS: "1"
      POST: "1"
      EXEC: "1"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks:
      - monitor_net
    restart: unless-stopped

  gluetun-monitor:
    image: ghcr.io/csmarshall/gluetun-monitor:2
    container_name: gluetun-monitor
    depends_on:
      - gluetun-monitor-proxy
    environment:
      DOCKER_HOST: tcp://gluetun-monitor-proxy:2375
      GLUETUN_CONTAINER: gluetun
      SITES: https://www.cloudflare.com,https://github.com
    networks:
      - monitor_net
    restart: unless-stopped
```

and add `monitor_net: {}` under the top-level `networks:`.

> [!WARNING]
> This proxy is **much broader** than `pia-wg-refresh-proxy`. The monitor must list, exec into, restart and recreate containers, so its proxy needs `CONTAINERS=1`, `POST=1` and `EXEC=1`, which also permit creating containers. That is root-equivalent on the host. It is kept off `pia-wg-refresh`'s network and does not touch your PIA credentials, but read the [project's security notes](https://github.com/csmarshall/gluetun-monitor#security-considerations) and decide for yourself. Any equivalent that restarts namespace dependents works; `gluetun-monitor` is simply the one this stack was run with.

If you run without port forwarding, remove `pia-wg-pf` and this requirement goes away: `pia-wg-refresh` is not in the namespace, so it never gets stranded.

## 1. Get the Files and Configure

Copy the whole `docs/examples/` directory from this repository (the compose file mounts the proxy template that sits next to it), then create `.env`:

```bash
cd docs/examples
cp .env.example .env
```

```ini
PIA_USERNAME=your_pia_username
PIA_PASSWORD=your_pia_password

# Must support port forwarding. Run: pia-wg-config regions --pf-only
PIA_REGION=ca_toronto

# on = dual stack, off = IPv4 only, kill = dual stack with an IPv6 block rule
IPV6_MODE=kill

TZ=Europe/London
```

Do not wrap values in quotes: Docker's `--env-file` passes them literally. The compose file refuses to start if `PIA_USERNAME`, `PIA_PASSWORD` or `PIA_REGION` is missing.

By default the stack uses the published image, `ghcr.io/thecodingrasta/pia-wg-config-generator:latest`. To use a local build, run `docker build -t pia-wg-config-generator:local .` from the repository root and set `PIA_WG_IMAGE=pia-wg-config-generator:local` in `.env`.

## 2. Seed the First Config

Gluetun cannot start without an initial WireGuard config. Generate it once before the first `docker compose up`. `--entrypoint sh` lets the container expand the values from `.env`, which keeps special characters in your password intact:

```bash
mkdir -p config/gluetun/wireguard

docker run --rm --env-file .env -v "$PWD/config/gluetun:/gluetun" \
  --entrypoint sh ghcr.io/thecodingrasta/pia-wg-config-generator:latest \
  -c 'pia-wg-config generate --region="$PIA_REGION" --port-forwarding --ipv6-mode="$IPV6_MODE" --outfile=/gluetun/wireguard/wg0.conf --verbose'
```

After that, `pia-wg-refresh` keeps the file refreshed. This seed step replaces a Compose init container, and is required only when `./config/gluetun/wireguard/wg0.conf` does not exist yet.

## 3. Start It

```bash
docker compose up -d
docker compose logs -f pia-wg-refresh
docker compose logs -f pia-wg-pf
docker compose logs -f gluetun
```

Startup order after seeding:

1. Gluetun starts from the existing `wg0.conf`.
2. `pia-wg-refresh-proxy` and `pia-wg-refresh` start; `pia-wg-refresh` begins watching tunnel health. On startup it adopts a healthy, young registration instead of re-registering (`--adopt-healthy-registration`, on by default), so recreating it does not restart Gluetun.
3. Once Gluetun passes its healthcheck, `pia-wg-pf` starts and binds the port-forward lease.
4. From then on, each refresh writes a new config, hands the PF metadata to `pia-wg-pf`, and restarts Gluetun through the proxy. `pia-wg-pf` picks up the new registration and updates `forwarded_port` once the tunnel is back, without generating another config.

## What Gets Written

With `--state-dir=/gluetun/wireguard`, the daemons write:

| File | Written by | Purpose |
|------|------------|---------|
| `/gluetun/wireguard/wg0.conf` | `refresh` | WireGuard config consumed by Gluetun |
| `/gluetun/wireguard/status.json` | `refresh` | Status snapshot, rewritten on every health check (fields documented in the [README](../README.md#statusjson)) |
| `/gluetun/wireguard/registration_lifetime.json` | `refresh` | Measured registration lifetimes, used to schedule proactive refreshes |
| `/gluetun/wireguard/pending_port_forward.json` | `refresh` | Registration handed to `pf` across a Gluetun restart |
| `/gluetun/wireguard/forwarded_port` | `pf` | Current forwarded port, when port forwarding is enabled |

On the host, these are under `./config/gluetun/wireguard/`. Treat that directory as secret: `wg0.conf` contains your WireGuard private key.

## Why the First Config Is Separate

PIA port forwarding is only available through the active VPN tunnel, but Gluetun needs `wg0.conf` before it can create that tunnel. A completely empty install therefore needs one seed command. If you want `docker compose up` to bootstrap an empty directory on its own, Compose needs an extra one-shot seed service; that is the piece removed to keep the long-running stack to the four containers above.

## Why `--restart-container` Matters

PIA sessions are refreshed over time, and a refreshed `wg0.conf` can change even when the forwarded port stays the same. `--restart-container=gluetun` restarts Gluetun after each config refresh through Docker's API so it reloads the new file.

If your Gluetun container has a different name, set `--restart-container` **and** the proxy's `PIA_WG_RESTART_TARGET` to that exact name, and point `--control-server-url` at it. A `403` from the proxy in the `pia-wg-refresh` log means the two names do not match.

`--docker-socket` still works for setups that accept mounting the socket directly, at the cost described above.

## Port Forwarding

Keep `--port-forwarding` enabled only on PIA regions that support it. To list compatible regions:

```bash
pia-wg-config regions --username YOU --password SECRET --pf-only
```

When port forwarding succeeds, `pia-wg-pf` writes `/gluetun/wireguard/forwarded_port`. Use that file from qBittorrent, scripts, or another helper container to update an application with the current port.

`--on-port-change` (a `pf` flag) is separate from `--restart-container`. Use it only when another application needs to be told that the port changed:

```bash
--on-port-change="/scripts/update-qbittorrent-port.sh {port}"
```

## Alerting

Set `PIA_WG_ALERT_WEBHOOK` in `.env` to a Discord webhook or an ntfy topic URL. The example already wires `--on-failure=pia-wg-alert --on-recovered=pia-wg-alert`; with the variable unset the hook does nothing. Details and the variables the hook understands are in the [README](../README.md#alerting).

## Using an Existing Stack Prefix

If your Gluetun container is named dynamically, for example `media-gluetun`, configure the restart target, the proxy and the control-server URL to match:

```yaml
command: >
  refresh
    --control-server-url=http://${STACK_PREFIX}-gluetun:8000
    --restart-container=${STACK_PREFIX}-gluetun
```

and set `PIA_WG_RESTART_TARGET: ${STACK_PREFIX}-gluetun` on the proxy.

## IPv6 Mode

| Value | Behavior |
|-------|----------|
| `on` | Route IPv4 and IPv6 through the tunnel |
| `off` | IPv4 only |
| `kill` | Route IPv4 and IPv6, plus add an ip6tables rule to block IPv6 outside the tunnel |

If you do not want to think about IPv6, start with `IPV6_MODE=kill`.

## Upgrading From the Single `daemon`

Earlier releases ran one `daemon` container inside Gluetun's namespace. It remains available, but it **cannot self-recover** from a provider-side eviction (see the note at the top). To move to the four-container stack:

1. Keep `./config/gluetun`. The new containers use the same `wireguard/wg0.conf`, so Gluetun keeps its current tunnel and no re-seeding is needed.
2. Stop and remove the old daemon container.
3. Add `pia-wg-refresh-proxy`, `pia-wg-refresh` and `pia-wg-pf` from [`docs/examples/docker-compose.yml`](examples/docker-compose.yml), set `PIA_WG_RESTART_TARGET` and `--restart-container`/`--control-server-url` to your Gluetun container's name, and run `docker compose up -d`.
4. Add a namespace-dependent restarter such as `gluetun-monitor` if you use port forwarding (see above).

`pia-wg-pf` re-acquires the port-forward lease, and the forwarded port usually changes, so update your downloader afterwards.

## Troubleshooting

Check generated files:

```bash
ls -la ./config/gluetun/wireguard
cat ./config/gluetun/wireguard/status.json
```

Check the logs:

```bash
docker compose logs -f pia-wg-refresh
docker compose logs -f pia-wg-pf
docker compose logs -f gluetun
docker compose logs pia-wg-refresh-proxy
```

If Gluetun does not restart after a refresh, check:

1. `pia-wg-refresh` can reach its Docker endpoint (`DOCKER_HOST` or `--docker-host`), and the proxy's `PIA_WG_RESTART_TARGET` equals the Gluetun container name. A `403` from the proxy in the `pia-wg-refresh` log means the name does not match.
2. `--restart-container` matches the actual Gluetun container name.
3. The image was rebuilt and the containers were recreated after updating the binary.

If the proxy logs `HAProxy was started as the root user ...`, its template predates the explicit `uid 0` line. Update `restart-only-docker-proxy.cfg.template` from this repository and recreate the proxy (`docker compose up -d --force-recreate pia-wg-refresh-proxy`). It does not affect Gluetun.

If `forwarded_port` is missing or stops updating, check:

1. `--port-forwarding` is present on `pia-wg-refresh`, and `PIA_REGION` supports port forwarding.
2. `status.json` does not contain a port-forwarding error.
3. `pia-wg-pf` is not stranded after a Gluetun restart (`docker exec pia-wg-pf ls /sys/class/net` shows only `lo`). See [Keeping `pia-wg-pf` Attached](#keeping-pia-wg-pf-attached-gluetun-monitor).

After rebuilding the image locally, recreate the daemons:

```bash
docker build --no-cache -t pia-wg-config-generator:local .
docker compose up -d --force-recreate pia-wg-refresh pia-wg-pf
```

Recreating `pia-wg-refresh` does not restart Gluetun: it adopts a healthy, young registration and logs `[startup] adopted existing healthy registration ...`. Recreating `pia-wg-pf` re-acquires the lease, and the forwarded port usually changes, so update your downloader afterwards.
