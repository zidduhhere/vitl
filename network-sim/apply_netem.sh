#!/usr/bin/env bash
# Injects loss + a bandwidth cap on an interface to simulate the
# "extreme network conditions" constraint on the field-worker link.
# Requires Linux (tc/netem) — use WSL2 if developing on Windows.
#
# Usage: ./apply_netem.sh [interface] [loss_pct] [rate_kbit] [delay_ms]
#   ./apply_netem.sh lo 20 64 100

set -euo pipefail

IFACE="${1:-lo}"
LOSS_PCT="${2:-20}"
RATE_KBIT="${3:-64}"
DELAY_MS="${4:-100}"

if [ "$(id -u)" -ne 0 ]; then
  echo "This script needs root (tc requires CAP_NET_ADMIN). Try: sudo $0 $*" >&2
  exit 1
fi

echo "Applying netem on ${IFACE}: ${LOSS_PCT}% loss, ${RATE_KBIT}kbit cap, ${DELAY_MS}ms delay"

# Remove any existing qdisc on this interface first so reapplying is safe.
tc qdisc del dev "${IFACE}" root 2>/dev/null || true

tc qdisc add dev "${IFACE}" root netem loss "${LOSS_PCT}%" delay "${DELAY_MS}ms" rate "${RATE_KBIT}kbit"

echo "Applied. Verify with: tc qdisc show dev ${IFACE}"
echo "Reset with: ./reset_netem.sh ${IFACE}"
