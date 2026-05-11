# AV Bridge — Project Files

This archive contains everything built across the design and development session.

## Contents

### av-bridge/
The on-premises Go service. Drop this into a GitHub repository.
- `cmd/av-bridge/` — main entry point
- `internal/` — all packages (hub, cloud, config, API, device adapters, store, alerts)
- `internal/device/adapters/` — REST, WebSocket, Telnet, Serial, Tesira, Sony Bravia, Poly G7500
- `poc-config.yaml` — PoC configuration for real hardware
- `poc.env` — secrets template (fill in your IPs and credentials, never commit this)
- `poc-test.sh` — end-to-end smoke test script
- `Makefile`, `Dockerfile`, `docker-compose.yml`
- `deployments/av-bridge.service` — systemd unit

### av-bridge-ops/
Deployment and operations tooling. Can be a separate GitHub repository.
- `ansible/` — idempotent Ansible role for fleet provisioning
- `grafana/` — Prometheus scrape config, alerting rules, Grafana dashboard JSON
- `scripts/av-bridge-update.sh` — standalone self-update script

### docs/
- `av-bridge-solution-v3.docx` — Solution architecture, AWS design, delivery plan, vendor device appendix
- `av-bridge-poc-setup-guide.docx` — Step-by-step Windows PoC setup guide

## Quick start (PoC)

See `docs/av-bridge-poc-setup-guide.docx` for the full walkthrough.

1. Edit `av-bridge/poc.env` with your device IPs and credentials
2. Edit `av-bridge/poc-config.yaml` with your Tesira instance tag names
3. `docker run -p 9000:8080 mendhak/http-https-echo:latest`
4. `go build -o av-bridge.exe ./cmd/av-bridge && ./av-bridge.exe -config poc-config.yaml`

## Pushing to GitHub

See `docs/av-bridge-poc-setup-guide.docx` Part 7, or follow the instructions in the
solution architecture document for the full deployment approach.

Remember to update the module path in `go.mod` from `github.com/av-bridge` to
`github.com/YOUR-USERNAME/av-bridge` before pushing.
