# PIA WireGuard Config Generator

<p align="center">
  <strong>Automation-friendly WireGuard config generation for Private Internet Access.</strong>
</p>

<p align="center">
  Built for Docker, Gluetun, routers, firewalls, homelabs, headless servers and long-running VPN gateways.
</p>

<p align="center">
  <a href="https://github.com/thecodingrasta/pia-wg-config-generator/actions"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/thecodingrasta/pia-wg-config-generator/ci.yml?branch=main&label=CI"></a>
  <a href="https://github.com/thecodingrasta/pia-wg-config-generator/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/thecodingrasta/pia-wg-config-generator?sort=semver"></a>
  <a href="https://github.com/thecodingrasta/pia-wg-config-generator/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/github/license/thecodingrasta/pia-wg-config-generator"></a>
  <a href="https://github.com/thecodingrasta/pia-wg-config-generator/issues"><img alt="Issues" src="https://img.shields.io/github/issues/thecodingrasta/pia-wg-config-generator"></a>
  <a href="https://github.com/thecodingrasta/pia-wg-config-generator/stargazers"><img alt="GitHub stars" src="https://img.shields.io/github/stars/thecodingrasta/pia-wg-config-generator?style=social"></a>
</p>

---

## What This Does

Private Internet Access (PIA) doesn't provide permanent, static WireGuard configuration files through its user portal.

Instead, WireGuard connections are created through an authenticated API flow that dynamically provisions the session details needed to build a valid WireGuard configuration.

This tool automates that flow.

It can generate a standard `wg0.conf`, refresh it over time, maintain PIA port-forwarding leases, restart Gluetun when required, and write integration-friendly output files for downstream services.

In short:

> **PIA WireGuard, made practical for infrastructure-style setups.**

---

## Why This Exists

PIA WireGuard is not really a “download once and forget” workflow.

To establish a tunnel, a client needs to:

* authenticate with PIA,
* register a WireGuard public key,
* retrieve dynamic connection metadata,
* select a valid server and region,
* generate a WireGuard config,
* optionally bind a forwarded port,
* optionally renew that forwarded port over time.

That is awkward for:

* Docker stacks,
* Gluetun containers,
* Unraid boxes,
* OpenWrt routers,
* MikroTik routers,
* pfSense / OPNsense firewalls,
* headless Linux servers,
* long-running VPN gateway containers,
* P2P setups that depend on port forwarding.

`pia-wg-config-generator` bridges that gap by turning PIA WireGuard into something closer to a repeatable, refreshable and automation-friendly workflow.

---

## Features

* Generate standard WireGuard configs once and exit.
* Run as a long-running daemon for automatic refresh.
* Renew rolling PIA port-forwarding leases.
* Write the active forwarded port to a file for downstream apps.
* Restart a Docker container after config refresh, useful for Gluetun.
* Run hooks after config refresh or forwarded-port changes.
* Select regions by ID, friendly name, or server common name.
* Filter to port-forwarding-capable regions.
* Support IPv6 dual-stack, IPv4-only, or IPv6 killswitch modes.
* Cache PIA tokens to avoid unnecessary authentication calls.
* Output to stdout or file.
* Work with Docker, Gluetun, routers, firewalls and headless systems.
* Ship as a single Go binary.

> [!IMPORTANT]
> PIA WireGuard sessions and port-forwarding leases can be ephemeral.
>
> This project is designed around refresh, automation and long-running infrastructure use cases.

---

## Common Use Cases

| Use case                                 | Supported |
| ---------------------------------------- | --------: |
| Generate a one-off `wg0.conf`            |       Yes |
| Use with `wg-quick`                      |       Yes |
| Use with Gluetun custom WireGuard mode   |       Yes |
| Run as a refresh daemon                  |       Yes |
| Maintain PIA port forwarding             |       Yes |
| Export active forwarded port to file     |       Yes |
| Restart Gluetun after config refresh     |       Yes |
| Use on headless Linux servers            |       Yes |
| Import config into routers/firewalls     |       Yes |
| Use without the official PIA desktop app |       Yes |

---

## Quick Start

### Install With Go

```bash
go install github.com/thecodingrasta/pia-wg-config-generator@latest
```

Make sure your Go bin directory is available on your `PATH`.

For example:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Build From Source

```bash
git clone https://github.com/thecodingrasta/pia-wg-config-generator.git
cd pia-wg-config-generator
make build
```

### Generate a WireGuard Config

```bash
pia-wg-config generate \
  --username "YOUR_PIA_USERNAME" \
  --password "YOUR_PIA_PASSWORD" \
  --region "uk_southampton" \
  --outfile wg0.conf
```

Or using environment variables:

```bash
PIA_USERNAME="YOUR_PIA_USERNAME" \
PIA_PASSWORD="YOUR_PIA_PASSWORD" \
pia-wg-config generate --region "uk_southampton" --outfile wg0.conf
```

The generated `wg0.conf` can be used with:

* `wg-quick`,
* WireGuard desktop/mobile clients,
* routers and firewalls that support WireGuard,
* Docker VPN gateways,
* Gluetun custom WireGuard mode.

---

## Example: `wg-quick`

```bash
sudo cp wg0.conf /etc/wireguard/wg0.conf
sudo wg-quick up wg0
```

To stop the tunnel:

```bash
sudo wg-quick down wg0
```

---

## Commands

### `generate`

Generate a WireGuard config once and exit.

```bash
pia-wg-config generate [options]
```

| Flag                      | Environment variable | Description                                     |
| ------------------------- | -------------------- | ----------------------------------------------- |
| `--username`, `-u`        | `PIA_USERNAME`       | PIA account username                            |
| `--password`, `-w`        | `PIA_PASSWORD`       | PIA account password                            |
| `--region`, `-r`          |                      | Region ID, friendly name, or server common name |
| `--outfile`, `-o`         |                      | File to write the config to. Defaults to stdout |
| `--port-forwarding`, `-p` |                      | Restrict to port-forwarding-capable servers     |
| `--ipv6-mode`             |                      | `on`, `off`, or `kill`                          |
| `--server`, `-s`          |                      | Embed server identity as a comment              |
| `--verbose`, `-v`         |                      | Enable verbose logging                          |

Example:

```bash
pia-wg-config generate \
  --region ca_toronto \
  --port-forwarding \
  --ipv6-mode kill \
  --outfile wg0.conf
```

---

### `regions`

List available PIA regions.

```bash
pia-wg-config regions [options]
```

| Flag               | Description                               |
| ------------------ | ----------------------------------------- |
| `--pf-only`        | Only show port-forwarding-capable regions |
| `--json`           | Output regions as JSON                    |
| `--username`, `-u` | PIA username                              |
| `--password`, `-w` | PIA password                              |

Examples:

```bash
pia-wg-config regions
```

```bash
pia-wg-config regions --pf-only
```

```bash
pia-wg-config regions --pf-only --json
```

---

### `daemon`

Run continuously and refresh the WireGuard config/session over time.

```bash
pia-wg-config daemon [options]
```

| Flag                       | Environment variable |                Default | Description                                                                 |
| -------------------------- | -------------------- | ---------------------: | --------------------------------------------------------------------------- |
| `--username`, `-u`         | `PIA_USERNAME`       |                        | PIA account username                                                        |
| `--password`, `-w`         | `PIA_PASSWORD`       |                        | PIA account password                                                        |
| `--region`, `-r`           |                      |         `amsterdam404` | Region ID, friendly name, or server common name                             |
| `--state-dir`              |                      |               `/state` | Directory for generated state files                                         |
| `--refresh-interval`       |                      |                  `12h` | How often to regenerate the config                                          |
| `--refresh-jitter`         |                      |                  `30m` | Random jitter added to refresh timing                                       |
| `--retry-delay`            |                      |                   `5m` | How long to wait before retrying after a failed refresh                     |
| `--port-forwarding`, `-p`  |                      |                `false` | Acquire and renew a port-forwarding lease                                   |
| `--wait-for-gateway`       |                      |                 `true` | Wait for the PIA port-forwarding gateway before requesting a forwarded port |
| `--gateway-timeout`        |                      |                   `2m` | Maximum time to wait for the PIA gateway after a config refresh             |
| `--gateway-check-interval` |                      |                   `5s` | How often to check the PIA gateway while waiting                            |
| `--restart-container`      |                      |                        | Docker container to restart after each config refresh                       |
| `--docker-socket`          |                      | `/var/run/docker.sock` | Docker socket used by `--restart-container`                                 |
| `--on-config-change`       |                      |                        | Shell command run after each successful config refresh                      |
| `--on-port-change`         |                      |                        | Shell command run only when the forwarded port changes                      |
| `--ipv6-mode`              |                      |                   `on` | `on`, `off`, or `kill`                                                      |
| `--server`, `-s`           |                      |                `false` | Embed server identity as a comment                                          |
| `--verbose`, `-v`          |                      |                `false` | Enable verbose logging                                                      |

Daemon output files are written atomically to `--state-dir`.

| File                        | Description                                            |
| --------------------------- | ------------------------------------------------------ |
| `wg0.conf`                  | Generated WireGuard configuration                      |
| `status.json`               | Daemon status snapshot                                 |
| `forwarded_port`            | Current forwarded port when port forwarding is enabled |
| `pending_port_forward.json` | Temporary metadata used across a Gluetun restart       |

---

## IPv6 Modes

| Mode   | Description                                                                         |
| ------ | ----------------------------------------------------------------------------------- |
| `on`   | Full dual-stack tunnel using IPv4 and IPv6                                          |
| `off`  | IPv4-only tunnel. IPv6 is not routed through WireGuard                              |
| `kill` | Dual-stack tunnel with an `ip6tables` rule to block IPv6 traffic outside the tunnel |

`kill` mode adds the following rules to the generated config:

```ini
PostUp = ip6tables -I OUTPUT ! -o %i -j REJECT
PostDown = ip6tables -D OUTPUT ! -o %i -j REJECT
```

> [!NOTE]
> `kill` mode requires `ip6tables` support on the host/container where the WireGuard config is applied.

---

## Docker / Gluetun

This is the recommended long-running setup for containerised VPN gateways.

The flow is:

1. Build or pull the `pia-wg-config` image.
2. Seed the initial Gluetun WireGuard config once.
3. Start Gluetun using `VPN_SERVICE_PROVIDER=custom`.
4. Start the daemon inside Gluetun’s network namespace.
5. Let the daemon refresh `wg0.conf`, restart Gluetun, then re-enter the new network namespace.
6. Renew and write the active forwarded port.

### Build the Image Locally

```bash
docker build -t pia-wg-config-generator:local .
```

### Seed the Initial Config

On a fresh install, generate the first config before starting Compose:

```bash
docker run --rm \
  --env-file .env \
  -v ./config/gluetun:/gluetun \
  pia-wg-config-generator:local \
  generate \
    --region="${PIA_REGION}" \
    --port-forwarding \
    --ipv6-mode="${IPV6_MODE}" \
    --outfile=/gluetun/wireguard/wg0.conf \
    --verbose
```

After `./config/gluetun/wireguard/wg0.conf` exists, Compose can start normally.

### Compose Example

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
    command: >
      daemon
        --region=${PIA_REGION}
        --state-dir=/gluetun/wireguard
        --port-forwarding
        --refresh-interval=12h
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

Example `.env`:

```ini
PIA_USERNAME=your_pia_username
PIA_PASSWORD=your_pia_password
PIA_REGION=ca_toronto
IPV6_MODE=kill
```

See [`docs/gluetun.md`](docs/gluetun.md) for the full Gluetun guide and troubleshooting notes.

> [!WARNING]
> Mounting the Docker socket gives the daemon access to Docker container control.
>
> Only use `--restart-container` in environments where you trust this container and understand the Docker socket security implications.

---

## Routers and Firewalls

The generated `wg0.conf` is a standard WireGuard configuration.

It should be portable to WireGuard-compatible systems, including:

* OpenWrt,
* pfSense,
* OPNsense,
* MikroTik RouterOS,
* ASUS Merlin,
* Unraid,
* other WireGuard-compatible routers and firewalls.

The config includes the usual WireGuard fields:

```ini
[Interface]
PrivateKey = ...
Address = ...
DNS = ...

[Peer]
PublicKey = ...
AllowedIPs = ...
Endpoint = ...
PersistentKeepalive = ...
```

Router-specific notes live in [`system-tests/routers/README.md`](system-tests/routers/README.md).

> [!TIP]
> If you successfully test a platform, please open an issue or discussion with your setup notes so support can be documented properly.

---

## Windows Notes

On Windows, PIA’s token endpoint may reject requests made through the system `curl.exe` because of its Schannel TLS behaviour.

If that happens, point the tool at an OpenSSL-linked curl binary:

```powershell
$env:CURL_PATH = "C:\tools\curl-openssl\curl.exe"
pia-wg-config generate -o wg0.conf
```

The tool can fall back to the metadata server if curl is unavailable, but setting `CURL_PATH` may improve reliability and token caching.

---

## Output Files

When running in daemon mode, these files may be created in `--state-dir`.

| File                        | Purpose                                             |
| --------------------------- | --------------------------------------------------- |
| `wg0.conf`                  | WireGuard configuration                             |
| `status.json`               | Machine-readable daemon status                      |
| `forwarded_port`            | Current active forwarded port                       |
| `pending_port_forward.json` | Temporary state used during Gluetun restart handoff |

Example `forwarded_port` usage:

```bash
cat ./config/gluetun/wireguard/forwarded_port
```

This is useful for downstream services that need to consume the active PIA forwarded port.

---

## Security Notes

This tool needs access to your PIA credentials or a valid token in order to generate and refresh WireGuard configs.

Recommended practice:

* Prefer environment variables over command-line credentials.
* Avoid committing `.env` files.
* Restrict permissions on state directories.
* Treat generated WireGuard configs as secrets.
* Treat forwarded-port state as sensitive infrastructure metadata.
* Be careful when mounting the Docker socket.
* Use least privilege wherever possible.

> [!CAUTION]
> Anyone with access to a generated WireGuard config may be able to use that VPN tunnel until the session expires or is invalidated.

For vulnerability reports, see [`SECURITY.md`](SECURITY.md).

---

## Testing

Unit tests are offline and use vendored dependencies.

```bash
make test
make test-race
make lint
```

Integration tests require real PIA credentials:

```bash
PIA_USERNAME=whoDis PIA_PASSWORD=superDuperSecretPassword
```

Live system tests require Docker and real PIA credentials:

```bash
PIA_USERNAME=you \
PIA_PASSWORD=secret \
docker compose \
  -f system-tests/gluetun/docker-compose.yml \
  --env-file system-tests/gluetun/.env \
  up --build --abort-on-container-exit --exit-code-from tester
```

CI runs on GitHub Actions and covers:

* unit tests on Linux, macOS and Windows,
* Linux race detector,
* binary build artefacts,
* integration tests, which skip safely when credentials are not present,
* Docker smoke tests for the Gluetun, Gluetun port-forwarding and WireGuard-client test harnesses without real PIA credentials.

Live PIA Docker tests are intended for local/manual validation because public CI must not depend on private VPN account credentials.

---

## Architecture

High-level project structure:

```text
main.go               CLI entry point
pia/pia.go            PIA API client
pia/wg.go             WireGuard config generation
pia/pf.go             Port-forwarding client
pia/token_fetcher.go  curl-based token acquisition
pia/token_cacher.go   Disk token cache
pia/helpers.go        Shared helpers/utilities
```

### Token Acquisition

Token acquisition follows this order:

1. Use a cached token when available.
2. Try curl against PIA’s token endpoint.
3. Fall back to the metadata server.

### Port-Forwarding Flow

When daemon mode and port forwarding are enabled:

1. Authenticate with PIA.
2. Generate and write a refreshed WireGuard config.
3. Restart Gluetun when `--restart-container` is configured.
4. Persist pending port-forwarding metadata.
5. Exit so Docker restarts the daemon into Gluetun’s new network namespace.
6. Wait for the PIA gateway to become reachable through the active tunnel.
7. Request a port-forwarding signature.
8. Bind the forwarded port.
9. Renew the lease periodically.
10. Write the active port to `forwarded_port`.
11. Run `--on-port-change` when the port changes.

---

## Roadmap

The v1 goal is to stay infrastructure-friendly without overcomplicating the project.

Planned or potential improvements:

* Prebuilt release binaries for more platforms.
* Published container image via GHCR.
* More router-specific guides.
* More Gluetun examples.
* Unraid-focused guide.
* OpenWrt-focused guide.
* MikroTik RouterOS-focused guide.
* Better status/health output.
* Optional latency-based region selection.
* More examples for downstream forwarded-port consumers.
* Improved system tests around Gluetun and port forwarding.

See [Issues](../../issues) and [Discussions](../../discussions) for current work and feature ideas.

---

## Feedback Wanted

Real-world testing is extremely valuable.

Please open an issue or discussion if:

* a region fails,
* a generated config does not connect,
* port forwarding behaves unexpectedly,
* your router needs slightly different output,
* Gluetun restart/refresh behaviour does not work for your setup,
* Windows token fetching behaves weirdly,
* docs are unclear,
* you want support for a specific platform.

Useful feedback includes:

* operating system,
* router/firewall platform,
* Docker/Gluetun version,
* selected PIA region,
* whether port forwarding was enabled,
* logs with secrets removed,
* generated config shape with keys removed,
* what you expected,
* what happened instead.

---

## Contributing

Contributions are both welcomed and greatly appreciated.

Good first contribution areas:

* documentation improvements,
* platform setup guides,
* router/firewall notes,
* Docker/Gluetun examples,
* test coverage,
* region compatibility reports,
* Windows reliability notes,
* bug reports with clean reproduction steps.

Before opening a pull request, please be sure to read [`CONTRIBUTING.md`](CONTRIBUTING.md) first.

If you're not sure whether something belongs in the project, open a discussion first.

---

## Background and Credits

This project is based on the WireGuard workflow documented by Private Internet Access in their open-source [`pia-foss/manual-connections`](https://github.com/pia-foss/manual-connections) repository.

The initial Go implementation was inspired by [`kylegrantlucas/pia-wg-config`](https://github.com/kylegrantlucas/pia-wg-config), with additional improvements informed by [`Ephemeral-Dust/pia-wg-config`](https://github.com/Ephemeral-Dust/pia-wg-config).

This is also my first Go project, so it's been both a practical infrastructure tool and a solid learning experience.

---

## Disclaimer

> [!WARNING]
> This project is not affiliated with, sponsored by, or endorsed by Private Internet Access.
>
> Private Internet Access, PIA and related marks belong to their respective owners.
> 
> Use this project at your own risk. Always review generated configs before using them in production or exposing dependent services to the internet.
>
