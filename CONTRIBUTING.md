# Contributing

Thanks for taking the time to contribute. The bar is straightforward: working code, passing tests, clear intent.

---

## Getting Started

```bash
git clone https://github.com/thecodingrasta/pia-wg-config-generator
cd pia-wg-config-generator
make build   # sanity check
make test    # all unit tests should pass with no credentials
```

You need Go 1.26 or newer (`go.mod` is the source of truth). All dependencies are vendored under `vendor/`, so you do not need internet access to build or run unit tests.

> [!NOTE]
> Do not lower the `go` directive in `go.mod` below 1.24. Older Go linkers do not emit `LC_UUID`, and macOS 15 and later refuse to run the resulting test binaries (`dyld: missing LC_UUID load command`). See [golang/go#68678](https://github.com/golang/go/issues/68678).

---

## Project Layout

```
main.go                   CLI entry point (generate, regions, daemon, refresh, pf, healthcheck)
refresh.go                refresh daemon - self-recovering (debounced reactive recovery, lifetime-aware cadence, startup adoption, single-flight)
pf_daemon.go              pf daemon - port-forward lease inside the VPN namespace
alert.go                  Once-per-outage alert policy and the non-blocking hook dispatcher
healthcheck.go            `healthcheck` subcommand - container health from status.json (never restarts anything)
scripts/pia-wg-alert      POSIX sh alert hook (Discord, ntfy, generic JSON), shipped in the image
docs/examples/            Four-container compose file, .env template, restart-only Docker proxy template
pia/
  pia.go                  PIAClient - API auth, server selection, AddKey
  wg.go                   WireGuard config generation (text/template)
  pf.go                   Port-forwarding client (GetSignature, BindPort)
  health.go               Tunnel-health detection (gluetun status AND public IP; handshake age)
  lifetime.go             Registration-lifetime measurement (drives the cadence)
  redact.go               Secret redaction for logs, status.json and alert messages
  token_fetcher.go        curl-based token acquisition (WAF bypass)
  token_cacher.go         Disk token cache
  helpers.go              Shared utilities
  *_test.go               Unit tests (offline, mocked)
  integration_test.go     Integration tests (build tag: integration)
acceptance_test.go        Recovery-loop acceptance tests (mock control server + fake generator)
system-tests/
  gluetun/                Docker Compose - Gluetun end-to-end test (legacy single daemon, needs PIA credentials)
  wireguard-client/       Docker Compose - direct WireGuard client test
  common/tester/          Shared tester container and verification script
  stack-smoke.sh          Four-container stack smoke test (no PIA credentials; runs in CI)
  ci-smoke.sh             Credential-free fixture smoke test for the gluetun and wireguard-client harnesses
  desktop/                Manual desktop verification scripts
  routers/                Router notes
docs/
  gluetun.md              Docker + Gluetun setup guide
version.go                Build-time version stamping (--version)
Dockerfile                Multi-stage, multi-arch build for the container image
Makefile                  All build/test targets
.golangci.yml             golangci-lint configuration
.github/
  workflows/              ci (validation), release (drafts a release), publish-image (GHCR), CodeQL, scheduled govulncheck, dependency review, Scorecard
  dependabot.yml          Automated dependency updates (Go modules, Actions, Docker)
  release.yml             Release-note categories
.coderabbit.yaml          CodeRabbit review configuration
```

---

## Tests

### Unit tests (no credentials needed)

```bash
make test          # go test ./...
make test-race     # with race detector (requires gcc)
make lint          # go vet, plus golangci-lint when it is installed
make vuln          # govulncheck: vulnerabilities reachable from this code
make cover         # tests with a coverage total
```

CI runs all of the above, so running them before you push avoids a red build. Install [golangci-lint v2](https://golangci-lint.run/welcome/install/) locally to match CI.

### Integration tests (real PIA API)

```bash
PIA_USERNAME=you PIA_PASSWORD=secret make test-integration
# Optional env:
#   PIA_REGION=uk_southampton   (default)
#   PIA_PF=1                    (test port-forwarding flow)
```

Integration tests use the `//go:build integration` tag and are excluded from `make test`. They skip gracefully when credentials are absent, so they are safe to include in CI with secrets gating.

### System tests (Docker)

Without PIA credentials (this is what CI runs; Docker required):

```bash
bash system-tests/stack-smoke.sh              # the four-container stack: compose file, restart-only proxy, image
bash system-tests/ci-smoke.sh gluetun        # Gluetun and WireGuard-client harnesses against a fixture config
```

`stack-smoke.sh` only creates containers named `pia-wg-smoke-*` and removes them on exit, so it is safe to run next to a real stack. Change `docs/examples/` or the proxy template and run it before you push.

With real PIA credentials (manual only; these exercise the legacy single `daemon`):

```bash
# Copy and edit the env file
cp system-tests/gluetun/.env.example system-tests/gluetun/.env

docker compose -f system-tests/gluetun/docker-compose.yml \
  --env-file system-tests/gluetun/.env \
  up --build --abort-on-container-exit --exit-code-from tester
```

### Checking workflow changes

Lint workflow edits before you push; a workflow that fails on its first run is expensive to debug:

```bash
go run github.com/rhysd/actionlint/cmd/actionlint@latest   # picks up ShellCheck if it is installed
```

Third-party actions are pinned to full commit SHAs with the version in a trailing comment. Verify a new pin resolves to its tag with `git ls-remote https://github.com/<owner>/<repo>.git refs/tags/<tag>` (an annotated tag lists the commit under `<tag>^{}`).

---

## Adding a New Test

**Unit tests** live in `pia/*_test.go` and must be offline. Use `httptest.NewTLSServer` or mock structs to avoid real API calls. See `pia/pf_test.go` for the pattern.

**Integration tests** belong in `pia/integration_test.go` under the `//go:build integration` tag. Gate each test on `credsOrSkip(t)`. Keep API calls minimal - the existing suite is designed around one `NewPIAClient` + one `GenerateWithMetadata` per test.

---

## Dependency Management

Dependencies are vendored. If you add or update a dependency:

```bash
go get github.com/some/dep@vX.Y.Z
go mod tidy
go mod vendor
```

Commit the updated `go.mod`, `go.sum`, and `vendor/` together.

---

## Code Style

- Standard `gofmt` formatting; CI runs `go vet` and `golangci-lint` (see `.golangci.yml`)
- Errors are wrapped with `github.com/pkg/errors` (`errors.Wrap`) where context is added; use stdlib `fmt.Errorf("...: %w", err)` for simple wrapping
- Keep `main.go` as orchestration only - business logic belongs in `pia/`
- Any shared state accessed by more than one goroutine must be mutex-protected (see `pfLeaseState` in `main.go` and the single-flight / recovery-state locks in `refresh.go`)

---

## Pull Requests

1. Fork, then branch from `main`
2. Make your change with a matching test
3. `make test && make lint` pass locally
4. Open a PR with a clear description of what changed and why (the PR template lists what reviewers check)

CI must pass before merge. The single required status check is **CI gate**, which aggregates every job. Dependency updates arrive as Dependabot PRs; review `vendor/` changes the same way as any other change.

For larger changes (new commands, architectural shifts), open an issue first to discuss the approach.

---

## Releasing

Maintainers only. A release is **drafted** by a workflow and **published** by a person; nothing reaches users until you press Publish.

1. Merge everything for the release into `main` and wait for **CI gate** to pass. Update `CHANGELOG.md` in the same change.
2. Tag the merge commit with a strict semantic version and push the tag:

   ```bash
   git tag -a v1.2.0 -m "v1.2.0"
   git push origin v1.2.0
   ```

   Tags must look like `v1.2.3` or `v1.2.3-rc.1`. A tag with a hyphen is treated as a pre-release. Pushing a tag does not start a release by itself.
3. Open **Actions > Draft release > Run workflow**. Pick the tag under "Use workflow from" (or `main`, if it is still at the tagged commit) and enter the same version. For a first run of a new pipeline, rehearse with a pre-release tag such as `v1.2.0-rc.1`.
4. The workflow then:
   - runs the complete CI suite as its gate (tests on every platform, lint, `govulncheck`, module hygiene, cross-compilation, the Docker smoke tests),
   - verifies the tag exists, points at the commit being built and is reachable from `main`,
   - builds static binaries stamped with the tag, the commit and its date, and writes `SHA256SUMS`,
   - runs each binary on a native runner of its own platform (Linux amd64, Windows amd64, Windows arm64), after re-checking its checksum,
   - attaches signed build-provenance attestations and creates a **draft** release with generated notes and exactly these assets: `pia-wg-config` (Linux amd64), `pia-wg-config.exe` (Windows amd64), `pia-wg-config-arm64.exe` (Windows arm64) and `SHA256SUMS`.
5. Review the draft (notes, assets), then press **Publish**. Publishing triggers **Publish container image**, which pushes a multi-architecture image to `ghcr.io/thecodingrasta/pia-wg-config-generator` with provenance and an SBOM, then pulls it back by digest and checks it reports the right version. A pre-release publishes only its exact version tag; a final release also moves `{major}.{minor}`, `{major}` and `latest`.

If the image push fails, run **Publish container image** manually with the same tag; you do not need to republish the release. If a draft was created but is wrong, delete the draft in the UI and run **Draft release** again; the workflow refuses to overwrite an existing release or draft.

The first image push creates the package on GHCR. Check once, under the repository's Packages, that it is public and linked to this repository.

Pull requests are grouped into release-note sections by label (`breaking`, `enhancement`, `bug`, `security`, `documentation`, `dependencies`); see `.github/release.yml`.
