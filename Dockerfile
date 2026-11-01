# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: Build (always runs on the builder's native platform and
# cross-compiles, so multi-arch images do not need QEMU for the Go build).
# ─────────────────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src

# Cache dependency metadata before copying source.
COPY go.mod go.sum ./
# All dependencies are vendored; no network access needed.
COPY vendor/ vendor/

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=vendor -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /pia-wg-config .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Runtime
# ─────────────────────────────────────────────────────────────────────────────
FROM alpine:3.23

# curl is used for token acquisition on environments where the system curl
# is blocked by PIA's WAF (Windows-style TLS fingerprint).
# Users may override CURL_PATH to point at a specific OpenSSL-linked build.
RUN apk add --no-cache ca-certificates curl

COPY --from=builder /pia-wg-config /usr/local/bin/pia-wg-config

# Optional notification hook for `refresh --on-failure/--on-recovered`. A no-op
# unless PIA_WG_ALERT_WEBHOOK is set. See docs/gluetun.md (Alerting).
COPY --chmod=0755 scripts/pia-wg-alert /usr/local/bin/pia-wg-alert

# Default state directory; mount a volume here in Docker deployments.
VOLUME ["/state"]

ENTRYPOINT ["pia-wg-config"]
CMD ["--help"]
