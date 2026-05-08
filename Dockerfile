# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: Build
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.21-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Cache dependencies before copying source
COPY go.mod go.sum ./
# All dependencies are vendored; no network access needed
COPY vendor/ vendor/

# Copy source
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /pia-wg-config .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Runtime
# ─────────────────────────────────────────────────────────────────────────────
FROM alpine:3.19

# curl is used for token acquisition on environments where the system curl
# is blocked by PIA's WAF (Windows-style TLS fingerprint). docker-cli is used
# by the optional restart-gluetun hook when the Docker socket is mounted.
# Users may override CURL_PATH to point at a specific OpenSSL-linked build.
RUN apk add --no-cache ca-certificates curl docker-cli

COPY --from=builder /pia-wg-config /usr/local/bin/pia-wg-config
COPY docker/restart-gluetun /usr/local/bin/restart-gluetun

RUN chmod +x /usr/local/bin/restart-gluetun

# Default state directory; mount a volume here in Docker deployments.
VOLUME ["/state"]

ENTRYPOINT ["pia-wg-config"]
CMD ["--help"]
