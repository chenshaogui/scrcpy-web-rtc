# tango-mirror in a box:  node server.mjs --no-page --no-signal  +  adb
#
# Everything below is multi-arch (linux/amd64 + linux/arm64): the base images
# are multi-arch and adb comes from the distro, so the same Dockerfile builds
# natively for either CPU.
#
#   # one manifest with both architectures (needs somewhere to push it)
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     -t <registry>/tango-mirror:0.6.0 --push .
#
#   # just this machine: --load takes a single platform
#   docker buildx build --platform linux/amd64 -t tango-mirror:amd64 --load .
#   docker buildx build --platform linux/arm64 -t tango-mirror:arm64 --load .
#
# Cross-arch builds run under QEMU: Docker Desktop ships it, on plain Linux do
#   docker run --privileged --rm tonistiigi/binfmt --install all
#
# Run it (defaults to the container's own adb server, devices attached over Wi-Fi):
#   docker run -d --name tango-mirror -p 8010:8010 \
#     -e ADB_DEVICES=192.168.1.7:5555 tango-mirror:amd64
#   docker exec -it tango-mirror adb devices
#
# Rather reuse the adb server already running on the host (USB devices included):
#   -e ADB_HOST=host.docker.internal --add-host host.docker.internal:host-gateway

ARG NODE_VERSION=22

# ---------------------------------------------------------------------------
# stage 1 — browser bundle (esbuild) and the scrcpy server jar (server.bin)
# ---------------------------------------------------------------------------
FROM node:${NODE_VERSION}-bookworm-slim AS build
# must match VERSION in server.mjs
ARG SCRCPY_VERSION=3.1
WORKDIR /app

# 更换为清华镜像源
RUN sed -i 's/deb.debian.org/mirrors.tuna.tsinghua.edu.cn/g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || \
    sed -i 's/deb.debian.org/mirrors.tuna.tsinghua.edu.cn/g' /etc/apt/sources.list

COPY package.json package-lock.json ./
RUN npm ci
COPY public ./public
COPY src ./src
COPY *.mjs ./
RUN npm run build \
 && npx --yes fetch-scrcpy-server "${SCRCPY_VERSION}" \
 && cp node_modules/@yume-chan/fetch-scrcpy-server/server.bin ./server.bin \
 && cp node_modules/@yume-chan/fetch-scrcpy-server/scrcpy.LICENSE ./scrcpy-server.LICENSE

 RUN ls -la /app

# ---------------------------------------------------------------------------
# stage 2 — runtime: node + adb + the bundled page
# ---------------------------------------------------------------------------
FROM node:${NODE_VERSION}-bookworm-slim AS runtime

# adb ships in Debian's android-tools on amd64 and arm64 alike (1.0.41 here,
# which is what scrcpy's server needs); no udev rules — in a container the
# device nodes come from the host
RUN apt-get update \
 && apt-get install -y --no-install-recommends adb \
 && rm -rf /var/lib/apt/lists/* \
 && adb version

ENV NODE_ENV=production \
    PORT=8010 \
    ADB_HOST=127.0.0.1 \
    ADB_PORT=5037

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY server.mjs signal.mjs webrtc.mjs ./
COPY --from=build /app/public ./public
COPY --from=build /app/server.bin ./server.bin
COPY --from=build /app/scrcpy-server.LICENSE ./scrcpy-server.LICENSE

# the app speaks to an adb *server* over TCP (ADB_HOST:ADB_PORT) and never
# spawns the adb binary itself, so start one inside the container and hand it
# the device list before node boots
COPY <<'EOF' /usr/local/bin/tango-entrypoint
#!/bin/sh
set -e

# a host HOME (C:\Users\... or /Users/...) can leak into the container through
# the CLI; adb keeps its keys in $HOME/.android, so fall back to a real path
if [ -z "${HOME:-}" ] || [ ! -w "${HOME}" ]; then
    HOME=/root
    export HOME
fi

adb_host="${ADB_HOST:-127.0.0.1}"
adb_port="${ADB_PORT:-5037}"

case "$adb_host" in
    127.0.0.1|localhost|::1)
        # no -H/-L here on purpose: given an explicit host adb assumes a remote
        # one and refuses to spawn a daemon ("cannot start server on remote host")
        adb -P "$adb_port" start-server >/dev/null
        echo "tango-mirror: $(adb version | head -n1) listening on ${adb_host}:${adb_port}"
        ;;
    *)
        echo "tango-mirror: using the adb server at ${adb_host}:${adb_port}"
        ;;
esac

# set once the local server is up, so `docker exec -it tango-mirror adb devices`
# and the connects below both go through the same server
export ADB_SERVER_SOCKET="tcp:${adb_host}:${adb_port}"

# ADB_DEVICES=192.168.1.7:5555,192.168.1.8:5555, connected before the page loads
for device in $(echo "${ADB_DEVICES:-}" | tr ',' ' '); do
    [ -n "$device" ] || continue
    adb connect "$device" || echo "tango-mirror: adb connect $device failed" >&2
done

exec "$@"
EOF

# guard against a CRLF checkout on Windows, the heredoc keeps bytes verbatim
RUN sed -i 's/\r$//' /usr/local/bin/tango-entrypoint \
 && chmod +x /usr/local/bin/tango-entrypoint

EXPOSE 8010
# adb keys, so device authorizations survive a container rebuild
VOLUME ["/root/.android"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD node -e "fetch('http://127.0.0.1:'+(process.env.PORT||8010)+'/').then(r=>process.exit(r.status<500?0:1)).catch(()=>process.exit(1))"

ENTRYPOINT ["tango-entrypoint"]
CMD ["node", "server.mjs", "--no-page", "--no-signal"]
