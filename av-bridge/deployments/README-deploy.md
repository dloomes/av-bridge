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

- `hub.listen_addr` — defaults to `127.0.0.1:8080` (this host only). Set
  `"0.0.0.0:8080"` only if another host needs the local API, and then also:
- `api.auth.enabled: true` and an `api_keys` entry that references
  `${AV_BRIDGE_API_KEY}` from the env file. Without a usable key the
  Collector logs an error and listens on `127.0.0.1` regardless of
  `hub.listen_addr`.
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

Smoke test (from another host only if you opened `listen_addr` with auth;
otherwise use `127.0.0.1` on this host):

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
