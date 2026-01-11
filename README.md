# PIA-WgConfigGenerator

**A WireGuard config generator and sidecar daemon for Private Internet Access (PIA), designed for Docker, Routers and long-running tunnels.**

Private Internet Access doesn't provide static WireGuard configuration files through its user portal.  
Instead, WireGuard connections are established through an authenticated API flow that dynamically provisions connection parameters.

This project bridges that gap by generating WireGuard configurations using PIA’s official APIs and by providing tooling designed for long-running, automated setups such as Docker, Gluetun, Routers and headless systems.

---

## Why This Exists

PIA’s WireGuard implementation requires:
- authenticating with the PIA API,
- registering a client-generated WireGuard public key,
- dynamically retrieving server, gateway, and DNS information,
- and (where supported) maintaining rolling port-forwarding leases.

This makes static, “download once and forget” configurations impractical.

This project exists to:
- automate that process,
- make WireGuard usable in infrastructure and homelab environments,
- and enable a **start-it-and-forget-it** workflow where possible.

---

## Features

- Generate WireGuard configurations for PIA using official server metadata
- Select regions by name or ID
- Restrict selection to port-forwarding capable regions
- Optional inclusion of server identity metadata
- Designed for Docker, Gluetun, Routers and headless systems
- Written in Go as a single self-contained binary

> ⚠️ **Important** ⚠️
>
> PIA WireGuard sessions and port-forwarding leases may be ephemeral.  
> This project is designed with refresh and automation in mind and is evolving toward a daemon-based model.

---

## Usage

Install the binary:

```bash
go install github.com/thecodingrasta/PIA-WgConfigGenerator@latest
```

Generate a WireGuard config:

```bash
piawg generate -o wg0.conf USERNAME PASSWORD
```

The resulting wg0.conf can be used with WireGuard directly or mounted into a VPN gateway such as Gluetun.

---

## Intended Use Cases

- Docker stacks using a VPN gateway container
- Gluetun-based WireGuard setups
- Seedboxes and torrent clients requiring port forwarding
- Routers and firewalls with WireGuard support
- Headless or server environments replacing OpenVPN with WireGuard

---

## Project Direction

In addition to configuration generation, this project aims to support:

- optional daemon / sidecar mode
- automatic refresh of WireGuard sessions
- maintenance of rolling PIA port-forwarding leases
- clean integration with containerised workflows
- external hooks for updating dependent services

These features are being designed to be opt-in and to degrade gracefully if upstream tooling (e.g. Gluetun) later provides native support.

---

## Background & Credits

This project is based on the WireGuard workflow documented in the open-source [manual-connections repository](https://github.com/pia-foss/manual-connections) published by Private Internet Access.

The initial Go implementation was inspired by and derived from [kylegrantlucas/pia-wg-config](https://github.com/kylegrantlucas/pia-wg-config),
with additional functionality and improvements informed by [Ephemeral-Dust/pia-wg-config](https://github.com/Ephemeral-Dust/pia-wg-config).

**⚠️ This project is not affiliated with or endorsed by Private Internet Access in any way! ⚠️**
