# ============================================================
# PIA-WgConfigGenerator – Makefile
# ============================================================

# Go settings
GO          ?= go
GOFLAGS     ?=
PKG         := ./...
BIN_NAME    := pia-wg-config

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
	@echo "  lint                  Run basic Go vet checks"
	@echo ""
	@echo "  test-integration      Run integration tests (requires PIA creds)"
	@echo ""
	@echo "  test-gluetun          Full system test using Gluetun (Docker)"
	@echo "  test-gluetun-pf       Full system test using Gluetun + port forwarding"
	@echo "  test-wireguard-client Full system test using a WireGuard client container"
	@echo ""
	@echo "  clean                 Remove build artefacts"
	@echo ""

# ------------------------------------------------------------
# Build
# ------------------------------------------------------------
.PHONY: build
build:
	@echo "$(GREEN)==> Building $(BIN_NAME)$(RESET)"
	$(GO) build $(GOFLAGS) -o $(BIN_NAME) .

# ------------------------------------------------------------
# Unit tests (offline, mocked)
# ------------------------------------------------------------
.PHONY: test
test:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GO) test $(GOFLAGS) $(PKG)

.PHONY: test-both
test-both:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GO) test $(GOFLAGS) $(PKG)
	$(MAKE) test-integration

.PHONY: test-all
test-all:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GO) test $(GOFLAGS) $(PKG)
	$(MAKE) test-integration
	$(MAKE) test-wireguard-client

.PHONY: test-race
test-race:
	@echo "$(GREEN)==> Running unit tests (race detector)$(RESET)"
	$(GO) test -race $(PKG)

# ------------------------------------------------------------
# Lint / vet
# ------------------------------------------------------------
.PHONY: lint
lint:
	@echo "$(GREEN)==> Running go vet$(RESET)"
	$(GO) vet $(PKG)

# ------------------------------------------------------------
# Integration tests (real PIA API, env-gated)
# ------------------------------------------------------------
.PHONY: test-integration
test-integration:
	@echo "==> Running integration tests (skips if PIA_USERNAME and/or PIA_PASSWORD is not set)"
	$(GO) test -tags=integration $(PKG)

# ------------------------------------------------------------
# System tests – Docker based
# ------------------------------------------------------------
.PHONY: test-gluetun test-wireguard-client test-gluetun-pf

test-gluetun:
	@cd system-tests/gluetun && docker compose up --build --abort-on-container-exit --exit-code-from tester

test-gluetun-pf:
	@cd system-tests/gluetun && PIA_PF=1 docker compose up --build --abort-on-container-exit --exit-code-from tester

test-wireguard-client:
	@cd system-tests/wireguard-client && docker compose up --build --abort-on-container-exit --exit-code-from tester

# ------------------------------------------------------------
# Cleanup
# ------------------------------------------------------------
.PHONY: clean
clean:
	@echo "$(GREEN)==> Cleaning build artefacts$(RESET)"
	@rm -f $(BIN_NAME)
