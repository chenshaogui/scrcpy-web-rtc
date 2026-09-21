ARG NODE_VERSION=22

# ---------------------------------------------------------------------------
# stage 1 — browser bundle (esbuild) and the scrcpy server jar (server.bin)
# ---------------------------------------------------------------------------
FROM node:${NODE_VERSION}-bookworm-slim
# must match VERSION in server.mjs
ARG SCRCPY_VERSION=3.1
WORKDIR /app

# 更换为清华镜像源
RUN sed -i 's/deb.debian.org/mirrors.tuna.tsinghua.edu.cn/g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || \
    sed -i 's/deb.debian.org/mirrors.tuna.tsinghua.edu.cn/g' /etc/apt/sources.list

# 安装 ADB 工具
RUN apt-get update && \
    apt-get install -y \
        android-tools-adb \
        python3 \
        python3-pip \
        make \
        g++ \
        gcc \
        libc6-dev \
        build-essential \
        && \
    rm -rf /var/lib/apt/lists/*

# 使用标准 npm + 淘宝镜像，避免 cnpm 生成的 esbuild 启动脚本把 ELF 当 JS 解析
RUN npm config set registry https://registry.npmmirror.com

# 安装依赖
RUN npm install

# 确保 esbuild 的原生二进制被 npm 正确链接（覆盖 cnpm/异常安装可能留下的错误包装脚本）
RUN npm install esbuild --no-save --registry=https://registry.npmmirror.com

# 构建前端 + 下载 scrcpy server
RUN npm run build \
 && npx --yes fetch-scrcpy-server "${SCRCPY_VERSION}"

ENV NODE_ENV=production \
    PORT=8010 \
    ADB_HOST=127.0.0.1 \
    ADB_PORT=5555

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
adb_port="${ADB_PORT:-5555}"

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