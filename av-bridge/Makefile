BINARY     := av-bridge
CMD        := ./cmd/av-bridge
VERSION    := $(shell git describe --tags --always 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

# Default Go toolchain
GO         := go
GOFLAGS    :=

.PHONY: all build test test-cover lint vet fmt clean install docker docker-push help

all: fmt vet test build ## Run fmt, vet, test, then build

## ── Build ────────────────────────────────────────────────────────────────────

build: ## Build the binary for the current platform
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@echo "Built: ./$(BINARY) ($(VERSION))"

build-linux: ## Cross-compile for Linux amd64 (useful on macOS)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 $(CMD)

build-arm64: ## Cross-compile for Linux arm64 (Raspberry Pi 4+)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-arm64 $(CMD)

dist-arm64: build-arm64 ## Bundle arm64 binary + service + install.sh into a tarball
	@mkdir -p dist/av-bridge-linux-arm64
	cp $(BINARY)-linux-arm64 dist/av-bridge-linux-arm64/$(BINARY)
	cp deployments/av-bridge.service dist/av-bridge-linux-arm64/
	cp install.sh dist/av-bridge-linux-arm64/
	cp config.example.yaml dist/av-bridge-linux-arm64/
	cp deployments/README-deploy.md dist/av-bridge-linux-arm64/README.md 2>/dev/null || true
	tar -C dist -czf dist/av-bridge-linux-arm64.tar.gz av-bridge-linux-arm64
	@echo ""
	@echo "Built: dist/av-bridge-linux-arm64.tar.gz ($(VERSION))"
	@echo "  Copy to Pi:   scp dist/av-bridge-linux-arm64.tar.gz pi@<host>:~/"
	@echo "  Install:      ssh pi@<host> 'tar xzf av-bridge-linux-arm64.tar.gz && cd av-bridge-linux-arm64 && sudo ./install.sh'"

dist-amd64: build-linux ## Bundle linux/amd64 binary + service + install.sh into a tarball
	@mkdir -p dist/av-bridge-linux-amd64
	cp $(BINARY)-linux-amd64 dist/av-bridge-linux-amd64/$(BINARY)
	cp deployments/av-bridge.service dist/av-bridge-linux-amd64/
	cp install.sh dist/av-bridge-linux-amd64/
	cp config.example.yaml dist/av-bridge-linux-amd64/
	cp deployments/README-deploy.md dist/av-bridge-linux-amd64/README.md 2>/dev/null || true
	tar -C dist -czf dist/av-bridge-linux-amd64.tar.gz av-bridge-linux-amd64
	@echo ""
	@echo "Built: dist/av-bridge-linux-amd64.tar.gz ($(VERSION))"

## ── Test ─────────────────────────────────────────────────────────────────────

test: ## Run all unit tests
	$(GO) test ./... -timeout 30s

test-cover: ## Run tests with coverage report
	$(GO) test ./... -timeout 30s -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-race: ## Run tests with race detector
	$(GO) test -race ./... -timeout 60s

test-verbose: ## Run tests with verbose output
	$(GO) test -v ./... -timeout 30s

## ── Code Quality ─────────────────────────────────────────────────────────────

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go source files
	$(GO) fmt ./...

lint: ## Run golangci-lint (install: https://golangci-lint.run/usage/install/)
	@which golangci-lint > /dev/null 2>&1 || (echo "golangci-lint not found. Install from https://golangci-lint.run/usage/install/" && exit 1)
	golangci-lint run ./...

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

## ── Run ──────────────────────────────────────────────────────────────────────

run: build ## Build and run with example config
	./$(BINARY) -config config.example.yaml

## ── Install ──────────────────────────────────────────────────────────────────

install: build ## Install binary and systemd unit (requires sudo)
	@echo "Installing av-bridge..."
	sudo bash install.sh

uninstall: ## Remove the installed service and binary
	@echo "Stopping and removing av-bridge..."
	-sudo systemctl stop av-bridge
	-sudo systemctl disable av-bridge
	sudo rm -f /usr/local/bin/av-bridge
	sudo rm -f /etc/systemd/system/av-bridge.service
	sudo systemctl daemon-reload
	@echo "Done. Config files in /etc/av-bridge were preserved."

## ── Docker ───────────────────────────────────────────────────────────────────

docker: ## Build Docker image
	docker build -t av-bridge:$(VERSION) -t av-bridge:latest .

docker-push: ## Push Docker image (set REGISTRY env var)
	docker push $(REGISTRY)/av-bridge:$(VERSION)
	docker push $(REGISTRY)/av-bridge:latest

compose-up: ## Start docker-compose stack (service + mock cloud)
	docker compose up --build

compose-down: ## Stop docker-compose stack
	docker compose down

## ── Certs (dev TLS) ──────────────────────────────────────────────────────────

certs: ## Generate self-signed TLS certs for local dev
	@mkdir -p certs
	openssl req -x509 -newkey rsa:4096 -keyout certs/server.key \
	  -out certs/server.crt -days 365 -nodes \
	  -subj "/CN=av-bridge-dev" \
	  -addext "subjectAltName=IP:127.0.0.1,DNS:localhost"
	@echo "Certs written to ./certs/"

## ── Housekeeping ─────────────────────────────────────────────────────────────

clean: ## Remove build artefacts
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64
	rm -rf dist/
	rm -f coverage.out coverage.html

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
