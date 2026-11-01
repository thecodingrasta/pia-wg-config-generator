# ============================================================
# PIA-WgConfigGenerator – Makefile
# ============================================================

# Go settings
GO          ?= go
GOFLAGS     ?=
PKG         := ./...
BIN_NAME    := pia-wg-config
LOCAL_GOCACHE ?= $(CURDIR)/.go-build-cache
LOCAL_GOPATH  ?= $(CURDIR)/.go-path
ifeq ($(OS),Windows_NT)
GOENV      := set "GOCACHE=$(LOCAL_GOCACHE)" && set "GOPATH=$(LOCAL_GOPATH)" &&
else
GOENV      := GOCACHE="$(LOCAL_GOCACHE)" GOPATH="$(LOCAL_GOPATH)"
endif
LOCAL_GOCACHE ?= $(CURDIR)/.go-build-cache
LOCAL_GOPATH  ?= $(CURDIR)/.go-path
ifeq ($(OS),Windows_NT)
GOENV      := set "GOCACHE=$(LOCAL_GOCACHE)" && set "GOPATH=$(LOCAL_GOPATH)" &&
else
GOENV      := GOCACHE="$(LOCAL_GOCACHE)" GOPATH="$(LOCAL_GOPATH)"
endif

# Pretty Colours
GREEN  := \033[0;32m
YELLOW := \033[0;33m
RED    := \033[0;31m
RESET  := \033[0m

.DEFAULT_GOAL := help

# ------------------------------------------------------------
# Help
# ------------------------------------------------------------
.PHONY: help
help:
	@echo ""
	@echo "PIA-WgConfigGenerator >>> available targets:"
	@echo ""
	@echo "  build                 Build binary"
	@echo "  test                  Run unit tests"
	@echo "  test-both             Run unit & integration tests"
	@echo "  test-all              Run unit, integration & system tests"
	@echo "  test-race             Run unit tests with race detector"
	@echo "  lint                  Run golangci-lint (falls back to go vet if it is not installed)"
	@echo "  vuln                  Run govulncheck for reachable vulnerabilities"
	@echo "  cover                 Run unit tests and print total coverage"
	@echo "  tidy                  go mod tidy + go mod vendor"
	@echo "  release-build         Cross-compile the three release binaries into ./release"
	@echo ""
	@echo "  test-integration      Run integration tests (requires PIA creds)"
	@echo ""
	@echo "  test-stack            Smoke-test the four-container stack (Docker, no PIA creds)"
	@echo "  test-gluetun          Full system test using Gluetun (requires PIA creds)"
	@echo "  test-gluetun-pf       Full system test using Gluetun + port forwarding (requires PIA creds)"
	@echo "  test-wireguard-client Full system test using a WireGuard client container (requires PIA creds)"
	@echo ""
	@echo "  clean                 Remove build artefacts"
	@echo ""

# ------------------------------------------------------------
# Build
# ------------------------------------------------------------
.PHONY: build
build:
	@echo "$(GREEN)==> Building $(BIN_NAME)$(RESET)"
	$(GOENV) $(GO) build $(GOFLAGS) -o $(BIN_NAME) .
	$(GOENV) $(GO) build $(GOFLAGS) -o $(BIN_NAME) .

# ------------------------------------------------------------
# Unit tests (offline, mocked)
# ------------------------------------------------------------
.PHONY: test
test:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)

.PHONY: test-both
test-both:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(MAKE) test-integration

.PHONY: test-all
test-all:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(MAKE) test-integration
	$(MAKE) test-wireguard-client
	$(MAKE) test-wireguard-client

.PHONY: test-race
test-race:
	@echo "$(GREEN)==> Running unit tests (race detector)$(RESET)"
	$(GOENV) $(GO) test -race $(PKG)
	$(GOENV) $(GO) test -race $(PKG)

# ------------------------------------------------------------
# Lint / vet
# ------------------------------------------------------------
.PHONY: lint
lint:
	@echo "$(GREEN)==> Running go vet$(RESET)"
	$(GOENV) $(GO) vet $(PKG)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "$(GREEN)==> Running golangci-lint$(RESET)"; \
		golangci-lint run $(PKG); \
	else \
		echo "$(YELLOW)golangci-lint not installed; ran go vet only (https://golangci-lint.run/welcome/install/)$(RESET)"; \
	fi

.PHONY: vuln
vuln:
	@echo "$(GREEN)==> Running govulncheck$(RESET)"
	$(GOENV) $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 $(PKG)

.PHONY: cover
cover:
	@echo "$(GREEN)==> Running unit tests with coverage$(RESET)"
	$(GOENV) $(GO) test -mod=vendor -covermode=atomic -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -n 1

.PHONY: tidy
tidy:
	$(GO) mod tidy
	$(GO) mod vendor

# Builds exactly the assets a tagged release publishes. CI does the same in
# .github/workflows/release.yml; this target is for local verification.
RELEASE_VERSION ?= dev
RELEASE_LDFLAGS  = -s -w -X main.version=$(RELEASE_VERSION)

.PHONY: release-build
release-build:
	@echo "$(GREEN)==> Building release binaries ($(RELEASE_VERSION))$(RESET)"
	@mkdir -p release
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 $(GO) build -mod=vendor -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o release/pia-wg-config .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -mod=vendor -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o release/pia-wg-config.exe .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 $(GO) build -mod=vendor -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o release/pia-wg-config-arm64.exe .

# ------------------------------------------------------------
# Integration tests (real PIA API, env-gated)
# ------------------------------------------------------------
.PHONY: test-integration
test-integration:
	@echo "==> Running integration tests (skips if PIA_USERNAME and/or PIA_PASSWORD is not set)"
	$(GOENV) $(GO) test -tags=integration $(PKG)
	$(GOENV) $(GO) test -tags=integration $(PKG)

# ------------------------------------------------------------
# System tests – Docker based
# ------------------------------------------------------------
.PHONY: test-gluetun test-wireguard-client test-gluetun-pf test-stack

# Credential-free: validates docs/examples and the restart-only proxy. CI runs it.
test-stack:
	@bash system-tests/stack-smoke.sh

test-gluetun:
	@cd system-tests/gluetun && docker compose up --build --abort-on-container-exit --exit-code-from tester
	@cd system-tests/gluetun && docker compose up --build --abort-on-container-exit --exit-code-from tester

test-gluetun-pf:
	@cd system-tests/gluetun && PIA_PF=1 docker compose up --build --abort-on-container-exit --exit-code-from tester

test-wireguard-client:
	@cd system-tests/wireguard-client && docker compose up --build --abort-on-container-exit --exit-code-from tester
	@cd system-tests/gluetun && PIA_PF=1 docker compose up --build --abort-on-container-exit --exit-code-from tester

test-wireguard-client:
	@cd system-tests/wireguard-client && docker compose up --build --abort-on-container-exit --exit-code-from tester

# ------------------------------------------------------------
# Cleanup
# ------------------------------------------------------------
.PHONY: clean
clean:
	@echo "$(GREEN)==> Cleaning build artefacts$(RESET)"
	@rm -f $(BIN_NAME) coverage.out
	@rm -rf release "$(LOCAL_GOCACHE)" "$(LOCAL_GOPATH)"
