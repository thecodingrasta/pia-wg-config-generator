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

All dependencies are vendored under `vendor/`. You do not need internet access to build or run unit tests.

---

## Project Layout

```
main.go                   CLI entry point (generate, regions, daemon)
pia/
  pia.go                  PIAClient — API auth, server selection, AddKey
  wg.go                   WireGuard config generation (text/template)
  pf.go                   Port-forwarding client (GetSignature, BindPort)
  token_fetcher.go        curl-based token acquisition (WAF bypass)
  token_cacher.go         Disk token cache
  helpers.go              Shared utilities
  *_test.go               Unit tests (offline, mocked)
  integration_test.go     Integration tests (build tag: integration)
system-tests/
  gluetun/                Docker Compose — Gluetun end-to-end test
  wireguard-client/       Docker Compose — direct WireGuard client test
  common/tester/          Shared tester container and verification script
  desktop/                Manual desktop verification scripts
  routers/                Router notes
docs/
  gluetun.md              Docker + Gluetun setup guide
Dockerfile                Multi-stage build for daemon image
Makefile                  All build/test targets
```

---

## Tests

### Unit tests (no credentials needed)

```bash
make test          # go test ./...
make test-race     # with race detector (requires gcc)
make lint          # go vet
```

### Integration tests (real PIA API)

```bash
PIA_USERNAME=you PIA_PASSWORD=secret make test-integration
# Optional env:
#   PIA_REGION=uk_southampton   (default)
#   PIA_PF=1                    (test port-forwarding flow)
```

Integration tests use the `//go:build integration` tag and are excluded from `make test`. They skip gracefully when credentials are absent, so they are safe to include in CI with secrets gating.

### System tests (Docker)

```bash
# Copy and edit the env file
cp system-tests/gluetun/.env.example system-tests/gluetun/.env

docker compose -f system-tests/gluetun/docker-compose.yml \
  --env-file system-tests/gluetun/.env \
  up --build --abort-on-container-exit --exit-code-from tester
```

---

## Adding a New Test

**Unit tests** live in `pia/*_test.go` and must be offline. Use `httptest.NewTLSServer` or mock structs to avoid real API calls. See `pia/pf_test.go` for the pattern.

**Integration tests** belong in `pia/integration_test.go` under the `//go:build integration` tag. Gate each test on `credsOrSkip(t)`. Keep API calls minimal — the existing suite is designed around one `NewPIAClient` + one `GenerateWithMetadata` per test.

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

- Standard `gofmt` formatting; CI runs `go vet`
- Errors are wrapped with `github.com/pkg/errors` (`errors.Wrap`) where context is added; use stdlib `fmt.Errorf("...: %w", err)` for simple wrapping
- Keep `main.go` as orchestration only — business logic belongs in `pia/`
- Any shared state accessed by more than one goroutine must be mutex-protected (see `pfLeaseState` in `main.go`)

---

## Pull Requests

1. Fork → branch from `main`
2. Make your change with a matching test
3. `make test && make lint` pass locally
4. Open a PR with a clear description of what changed and why

For larger changes (new commands, architectural shifts), open an issue first to discuss the approach.
