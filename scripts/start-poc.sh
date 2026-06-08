#!/bin/bash
# start-poc.sh — One-click startup for the Firecracker Sandbox POC.
#
# Automatically downloads dependencies (kernel, firecracker binary), builds Go
# binaries, generates a rootfs image, sets up networking, and starts the
# sandbox API server.
#
# Prerequisites:
#   - Linux host with KVM support (/dev/kvm)
#   - Go 1.22+ installed
#   - Root access (sudo)
#   - curl, tar
#
# Usage:
#   sudo ./scripts/start-poc.sh                          # Interactive language prompt
#   sudo ./scripts/start-poc.sh --language go            # Go sandbox
#   sudo ./scripts/start-poc.sh --language node          # Node.js sandbox
#   sudo ./scripts/start-poc.sh --language universal     # All runtimes in one rootfs
#   sudo ./scripts/start-poc.sh --language go --skip-network  # Skip bridge/TAP setup
#   sudo ./scripts/start-poc.sh --language go --size 4096    # Custom rootfs size (MB)

set -euo pipefail

# ─── Configuration ─────────────────────────────────────────────────────────────
FIRECRACKER_VERSION="${FIRECRACKER_VERSION:-1.8.1}"
KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin"
FIRECRACKER_URL="https://github.com/firecracker-microvm/firecracker/releases/download/v${FIRECRACKER_VERSION}/firecracker-v${FIRECRACKER_VERSION}-x86_64.tgz"

BASE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="$BASE_DIR/bin"
KERNEL_DIR="$BASE_DIR/kernel"
ROOTFS_DIR="$BASE_DIR/rootfs"

KERNEL_PATH="$KERNEL_DIR/vmlinux.bin"
FIRECRACKER_PATH="$BIN_DIR/firecracker"

# ─── Defaults ──────────────────────────────────────────────────────────────────
LANGUAGE=""
SKIP_NETWORK=false
SIZE_MB="${SIZE_MB:-2048}"
CLEANUP_ON_EXIT=true

# ─── Helpers ───────────────────────────────────────────────────────────────────
info()  { echo "   $*"; }
ok()    { echo "   ✓ $*"; }
die()   { echo "❌ $*" >&2; exit 1; }

usage() {
    sed -n '2,/^$/p' "$0" | grep '^#' | sed 's/^# \?//'
    echo ""
    echo "Options:"
    echo "  --language, -l LANG   Language runtime: go, java, node, python, universal"
    echo "  --skip-network        Skip TAP/bridge network setup"
    echo "  --size, -s MB         Rootfs image size (default: 2048)"
    echo "  --no-cleanup          Keep firecracker process on script exit"
    echo "  --help, -h            Show this help"
}

# ─── Argument Parsing ──────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --language|-l) LANGUAGE="${2:?--language requires a value}"; shift 2 ;;
        --skip-network) SKIP_NETWORK=true; shift ;;
        --size|-s)     SIZE_MB="${2:?--size requires a value}"; shift 2 ;;
        --no-cleanup)  CLEANUP_ON_EXIT=false; shift ;;
        --help|-h)     usage; exit 0 ;;
        *) die "Unknown option: $1 (use --help for usage)" ;;
    esac
done

# ─── Step 1: Prerequisites ─────────────────────────────────────────────────────
echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║     🔥 Firecracker Sandbox POC — One-Click Startup         ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

echo "1️⃣  Checking prerequisites..."

[[ "$(uname -s)" == "Linux" ]] || die "Firecracker requires Linux. On macOS, use: make build && ./bin/sandbox-api (simulation mode)"
[[ -e /dev/kvm ]] || die "KVM not available (/dev/kvm not found). Enable virtualization in your BIOS/UEFI."
[[ $EUID -eq 0 ]] || die "This script must be run as root (needs KVM, TAP device, and network access)."
command -v go &>/dev/null || die "Go is not installed. Install Go 1.22+: https://go.dev/dl/"
command -v curl &>/dev/null || die "curl is not installed."
command -v tar &>/dev/null || die "tar is not installed."

ok "Linux + KVM + Go"

# ─── Step 2: Download Firecracker Binary ────────────────────────────────────────
echo ""
echo "2️⃣  Firecracker binary..."

mkdir -p "$BIN_DIR"

if [[ -x "$FIRECRACKER_PATH" ]]; then
    ok "Already exists: $FIRECRACKER_PATH"
    "$FIRECRACKER_PATH" --version 2>&1 | head -1 | sed 's/^/   /' || true
else
    echo "   Downloading Firecracker v${FIRECRACKER_VERSION}..."
    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT

    if ! curl -fsSL --progress-bar -o "$tmpdir/firecracker.tgz" "$FIRECRACKER_URL"; then
        die "Failed to download Firecracker from: $FIRECRACKER_URL"
    fi

    tar -xzf "$tmpdir/firecracker.tgz" -C "$tmpdir"
    # The archive contains: release-v1.x.x-x86_64/firecracker
    fc_bin="$(find "$tmpdir" -name firecracker -type f | head -1)"
    [[ -n "$fc_bin" ]] || die "Could not find firecracker binary in archive"
    cp "$fc_bin" "$FIRECRACKER_PATH"
    chmod +x "$FIRECRACKER_PATH"
    rm -rf "$tmpdir"
    trap - EXIT

    ok "Downloaded: $FIRECRACKER_PATH"
fi

# ─── Step 3: Download Kernel ────────────────────────────────────────────────────
echo ""
echo "3️⃣  Guest kernel..."

mkdir -p "$KERNEL_DIR"

if [[ -f "$KERNEL_PATH" ]]; then
    ok "Already exists: $KERNEL_PATH"
else
    echo "   Downloading kernel..."
    curl -fsSL --progress-bar -o "$KERNEL_PATH" "$KERNEL_URL" \
        || die "Failed to download kernel from: $KERNEL_URL"
    ok "Downloaded: $KERNEL_PATH"
fi

# ─── Step 4: Build Go Binaries ──────────────────────────────────────────────────
echo ""
echo "4️⃣  Building Go binaries..."

cd "$BASE_DIR"
make build
ok "sandbox-api + vm-agent built"

# ─── Step 5: Generate Rootfs ────────────────────────────────────────────────────
echo ""
echo "5️⃣  Rootfs image..."

if [[ -z "$LANGUAGE" ]]; then
    # Interactive prompt
    echo "   Which language runtime?"
    echo ""
    PS3=$'\n   Enter choice [1-6]: '
    select opt in "go" "java" "node" "python" "universal (all runtimes)" "quit"; do
        case "$opt" in
            go|java|node|python) LANGUAGE="$opt"; break ;;
            "universal (all runtimes)") LANGUAGE="universal"; break ;;
            quit) echo "Aborted."; exit 0 ;;
            *) echo "   Please choose 1-6." ;;
        esac
    done
fi

ROOTFS_IMAGE="${ROOTFS_DIR}/${LANGUAGE}.ext4"

if [[ -f "$ROOTFS_IMAGE" ]]; then
    ok "Already exists: $ROOTFS_IMAGE ($(du -h "$ROOTFS_IMAGE" | cut -f1))"
else
    echo "   Generating ${LANGUAGE} rootfs (${SIZE_MB} MB)..."
    SIZE_MB="$SIZE_MB" "$ROOTFS_DIR/generate-rootfs.sh" --language "$LANGUAGE" --output "$ROOTFS_IMAGE"
    ok "Generated: $ROOTFS_IMAGE ($(du -h "$ROOTFS_IMAGE" | cut -f1))"
fi

# ─── Step 6: Setup Networking ──────────────────────────────────────────────────
echo ""
echo "6️⃣  Host networking..."

if $SKIP_NETWORK; then
    info "Skipping (--skip-network flag set)"
else
    "$BASE_DIR/scripts/setup-network.sh"
    ok "Bridge + NAT configured"
fi

# ─── Step 7: Start Sandbox API ─────────────────────────────────────────────────
echo ""
echo "7️⃣  Starting Sandbox API Server..."
echo ""

export FIRECRACKER_BIN="$FIRECRACKER_PATH"
export KERNEL_IMAGE="$KERNEL_PATH"
export ROOTFS_IMAGE="$ROOTFS_IMAGE"

# Cleanup on exit
if $CLEANUP_ON_EXIT; then
    cleanup() {
        echo ""
        echo "🛑 Shutting down..."
        # Kill any firecracker processes we spawned
        pkill -f "firecracker.*--api-sock" 2>/dev/null || true
    }
    trap cleanup EXIT INT TERM
fi

echo "   FIRECRACKER_BIN  = $FIRECRACKER_PATH"
echo "   KERNEL_IMAGE     = $KERNEL_PATH"
echo "   ROOTFS_IMAGE     = $ROOTFS_IMAGE"
echo "   Language         = $LANGUAGE"
echo ""
echo "   API endpoint: http://localhost:8080"
echo "   Health check:  curl http://localhost:8080/health"
echo ""
echo "   Quick test:"
echo "     curl -s -X POST http://localhost:8080/sandboxes \\"
echo "       -H 'Content-Type: application/json' \\"
echo "       -d '{\"language\":\"${LANGUAGE}\",\"vcpus\":2,\"mem_size_mib\":512}' | jq ."
echo ""
echo "   Stop: Ctrl+C or pkill sandbox-api"
echo ""

exec "$BIN_DIR/sandbox-api"
