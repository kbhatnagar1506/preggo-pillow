#!/usr/bin/env bash
# Put the Pi on its own WiFi hotspot so the demo never touches venue WiFi.
#
# Venue WiFi is the most common way a hardware demo dies: captive portals,
# congested 2.4GHz, a Pi that silently cannot resolve DNS at hour 35.
# Nothing in the live demo may depend on the network.
set -euo pipefail

SSID="${1:-lull-demo}"
PASS="${2:-lullbaby2026}"

echo "creating hotspot SSID=$SSID"
sudo nmcli device wifi hotspot ifname wlan0 ssid "$SSID" password "$PASS"
echo
echo "connect the demo laptop to '$SSID', then open:"
nmcli -g IP4.ADDRESS device show wlan0 | head -1 | cut -d/ -f1 | sed 's|^|  http://|; s|$|:8080|'
