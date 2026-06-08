#!/bin/bash
# generate-rootfs.sh — Interactively generates an ext4 rootfs image for Firecracker.
#
# Supported runtimes: go (Go 1.22+), java (OpenJDK 21), node (Node.js + npm),
#                     python (Python 3 + pip), universal (all of the above)
#
# Usage (interactive):
#   sudo ./rootfs/generate-rootfs.sh
#
# Usage (non-interactive):
#   sudo ./rootfs/generate-rootfs.sh --language go
#   sudo ./rootfs/generate-rootfs.sh --language node --output /path/to/node.ext4
#   sudo ./rootfs/generate-rootfs.sh --language universal
#
# Environment variables:
#   SIZE_MB      — image size in MB (default: 2048)
#   ALPINE_VER   — Alpine version to use   (default: 3.20)
#   ARCH         — CPU architecture        (default: x86_64)

set -euo pipefail

# ─── defaults ────────────────────────────────────────────────────────────────
LANGUAGE=""
OUTPUT=""
SIZE_MB="${SIZE_MB:-2048}"
ALPINE_VER="${ALPINE_VER:-3.20}"
ARCH="${ARCH:-$(uname -m)}"
ALPINE_MIRROR="https://dl-cdn.alpinelinux.org/alpine"

# ─── helpers ─────────────────────────────────────────────────────────────────
die()  { echo "❌ $*" >&2; exit 1; }
info() { echo "   $*"; }

# Normalize architecture for official download URLs.
# Alpine uses x86_64/aarch64; Go/Node use amd64/arm64.
download_arch() {
    case "${1:-$ARCH}" in
        x86_64)  echo "amd64" ;;
        aarch64) echo "arm64" ;;
        *)       echo "$ARCH" ;;
    esac
}

cleanup() {
    local rc=$?
    if mountpoint -q "${MOUNT_DIR:-}" 2>/dev/null; then
        # Unmount any bind-mounts first, then the image itself
        umount "${MOUNT_DIR}/proc"  2>/dev/null || true
        umount "${MOUNT_DIR}/sys"   2>/dev/null || true
        umount "${MOUNT_DIR}/dev"   2>/dev/null || true
        umount "${MOUNT_DIR}"       2>/dev/null || true
    fi
    [[ -d "${MOUNT_DIR:-}" ]] && rmdir "${MOUNT_DIR}" 2>/dev/null || true
    rm -f "${MINIROOTFS_TMP:-}"
    exit $rc
}
trap cleanup EXIT

# ─── argument parsing ─────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --language|-l) LANGUAGE="${2:?--language requires a value}"; shift 2 ;;
        --output|-o)   OUTPUT="${2:?--output requires a value}";     shift 2 ;;
        --size|-s)     SIZE_MB="${2:?--size requires a value}";      shift 2 ;;
        --help|-h)
            sed -n '2,/^$/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) die "Unknown option: $1  (use --help for usage)" ;;
    esac
done

# ─── root check ──────────────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "This script must be run as root (needs mount/chroot)."

# ─── dependency check ────────────────────────────────────────────────────────
for cmd in dd mkfs.ext4 mount umount chroot curl tar mountpoint; do
    command -v "$cmd" &>/dev/null || die "Required command not found: $cmd"
done

# ─── interactive language selection ──────────────────────────────────────────
if [[ -z "$LANGUAGE" ]]; then
    echo ""
    echo "🔥  Firecracker rootfs generator"
    echo "================================="
    echo "Which language runtime do you want in the rootfs?"
    echo ""
    PS3=$'\nEnter choice [1-6]: '
    select opt in "go (Go 1.22+)" "java (OpenJDK 21 + Maven)" "node (Node.js + npm)" "python (Python 3 + pip)" "universal (all runtimes)" "quit"; do
        case "$opt" in
            "go (Go 1.22+)")
                LANGUAGE="go"
                break
                ;;
            "java (OpenJDK 21 + Maven)")
                LANGUAGE="java"
                break
                ;;
            "node (Node.js + npm)")
                LANGUAGE="node"
                break
                ;;
            "python (Python 3 + pip)")
                LANGUAGE="python"
                break
                ;;
            "universal (all runtimes)")
                LANGUAGE="universal"
                break
                ;;
            quit)
                echo "Aborted."
                exit 0
                ;;
            *)
                echo "Please choose 1-6."
                ;;
        esac
    done
fi

# Validate language
case "$LANGUAGE" in
    go|java|node|python|universal) ;;
    *) die "Unsupported language: '$LANGUAGE'. Supported: go, java, node, python, universal" ;;
esac

OUTPUT="${OUTPUT:-rootfs/${LANGUAGE}.ext4}"

# ─── summary ─────────────────────────────────────────────────────────────────
echo ""
echo "🏗️   Building rootfs"
echo "     Language : $LANGUAGE"
echo "     Output   : $OUTPUT"
echo "     Size     : ${SIZE_MB} MB"
echo "     Alpine   : ${ALPINE_VER} (${ARCH})"
echo ""

mkdir -p "$(dirname "$OUTPUT")"

# ─── download Alpine minirootfs ───────────────────────────────────────────────
MINIROOTFS_URL="${ALPINE_MIRROR}/v${ALPINE_VER}/releases/${ARCH}/alpine-minirootfs-${ALPINE_VER}.0-${ARCH}.tar.gz"
MINIROOTFS_TMP="$(mktemp /tmp/alpine-minirootfs.XXXXXX.tar.gz)"
# Make sure temp file is cleaned up on exit regardless of trap order
trap 'rm -f "$MINIROOTFS_TMP"' INT TERM

echo "⬇️   Downloading Alpine ${ALPINE_VER} minirootfs..."
curl -fsSL --progress-bar -o "$MINIROOTFS_TMP" "$MINIROOTFS_URL" \
    || die "Failed to download Alpine minirootfs from: $MINIROOTFS_URL"

# ─── create ext4 image ───────────────────────────────────────────────────────
echo "💾  Creating ${SIZE_MB} MB ext4 image: $OUTPUT"
dd if=/dev/zero of="$OUTPUT" bs=1M count="$SIZE_MB" status=progress
mkfs.ext4 -F -O ^has_journal -L "rootfs-${LANGUAGE}" "$OUTPUT"

# ─── mount image ─────────────────────────────────────────────────────────────
MOUNT_DIR="$(mktemp -d /tmp/rootfs-mount.XXXXXX)"
mount -o loop "$OUTPUT" "$MOUNT_DIR"

# ─── extract Alpine base system ──────────────────────────────────────────────
echo "📦  Extracting Alpine base system..."
tar -xzf "$MINIROOTFS_TMP" -C "$MOUNT_DIR"
rm -f "$MINIROOTFS_TMP"

# Bind-mount kernel filesystems so apk and chroot work correctly
mount -t proc  none "$MOUNT_DIR/proc"
mount -t sysfs none "$MOUNT_DIR/sys"
mount --bind /dev   "$MOUNT_DIR/dev"

# Copy host DNS resolver so apk can reach the internet inside the chroot
cp /etc/resolv.conf "$MOUNT_DIR/etc/resolv.conf"

# ─── update Alpine package index ─────────────────────────────────────────────
echo "🔄  Updating Alpine package index..."
chroot "$MOUNT_DIR" apk update --no-progress

# ─── install common packages ─────────────────────────────────────────────────
echo "📦  Installing base packages..."
chroot "$MOUNT_DIR" apk add --no-progress \
    ca-certificates \
    openssl \
    bash \
    curl \
    git \
    musl-dev

# ─── install language-specific packages ──────────────────────────────────────
case "$LANGUAGE" in
    go|universal)
        GO_VER="${GO_VERSION:-1.22.10}"
        DL_ARCH="$(download_arch)"
        GO_TAR="go${GO_VER}.linux-${DL_ARCH}.tar.gz"
        echo "🐹  Installing Go ${GO_VER} (direct download)..."
        curl -fsSL --progress-bar "https://go.dev/dl/${GO_TAR}" \
            -o "/tmp/${GO_TAR}" \
            || die "Failed to download Go from https://go.dev/dl/${GO_TAR}"
        tar -C "$MOUNT_DIR/usr/local" -xzf "/tmp/${GO_TAR}"
        rm -f "/tmp/${GO_TAR}"
        chroot "$MOUNT_DIR" /usr/local/go/bin/go version
        mkdir -p "$MOUNT_DIR/root/go/src" \
                 "$MOUNT_DIR/root/go/pkg" \
                 "$MOUNT_DIR/root/go/bin"
        cat >> "$MOUNT_DIR/etc/profile" << 'GOENV'

# Go environment
export GOPATH=/root/go
export PATH="$PATH:/usr/local/go/bin:$GOPATH/bin"
GOENV
        ;;&  # fall through to next match
    java|universal)
        echo "☕  Installing OpenJDK 21 and Maven..."
        chroot "$MOUNT_DIR" apk add --no-progress \
            openjdk21-jre \
            openjdk21-jdk \
            maven
        chroot "$MOUNT_DIR" java -version
        chroot "$MOUNT_DIR" mvn  --version
        JAVA_HOME_PATH="$(chroot "$MOUNT_DIR" sh -c 'readlink -f /usr/bin/java | sed "s|/bin/java||"')"
        cat >> "$MOUNT_DIR/etc/profile" << JAVAENV

# Java environment
export JAVA_HOME=${JAVA_HOME_PATH}
export PATH="\$PATH:\$JAVA_HOME/bin"
JAVAENV
        ;;&  # fall through to next match
    node|universal)
        NODE_VER="${NODE_VERSION:-22.12.0}"
        DL_ARCH="$(download_arch)"
        NODE_TAR="node-v${NODE_VER}-linux-${DL_ARCH}.tar.xz"
        echo "🟢  Installing Node.js ${NODE_VER} (direct download)..."
        curl -fsSL --progress-bar "https://nodejs.org/dist/v${NODE_VER}/${NODE_TAR}" \
            -o "/tmp/${NODE_TAR}" \
            || die "Failed to download Node.js from https://nodejs.org/dist/v${NODE_VER}/${NODE_TAR}"
        tar -C "$MOUNT_DIR/usr/local" --strip-components=1 -xJf "/tmp/${NODE_TAR}"
        rm -f "/tmp/${NODE_TAR}"
        chroot "$MOUNT_DIR" node --version
        chroot "$MOUNT_DIR" npm --version
        ;;&  # fall through to next match
    python|universal)
        echo "🐍  Installing Python runtime..."
        chroot "$MOUNT_DIR" apk add --no-progress \
            python3 \
            py3-pip \
            py3-virtualenv
        chroot "$MOUNT_DIR" python3 --version
        chroot "$MOUNT_DIR" pip3 --version
        ;;
esac

# ─── workspace directory ─────────────────────────────────────────────────────
mkdir -p "$MOUNT_DIR/workspace"

# ─── networking configuration ────────────────────────────────────────────────
cat > "$MOUNT_DIR/etc/network/interfaces" << 'EOF'
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet static
    address 10.0.0.2/24
    gateway 10.0.0.1
EOF

# DNS
cat > "$MOUNT_DIR/etc/resolv.conf" << 'EOF'
nameserver 8.8.8.8
nameserver 8.8.4.4
EOF

# Hostname
echo "firecracker-vm" > "$MOUNT_DIR/etc/hostname"

# ─── vm-agent init script (OpenRC) ───────────────────────────────────────────
mkdir -p "$MOUNT_DIR/etc/init.d"
cat > "$MOUNT_DIR/etc/init.d/vm-agent" << 'EOF'
#!/sbin/openrc-run
description="Firecracker VM agent"

command="/usr/local/bin/vm-agent"
command_background=true
pidfile="/run/vm-agent.pid"

depend() {
    after net
}
EOF
chmod +x "$MOUNT_DIR/etc/init.d/vm-agent"

# Enable vm-agent at boot (if binary exists at build time; safe to ignore otherwise)
chroot "$MOUNT_DIR" sh -c \
    'rc-update add vm-agent default 2>/dev/null || true'

# ─── inittab / getty ─────────────────────────────────────────────────────────
# Ensure a serial console getty is available for debugging
if [[ -f "$MOUNT_DIR/etc/inittab" ]]; then
    grep -q 'ttyS0' "$MOUNT_DIR/etc/inittab" || \
        echo 'ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100' \
            >> "$MOUNT_DIR/etc/inittab"
fi

# ─── unmount ─────────────────────────────────────────────────────────────────
echo "🔧  Finalizing image..."
umount "$MOUNT_DIR/proc"
umount "$MOUNT_DIR/sys"
umount "$MOUNT_DIR/dev"
umount "$MOUNT_DIR"
rmdir  "$MOUNT_DIR"
# Clear MOUNT_DIR so the trap's mountpoint check is a no-op
MOUNT_DIR=""

# ─── done ────────────────────────────────────────────────────────────────────
echo ""
echo "✅  Rootfs image ready: $OUTPUT"
echo ""
echo "To attach to a Firecracker VM:"
echo ""
echo "  PUT http+unix://firecracker.sock/drives/rootfs \\"
echo "      '{\"drive_id\":\"rootfs\",\"path_on_host\":\"${OUTPUT}\",\"is_root_device\":true,\"is_read_only\":false}'"
echo ""
echo "Or set the env var and start the sandbox API:"
echo "  export ROOTFS_IMAGE=${OUTPUT}"
echo "  sudo -E ./bin/sandbox-api"
