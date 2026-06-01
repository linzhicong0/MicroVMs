#!/bin/bash
# teardown-network.sh — Removes the network bridge and cleanup NAT rules.
# Must be run as root.

set -euo pipefail

BRIDGE_NAME="${BRIDGE_NAME:-br0}"
HOST_IFACE="${HOST_IFACE:-eth0}"

echo "🧹 Tearing down Firecracker network..."

# Remove all TAP devices attached to the bridge
for tap in $(ls /sys/class/net/ | grep '^tap'); do
    echo "   Removing TAP device: $tap"
    ip link set "$tap" nomaster 2>/dev/null || true
    ip link del "$tap" 2>/dev/null || true
done

# Remove NAT rules
echo "   Removing NAT rules..."
iptables -t nat -D POSTROUTING -o "$HOST_IFACE" -j MASQUERADE 2>/dev/null || true
iptables -D FORWARD -i "$BRIDGE_NAME" -j ACCEPT 2>/dev/null || true
iptables -D FORWARD -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true

# Remove bridge
if ip link show "$BRIDGE_NAME" &>/dev/null; then
    echo "   Removing bridge $BRIDGE_NAME..."
    ip link set "$BRIDGE_NAME" down
    ip link del "$BRIDGE_NAME"
fi

echo "✅ Network teardown complete!"
