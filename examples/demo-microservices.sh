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
NO_CLEANUP=false

# ─── Argument Parsing ──────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-cleanup) NO_CLEANUP=true; shift ;;
        --help|-h) sed -n '2,/^$/p' "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

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
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -o /tmp/go-service .
echo "   Built: go-service (linux/$GOARCH)"
echo ""

# ─── Deploy Go service ─────────────────────────────────────────────────────────
echo "6️⃣  Deploying Go service..."

# Serve the binary via a temp HTTP server so the VM can download it
# (avoids ARG_MAX limit from passing base64 on the command line)
cp /tmp/go-service /tmp/go-service-download
cd /tmp
python3 -m http.server 9999 --bind 0.0.0.0 >/dev/null 2>&1 &
FILE_SERVER_PID=$!
sleep 1

# In KVM mode, VMs reach the host via the bridge gateway (10.0.0.1)
# In simulation mode, use localhost
if [[ -n "$GO_IP" ]]; then
    FILE_URL="http://10.0.0.1:9999/go-service-download"
else
    FILE_URL="http://localhost:9999/go-service-download"
fi

curl -sf -X POST "$API_URL/sandboxes/$GO_ID/exec" \
    -H "Content-Type: application/json" \
    -d "{\"cmd\":\"curl -sf $FILE_URL -o /workspace/go-service && chmod +x /workspace/go-service\",\"cwd\":\"/workspace\"}" > /dev/null

kill $FILE_SERVER_PID 2>/dev/null || true
rm -f /tmp/go-service-download
echo "   Downloaded & written: /workspace/go-service"

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
sleep 2
echo ""

# Test from inside the VMs using exec API (avoids host routing issues)
echo "   → Python service /hello:"
PY_HELLO=$(curl -sf -X POST "$API_URL/sandboxes/$PY_ID/exec" \
    -H "Content-Type: application/json" \
    -d '{"cmd":"curl -sf http://localhost:8080/hello","cwd":"/workspace"}')
echo "     $PY_HELLO"
echo ""

echo "   → Go service /greeting (calls Python /hello across VMs):"
GREETING=$(curl -sf -X POST "$API_URL/sandboxes/$GO_ID/exec" \
    -H "Content-Type: application/json" \
    -d "{\"cmd\":\"curl -sf http://localhost:8080/greeting\",\"cwd\":\"/workspace\"}")
echo "     $GREETING"
echo ""

# ─── Cleanup ────────────────────────────────────────────────────────────────────
if $NO_CLEANUP; then
    echo ""
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║  ✅ Services running — VMs kept alive (--no-cleanup)       ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo ""
    echo "   Python sandbox: $PY_ID  (${PY_IP:-localhost})"
    echo "   Go sandbox:     $GO_ID  (${GO_IP:-localhost})"
    echo ""
    echo "   Test the Go service (calls Python /hello across VMs):"
    echo "     curl -s -X POST $API_URL/sandboxes/$GO_ID/exec \\"
    echo "       -H 'Content-Type: application/json' \\"
    echo "       -d '{\"cmd\":\"curl -sf http://localhost:8080/greeting\"}'"
    echo ""
    echo "   Stop sandboxes:"
    echo "     curl -X DELETE $API_URL/sandboxes/$GO_ID"
    echo "     curl -X DELETE $API_URL/sandboxes/$PY_ID"
    echo ""
else
    echo "8️⃣  Cleaning up..."
    curl -sf -X DELETE "$API_URL/sandboxes/$GO_ID" > /dev/null
    curl -sf -X DELETE "$API_URL/sandboxes/$PY_ID" > /dev/null
    echo "   Sandboxes stopped"
    echo ""

    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║  ✅ Microservice POC complete                               ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
fi
