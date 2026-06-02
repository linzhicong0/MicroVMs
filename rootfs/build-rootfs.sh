#!/bin/bash
# build-rootfs.sh — Builds a minimal rootfs ext4 image for a given language.
# This is a reference script showing how to create language-specific images.
#
# Usage: ./build-rootfs.sh <language> <output_path>
# Example: ./build-rootfs.sh node rootfs/node.ext4
#
# In production, this would be part of a CI/CD pipeline that:
# 1. Creates a rootfs image
# 2. Boots it in Firecracker
# 3. Takes a base snapshot
# 4. Stores the snapshot for instant resume

set -euo pipefail

LANGUAGE="${1:?Usage: $0 <language> [output_path]}"
OUTPUT="${2:-rootfs/${LANGUAGE}.ext4}"
SIZE_MB="${SIZE_MB:-2048}"

echo "🏗️  Building rootfs for: $LANGUAGE"
echo "   Output: $OUTPUT"
echo "   Size: ${SIZE_MB}MB"

mkdir -p "$(dirname "$OUTPUT")"

# Create empty ext4 image
dd if=/dev/zero of="$OUTPUT" bs=1M count="$SIZE_MB" status=progress
mkfs.ext4 "$OUTPUT"

# Mount and populate
MOUNT_DIR=$(mktemp -d)
mount -o loop "$OUTPUT" "$MOUNT_DIR"

# Install base system (using debootstrap for Debian or alpine-make-rootfs for Alpine)
# For this reference, we show the Alpine approach:
echo "   Installing Alpine base system..."
# In production: alpine-make-rootfs or debootstrap

# Create essential directories
mkdir -p "$MOUNT_DIR"/{bin,sbin,usr/bin,usr/sbin,etc/init.d,etc/network,proc,sys,dev,tmp,root,workspace}

# Install the VM agent binary
echo "   Installing VM agent..."
# In production: copy the compiled vm-agent binary
# cp bin/vm-agent "$MOUNT_DIR/usr/local/bin/vm-agent"

# Install language runtime
case "$LANGUAGE" in
  node)
    echo "   Installing Node.js LTS..."
    # In production: curl -fsSL https://nodejs.org/dist/v20.x/... | tar -xz
    # Also install: npm, pnpm, yarn
    ;;
  python)
    echo "   Installing Python 3.12..."
    # In production: apk add python3 py3-pip
    # Also install: uv, poetry
    ;;
  java)
    echo "   Installing OpenJDK 21..."
    # In production: apk add openjdk21-jre
    # Also install: maven, gradle
    ;;
  go)
    echo "   Installing Go 1.22..."
    # In production: curl -fsSL https://go.dev/dl/go1.22.linux-amd64.tar.gz | tar -xz
    ;;
  rust)
    echo "   Installing Rust stable..."
    # In production: rustup installer
    ;;
  universal)
    echo "   Installing universal base (git, curl, build-essential)..."
    # In production: apk add git curl make gcc
    ;;
  *)
    echo "   Unknown language: $LANGUAGE (installing base only)"
    ;;
esac

# Create init script that starts the VM agent
cat > "$MOUNT_DIR/etc/init.d/vm-agent" << 'EOF'
#!/bin/sh
# Start the VM agent on boot
/usr/local/bin/vm-agent &
EOF
chmod +x "$MOUNT_DIR/etc/init.d/vm-agent"

# Configure networking (will be overridden by MMDS at boot)
cat > "$MOUNT_DIR/etc/network/interfaces" << 'EOF'
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet static
    address 10.0.0.2/24
    gateway 10.0.0.1
EOF

# Unmount
umount "$MOUNT_DIR"
rmdir "$MOUNT_DIR"

echo "✅ Rootfs image created: $OUTPUT"
echo ""
echo "To use with Firecracker:"
echo "  PUT /drives/rootfs {\"drive_id\": \"rootfs\", \"path_on_host\": \"$OUTPUT\", \"is_root_device\": true}"
