# ============================================================
# PIA-WgConfigGenerator – Makefile
# ============================================================

# Go settings
GO          ?= go
GOFLAGS     ?=
PKG         := ./...
BIN_NAME    := pia-wg-config

# System test paths
SYS_TEST_DIR        := test/system
GLUETUN_DIR         := $(SYS_TEST_DIR)/gluetun
WG_DIRECT_DIR       := $(SYS_TEST_DIR)/wireguard-direct

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
	@echo "  test-system-gluetun   Full system test using Gluetun (Docker)"
	@echo "  test-system-wg        Full system test using direct WireGuard"
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
	$(MAKE) test-wgquick

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
.PHONY: test-gluetun test-wgquick test-gluetun-pf test-wgquick-pf test-wgquick-ipv6-off test-wgquick-ipv6-kill

test-gluetun:
	@./system-tests/verify.sh --dir ./system-tests/gluetun --ipv6 auto

test-wgquick:
	@./system-tests/verify.sh --dir ./system-tests/wg-quick --ipv6 auto

test-gluetun-pf:
	@./system-tests/verify.sh --dir ./system-tests/gluetun --pf --ipv6 auto

test-wgquick-pf:
	@./system-tests/verify.sh --dir ./system-tests/wg-quick --pf --ipv6 auto

test-wgquick-ipv6-off:
	@./system-tests/verify.sh --dir ./system-tests/wg-quick --ipv6 off

test-wgquick-ipv6-kill:
	@./system-tests/verify.sh --dir ./system-tests/wg-quick --ipv6 kill

# ------------------------------------------------------------
# Cleanup
# ------------------------------------------------------------
.PHONY: clean
clean:
	@echo "$(GREEN)==> Cleaning build artefacts$(RESET)"
	@rm -f $(BIN_NAME)