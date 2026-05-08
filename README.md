# PIA-Wg-Config-Generator

**A WireGuard config generator and sidecar daemon for Private Internet Access (PIA), built for Docker, Gluetun, routers and long-running tunnels.**

Private Internet Access does not provide static WireGuard configuration files through its user portal.

Instead, WireGuard connections are created through an authenticated API flow that dynamically provisions the required connection details, including keys, server assignment, gateway and DNS information.

This project automates that flow and makes PIA WireGuard practical for infrastructure-style setups such as Docker stacks, Gluetun sidecars, routers, firewalls and headless servers.

---

## Why This Exists

PIA’s WireGuard implementation is not really a “download once and forget” setup.

To establish a tunnel, a client needs to:

- authenticate with PIA,
- register a WireGuard public key,
- retrieve dynamic server and connection metadata,
- generate a valid WireGuard config,
- and optionally maintain a rolling port-forwarding lease.

That makes static configs awkward, especially for homelabs, containers and long-running VPN gateways.

This tool bridges that gap by providing:

- one-shot WireGuard config generation,
- daemon mode for long-running setups,
- automatic refresh support,
- rolling port-forwarding renewal,
- and integration-friendly output files for Docker/Gluetun style workflows.

In short, it turns PIA WireGuard into something much closer to a **start-it-and-forget-it** setup.

---

## Features

- One-shot WireGuard config generation to file or stdout
- Daemon mode with automatic config/session refresh
- Rolling PIA port-forwarding lease renewal
- Hook support when the forwarded port changes
- Region selection by ID or friendly name
- Optional filtering to port-forwarding capable regions
- IPv6 modes: dual-stack, IPv4-only, or dual-stack with an ip6tables killswitch
- Token caching to avoid unnecessary authentication calls
- Works well with Docker, Gluetun, routers and headless systems
- Single self-contained Go binary

> [!IMPORTANT]
> PIA WireGuard sessions and port-forwarding leases may be ephemeral.
>
> This project is designed around refresh, automation and long-running infrastructure use cases.

---

## Quick Start

### Install

```bash
go install github.com/thecodingrasta/pia-wg-config-generator@latest
```

### Or build from source

```bash
git clone https://github.com/thecodingrasta/pia-wg-config-generator
cd pia-wg-config-generator
make build
```

### Generate a WireGuard config

```bash
pia-wg-config generate --username YOU --password SECRET --region uk_southampton -o wg0.conf
```

Or using environment variables:

```bash
PIA_USERNAME=you PIA_PASSWORD=secret pia-wg-config generate -o wg0.conf
```

The generated `wg0.conf` can be used with `wg-quick`, imported into WireGuard clients, loaded onto a router, or mounted into a VPN gateway container such as [Gluetun](https://github.com/qdm12/gluetun).

---

## Commands

### `generate`

Generate a WireGuard config once and exit.

```bash
pia-wg-config generate [options]
```

Common options:

| Flag | Env | Description |
|------|-----|-------------|
| `--username`, `-u` | `PIA_USERNAME` | PIA account username |
| `--password`, `-w` | `PIA_PASSWORD` | PIA account password |
| `--region`, `-r` | | Region ID or friendly name |
| `--outfile`, `-o` | | File to write the config to. Defaults to stdout |
| `--port-forwarding`, `-p` | | Restrict to port-forwarding capable servers |
| `--ipv6-mode` | | `on`, `off`, or `kill` |
| `--server`, `-s` | | Embed server identity as a comment |
| `--verbose`, `-v` | | Enable verbose logging |

---

### `regions`

List available PIA regions.

```bash
pia-wg-config regions [--pf-only] [--json]
```

| Flag | Description |
|------|-------------|
| `--pf-only` | Only show port-forwarding capable regions |
| `--json` | Output regions as JSON |
| `--username`, `-u` | PIA username |
| `--password`, `-w` | PIA password |

---

### `daemon`

Run continuously and refresh the WireGuard config/session over time.

```bash
pia-wg-config daemon [options]
```

Common options:

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--username`, `-u` | `PIA_USERNAME` | | PIA account username |
| `--password`, `-w` | `PIA_PASSWORD` | | PIA account password |
| `--region`, `-r` | | `amsterdam404` | Region ID or friendly name |
| `--state-dir` | | `/state` | Directory for generated state files |
| `--refresh-interval` | | `12h` | How often to regenerate the config |
| `--refresh-jitter` | | `30m` | Random jitter added to refresh timing |
| `--port-forwarding`, `-p` | | `false` | Acquire and renew a port-forwarding lease |
| `--on-port-change` | | | Shell command run when the port changes |
| `--ipv6-mode` | | `on` | `on`, `off`, or `kill` |
| `--server`, `-s` | | `false` | Embed server identity as a comment |
| `--verbose`, `-v` | | `false` | Enable verbose logging |

Daemon output files are written atomically to `--state-dir`:

| File | Description |
|------|-------------|
| `wg0.conf` | Generated WireGuard configuration |
| `status.json` | Daemon status snapshot |
| `forwarded_port` | Current forwarded port when port-forwarding is enabled |

---

## IPv6 Modes

| Mode | Description |
|------|-------------|
| `on` | Full dual-stack tunnel using IPv4 and IPv6 |
| `off` | IPv4-only tunnel. IPv6 is not routed through WireGuard |
| `kill` | Dual-stack tunnel with an ip6tables rule to block IPv6 traffic outside the tunnel |

`kill` mode adds the following rules to the generated config:

```ini
PostUp = ip6tables -I OUTPUT ! -o %i -j REJECT
PostDown = ip6tables -D OUTPUT ! -o %i -j REJECT
```

---

## Docker / Gluetun

A common setup is to run this as a sidecar alongside Gluetun.

The daemon writes `wg0.conf` and port-forwarding state to a shared Docker volume, while Gluetun consumes the generated WireGuard config.

```yaml
services:
  pia-wg-daemon:
    build: .
    environment:
      PIA_USERNAME: ${PIA_USERNAME}
      PIA_PASSWORD: ${PIA_PASSWORD}
    command: >
      daemon --region ca_toronto
             --port-forwarding
             --state-dir /state
             --refresh-interval 12h
             --on-port-change "echo Port is {port}"
    volumes:
      - state:/state

  gluetun:
    image: qmcgaw/gluetun:latest
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      VPN_SERVICE_PROVIDER: custom
      VPN_TYPE: wireguard
      WIREGUARD_CONF_FILE: /gluetun/wg0.conf
    volumes:
      - state:/gluetun:rw
    depends_on:
      - pia-wg-daemon

volumes:
  state:
```

See [`docs/gluetun.md`](docs/gluetun.md) for a fuller setup guide.

---

## Routers

The generated `wg0.conf` is a standard WireGuard configuration and should work with routers/firewalls that support WireGuard, including:

- OpenWrt
- pfSense
- OPNsense
- MikroTik RouterOS
- other WireGuard-compatible systems

The config includes the usual WireGuard fields:

- `[Interface]` — `PrivateKey`, `Address`, `DNS`
- `[Peer]` — `PublicKey`, `AllowedIPs`, `Endpoint`, `PersistentKeepalive`

Router-specific notes live in [`system-tests/routers/README.md`](system-tests/routers/README.md).

---

## Windows Notes

On Windows, PIA’s token endpoint may reject requests made through the system `curl.exe` due to its Schannel TLS fingerprint.

If that happens, point the tool at an OpenSSL-linked curl binary:

```powershell
$env:CURL_PATH = "C:\tools\curl-openssl\curl.exe"
pia-wg-config generate -o wg0.conf
```

The tool can still fall back to the metadata server if curl is unavailable, but setting `CURL_PATH` can improve reliability and token caching.

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
PIA_USERNAME=you PIA_PASSWORD=secret make test-integration
```

System tests require Docker:

```bash
PIA_USERNAME=you PIA_PASSWORD=secret \
  docker compose -f system-tests/gluetun/docker-compose.yml \
  --env-file system-tests/gluetun/.env \
  up --build --abort-on-container-exit
```

CI runs on GitHub Actions and covers:

- unit tests on Linux, macOS and Windows,
- race detector on Linux,
- binary build artefacts,
- integration/system tests when PIA secrets are configured.

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

Token acquisition strategy:

1. Use cached token when available.
2. Try curl against PIA’s token endpoint.
3. Fall back to the metadata server.

Port-forwarding flow:

1. Authenticate and retrieve a token.
2. Request a port-forwarding signature.
3. Bind the forwarded port.
4. Renew the lease periodically.
5. Write the active port to `forwarded_port`.
6. Run `--on-port-change` if the port changes.

---

## Project Direction

The goal is to keep this tool infrastructure-friendly without overcomplicating it.

Planned/improving areas include:

- cleaner sidecar workflows,
- stronger Gluetun integration patterns,
- better router examples,
- safer refresh/reload hooks,
- improved status reporting,
- and more resilient long-running daemon behaviour.

The intention is for these features to stay opt-in and degrade gracefully if upstream tooling later adds native support.

---

## Background & Credits

This project is based on the WireGuard workflow documented by Private Internet Access in their open-source [manual-connections](https://github.com/pia-foss/manual-connections) repository.

The initial Go implementation was inspired by [kylegrantlucas/pia-wg-config](https://github.com/kylegrantlucas/pia-wg-config), with additional improvements informed by [Ephemeral-Dust/pia-wg-config](https://github.com/Ephemeral-Dust/pia-wg-config).

This is also my first Go project, so it's been a solid learning experience as well as a practical tool for my own infrastructure.

** ⚠️ [!WARNING] ⚠️
> This project is not affiliated with or endorsed by Private Internet Access in any way! **
