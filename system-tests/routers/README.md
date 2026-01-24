# Router validation

While routers differ, they all consume the same core WireGuard fields.

## What this project guarantees
- `wg0.conf` contains:
  - Interface: PrivateKey, Address, DNS
  - Peer: PublicKey, AllowedIPs, Endpoint (IP:Port), PersistentKeepalive

## What you must verify on your router
- Most importantly whether it even supports WireGuard and full-tunnel routing.
- DNS behavior: use the provided DNS or your router DNS policy.
- NAT and/or firewall rules for LAN clients routed through the tunnel.

## Port forwarding (PIA)
If enabled, this CLI tool writes to `forwarded_port`.
How you then apply that port on your router depends on the manufacturer and software version. 
The forwarded port should be used as the inbound listening port for any services (such as qBittorrent) running behind the tunnel.

## Quick manual checks
- Ensure the endpoint field uses the API-provided ServerIP:ServerPort
- AllowedIPs is set to `0.0.0.0/0`
- Keepalive is set (25 seconds) to maintain NAT mapping