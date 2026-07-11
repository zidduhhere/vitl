#!/usr/bin/env bash
# Clears any netem qdisc rules applied by apply_netem.sh.
#
# Usage: ./reset_netem.sh [interface]

set -euo pipefail

IFACE="${1:-lo}"

if [ "$(id -u)" -ne 0 ]; then
  echo "This script needs root (tc requires CAP_NET_ADMIN). Try: sudo $0 $*" >&2
  exit 1
fi

tc qdisc del dev "${IFACE}" root 2>/dev/null || true
echo "Cleared netem rules on ${IFACE}."
