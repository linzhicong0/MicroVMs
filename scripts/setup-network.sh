#!/bin/bash
# setup-network.sh — Creates the host bridge and NAT rules for Firecracker MicroVMs.
# Must be run as root.
#
# This script sets up:
# 1. A Linux bridge (br0) at 10.0.0.1/24
# 2. IP forwarding
# 3. NAT/masquerade for internet access from VMs
# 4. Forwarding rules for inter-VM communication

set -euo pipefail

BRIDGE_NAME="${BRIDGE_NAME:-br0}"
BRIDGE_IP="${BRIDGE_IP:-10.0.0.1/24}"
HOST_IFACE="${HOST_IFACE:-eth0}"

echo "🌐 Setting up Firecracker network..."
echo "   Bridge: $BRIDGE_NAME ($BRIDGE_IP)"
echo "   Host interface: $HOST_IFACE"

# Create bridge if it doesn't exist
if ! ip link show "$BRIDGE_NAME" &>/dev/null; then
    echo "   Creating bridge $BRIDGE_NAME..."
    ip link add "$BRIDGE_NAME" type bridge
    ip addr add "$BRIDGE_IP" dev "$BRIDGE_NAME"
    ip link set "$BRIDGE_NAME" up
else
    echo "   Bridge $BRIDGE_NAME already exists"
fi

# Enable IP forwarding
echo "   Enabling IP forwarding..."
echo 1 > /proc/sys/net/ipv4/ip_forward

# Setup NAT (masquerade for outbound traffic from VMs)
echo "   Setting up NAT rules..."
iptables -t nat -C POSTROUTING -o "$HOST_IFACE" -j MASQUERADE 2>/dev/null || \
    iptables -t nat -A POSTROUTING -o "$HOST_IFACE" -j MASQUERADE

# Allow forwarding from bridge
iptables -C FORWARD -i "$BRIDGE_NAME" -j ACCEPT 2>/dev/null || \
    iptables -A FORWARD -i "$BRIDGE_NAME" -j ACCEPT

iptables -C FORWARD -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
    iptables -A FORWARD -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT

echo "✅ Network setup complete!"
echo ""
echo "To create a TAP device for a VM:"
echo "  ip tuntap add tapN mode tap"
echo "  ip link set tapN master $BRIDGE_NAME"
echo "  ip link set tapN up"
