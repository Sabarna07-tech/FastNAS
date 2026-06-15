# syntax=docker/dockerfile:1

# --- Build stage ---
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Build a static, pure-Go binary (no CGO; modernc sqlite is pure Go).
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/fastnas ./cmd/server

# --- Runtime stage ---
FROM alpine:3.20

# Tailscale's DERP/control connections need root CA certificates.
RUN apk add --no-cache ca-certificates

WORKDIR /var/lib/fastnas
COPY --from=builder /out/fastnas /usr/local/bin/fastnas

# Persist the SQLite DB, uploaded files, and Tailscale node state.
ENV DATA_DIR=/var/lib/fastnas/data
VOLUME ["/var/lib/fastnas"]

# Optional local debug listener (the Tailscale interface needs no host ports).
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/fastnas"]
