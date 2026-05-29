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

# ------------------------------------------------------------
# Unit tests (offline, mocked)
# ------------------------------------------------------------
.PHONY: test
test:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)

.PHONY: test-both
test-both:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(MAKE) test-integration

.PHONY: test-all
test-all:
	@echo "$(GREEN)==> Running unit tests$(RESET)"
	$(GOENV) $(GO) test $(GOFLAGS) $(PKG)
	$(MAKE) test-integration
	$(MAKE) test-wireguard-client

.PHONY: test-race
test-race:
	@echo "$(GREEN)==> Running unit tests (race detector)$(RESET)"
	$(GOENV) $(GO) test -race $(PKG)

# ------------------------------------------------------------
# Lint / vet
# ------------------------------------------------------------
.PHONY: lint
lint:
	@echo "$(GREEN)==> Running go vet$(RESET)"
	$(GOENV) $(GO) vet $(PKG)

# ------------------------------------------------------------
# Integration tests (real PIA API, env-gated)
# ------------------------------------------------------------
.PHONY: test-integration
test-integration:
	@echo "==> Running integration tests (skips if PIA_USERNAME and/or PIA_PASSWORD is not set)"
	$(GOENV) $(GO) test -tags=integration $(PKG)

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
	@rm -rf "$(LOCAL_GOCACHE)" "$(LOCAL_GOPATH)"
