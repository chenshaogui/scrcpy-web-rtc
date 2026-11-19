# scrcpy-webrtc-go

完整的 Go 语言服务端重写——**不使用 Node.js，不留任何 Node 组件**。浏览器前端仍是 JS（浏览器无法直接运行 Go），但与 Node 实现共用同一份 `public/` 前端。

## 架构对照

| 模块 | Node 实现 | Go 实现 |
|---|---|---|
| ADB over TCP | `@yume-chan/adb` | `internal/adb`（自研协议栈：packet/AUTH-RSA/socket 复用/sync push/reverse/shell v2） |
| scrcpy 协议 | `@yume-chan/scrcpy` + `adb-scrcpy` | `internal/scrcpy`（选项序列化、server 启动、媒体分包、控制消息） |
| WebRTC | `werift` | `pion/webrtc/v4` + `internal/rtp`（H264 FU-A / RTP） |
| 信令 | `signal.mjs`（MQTT over WS + AES-GCM） | `internal/signal`（自研 MQTT 3.1.1 QoS-0 + AES-256-GCM） |
| HTTP/WS API | `server.mjs` | `internal/server` |
| fnOS 网关代理 | `proxy.mjs` | `cmd/gateway`（独立工具） |

## 构建

需要 Go 1.22+。构建脚本会自动从项目根复制前端（`public/`）和 scrcpy-server（`server.bin`），然后编译单文件二进制：

```bash
node go-version/build.mjs
# 产物：go-version/dist/scrcpy-webrtc[.exe] + dist/public/ + dist/server.bin
```

国内网络会自动用 `goproxy.cn`。

## 运行

把 `dist/` 整个目录拷到目标机器，运行二进制：

```bash
cd dist
./scrcpy-webrtc                    # 默认 8010 端口，信令开启（需 --page）
./scrcpy-webrtc --no-signal --no-page   # 纯本地
./scrcpy-webrtc --port 9000 --shell --token mysecret
./scrcpy-webrtc --direct-adb       # 直连 adbd（等同 ADB_DIRECT=1）
```

参数与 Node 版一致：`--port` / `--shell` / `--token` / `--page` / `--no-page` / `--no-signal` / `--signal` / `--signal-broker` / `--turn` / `--turn-auth` / `--video-encoder` / `--audio-encoder` / `--direct-adb` / `--no-qr`。

环境变量：`PORT`、`ADB_DIRECT`、`SCRCPY_WEBRTC_TOKEN`、`SCRCPY_WEBRTC_PAGE`、`VIDEO_ENCODER`、`AUDIO_ENCODER`、`TURN_URL`、`TURN_AUTH`、`CF_TURN_KEY_ID`、`CF_TURN_API_TOKEN`、`SCRCPY_WEBRTC_SIGNAL_BROKERS`、`TRIM_PKGVAR`（设备列表与密钥持久化目录）。

## 测试

```bash
cd go-version
go test ./... -v
```

覆盖：ADB packet 帧/校验和/命令字节/mincrypt 公钥/签名、scrcpy 全部控制消息字节布局/选项序列化/媒体包解析、AES-GCM 与 MQTT 编码、网关代理（HTTP + WebSocket 隧道）。

## 实现说明与已知取舍

- **平台**：面向直连 adbd（无线 ADB / redroid），与 native fpk 的 `ADB_DIRECT=1` 路径一致；不依赖宿主机 `adb` 或 adb-server。
- **加密认证**：RSA-2048 PKCS#1 v1.5 签名（token 直接作为 SHA-1 DigestInfo），首次配对回退到 mincrypt 公钥认证；密钥持久化在 `$TRIM_PKGVAR/adbkey`。
- **流媒体**：scrcpy-server 的 H.264 Annex-B 经 FU-A 分包写入 pion RTP 轨道；Opus 走 48kHz RTP；P2P 未建立时视频/音频带 10 字节头走 WebSocket 回退。
- **剪贴板**：浏览器→设备方向已实现；设备→浏览器方向（控制 socket 上的 device message 多路复用）在此移植中留作 TODO。
- **TURN**：静态 `turn:`/`turns:` URL 与 Cloudflare 动态凭证（`--turn cloudflare` + `CF_TURN_KEY_ID`/`CF_TURN_API_TOKEN`，缓存 12h）均已实现；rtc-offer 会携带 TURN 凭据供浏览器生成中继候选（远程/跨 NAT 观看必需）。
- **隧道**（cloudflared/tunwg/wush）：属于可选的公网暴露层，本 Go 实现聚焦服务端核心，未移植隧道进程管理；可在此服务前挂任意反向代理/隧道。
- **fnOS 网关代理**：`cmd/gateway` 是独立工具，可替代 native 包的 `proxy.cjs`；本服务也内置了静态页服务，可直接用 `--no-page` + 网关。

## 目录结构

```
go-version/
├── go.mod / go.sum
├── build.mjs                       # 构建脚本（复制资源 + go build）
├── cmd/
│   ├── scrcpy-webrtc/main.go       # 主服务端
│   └── gateway/main.go             # fnOS 网关代理（可选）
├── internal/
│   ├── adb/                        # ADB over TCP 协议栈
│   ├── scrcpy/                     # scrcpy 协议
│   ├── rtp/                        # H264/Opus RTP 封包
│   ├── rtc/                        # pion WebRTC 会话
│   ├── signal/                     # MQTT 信令 + AES-GCM
│   ├── server/                     # HTTP/WS API + 会话管理
│   └── gateway/                    # 网关代理核心
└── dist/                           # 构建输出（未提交）
```
