# 已知问题：飞牛 iOS App 内嵌页面（WKWebView）快速连点丢失触摸

## 现象
- 设备：iPhone 11 / iOS 17.7，通过 **飞牛 iOS App 内嵌 WKWebView** 打开本项目的 H5 控制页。
- 手指**快速连续点击**（如连续输入数字）时，约一半点击丢失；放慢点击正常。
- **同一页面、同一服务端，用 iPhone Safari 打开时快速连点完全正常，不复现。**

## 定位过程与结论
在页面 `window` 的 **capture 阶段**常驻记录物理触控，并在服务端每成功注入一个 touch 后回执（ACK）对账。一次"快点 10 下"的实测：

| 环节 | 计数 |
|---|---|
| 物理点击 | 10 |
| 页面收到 `touchstart`（capture） | 7 |
| 客户端发出 `down` | 7 |
| 服务端收到并注入（ACK） | 7 |
| 设备实际显示 | 约一半 |

- **缺失的 3 下从未被派发到网页**：不是页面丢弃、不是 WebRTC/网络丢包、也不是服务端/ redroid 未处理。
- 已送达页面的触摸 **100% 成对发出且服务端全部收到**（通道/服务端零丢失）。
- 页面侧该关的都关了：`viewport user-scalable=no, maximum-scale=1`、`touch-action:none`、`-webkit-touch-callout:none`、`user-select:none`，WebKit 页面内的双击缩放/滚动等手势均已禁用。

**根因判定：发生在飞牛 App 的 WKWebView 容器层**——典型为承载第三方页的 `WKWebView.scrollView.canCancelContentTouches = YES`（默认），或其上挂载的原生 `UIGestureRecognizer`（tap / 双击 / 滑动返回等）在连续快点时识别到手势，取消了正在投递给网页的触摸序列。**非本页、非服务端问题。**

## 请求 App 侧处理（待飞牛确认）
1. 对承载第三方应用的 WKWebView 设置 `scrollView.canCancelContentTouches = NO`；
2. 或将自定义手势识别器设 `cancelsTouchesInView = NO`（必要时 `delaysTouchesBegan = false`）；
3. 或提供开关，允许此类"需要原始连续触摸"的应用页面关闭容器手势识别。

## 当前用户侧临时绕法
- 用 iPhone **Safari** 打开同一地址，并"添加到主屏幕"以 standalone 方式运行（体验接近独立 App，触摸不丢失）。
- 或在 App 内放慢点击节奏。

## 本仓库内已做的相关修复（保留）
- 0.1.15 客户端：去掉 `onpointerdown` 里的 `if (!e.isPrimary) return;`，改为只认最新指针，保证 iOS 上 DOWN/UP 严格成对（解决另一类真实的隔一丢一）。
- 0.1.10/0.1.11：pointermove rAF 帧合并、blur/visibilitychange 兜底释放卡住的手指、中文输入法 composition 处理。
- 服务端：控制消息单条 FIFO 串行队列 + ≥12ms 节流（`MIN_CONTROL_GAP_MS`），消除 down/up 在共享 ADB 控制 socket 上乱序。（注：此节流对人手快连不触发，主要是顺序保证，非本次漏键的解。）

## 请求 App 侧处理之二：绕道系统浏览器的 openURL 不生效（0.1.37/0.1.38 真机实测）
为把页面从上述 WKWebView 搬到 Safari，我们按开放平台文档接了 JS SDK（`@trimjs/web-app`，manifest 已声明 `micro_app=true`）：
```ts
await new TrimApp().openURL("https://<fnOS 域名>/app/scrcpy-remote", "_blank");
```
实测：Promise 正常 resolve（说明请求确实发出去了），但系统浏览器没有被拉起，页面仍在前台。结合 SDK 源码判断是宿主侧 handler 未生效：
- `openURL` 在移动端分支只做 `callAppMethod("openSystemBrowser", url)`；
- `callAppMethod` 开头 `if (!this.flutterInAppWebView) return null;` —— 桥不存在时静默返回，不抛错；
- `index.d.ts` 里 `openURL(...): Promise<unknown>`，成功与否没有回传信号，页面无法自查（我们改用 `document.visibilityState` 转 hidden 来间接判定）。

请飞牛侧确认/处理：
1. App 内嵌微应用的 WebView 是否注册了 `openSystemBrowser` handler（Flutter InAppWebView `callHandler` 名），未注册则文档所述“移动 WebView 宿主中会用系统浏览器打开 URL”实际不成立；
2. 若已注册，是否对 URL 有域名/协议白名单限制（我们的地址是 fnOS 网关路径 `/app/<appname>`，含 FN Connect 中继 origin 与自建 DDNS 两种），被拦时请给可观测的错误回传；
3. 建议 SDK 让 `openURL` 回传真实结果（成功/未实现/被策略拒绝），避免第三方页把“静默 no-op”误当成功。

在此之前的可用路径：页面已把干净地址（`location.origin` + 网关前缀，不带 fnOS 网关注入的会话 token）复制进剪贴板，用户手动切到 Safari 粘贴即可；体验上差一次手动，但可用。

> 诊断期加入的 🐛 事件日志面板、`dbg()` 埋点、全局触摸计数器、服务端 `ctrl-ack` 已于 0.1.23 全部移除，本文件仅保留结论备查。
