#!/bin/sh
set -e

if [ -z "${HOME:-}" ] || [ ! -w "${HOME}" ]; then
    HOME=/root
    export HOME
fi

adb_host="${ADB_HOST:-127.0.0.1}"
adb_port="${ADB_PORT:-5555}"

case "$adb_host" in
    127.0.0.1|localhost|::1)
        adb -P "$adb_port" start-server >/dev/null
        echo "tango-mirror: $(adb version | head -n1) listening on ${adb_host}:${adb_port}"
        ;;
    *)
        echo "tango-mirror: using the adb server at ${adb_host}:${adb_port}"
        ;;
esac

export ADB_SERVER_SOCKET="tcp:${adb_host}:${adb_port}"

for device in $(echo "${ADB_DEVICES:-}" | tr ',' ' '); do
    [ -n "$device" ] || continue
    adb connect "$device" || echo "tango-mirror: adb connect $device failed" >&2
done

exec "$@"