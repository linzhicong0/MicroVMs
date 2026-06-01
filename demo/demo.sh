#!/bin/bash
# demo.sh — Demonstrates the Firecracker Sandbox POC end-to-end.
# Requires the sandbox-api server to be running on :8080.
#
# Usage: ./demo/demo.sh

set -euo pipefail

API_URL="${API_URL:-http://localhost:8080}"

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║     🔥 Firecracker Sandbox POC — End-to-End Demo           ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# Check if API is running
echo "1️⃣  Checking API health..."
HEALTH=$(curl -s "$API_URL/health")
echo "   $HEALTH"
echo ""

# Create a Node.js sandbox
echo "2️⃣  Creating Node.js sandbox..."
NODE_SANDBOX=$(curl -s -X POST "$API_URL/sandboxes" \
  -H "Content-Type: application/json" \
  -d '{
    "language": "node",
    "vcpus": 2,
    "mem_size_mib": 512,
    "env_vars": {"NODE_ENV": "development"}
  }')
NODE_ID=$(echo "$NODE_SANDBOX" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null || echo "$NODE_SANDBOX" | jq -r '.id')
echo "   Sandbox ID: $NODE_ID"
echo "   Full response: $NODE_SANDBOX"
echo ""

# Create a Python sandbox in the same network group
echo "3️⃣  Creating Python sandbox (same network group)..."
PY_SANDBOX=$(curl -s -X POST "$API_URL/sandboxes" \
  -H "Content-Type: application/json" \
  -d '{
    "language": "python",
    "vcpus": 2,
    "mem_size_mib": 256,
    "network_group": "my-microservice-app"
  }')
PY_ID=$(echo "$PY_SANDBOX" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null || echo "$PY_SANDBOX" | jq -r '.id')
echo "   Sandbox ID: $PY_ID"
echo ""

# Execute commands
echo "4️⃣  Executing command in Node.js sandbox..."
EXEC_RESULT=$(curl -s -X POST "$API_URL/sandboxes/$NODE_ID/exec" \
  -H "Content-Type: application/json" \
  -d '{"cmd": "echo Hello from Node.js sandbox!", "cwd": "/workspace"}')
echo "   Result: $EXEC_RESULT"
echo ""

# Write a file
echo "5️⃣  Writing file to sandbox..."
curl -s -X PUT "$API_URL/sandboxes/$NODE_ID/files/index.js" \
  -H "Content-Type: application/json" \
  -d '{"content": "const http = require(\"http\");\nconst server = http.createServer((req, res) => {\n  res.end(\"Hello from Firecracker!\");\n});\nserver.listen(3000);\n"}'
echo "   Written: /workspace/index.js"
echo ""

# Read the file back
echo "6️⃣  Reading file from sandbox..."
FILE_CONTENT=$(curl -s "$API_URL/sandboxes/$NODE_ID/files/index.js")
echo "   Content: $FILE_CONTENT"
echo ""

# Snapshot
echo "7️⃣  Creating snapshot..."
SNAP=$(curl -s -X POST "$API_URL/sandboxes/$NODE_ID/snapshot")
echo "   Snapshot: $SNAP"
echo ""

# List all sandboxes
echo "8️⃣  Listing all sandboxes..."
ALL=$(curl -s "$API_URL/sandboxes")
echo "   $ALL"
echo ""

# Clean up
echo "9️⃣  Stopping sandboxes..."
curl -s -X DELETE "$API_URL/sandboxes/$NODE_ID" > /dev/null
curl -s -X DELETE "$API_URL/sandboxes/$PY_ID" > /dev/null
echo "   Done!"
echo ""

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║     ✅ Demo Complete!                                       ║"
echo "║                                                            ║"
echo "║  This POC demonstrates:                                    ║"
echo "║  • Sandbox creation with language-specific configs         ║"
echo "║  • Command execution inside sandboxes                      ║"
echo "║  • File read/write operations                              ║"
echo "║  • VM snapshot for hibernate/resume                        ║"
echo "║  • Network group assignment for microservices              ║"
echo "║  • TAP device allocation per VM                            ║"
echo "╚══════════════════════════════════════════════════════════════╝"
