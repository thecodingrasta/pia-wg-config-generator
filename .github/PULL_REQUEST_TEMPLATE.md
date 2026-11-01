## Summary

<!-- What does this change, and why? Link the issue it closes: "Closes #123". -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change (flag, file format, default or behaviour change)
- [ ] Documentation only
- [ ] CI / tooling / dependencies

## How was this tested?

<!-- Commands you ran and what you observed. Redact credentials, tokens, keys and IPs. -->

- [ ] `go vet ./...` and `go test -mod=vendor ./...` pass
- [ ] `make test-race` passes (needed for concurrency changes)
- [ ] New or changed behaviour has a test
- [ ] Docker/Gluetun changes were exercised with `system-tests/` (or explain why not)

## Checklist

- [ ] README and `docs/` match the new behaviour (flags, defaults, file names)
- [ ] No secrets, tokens or WireGuard private keys in code, logs, fixtures or screenshots
- [ ] Dependencies changed? `go mod tidy && go mod vendor` was run and `vendor/` is committed
- [ ] Anything that creates PIA registrations or changes refresh timing was considered for rate limits
