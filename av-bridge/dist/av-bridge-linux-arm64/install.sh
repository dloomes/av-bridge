#!/usr/bin/env bash
# install.sh — Install av-bridge as a systemd daemon.
#
# Two usage modes:
#   1. Source-tree install:   run from a clone, builds the binary with `go build`
#   2. Pre-built install:     run from a tarball produced by `make dist-arm64`,
#                             which already contains the compiled `av-bridge`
#                             binary next to this script. No Go toolchain needed.
#
# The script picks the right mode automatically: if the binary already exists
# in the current directory (or BINARY_PATH is set), it is used as-is.
set -euo pipefail

BINARY="av-bridge"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/av-bridge"
DATA_DIR="/var/lib/av-bridge"
SERVICE_USER="av-bridge"

# Pick service file location: prefer the one next to this script (tarball mode),
# fall back to deployments/ in a source tree.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$SCRIPT_DIR/av-bridge.service" ]; then
    SERVICE_FILE="$SCRIPT_DIR/av-bridge.service"
elif [ -f "$SCRIPT_DIR/deployments/av-bridge.service" ]; then
    SERVICE_FILE="$SCRIPT_DIR/deployments/av-bridge.service"
else
    echo "ERROR: cannot find av-bridge.service" >&2
    exit 1
fi

# Pick example config: tarball-bundled (./config.example.yaml) or source tree.
if [ -f "$SCRIPT_DIR/config.example.yaml" ]; then
    EXAMPLE_CONFIG="$SCRIPT_DIR/config.example.yaml"
else
    EXAMPLE_CONFIG=""
fi

# Resolve the binary: explicit override wins, then bundled binary, then build.
if [ -n "${BINARY_PATH:-}" ]; then
    SOURCE_BINARY="$BINARY_PATH"
elif [ -x "$SCRIPT_DIR/$BINARY" ]; then
    SOURCE_BINARY="$SCRIPT_DIR/$BINARY"
elif command -v go >/dev/null 2>&1 && [ -d "$SCRIPT_DIR/cmd/av-bridge" ]; then
    echo "==> Building av-bridge from source..."
    (cd "$SCRIPT_DIR" && go build -o "$BINARY" ./cmd/av-bridge)
    SOURCE_BINARY="$SCRIPT_DIR/$BINARY"
else
    echo "ERROR: no pre-built binary and no Go toolchain available." >&2
    echo "       Either bundle the av-bridge binary next to this script, or" >&2
    echo "       set BINARY_PATH=/path/to/av-bridge before re-running." >&2
    exit 1
fi
echo "==> Using binary: $SOURCE_BINARY"

echo "==> Creating service user..."
if ! id "$SERVICE_USER" &>/dev/null; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

echo "==> Installing binary..."
install -m 755 "$SOURCE_BINARY" "$INSTALL_DIR/$BINARY"

echo "==> Creating directories..."
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
chown "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"

if [ ! -f "$CONFIG_DIR/config.yaml" ] && [ -n "$EXAMPLE_CONFIG" ]; then
    echo "==> Installing example config..."
    install -m 640 "$EXAMPLE_CONFIG" "$CONFIG_DIR/config.yaml"
    chown root:"$SERVICE_USER" "$CONFIG_DIR/config.yaml"
    echo "    !! Edit $CONFIG_DIR/config.yaml before starting the service"
fi

if [ ! -f "$CONFIG_DIR/env" ]; then
    cat > "$CONFIG_DIR/env" <<'EOF'
# av-bridge secrets — sourced by systemd via EnvironmentFile=
CLOUD_WEBHOOK_URL=http://localhost:9000/ingest
CLOUD_API_KEY=changeme

# Generate a strong random key for the local API:
#   openssl rand -hex 32
AV_BRIDGE_API_KEY=changeme

# Per-device secrets — match the ${VAR} references in config.yaml
TESIRA_PASSWORD=changeme
SONY_PSK=changeme
POLY_PASSWORD=changeme
EOF
    chmod 640 "$CONFIG_DIR/env"
    chown root:"$SERVICE_USER" "$CONFIG_DIR/env"
    echo "    !! Edit $CONFIG_DIR/env with your real credentials"
fi

echo "==> Installing systemd unit..."
install -m 644 "$SERVICE_FILE" /etc/systemd/system/av-bridge.service
systemctl daemon-reload

echo ""
echo "===================================================="
echo " av-bridge installed successfully!"
echo "===================================================="
echo ""
echo "  Next steps:"
echo "  1. Edit $CONFIG_DIR/config.yaml      (devices, listen address, auth)"
echo "  2. Edit $CONFIG_DIR/env              (secrets and AV_BRIDGE_API_KEY)"
echo "  3. systemctl enable --now av-bridge"
echo "  4. journalctl -u av-bridge -f"
echo ""
