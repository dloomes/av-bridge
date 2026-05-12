# av-bridge — Pi/Linux deploy bundle

This tarball contains everything needed to install av-bridge on a Linux host
without a Go toolchain.

```
av-bridge-linux-arm64/
├── av-bridge              # the binary (statically linked, CGO disabled)
├── av-bridge.service      # systemd unit
├── install.sh             # installer
├── config.example.yaml    # starter config
└── README.md              # this file
```

## Install

```bash
sudo ./install.sh
```

Creates the `av-bridge` system user, installs the binary to
`/usr/local/bin/av-bridge`, drops the systemd unit into
`/etc/systemd/system/`, and seeds `/etc/av-bridge/config.yaml` and
`/etc/av-bridge/env` if they don't already exist.

## Configure

Edit `/etc/av-bridge/config.yaml` — at minimum, set:

- `hub.listen_addr: "0.0.0.0:8080"` so the portal on another host can reach it
- `api.auth.enabled: true` and an `api_keys` entry that references
  `${AV_BRIDGE_API_KEY}` from the env file
- `devices:` — one entry per AV device on the LAN

Edit `/etc/av-bridge/env` — at minimum:

```bash
# Generate with: openssl rand -hex 32
AV_BRIDGE_API_KEY=...
# Per-device passwords referenced by ${VAR} in config.yaml
POLY_PASSWORD=...
TESIRA_PASSWORD=...
```

## Run

```bash
sudo systemctl enable --now av-bridge
sudo journalctl -u av-bridge -f
```

Smoke test from another host:

```bash
curl http://<this-host>:8080/healthz
curl -H "Authorization: Bearer $AV_BRIDGE_API_KEY" \
  http://<this-host>:8080/api/v1/devices
```

## Uninstall

```bash
sudo systemctl disable --now av-bridge
sudo rm /usr/local/bin/av-bridge /etc/systemd/system/av-bridge.service
sudo systemctl daemon-reload
# Config in /etc/av-bridge and state in /var/lib/av-bridge are kept.
```
