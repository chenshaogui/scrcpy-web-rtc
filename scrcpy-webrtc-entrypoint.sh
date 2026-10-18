#!/usr/bin/env bash
#
# Container entrypoint.
#
# ADB_HOST/ADB_PORT target the local adb-server (default 127.0.0.1:5037), which
# the Node app talks to via @yume-chan/adb-server-node-tcp. Start one inside the
# container so the connector always has something to reach.
#
# Optional ADB_DEVICE_HOST (+ ADB_DEVICE_PORT, default 5555) auto-connects a
# wireless-adb device on startup - useful when the host's adb-server isn't
# reachable from inside the container.

set -euo pipefail

# Direct mode (ADB_DIRECT / --direct-adb) talks to each device's adbd straight
# over TCP and needs no `adb` binary or adb-server at all, so skip every adb
# step below in that mode.
case "${ADB_DIRECT:-}" in
    1|true|TRUE|yes|on|YES|On) DIRECT=1 ;;
    *) DIRECT="" ;;
esac

# 1. Local adb-server so the Node app has something to talk to.
if [[ -z "$DIRECT" ]]; then
    adb start-server >/dev/null 2>&1 || true
fi

# 2. Optionally attach a wireless-adb device.
if [[ -z "$DIRECT" && -n "${ADB_DEVICE_HOST:-}" ]]; then
    port="${ADB_DEVICE_PORT:-5555}"
    echo "[entrypoint] adb connect $ADB_DEVICE_HOST:$port"
    for _ in $(seq 1 10); do
        if adb connect "$ADB_DEVICE_HOST:$port" 2>&1; then
            break
        fi
        sleep 3
    done
    adb devices
fi

# 3. Hand off to the CMD.
exec "$@"
