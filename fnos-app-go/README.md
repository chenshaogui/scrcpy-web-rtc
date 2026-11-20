# 云手机助手(Go) — fnOS 安装包

纯 Go 实现的服务端（无 Node.js 运行时依赖）打成的飞牛 NAS fpk 安装包。

## 安装包

在 `dist/` 下，按 NAS 架构选一个：

| 文件 | 架构 | 适用 |
|---|---|---|
| `scrcpy-remote-go_0.1.29_arm.fpk` | linux/arm64 (platform=arm) | ARM 服务器（树莓派、Radxa、OrangePi、飞牛 ARM 机型） |
| `scrcpy-remote-go_0.1.29_x86.fpk` | linux/amd64 (platform=x86) | x86_64 飞牛 NAS / 虚拟机 |

> 0.1.4 修复：① ADB 握手加真正的读超时——单客户端 adbd 被其他实例（如另一台 NAS 上的 native 版）占用时会 accept 连接但保持沉默，旧版永久阻塞、页面一直卡"连接中"无报错；现在 10 秒超时报明确错误；② ADB 连接加退避重试，解决 redroid 单客户端 adbd 刚断开时内核 ICMP 负缓存导致的瞬时 "no route to host"（实际 ping/telnet 都通）；③ 启动脚本在主服务早退时立即报错并清理网关进程，便于定位启动失败；④ 修复点断开后立即重连秒显"连接已断开"——旧 socket 的异步 close 事件污染新连接 UI，现断开前摘除处理器 + 事件身份守卫，且服务端真实错误不再被笼统文案覆盖；⑤ "连接中"阶段的 ADB 拨号可随断开立即取消，马上释放单客户端 adbd 槽位供重连；⑥ 修复飞牛桌面客户端黑屏——其 WebView 无 WebCodecs、画面只能走 WebRTC <video> 而自动播放被策略拒绝；现任意点击在手势内解锁播放、被拒时显示点屏提示、play() 日志报真实异常名；⑦ 远程访问（FN Connect 中继/跨 NAT）黑屏补齐 TURN 中继：rtc-offer 携带 TURN 凭据（旧版只发 STUN）、新增 Cloudflare 动态 TURN（与 native 同 API）、fpk 数据目录放 `turn.env` 即可启用（`TURN_URL=cloudflare` + `CF_TURN_KEY_ID`/`CF_TURN_API_TOKEN`，或 `TURN_URL=turn:host:3478` + `TURN_AUTH=user:pass`）；另修正 WS 视频帧 pts 字节序（小端→大端，与页面读取一致）。
> 0.1.3 修复：推送 scrcpy-server.jar 的写窗口 200ms 轮询导致推包极慢/卡死在"连接中"，改为 OKAY 到达即时唤醒（2MB 从 6 秒卡死降到 0.01 秒）。
> 0.1.2 修复：网关 WebSocket 升级转发丢失 query string，导致设备 serial 传不到后端、报 `device not found:`（设备已在线却连不上）。
> 0.1.1 修复：① redroid 首次连接 ADB 认证失败；② 观看端断开后不释放 ADB transport 导致下次连接被 RST 掉线。

在飞牛「应用中心 → 手动安装 / 本地安装」选择对应 fpk 即可。

### 直接 URL 访问

v0.1.29 起，应用中心的“云手机助手(浏览器)”入口直接访问服务端口，不经过飞牛统一网关：

```text
http://<NAS局域网IP>:18012/
```

例如 `http://192.168.3.112:18012/`。直连端口不经过飞牛登录校验，也未启用访问令牌；仅建议在可信局域网使用，不要直接暴露到公网。桌面 iframe 入口仍可照常使用。

## 与现有版本共存

- 应用名 `scrcpy-remote-go`、桌面入口「云手机助手(Go)」，与 native 版（`scrcpy-remote`）和 Docker 版完全独立，可同时安装。
- 网关路径前缀 `/app/scrcpy-remote-go`，内部端口 `18012`，数据目录由 fnOS 的 `TRIM_PKGVAR` 管理（adb 密钥 `adbkey` 与设备列表 `devices.json` 持久化在此，重装后仍保留信任）。
- 不声明 `install_dep_apps`，因此**不需要**先安装官方 `nodejs_v22` 应用。

## 包内结构

```
manifest                  platform=arm/x86，无 nodejs 依赖
cmd/main                  启停脚本：Go server + Go gateway
config/{privilege,resource}
app/
  scrcpy-webrtc            Go 主服务（ELF，交叉编译）
  gateway                  Go 网关代理（unix socket -> 18012 + 静态页）
  server.bin               scrcpy-server v3.1
  web/index.html           内联前端（含 app.js）
  ui/config + ui/images/   桌面入口与图标
```

## 运行架构

与 native 版一致的两进程模型，只是二进制换成 Go：

```
fnOS 统一网关 (unix socket app.sock)
        │  (Go gateway: 路径重写 /app/scrcpy-remote-go/* -> /*)
        ▼
Go gateway ──转发 /api/*──▶ Go server (127.0.0.1:18012)
        │                       │
        └─serve index.html      ├─ ADB over TCP (直连 adbd)
                                ├─ scrcpy v3.1 (H.264/Opus)
                                ├─ WebRTC (pion) + WS 回退
                                └─ MQTT+AES-GCM 可选信令
```

## 重新构建

```bash
cd fnos-app-go
node scripts/build-fpk.mjs              # 打 arm + x86 两个包
node scripts/build-fpk.mjs --only arm64 # 只打 arm
```

脚本会：交叉编译 Go 二进制（`GOOS=linux CGO_ENABLED=0`）→ 组装 payload（二进制 + server.bin + 内联前端页 + 图标）→ 调用 fnpack 打包。fnpack 复用 `fnos-app-native/.tools/fnpack.exe`。

前置条件：仓库根目录已执行 `npm run build`（生成 `public/app.js`）且 `server.bin` 存在（`npm run fetch-server`）。

## 版本说明

0.1.29 修复断开设备连接或关闭应用窗口时未锁屏：会在关闭 scrcpy 会话前向设备发送电源键按下/抬起事件。0.1.28 修复 Go 版音频静音：读取 scrcpy 音频流开头的 codec 元数据后再解析 Opus 帧，并记录音频流读取错误。0.1.27 浏览器入口改为直连 18012 端口，不经过飞牛登录校验；建议仅限可信局域网使用。0.1.26 曾尝试经统一网关鉴权，但直接打开网关地址会遇到飞牛 token 校验问题。0.1.25 增加直连 URL 入口。0.1.24 增加 Go 服务端口直连页面。0.1.23 移除临时 iOS 调试面板和日志上传接口。0.1.22 增加 scrcpy 编码器探测，下拉选择设备支持的视频/音频编码器。0.1.21 修复 Go 包使用旧页面导致的初始化错误。0.1.19 修复快速输入事件丢失并为自定义 scrcpy 参数增加预设下拉选项；0.1.18 修复 iOS H.264 解码器 codec ID。

## 验证记录

打包产物已在 WSL Debian（x86_64）实机验证：
- 主服务 `/api/devices` 返回 `[]`
- 网关 unix socket 转发 API 返回 `200 OK`
- 网关 serve 前端页返回 `200 OK`
- 两进程按 cmd/main 架构启动、优雅退出

## 已知取舍

与 Go 服务端一致：设备→浏览器剪贴板回传、公网隧道（cloudflared/tunwg/wush）未移植；核心投屏/控制链路完整。TURN 中继已支持（0.1.4 ⑦）：远程/跨 NAT 观看时在数据目录放 `turn.env` 启用，详见上文。详见 `../go-version/README.md`。
