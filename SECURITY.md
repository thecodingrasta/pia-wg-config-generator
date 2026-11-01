# Security Policy

## Supported Versions

Security updates are provided for the latest released version of `pia-wg-config-generator`.

| Version                 |   Supported |
| ----------------------- | ----------: |
| Latest release          |         Yes |
| Older releases          | Best effort |
| Unreleased local builds |          No |

## Reporting a Vulnerability

Please do **not** report security vulnerabilities through public GitHub issues, discussions, pull requests, or social media.

If you believe you have found a security issue, please report it privately using GitHub’s private vulnerability reporting feature:

## What To Include

Useful reports should include:

* affected version or commit,
* operating system and architecture,
* command or mode used,
* whether Docker/Gluetun/daemon mode was involved,
* impact description,
* reproduction steps,
* logs with secrets removed,
* suggested fix, if known.

Please ensure to remove all sensitive information before sharing logs or configs.

Sensitive information includes:

* PIA username,
* PIA password,
* PIA tokens,
* WireGuard private keys,
* generated `wg0.conf` private keys,
* Docker secrets,
* `.env` contents,
* public IP addresses, if you consider them sensitive.

## Expected Response

Security reports will be reviewed as soon as reasonably possible.

The expected process is:

1. Confirm receipt.
2. Reproduce and assess impact.
3. Prepare a fix.
4. Release a patched version.
5. Publish advisory details where appropriate.

Please allow time for a fix before publicly disclosing the issue.

## Security Scope

Security-sensitive areas include:

* PIA credential handling,
* token caching,
* generated WireGuard config output,
* daemon state files,
* port-forwarding metadata,
* shell hook execution,
* Docker socket access,
* Gluetun restart integration,
* file permissions,
* logs that may expose secrets.

## Docker Socket Warning

When using `--restart-container`, the daemon may require access to the Docker socket.

Mounting the Docker socket into a container is powerful and should be treated as sensitive. A process with Docker socket access may be able to control containers on the host.

Only use this feature in trusted environments.

## Secrets and Generated Files

Generated WireGuard configs should be treated as secrets.

Anyone with access to a generated config may be able to use the VPN tunnel until the session expires or is otherwise invalidated.

Recommended practice:

* do not commit generated configs,
* do not commit `.env` files,
* restrict state directory permissions,
* avoid passing credentials directly on the command line,
* prefer environment variables or secret managers,
* review logs before sharing them publicly.

## Out of Scope

The following are generally out of scope unless they demonstrate a direct project vulnerability:

* issues in PIA infrastructure,
* issues in WireGuard itself,
* issues in Docker, Gluetun, Go, curl, or operating system dependencies,
* leaked credentials caused by user misconfiguration,
* attacks requiring full local administrative access,
* unsupported forks or modified local builds.

## Responsible Disclosure

Please act in good faith.

Do not access, modify, exfiltrate, or disrupt systems that you do not own or have explicit permission to test.

Do not publicly disclose exploit details before a fix is available.
