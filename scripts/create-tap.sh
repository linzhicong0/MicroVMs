#!/bin/bash
# create-tap.sh — Creates a TAP device for a single VM and attaches it to the bridge.
# Usage: ./create-tap.sh <tap_name> <bridge_name>
# Example: ./create-tap.sh tap0 br0

set -euo pipefail

TAP_NAME="${1:?Usage: $0 <tap_name> [bridge_name]}"
BRIDGE_NAME="${2:-br0}"

echo "Creating TAP device: $TAP_NAME on bridge $BRIDGE_NAME"

ip tuntap add "$TAP_NAME" mode tap
ip link set "$TAP_NAME" master "$BRIDGE_NAME"
ip link set "$TAP_NAME" up

echo "✅ TAP device $TAP_NAME created and attached to $BRIDGE_NAME"
