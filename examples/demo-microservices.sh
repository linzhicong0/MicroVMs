#!/bin/bash
# demo-microservices.sh — Deploys two inter-communicating microservices into
# Firecracker MicroVMs and tests the cross-VM HTTP call.
#
# Prerequisites:
#   - sandbox-api running on :8080 (make build && ./bin/sandbox-api)
#   - For KVM mode: root access, firecracker binary, kernel, rootfs images
#   - For simulation mode: just the sandbox-api (responses are simulated)
#
# Usage:
#   ./examples/demo-microservices.sh
#   API_URL=http://my-host:9090 ./examples/demo-microservices.sh

set -euo pipefail

API_URL="${API_URL:-http://localhost:8080}"
BASE_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  🔗 Microservice POC: Go → Python across Firecracker VMs   ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# ─── Helper ─────────────────────────────────────────────────────────────────────
json_value() {
    # Extract a value from JSON: json_value '{"key":"val"}' 'key'
    python3 -c "import sys,json; print(json.load(sys.stdin)['$2'])" <<< "$1" 2>/dev/null || \
        echo "$1" | grep -o "\"$2\":\"[^\"]*\"" | head -1 | cut -d'"' -f4
}

# ─── Check API ──────────────────────────────────────────────────────────────────
echo "1️⃣  Checking sandbox API..."
HEALTH=$(curl -sf "$API_URL/health" 2>/dev/null) || {
    echo "❌ Sandbox API not reachable at $API_URL"
    echo "   Start it with: make build && ./bin/sandbox-api"
    exit 1
}
echo "   API is healthy: $HEALTH"
echo ""

# ─── Create Python sandbox ──────────────────────────────────────────────────────
echo "2️⃣  Creating Python sandbox..."
PY_RESP=$(curl -sf -X POST "$API_URL/sandboxes" \
    -H "Content-Type: application/json" \
    -d '{"language":"python","vcpus":2,"mem_size_mib":256}')
PY_ID=$(json_value "$PY_RESP" "id")
PY_IP=$(json_value "$PY_RESP" "ip" 2>/dev/null || echo "")
echo "   Sandbox ID: $PY_ID"
echo "   IP: ${PY_IP:-N/A (simulation mode)}"
echo ""

# ─── Deploy Python service ──────────────────────────────────────────────────────
echo "3️⃣  Deploying Python service..."
curl -sf -X PUT "$API_URL/sandboxes/$PY_ID/files/app.py" \
    -H "Content-Type: application/json" \
    -d "{\"content\":$(python3 -c "
import json
with open('$BASE_DIR/examples/python-service/app.py') as f:
    print(json.dumps(f.read()))
")}" > /dev/null
echo "   Written: /workspace/app.py"

# Start the Python service in the background
curl -sf -X POST "$API_URL/sandboxes/$PY_ID/exec" \
    -H "Content-Type: application/json" \
    -d '{"cmd":"cd /workspace && python3 app.py &","cwd":"/workspace"}' > /dev/null
echo "   Started: python3 app.py (background)"
echo ""

# ─── Create Go sandbox ─────────────────────────────────────────────────────────
echo "4️⃣  Creating Go sandbox..."
GO_RESP=$(curl -sf -X POST "$API_URL/sandboxes" \
    -H "Content-Type: application/json" \
    -d '{"language":"go","vcpus":2,"mem_size_mib":256}')
GO_ID=$(json_value "$GO_RESP" "id")
GO_IP=$(json_value "$GO_RESP" "ip" 2>/dev/null || echo "")
echo "   Sandbox ID: $GO_ID"
echo "   IP: ${GO_IP:-N/A (simulation mode)}"
echo ""

# ─── Cross-compile Go service ───────────────────────────────────────────────────
echo "5️⃣  Cross-compiling Go service..."
ARCH="${ARCH:-$(uname -m)}"
case "$ARCH" in
    x86_64)  GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    *)       GOARCH="$ARCH" ;;
esac

cd "$BASE_DIR/examples/go-service"
GOOS=linux GOARCH=$GOARCH go build -o /tmp/go-service .
echo "   Built: go-service (linux/$GOARCH)"
echo ""

# ─── Deploy Go service ─────────────────────────────────────────────────────────
echo "6️⃣  Deploying Go service..."

# Read binary and base64-encode it, then decode and write inside the VM
B64=$(base64 < /tmp/go-service)
curl -sf -X POST "$API_URL/sandboxes/$GO_ID/exec" \
    -H "Content-Type: application/json" \
    -d "{\"cmd\":\"echo $B64 | base64 -d > /workspace/go-service && chmod +x /workspace/go-service\",\"cwd\":\"/workspace\"}" > /dev/null
echo "   Written: /workspace/go-service"

# Build the python URL based on mode
if [[ -n "$PY_IP" ]]; then
    PY_URL="http://$PY_IP:8080"
else
    PY_URL="http://localhost:8080"  # simulation fallback
fi

# Start the Go service
curl -sf -X POST "$API_URL/sandboxes/$GO_ID/exec" \
    -H "Content-Type: application/json" \
    -d "{\"cmd\":\"PYTHON_SERVICE_URL=$PY_URL /workspace/go-service &\",\"cwd\":\"/workspace\"}" > /dev/null
echo "   Started: go-service (PYTHON_SERVICE_URL=$PY_URL)"
echo ""

# ─── Test ────────────────────────────────────────────────────────────────────────
echo "7️⃣  Testing services..."
echo ""

echo "   → Go service /health:"
if [[ -n "$GO_IP" ]]; then
    curl -sf "http://$GO_IP:8080/health" 2>/dev/null | sed 's/^/     /' || echo "     (unreachable)"
else
    echo "     (simulation mode — use sandbox exec)"
    curl -sf -X POST "$API_URL/sandboxes/$GO_ID/exec" \
        -H "Content-Type: application/json" \
        -d '{"cmd":"curl -sf http://localhost:8080/health","cwd":"/workspace"}' 2>/dev/null | sed 's/^/     /' || echo "     (simulated)"
fi
echo ""

echo "   → Python service /health:"
if [[ -n "$PY_IP" ]]; then
    curl -sf "http://$PY_IP:8080/health" 2>/dev/null | sed 's/^/     /' || echo "     (unreachable)"
else
    curl -sf -X POST "$API_URL/sandboxes/$PY_ID/exec" \
        -H "Content-Type: application/json" \
        -d '{"cmd":"curl -sf http://localhost:8080/health","cwd":"/workspace"}' 2>/dev/null | sed 's/^/     /' || echo "     (simulated)"
fi
echo ""

echo "   → Go service /call-python (cross-VM call):"
if [[ -n "$GO_IP" ]]; then
    curl -sf "http://$GO_IP:8080/call-python" 2>/dev/null | sed 's/^/     /' || echo "     (unreachable)"
else
    curl -sf -X POST "$API_URL/sandboxes/$GO_ID/exec" \
        -H "Content-Type: application/json" \
        -d '{"cmd":"curl -sf http://localhost:8080/call-python","cwd":"/workspace"}' 2>/dev/null | sed 's/^/     /' || echo "     (simulated)"
fi
echo ""

# ─── Cleanup ────────────────────────────────────────────────────────────────────
echo "8️⃣  Cleaning up..."
curl -sf -X DELETE "$API_URL/sandboxes/$GO_ID" > /dev/null
curl -sf -X DELETE "$API_URL/sandboxes/$PY_ID" > /dev/null
echo "   Sandboxes stopped"
echo ""

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  ✅ Microservice POC complete                               ║"
echo "╚══════════════════════════════════════════════════════════════╝"
