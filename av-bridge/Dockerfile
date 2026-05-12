# ── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Cache dependencies separately from source
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" \
    -o /av-bridge ./cmd/av-bridge

# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
FROM scratch

# Import CA certs and timezone data from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /av-bridge /av-bridge

# Config is mounted at runtime
VOLUME ["/etc/av-bridge"]

EXPOSE 8080

ENTRYPOINT ["/av-bridge"]
CMD ["-config", "/etc/av-bridge/config.yaml"]
