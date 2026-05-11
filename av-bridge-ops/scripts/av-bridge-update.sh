#!/usr/bin/env bash
# av-bridge-update
# Standalone self-update script for av-bridge.
# Can be deployed by Ansible (see roles/av-bridge) or manually.
#
# Install:
#   sudo cp av-bridge-update /usr/local/bin/av-bridge-update
#   sudo chmod +x /usr/local/bin/av-bridge-update
#
# Run manually:    sudo av-bridge-update
# Run via systemd: systemctl start av-bridge-update

set -euo pipefail

# ── Config ─────────────────────────────────────────────────────────────────────
BINARY="${AV_BRIDGE_BINARY:-/usr/local/bin/av-bridge}"
ARCH="${AV_BRIDGE_ARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}"
RELEASE_API="${AV_BRIDGE_RELEASE_API:-https://api.github.com/repos/your-org/av-bridge/releases/latest}"
RELEASE_DL="${AV_BRIDGE_RELEASE_DL:-https://github.com/your-org/av-bridge/releases/download}"
HEALTHZ="${AV_BRIDGE_HEALTHZ:-http://127.0.0.1:8080/healthz}"
CONFIG_DIR="${AV_BRIDGE_CONFIG_DIR:-/etc/av-bridge}"
SERVICE="${AV_BRIDGE_SERVICE:-av-bridge}"
LOG_TAG="av-bridge-update"
TMPDIR="$(mktemp -d)"

# ── Helpers ────────────────────────────────────────────────────────────────────
log()  { logger -t "$LOG_TAG" -- "$*";             echo "[$(date -u +%H:%M:%S)] $*"; }
warn() { logger -t "$LOG_TAG" -p user.warning -- "$*"; echo "[$(date -u +%H:%M:%S)] WARN: $*" >&2; }
die()  { logger -t "$LOG_TAG" -p user.err -- "$*";     echo "[$(date -u +%H:%M:%S)] ERROR: $*" >&2; exit 1; }

cleanup() { rm -rf "$TMPDIR"; }
trap cleanup EXIT

require() { command -v "$1" &>/dev/null || die "Required tool not found: $1"; }
require curl
require sha256sum
require systemctl

# ── 1. Resolve latest version ──────────────────────────────────────────────────
log "Querying latest release..."
LATEST=$(curl -fsSL \
  -H "Accept: application/vnd.github.v3+json" \
  "$RELEASE_API" \
  | grep '"tag_name"' \
  | head -1 \
  | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')

[[ -n "$LATEST" ]] || die "Could not fetch latest release tag from $RELEASE_API"
log "Latest release: $LATEST"

# ── 2. Check current version ───────────────────────────────────────────────────
CURRENT="none"
if [[ -x "$BINARY" ]]; then
  CURRENT=$("$BINARY" -version 2>&1 | grep -oP 'v[\d.]+(-\w+)?' | head -1 || echo "unknown")
  log "Installed: $CURRENT"
fi

if [[ "$CURRENT" == "$LATEST" ]]; then
  log "Already on $LATEST — nothing to do"
  exit 0
fi

# ── 3. Download ────────────────────────────────────────────────────────────────
BINARY_NAME="av-bridge-linux-${ARCH}"
DL_BASE="${RELEASE_DL}/${LATEST}"

log "Downloading $BINARY_NAME @ $LATEST..."
curl -fsSL --progress-bar \
  -o "$TMPDIR/$BINARY_NAME" \
  "${DL_BASE}/${BINARY_NAME}" \
  || die "Binary download failed"

curl -fsSL \
  -o "$TMPDIR/checksums.txt" \
  "${DL_BASE}/checksums.txt" \
  || die "Checksum file download failed"

# ── 4. Verify checksum ─────────────────────────────────────────────────────────
log "Verifying checksum..."
pushd "$TMPDIR" > /dev/null
grep "$BINARY_NAME" checksums.txt | sha256sum --check --status \
  || die "Checksum mismatch — aborting. Binary may be corrupted or tampered."
popd > /dev/null
log "Checksum verified"

# ── 5. Verify signature (cosign, optional) ────────────────────────────────────
if command -v cosign &>/dev/null; then
  log "Verifying cosign signature..."
  curl -fsSL -o "$TMPDIR/${BINARY_NAME}.sig"  "${DL_BASE}/${BINARY_NAME}.sig"  2>/dev/null || true
  curl -fsSL -o "$TMPDIR/${BINARY_NAME}.pem"  "${DL_BASE}/${BINARY_NAME}.pem"  2>/dev/null || true
  if [[ -s "$TMPDIR/${BINARY_NAME}.sig" && -s "$TMPDIR/${BINARY_NAME}.pem" ]]; then
    cosign verify-blob \
      --signature "$TMPDIR/${BINARY_NAME}.sig" \
      --certificate "$TMPDIR/${BINARY_NAME}.pem" \
      "$TMPDIR/$BINARY_NAME" \
      && log "Signature OK" \
      || warn "Signature check failed — proceeding (checksum passed)"
  else
    log "No signature bundle found — skipping cosign check"
  fi
fi

# ── 6. Smoke-test new binary ───────────────────────────────────────────────────
chmod +x "$TMPDIR/$BINARY_NAME"
NEW_VER=$("$TMPDIR/$BINARY_NAME" -version 2>&1 | head -1)
[[ -n "$NEW_VER" ]] || die "New binary failed to respond — aborting"
log "New binary reports: $NEW_VER"

# ── 7. Back up current binary ─────────────────────────────────────────────────
if [[ -x "$BINARY" ]]; then
  cp "$BINARY" "${BINARY}.prev"
  log "Backed up current binary to ${BINARY}.prev"
fi

# ── 8. Atomic swap ────────────────────────────────────────────────────────────
install -m 755 "$TMPDIR/$BINARY_NAME" "${BINARY}.new"
mv "${BINARY}.new" "$BINARY"
log "Binary installed: $BINARY"

# ── 9. Restart service ────────────────────────────────────────────────────────
log "Restarting $SERVICE..."
systemctl restart "$SERVICE"

# ── 10. Health check with rollback ────────────────────────────────────────────
log "Health check ($HEALTHZ)..."
MAX_WAIT=60
WAITED=0
while (( WAITED < MAX_WAIT )); do
  if curl -fsSL --max-time 3 "$HEALTHZ" 2>/dev/null | grep -q '"ok"'; then
    log "Health check passed"
    break
  fi
  sleep 5
  (( WAITED += 5 ))
done

if (( WAITED >= MAX_WAIT )); then
  warn "Health check timed out after ${MAX_WAIT}s — initiating rollback"
  if [[ -f "${BINARY}.prev" ]]; then
    install -m 755 "${BINARY}.prev" "$BINARY"
    systemctl restart "$SERVICE"
    warn "Rolled back to previous binary"

    # Notify cloud of rollback
    _notify_cloud "rollback" "$LATEST" "$CURRENT"
    exit 1
  else
    die "No rollback binary available — manual intervention required"
  fi
fi

# ── 11. Notify cloud portal ───────────────────────────────────────────────────
_notify_cloud() {
  local event="$1" new_ver="$2" prev_ver="$3"
  local env_file="${CONFIG_DIR}/env"
  if [[ -f "$env_file" ]]; then
    # shellcheck disable=SC1090
    set +u; source "$env_file" 2>/dev/null || true; set -u
    local webhook="${CLOUD_WEBHOOK_URL:-}"
    local apikey="${CLOUD_API_KEY:-}"
    local site_id
    site_id=$(hostname -s)
    if [[ -n "$webhook" ]]; then
      curl -fsSL -X POST "${webhook%/ingest}/updates" \
        -H "Authorization: Bearer $apikey" \
        -H "Content-Type: application/json" \
        -d "{\"event\":\"$event\",\"site_id\":\"$site_id\",\"version\":\"$new_ver\",\"previous\":\"$prev_ver\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}" \
        2>/dev/null \
        && log "Update event sent to cloud portal" \
        || warn "Could not notify cloud portal"
    fi
  fi
}

_notify_cloud "update" "$LATEST" "$CURRENT"

log "=============================="
log "Update complete: $CURRENT → $LATEST"
log "=============================="
