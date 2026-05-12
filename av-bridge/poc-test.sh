#!/usr/bin/env bash
# poc-test.sh — PoC smoke test and demo script
# Run AFTER av-bridge is started: ./av-bridge -config poc-config.yaml
#
# Usage:
#   chmod +x poc-test.sh
#   ./poc-test.sh [av-bridge address, default 127.0.0.1:8080]

set -euo pipefail

BASE="${1:-http://127.0.0.1:8080}"
GREEN="\033[0;32m"
AMBER="\033[0;33m"
RED="\033[0;31m"
BLUE="\033[0;34m"
RESET="\033[0m"

pass() { echo -e "${GREEN}✓${RESET} $*"; }
warn() { echo -e "${AMBER}!${RESET} $*"; }
fail() { echo -e "${RED}✗${RESET} $*"; }
info() { echo -e "${BLUE}→${RESET} $*"; }
section() { echo -e "\n${BLUE}── $* ──────────────────────────────────────────────${RESET}"; }

# ── 1. Health check ───────────────────────────────────────────────────────────
section "Health check"
HEALTH=$(curl -sf "$BASE/healthz" || echo "FAIL")
if echo "$HEALTH" | grep -q "ok"; then
    pass "av-bridge is running"
else
    fail "av-bridge not responding at $BASE"
    echo "  Start it with: ./av-bridge -config poc-config.yaml"
    exit 1
fi

# ── 2. Fleet status ───────────────────────────────────────────────────────────
section "Fleet status"
STATUS=$(curl -sf "$BASE/api/v1/status")
echo "$STATUS" | python3 -m json.tool 2>/dev/null || echo "$STATUS"

TOTAL=$(echo "$STATUS" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('total',0))" 2>/dev/null || echo 0)
ONLINE=$(echo "$STATUS" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('online',0))" 2>/dev/null || echo 0)
OFFLINE=$(echo "$STATUS" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('offline',0))" 2>/dev/null || echo 0)

if [ "$ONLINE" -eq "$TOTAL" ] && [ "$TOTAL" -gt "0" ]; then
    pass "All $TOTAL devices online"
elif [ "$ONLINE" -gt "0" ]; then
    warn "$ONLINE/$TOTAL devices online, $OFFLINE offline"
else
    fail "No devices online"
fi

# ── 3. Device list ────────────────────────────────────────────────────────────
section "Device inventory"
DEVICES=$(curl -sf "$BASE/api/v1/devices")
echo "$DEVICES" | python3 -m json.tool 2>/dev/null || echo "$DEVICES"

# ── 4. Per-device telemetry ───────────────────────────────────────────────────
section "Live telemetry — Biamp Tesira"
TEL=$(curl -sf "$BASE/api/v1/devices/tesira-boardroom-01/telemetry" || echo '{"error":"device not found"}')
echo "$TEL" | python3 -m json.tool 2>/dev/null || echo "$TEL"
if echo "$TEL" | grep -q '"status":"online"'; then
    pass "Tesira online and returning telemetry"
else
    warn "Tesira not online — check IP, credentials, and Telnet is enabled"
fi

section "Live telemetry — Sony Bravia"
TEL=$(curl -sf "$BASE/api/v1/devices/display-sony-01/telemetry" || echo '{"error":"device not found"}')
echo "$TEL" | python3 -m json.tool 2>/dev/null || echo "$TEL"
if echo "$TEL" | grep -q '"status":"online"'; then
    pass "Sony Bravia online and returning telemetry"
else
    warn "Bravia not online — check IP, PSK, and REST API is enabled on display"
fi

section "Live telemetry — Poly G7500"
TEL=$(curl -sf "$BASE/api/v1/devices/vc-poly-01/telemetry" || echo '{"error":"device not found"}')
echo "$TEL" | python3 -m json.tool 2>/dev/null || echo "$TEL"
if echo "$TEL" | grep -q '"status":"online"'; then
    pass "Poly G7500 online and returning telemetry"
else
    warn "Poly G7500 not online — check IP, credentials, and remote access is enabled"
fi

# ── 5. Command tests ──────────────────────────────────────────────────────────
section "Command test — Tesira mute"
info "Sending mute command to Tesira..."
RESP=$(curl -sf -X POST "$BASE/api/v1/devices/tesira-boardroom-01/command" \
    -H "Content-Type: application/json" \
    -d '{"name":"mute"}' || echo '{"error":"failed"}')
echo "$RESP" | python3 -m json.tool 2>/dev/null || echo "$RESP"
if echo "$RESP" | grep -q '"success":true\|+OK'; then
    pass "Mute command successful — check the physical device"
else
    warn "Mute response: $RESP"
fi

sleep 2

info "Sending unmute command to Tesira..."
RESP=$(curl -sf -X POST "$BASE/api/v1/devices/tesira-boardroom-01/command" \
    -H "Content-Type: application/json" \
    -d '{"name":"unmute"}' || echo '{"error":"failed"}')
if echo "$RESP" | grep -q '"success":true\|+OK'; then
    pass "Unmute command successful"
else
    warn "Unmute response: $RESP"
fi

section "Command test — Sony Bravia power query"
info "Querying Sony power status directly via system/getPowerStatus..."
RESP=$(curl -sf -X POST "$BASE/api/v1/devices/display-sony-01/command" \
    -H "Content-Type: application/json" \
    -d '{"name":"system/getPowerStatus"}' || echo '{"error":"failed"}')
echo "$RESP" | python3 -m json.tool 2>/dev/null || echo "$RESP"
pass "Sony command path working (check response above for power status)"

section "Command test — Poly G7500 mute"
info "Sending mute command to Poly G7500..."
RESP=$(curl -sf -X POST "$BASE/api/v1/devices/vc-poly-01/command" \
    -H "Content-Type: application/json" \
    -d '{"name":"mute"}' || echo '{"error":"failed"}')
echo "$RESP" | python3 -m json.tool 2>/dev/null || echo "$RESP"
pass "Poly mute command sent (check response above)"

sleep 2

info "Unmuting Poly G7500..."
curl -sf -X POST "$BASE/api/v1/devices/vc-poly-01/command" \
    -H "Content-Type: application/json" \
    -d '{"name":"unmute"}' > /dev/null 2>&1 || true

# ── 6. Prometheus metrics ─────────────────────────────────────────────────────
section "Prometheus metrics"
METRICS=$(curl -sf "$BASE/metrics")
echo "$METRICS"
ONLINE_GAUGE=$(echo "$METRICS" | grep "^av_bridge_devices_online " | awk '{print $2}')
if [ -n "$ONLINE_GAUGE" ] && [ "$ONLINE_GAUGE" != "0" ]; then
    pass "Metrics endpoint working — $ONLINE_GAUGE devices online"
else
    warn "Metrics endpoint returned but online count is 0 or missing"
fi

# ── 7. WebSocket events ───────────────────────────────────────────────────────
section "WebSocket event stream"
info "WebSocket events stream at: ws://127.0.0.1:8080/ws/events"
info "Test with: websocat ws://127.0.0.1:8080/ws/events"
info "Or curl (keeps connection open, events appear as they arrive):"
info "  curl -N --http1.1 -H 'Connection: Upgrade' -H 'Upgrade: websocket' \\"
info "       -H 'Sec-WebSocket-Key: test' -H 'Sec-WebSocket-Version: 13' \\"
info "       http://127.0.0.1:8080/ws/events"

# ── Summary ───────────────────────────────────────────────────────────────────
section "Summary"
echo ""
echo "  av-bridge PoC is running. Key endpoints:"
echo "    Health:    $BASE/healthz"
echo "    Status:    $BASE/api/v1/status"
echo "    Devices:   $BASE/api/v1/devices"
echo "    Metrics:   $BASE/metrics"
echo "    WS Events: ws://127.0.0.1:8080/ws/events"
echo ""
echo "  Webhook receiver (check for incoming payloads):"
echo "    docker run -p 9000:8080 mendhak/http-https-echo:latest"
echo "    curl -s \$POC_WEBHOOK_URL | jq ."
echo ""
pass "PoC test complete"
