# av-bridge

A vendor-agnostic, on-premises AV device gateway service written in Go.

**av-bridge** runs as a Linux systemd daemon on your customer's network. It maintains bidirectional communication with AV devices over multiple protocols and pushes telemetry and events to a cloud portal via REST webhook.

---

## Architecture

```
Customer Network                             Cloud
┌──────────────────────────────────────┐     ┌─────────────────┐
│                                      │     │                 │
│  AV Devices          av-bridge Hub   │────▶│  Cloud Portal   │
│  ──────────          ─────────────   │     │  (Webhook)      │
│  Display  ◀──REST──▶ REST Adapter    │     │                 │
│  Video VC ◀──WS────▶ WS Adapter      │◀────│  REST Commands  │
│  Audio DSP◀──Telnet▶ Telnet Adapter  │     │  WS Events      │
│  Camera   ◀──Serial▶ Serial Adapter  │     │                 │
│                      │               │     └─────────────────┘
│                      ▼               │
│               Local REST API         │
│               :8080                  │
└──────────────────────────────────────┘
```

**Hub** — Central coordinator. Connects all devices, polls for telemetry, drains async events, reconnects on failure.

**Adapters** — Protocol-specific drivers (REST, WebSocket, Telnet, Serial). Each implements the same `Device` interface.

**Cloud Publisher** — Buffers telemetry + events and pushes them to your cloud webhook on a configurable interval with retries.

**Local API** — REST + WebSocket API on `:8080`. Lets the cloud portal push commands inbound, and stream live events.

---

## Supported Devices

| Device Type   | Protocols Supported                  | Examples                          |
|---------------|--------------------------------------|-----------------------------------|
| Display       | REST, Telnet, Serial                 | Samsung MDC, NEC, LG Signage      |
| Conferencing  | REST, WebSocket                      | Cisco Room Series, Poly Studio    |
| Audio DSP     | Telnet, Serial, REST                 | Biamp Tesira, QSC Core            |
| Camera / PTZ  | Serial (VISCA), Telnet, REST         | Sony EVI, Panasonic AW            |

---

## Installation

### Prerequisites
- Go 1.22+
- Linux host (x86_64 or ARM)
- systemd

### Build & Install

```bash
git clone https://github.com/your-org/av-bridge
cd av-bridge
sudo ./install.sh
```

The installer will:
1. Build the binary
2. Create a `av-bridge` system user
3. Install to `/usr/local/bin/av-bridge`
4. Deploy the systemd unit
5. Create `/etc/av-bridge/config.yaml` and `/etc/av-bridge/env`

### Configure

Edit `/etc/av-bridge/config.yaml`:

```yaml
hub:
  listen_addr: "0.0.0.0:8080"
  heartbeat_period: 30s

cloud:
  webhook_url: "https://your-portal.com/api/v1/ingest"
  api_key: "${CLOUD_API_KEY}"
  push_interval: 30s

devices:
  - id: display-01
    name: Lobby Display
    type: display
    protocol: rest
    address: "192.168.1.50"
    # ...
```

Edit `/etc/av-bridge/env` with your secrets:

```
CLOUD_API_KEY=your-secret-key
DISPLAY_PASSWORD=changeme
```

### Start

```bash
sudo systemctl enable --now av-bridge
sudo journalctl -u av-bridge -f
```

---

## Two-host PoC: Pi + laptop portal

For a more realistic demo, run `av-bridge` on a Raspberry Pi (or any Linux box)
sitting on the customer LAN and run [the portal](../av-bridge-portal) on your
own machine. The portal proxies HTTP through Next.js so the browser only ever
talks to your laptop, but the WebSocket connects directly to the Pi.

### A — Build a Pi tarball on your dev machine

```bash
# From the av-bridge folder, on a machine with Go installed.
make dist-arm64        # for Raspberry Pi 4/5
# or
make dist-amd64        # for x86_64 Linux

ls dist/
# av-bridge-linux-arm64.tar.gz
```

The tarball contains the binary, the systemd unit, `install.sh`, and an
example config — no Go toolchain required on the Pi.

### B — Install on the Pi

```bash
scp dist/av-bridge-linux-arm64.tar.gz pi@<pi-ip>:~/
ssh pi@<pi-ip>
tar xzf av-bridge-linux-arm64.tar.gz
cd av-bridge-linux-arm64
sudo ./install.sh
```

Then configure:

```bash
# Generate a 32-byte random key
openssl rand -hex 32

sudo nano /etc/av-bridge/config.yaml   # set listen_addr 0.0.0.0:8080,
                                        # auth.enabled: true, devices, etc.
sudo nano /etc/av-bridge/env           # set AV_BRIDGE_API_KEY=<that key>,
                                        # device passwords, webhook secrets

sudo systemctl enable --now av-bridge
sudo journalctl -u av-bridge -f         # should show "loaded env file" + each
                                        # device transitioning to "online"
```

Quick smoke test from the Pi (or any host on the LAN):

```bash
curl http://<pi-ip>:8080/healthz                         # no auth needed
curl -H "Authorization: Bearer <KEY>" \
  http://<pi-ip>:8080/api/v1/devices                     # auth required
```

### C — Point the portal at the Pi

In `av-bridge-portal/.env.local`:

```
AV_BRIDGE_UPSTREAM=http://<pi-ip>:8080
NEXT_PUBLIC_AV_BRIDGE_WS=ws://<pi-ip>:8080
NEXT_PUBLIC_AV_BRIDGE_API_KEY=<the same key>
```

Restart the dev server (`AV_BRIDGE_UPSTREAM` is read by `next.config.mjs` once,
at startup):

```bash
cd ../av-bridge-portal
npm run dev
```

Open http://localhost:3000 — the dashboard should populate from the Pi.

### Tradeoffs and next steps

- **Cleartext on the LAN.** The API key travels in plain HTTP unless you turn
  on TLS. For a controlled demo VLAN that's fine; for real customer rollouts,
  flip on `api.tls.enabled` and supply a cert/key (the existing `make certs`
  target generates a self-signed pair). The portal's Next.js proxy will need
  the upstream URL changed to `https://...` and `AV_BRIDGE_UPSTREAM` left
  pointing at the cert's CN.
- **WS query token in logs.** Browsers can't set `Authorization` on the WS
  handshake, so we pass the key as `?token=...`. That string ends up in any
  HTTP access logs on the Pi. Acceptable for a PoC; for production, terminate
  TLS in front of the bridge.
- **No automatic reconnect on key change.** If you rotate the API key, you
  need to update both `/etc/av-bridge/env` (then `systemctl restart`) and the
  portal's `.env.local` (then restart `npm run dev`).

---

## Local API Reference

All endpoints are on `http://localhost:8080`.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Health check |
| GET | `/api/v1/devices` | List all devices and their status |
| GET | `/api/v1/devices/{id}` | Get a specific device |
| GET | `/api/v1/devices/{id}/telemetry` | Poll device for live telemetry |
| POST | `/api/v1/devices/{id}/command` | Send a command to a device |
| GET | `/ws/events` | WebSocket stream of all device events |

When `api.auth.enabled` is true, every endpoint except `/healthz` and `/metrics`
requires `Authorization: Bearer <api-key>`. The WebSocket accepts the key as
either a `Authorization` header (for non-browser clients) or a `?token=` query
parameter (for browsers).

### Send a command

```bash
curl -X POST http://localhost:8080/api/v1/devices/display-lobby-01/command \
  -H "Authorization: Bearer $AV_BRIDGE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "power_on"}'
```

### Stream events

```bash
websocat "ws://localhost:8080/ws/events?token=$AV_BRIDGE_API_KEY"
```

---

## Cloud Webhook Payload

av-bridge pushes a JSON payload to your configured webhook URL:

```json
{
  "source": "av-bridge",
  "timestamp": "2025-01-15T10:30:00Z",
  "telemetry": [
    {
      "device_id": "display-lobby-01",
      "device_name": "Lobby Main Display",
      "device_type": "display",
      "location": "Reception - Ground Floor",
      "protocol": "rest",
      "status": "online",
      "timestamp": "2025-01-15T10:30:00Z",
      "metrics": { "http_status": 200, "response_ms": 12 },
      "tags": { "make": "Samsung", "model": "QM65R" }
    }
  ],
  "events": [
    {
      "device_id": "vc-boardroom-01",
      "device_name": "Boardroom Video Bar",
      "device_type": "conferencing",
      "event_type": "ws_message",
      "payload": { "call_state": "connected", "remote": "sip:host@example.com" },
      "timestamp": "2025-01-15T10:30:05Z"
    }
  ]
}
```

---

## Adding a New Device Protocol

1. Create a new file in `internal/device/adapters/`
2. Implement the `device.Device` interface (embed `device.Base` for shared state)
3. Register it in `internal/device/adapters/factory.go`
4. Add your protocol string to `config.validate()`

---

## License

MIT
