import {
    WebCodecsVideoDecoder,
    WebGLVideoFrameRenderer,
    BitmapVideoFrameRenderer,
} from "@yume-chan/scrcpy-decoder-webcodecs";
import { SignalRoom, b64u } from "../signal.mjs";
// xterm is ~300 KB of the bundle and only matters once the shell is opened,
// so it loads on demand
let Terminal = null;
let FitAddon = null;

async function loadXterm() {
    if (Terminal) return;
    const [xterm, fit, css] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
        import("@xterm/xterm/css/xterm.css"),
    ]);
    Terminal = xterm.Terminal;
    FitAddon = fit.FitAddon;
    document.head.appendChild(document.createElement("style")).textContent =
        css.default ?? css;
}

// STUN only — TURN servers, when the backend has them (--turn), arrive
// with the WebRTC offer
const ICE_SERVERS = [
    { urls: "stun:stun.cloudflare.com:3478" },
    { urls: "stun:stun.l.google.com:19302" },
];

const STRINGS = {
    zh: {
        refresh: "刷新设备列表",
        fullscreen: "全屏",
        settings: "设置",
        quality: "画质",
        sound: "声音",
        power: "电源",
        back: "返回",
        home: "主页",
        recents: "最近任务",
        keyboard: "键盘",
        qAuto: "自动",
        qLow: "流畅",
        qMedium: "平衡",
        qHigh: "高清",
        encoder: "视频编码器",
        encoderAuto: "编码器：自动",
        encoderSwitched: (n) => `编码器：${n}`,
        encoderListFailed: (m) => `无法列出编码器：${m}`,
        audioEncoder: "音频编码器",
        audioEncoderAuto: "音频：自动",
        audioEncoderSwitched: (n) => `音频编码器：${n}`,
        audioEncoderWarning: (c) => `${c} 音频无法经 WebRTC 播放（仅 opus）`,
        kbdPlaceholder: "输入文字，回车发送到设备",
        send: "发送",
        emptyStage: "点击右下角 ⚙ 选择设备开始镜像",
        loadingDevices: "加载设备中…",
        noBackend: "未配置后端地址",
        noBackendHint: "未配置后端 — 在网址后加 ?server=后端域名",
        backendDown: "后端连接失败",
        backendDownHint: (b) => `连不上 ${b} — 检查服务是否在线，或用 ?server= 换个地址`,
        noDevices: "无设备 — 请先 adb connect",
        noDevicesHint: "后端正常，但没有已连接的设备",
        selectDevice: "选择设备…",
        device: "设备",
        connecting: "连接中…",
        error: (m) => `出错：${m}`,
        decodeError: (m) => `解码出错：${m}`,
        disconnected: "连接已断开",
        downshift: (p) => `网络受限，已降为${p}`,
        backendPrompt: "后端地址（scrcpy-webrtc 服务的域名，如 xxx.relay.hapi.run）：",
        shell: "设备 Shell",
        close: "关闭",
        shellExited: (c) => `会话结束，退出码 ${c}`,
        unauthorized: "认证失败",
        unauthorizedHint: "token 无效或缺失 — 用启动日志里的 ?token=… 链接打开",
        signalDown: "联系不到主机",
        signalDownHint: "确认主机正以 --signal 运行且在线，然后刷新",
        p2pFailed: "P2P 连接失败 — 在主机侧加 --turn 可解决",
        customOptions: "自定义 scrcpy 参数",
        customOptionsHint: "留空跟随画质预设；非空值会覆盖对应参数",
        optExtra: "其他参数 (键值对)",
        optAdd: "+",
        optApply: "应用",
        optReset: "重置",
        optInvalidExtra: "键值对格式错误",
        audioAutoDisabled: "设备音频不可用，已自动关闭",
    },
    en: {
        refresh: "Refresh devices",
        fullscreen: "Fullscreen",
        settings: "Settings",
        quality: "Quality",
        sound: "Sound",
        power: "Power",
        back: "Back",
        home: "Home",
        recents: "Recents",
        keyboard: "Keyboard",
        qAuto: "Auto",
        qLow: "Smooth",
        qMedium: "Balanced",
        qHigh: "HD",
        encoder: "Video encoder",
        encoderAuto: "Encoder: auto",
        encoderSwitched: (n) => `Encoder: ${n}`,
        encoderListFailed: (m) => `Cannot list encoders: ${m}`,
        audioEncoder: "Audio encoder",
        audioEncoderAuto: "Audio: auto",
        audioEncoderSwitched: (n) => `Audio encoder: ${n}`,
        audioEncoderWarning: (c) => `${c} audio can't play over WebRTC (opus only)`,
        kbdPlaceholder: "Type text, press Enter to send",
        send: "Send",
        emptyStage: "Tap the ⚙ button (bottom-right) to pick a device",
        loadingDevices: "Loading devices…",
        noBackend: "Backend not configured",
        noBackendHint: "No backend set — append ?server=your-domain to the URL",
        backendDown: "Backend unreachable",
        backendDownHint: (b) => `Cannot reach ${b} — check the service, or use ?server= to change it`,
        noDevices: "No devices — run adb connect first",
        noDevicesHint: "Backend is up, but no devices are connected",
        selectDevice: "Select device…",
        device: "Device",
        connecting: "Connecting…",
        error: (m) => `Error: ${m}`,
        decodeError: (m) => `Decode error: ${m}`,
        disconnected: "Disconnected",
        downshift: (p) => `Network limited, switched to ${p}`,
        backendPrompt: "Backend address (your scrcpy-webrtc server's domain, e.g. xxx.relay.hapi.run):",
        shell: "Device shell",
        close: "Close",
        shellExited: (c) => `session ended, exit code ${c}`,
        unauthorized: "Unauthorized",
        unauthorizedHint: "Missing or invalid token — open the ?token=… link from the server log",
        signalDown: "Can't reach the host",
        signalDownHint: "Make sure the host is running with --signal, then refresh",
        p2pFailed: "P2P failed — add --turn on the host to fix this",
        customOptions: "Custom scrcpy options",
        customOptionsHint: "Empty fields follow the quality preset; non-empty values override",
        optExtra: "Other options (key/value)",
        optAdd: "+",
        optApply: "Apply",
        optReset: "Reset",
        optInvalidExtra: "Invalid key/value",
        audioAutoDisabled: "Device audio unavailable — auto-disabled",
    },
};

// language: ?lang= param (persisted) > saved choice > browser language
const langParam = new URLSearchParams(location.search).get("lang");
if (langParam) localStorage.setItem("scrcpy-webrtc-lang", langParam);
const LANG = (localStorage.getItem("scrcpy-webrtc-lang") || navigator.language || "en")
    .toLowerCase()
    .startsWith("zh") ? "zh" : "en";
const t = STRINGS[LANG];

// backend resolution: ?server= param > saved value > same-origin.
// lets the static page live on GitHub Pages while the backend sits
// behind a tunnel; the URL param is the only way to point it elsewhere.
const backendParam = new URLSearchParams(location.search).get("server");
if (backendParam !== null) {
    localStorage.setItem("scrcpy-webrtc-backend", backendParam.replace(/^[a-z]+:\/\//, "").replace(/\/$/, ""));
}
// serverless signaling (#k=… from a --signal host): the key stays in the
// fragment — it never reaches any server, and the page needs it on reload
const frag = new URLSearchParams(location.hash.slice(1));
const SIGNAL_KEY = frag.get("k") || "";
const SIGNAL = Boolean(SIGNAL_KEY);

// token arrives via ?token= (or #…&t= in signal links), then is scrubbed
// from the URL so it doesn't linger in history or get shared along
const tokenParam = new URLSearchParams(location.search).get("token") ?? frag.get("t");
if (tokenParam !== null) {
    localStorage.setItem("scrcpy-webrtc-token", tokenParam);
    const clean = new URL(location.href);
    clean.searchParams.delete("token");
    frag.delete("t");
    clean.hash = frag.toString();
    history.replaceState(null, "", clean);
}
const TOKEN = localStorage.getItem("scrcpy-webrtc-token") || "";

const BACKEND = localStorage.getItem("scrcpy-webrtc-backend") || "";
const HTTP_BASE = BACKEND ? `https://${BACKEND}` : "";
const WS_BASE = BACKEND
    ? `wss://${BACKEND}`
    : `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}`;

function promptBackend() {
    const input = prompt(t.backendPrompt, BACKEND);
    if (input === null) return;
    localStorage.setItem("scrcpy-webrtc-backend", input.trim().replace(/^[a-z]+:\/\//, "").replace(/\/$/, ""));
    location.reload();
}

// ── remembered session settings ──────────────────────────────────
// the host keeps device, quality, encoders and mute per connection, so every
// reload used to reset them; keeping them in the page means coming back to the
// device you were mirroring, at the quality you left it at
const SESSION_KEY = "scrcpy-webrtc-session";
const session = (() => {
    try {
        const saved = JSON.parse(localStorage.getItem(SESSION_KEY) || "{}");
        return saved && typeof saved === "object" ? saved : {};
    } catch {
        return {}; // corrupt value — fall back to defaults
    }
})();

function rememberSession(patch) {
    Object.assign(session, patch);
    try {
        localStorage.setItem(SESSION_KEY, JSON.stringify(session));
    } catch {} // private mode / full quota — remembering is best-effort
}

// encoder names are device-specific MediaCodec components, so they are kept
// per serial: pinning one that this device doesn't have would break its session
const savedEncoders = (serial) => (serial && session.encoders?.[serial]) || {};
function rememberEncoder(serial, patch) {
    if (serial) {
        rememberSession({ encoders: { ...session.encoders, [serial]: { ...savedEncoders(serial), ...patch } } });
    }
}

const $ = (id) => document.getElementById(id);
const deviceSelect = $("device-select");
const qualitySelect = $("quality-select");
const encoderSelect = $("encoder-select");
const audioEncoderSelect = $("audio-encoder-select");
const settingsSheet = $("settings-sheet");
const settingsBackdrop = $("settings-backdrop");
const stageEl = $("stage");
const badgeEl = $("status-badge");
const kbdBar = $("kbdbar");
const kbdInput = $("kbd-input");
const shellPanel = $("shell-panel");

const PRESET_NAMES = { low: t.qLow, medium: t.qMedium, high: t.qHigh };

// apply translations to the static page
document.documentElement.lang = LANG;
for (const [id, key] of [
    ["refresh", "refresh"], ["btn-fullscreen", "fullscreen"],
    ["quality-select", "quality"], ["encoder-select", "encoder"],
    ["audio-encoder-select", "audioEncoder"],
    ["btn-mute", "sound"], ["btn-power", "power"],
    ["btn-back", "back"], ["btn-home", "home"], ["btn-recents", "recents"], ["btn-kbd", "keyboard"],
    ["btn-shell", "shell"], ["shell-close", "close"],
    ["btn-settings", "settings"], ["settings-close", "close"],
]) $(id).title = t[key];
// the settings sheet labels its rows in text rather than tooltips
for (const [id, key] of [
    ["settings-title", "settings"], ["label-device", "device"],
    ["label-refresh", "refresh"], ["label-quality", "quality"],
    ["label-encoder", "encoder"], ["label-audio-encoder", "audioEncoder"],
    ["label-sound", "sound"], ["label-fullscreen", "fullscreen"],
    ["label-power", "power"], ["label-back", "back"], ["label-home", "home"],
    ["label-recents", "recents"], ["label-kbd", "keyboard"], ["label-shell", "shell"],
    ["label-custom-options", "customOptions"], ["label-opt-extra", "optExtra"],
]) $(id).textContent = t[key];
$("opt-extra-add").textContent = t.optAdd;
$("opt-apply").textContent = t.optApply;
$("opt-reset").textContent = t.optReset;
for (const opt of qualitySelect.options) {
    opt.textContent = { auto: t.qAuto, low: t.qLow, medium: t.qMedium, high: t.qHigh }[opt.value];
}

// ── video encoder picker ───────────────────────────────────────
// the list comes from the host (scrcpy-server --list-encoders); the select
// stays hidden until one arrives, so the bar is unchanged on older hosts
function resetEncoderOptions() {
    encoderSelect.innerHTML = "";
    const auto = document.createElement("option");
    auto.value = "";
    auto.textContent = t.encoderAuto;
    encoderSelect.appendChild(auto);
    encoderSelect.value = "";
    encoderSelect.title = t.encoder;
    encoderSelect.hidden = true;
}

function fillEncoders(list, current) {
    const wanted = current || encoderSelect.value;
    resetEncoderOptions();
    for (const e of list) {
        if (e.type && e.type !== "video") continue;
        const opt = document.createElement("option");
        opt.value = e.name;
        opt.textContent = e.name;
        opt.title = [e.name, e.codec, e.hardwareType].filter(Boolean).join(" · ");
        encoderSelect.appendChild(opt);
    }
    // a host-side --video-encoder pin may not be in the device's own list
    if (wanted && !Array.from(encoderSelect.options).some((o) => o.value === wanted)) {
        const opt = document.createElement("option");
        opt.value = wanted;
        opt.textContent = wanted;
        encoderSelect.appendChild(opt);
    }
    encoderSelect.value = wanted;
    encoderSelect.hidden = encoderSelect.options.length < 2;
}

// the host reports the encoder the device actually opened, which can differ
// from what was asked for — surfacing it makes a silent fallback visible
function setActiveEncoder(name) {
    const auto = encoderSelect.options[0];
    if (!auto) return;
    auto.textContent = name ? `${t.encoderAuto} · ${name}` : t.encoderAuto;
}

// ── audio encoder picker ───────────────────────────────────────
// the same device list, filtered to its audio half. The WebRTC audio track
// only carries opus, so other codecs are labelled rather than hidden: seeing
// what the device offers beats wondering why it is missing
function resetAudioEncoderOptions() {
    audioEncoderSelect.innerHTML = "";
    const auto = document.createElement("option");
    auto.value = "";
    auto.textContent = t.audioEncoderAuto;
    audioEncoderSelect.appendChild(auto);
    audioEncoderSelect.value = "";
    audioEncoderSelect.title = t.audioEncoder;
    audioEncoderSelect.hidden = true;
}

function fillAudioEncoders(list, current) {
    const wanted = current || audioEncoderSelect.value;
    resetAudioEncoderOptions();
    for (const e of list) {
        if (e.type !== "audio") continue;
        const opt = document.createElement("option");
        opt.value = e.name;
        opt.dataset.codec = e.codec ?? "";
        opt.textContent = e.codec && e.codec !== "opus" ? `${e.name} (${e.codec})` : e.name;
        opt.title = [e.name, e.codec, e.hardwareType].filter(Boolean).join(" · ");
        audioEncoderSelect.appendChild(opt);
    }
    if (wanted && !Array.from(audioEncoderSelect.options).some((o) => o.value === wanted)) {
        const opt = document.createElement("option");
        opt.value = wanted;
        opt.textContent = wanted;
        audioEncoderSelect.appendChild(opt);
    }
    audioEncoderSelect.value = wanted;
    audioEncoderSelect.hidden = audioEncoderSelect.options.length < 2;
}

function setActiveAudioEncoder(name) {
    const auto = audioEncoderSelect.options[0];
    if (!auto) return;
    auto.textContent = name ? `${t.audioEncoderAuto} · ${name}` : t.audioEncoderAuto;
}

resetEncoderOptions();
resetAudioEncoderOptions();
// show the remembered preset before anything is even connected
if (Array.from(qualitySelect.options).some((o) => o.value === session.quality)) {
    qualitySelect.value = session.quality;
}
kbdInput.placeholder = t.kbdPlaceholder;
$("kbd-send").textContent = t.send;
stageEl.dataset.emptyHint = t.emptyStage;

let ws = null;
let decoder = null;
let writer = null;
let canvas = null;
let currentSerial = null;

let pc = null;
let dc = null;
let shellDc = null;
let videoEl = null;
let path = "ws"; // "ws" | "rtc"
let statsTimer = null;
let lastSize = "";
// always start muted on a fresh page load — autoplay policy would otherwise
// reject the first audio.play() with NotAllowedError, and silent-by-default
// matches what most users want anyway
let muted = true;
// whether the device should send an audio track at all — separate from
// browser-side muted. Some Android devices (notably redroid on 13+) ship an
// opus encoder that crashes on start; turning this off skips audio entirely.
let audioEnabled = session.audioEnabled !== false;
let lastClipFromDevice = null;
let lastClipToDevice = null;

function setBadge(text, cls = "") {
    badgeEl.hidden = !text;
    badgeEl.textContent = text;
    badgeEl.className = cls;
}

async function loadDevices() {
    const placeholder = (text) => {
        deviceSelect.innerHTML = "";
        const opt = document.createElement("option");
        opt.value = "";
        opt.textContent = text;
        deviceSelect.appendChild(opt);
    };
    placeholder(t.loadingDevices);
    let devices = [];
    if (SIGNAL) {
        try {
            devices = await signalDevices();
        } catch (e) {
            if (e.message === "unauthorized") {
                placeholder(t.unauthorized);
                setBadge(t.unauthorizedHint, "error");
            } else {
                placeholder(t.signalDown);
                setBadge(t.signalDownHint, "error");
            }
            return;
        }
    } else try {
        const res = await fetch(`${HTTP_BASE}/api/devices`, {
            headers: TOKEN ? { authorization: `Bearer ${TOKEN}` } : {},
        });
        if (res.status === 401) {
            placeholder(t.unauthorized);
            setBadge(t.unauthorizedHint, "error");
            return;
        }
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        devices = await res.json();
    } catch {
        // guide the user instead of a dead "retry": either the backend was
        // never configured (static hosting), or the configured one is down
        if (!BACKEND) {
            placeholder(t.noBackend);
            setBadge(t.noBackendHint, "error");
            promptBackend();
        } else {
            placeholder(t.backendDown);
            setBadge(t.backendDownHint(BACKEND), "error");
        }
        return;
    }
    if (devices.length === 0) {
        placeholder(t.noDevices);
        setBadge(t.noDevicesHint, "error");
        return;
    }
    setBadge("");
    placeholder(t.selectDevice);
    for (const d of devices) {
        const opt = document.createElement("option");
        opt.value = d.serial;
        opt.textContent = `${d.model?.replaceAll("_", " ") ?? d.product ?? t.device} · ${d.serial}`;
        deviceSelect.appendChild(opt);
    }
    // already connected: the list is only there to be kept in sync
    if (currentSerial && devices.some((d) => d.serial === currentSerial)) {
        deviceSelect.value = currentSerial;
        return;
    }
    // the device mirrored last time wins, so a reload picks up where it left
    // off; failing that, a single device needs no tap at all
    const again = devices.find((d) => d.serial === session.serial) ?? (devices.length === 1 ? devices[0] : null);
    if (again) {
        deviceSelect.value = again.serial;
        connect(again.serial);
        setSettingsOpen(false);
    }
}

// ---- serverless signaling (--signal hosts) --------------------------------
// The room replaces both /api/devices and the /api/stream WebSocket with
// encrypted messages on public MQTT brokers, addressed per-session by sid.
// There is no WS video fallback on this path — media is WebRTC-only.

let signalRoom = null;
let roomPromise = null;
const signalRoutes = new Map(); // sid -> handler

function ensureRoom() {
    roomPromise ??= SignalRoom.create({ key: b64u.decode(SIGNAL_KEY), role: "viewer" }).then((room) => {
        signalRoom = room;
        room.onmessage = (msg) => signalRoutes.get(msg.sid)?.(msg);
        return room;
    });
    return roomPromise;
}

const randSid = () =>
    Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, "0")).join("");

async function signalDevices() {
    await ensureRoom();
    const sid = randSid();
    return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
            signalRoutes.delete(sid);
            reject(new Error("timeout"));
        }, 12000);
        signalRoutes.set(sid, (msg) => {
            clearTimeout(timer);
            signalRoutes.delete(sid);
            if (msg.error) reject(new Error(msg.error));
            else resolve(msg.devices ?? []);
        });
        signalRoom.send({ sid, t: "devices", token: TOKEN || undefined });
    });
}

// quacks like the WebSocket connect() expects: send/close/onopen/onmessage/
// onclose/readyState — so the whole session protocol runs unchanged
function signalChannel(serial) {
    const sid = randSid();
    let hb = null;
    const chan = {
        readyState: WebSocket.CONNECTING,
        binaryType: "",
        onopen: null,
        onmessage: null,
        onclose: null,
        send(s) {
            signalRoom?.send({ sid, ...JSON.parse(s) });
        },
        close() {
            if (chan.readyState === WebSocket.OPEN) signalRoom?.send({ sid, t: "bye" });
            chan.readyState = WebSocket.CLOSED;
            clearInterval(hb);
            signalRoutes.delete(sid);
        },
    };
    ensureRoom().then(() => {
        if (chan.readyState === WebSocket.CLOSED) return;
        signalRoutes.set(sid, (msg) => {
            if (msg.t === "bye") {
                const wasOpen = chan.readyState === WebSocket.OPEN;
                chan.readyState = WebSocket.CLOSED;
                clearInterval(hb);
                signalRoutes.delete(sid);
                if (wasOpen) chan.onclose?.({ code: msg.code ?? 1000 });
            } else {
                chan.onmessage?.({ data: JSON.stringify(msg) });
            }
        });
        signalRoom.send({ sid, t: "open", serial, token: TOKEN || undefined });
        // brokers give no liveness signal for the peer; the host times a
        // session out after 90 s without traffic, so keep it warm
        hb = setInterval(() => signalRoom.send({ sid, t: "ping" }), 25000);
        chan.readyState = WebSocket.OPEN;
        chan.onopen?.();
    }).catch(() => {
        setBadge(t.signalDownHint, "error");
    });
    return chan;
}

function sendWs(obj) {
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
}

function sendControl(obj) {
    if (dc?.readyState === "open") dc.send(JSON.stringify(obj));
    else sendWs(obj);
}

function teardownRtc() {
    clearInterval(statsTimer);
    statsTimer = null;
    dc = null;
    shellDc = null;
    pc?.close();
    pc = null;
    videoEl?.remove();
    videoEl = null;
    if (path === "rtc") {
        path = "ws";
        if (canvas) canvas.style.display = "";
        sendWs({ t: "video-path", mode: "ws" });
        setBadge(`${lastSize} · WS`);
        // the rotated view lost its <video> — re-adopt the canvas
        autoRotate();
    }
}

function disconnect() {
    teardownRtc();
    resetEncoderOptions();
    resetAudioEncoderOptions();
    ws?.close();
    ws = null;
    writer?.releaseLock();
    writer = null;
    decoder?.dispose();
    decoder = null;
    canvas = null;
    currentSerial = null;
    path = "ws";
    // drop the previous stream's geometry too, so the next connection starts
    // from a clean slate instead of inheriting a stale orientation
    streamW = 0;
    streamH = 0;
    viewRotation = 0;
    paintRotateButton();
    stageEl.innerHTML = "";
    setBadge("");
}

// the host reads quality and encoders when it builds a session, so replaying
// the saved choices on open costs nothing — no mid-session restart
function restoreSession(serial) {
    if (session.quality) sendWs({ t: "quality", preset: session.quality });
    const enc = savedEncoders(serial);
    if (enc.video) sendWs({ t: "encoder", name: enc.video });
    if (enc.audio) sendWs({ t: "audio-encoder", name: enc.audio, codec: enc.audioCodec ?? "" });
    if (session.customOptions) sendWs({ t: "options", options: session.customOptions });
}

function connect(serial) {
    disconnect();
    currentSerial = serial;
    rememberSession({ serial });
    setBadge(t.connecting);
    if (SIGNAL) {
        ws = signalChannel(serial);
    } else {
        const url = `${WS_BASE}/api/stream?serial=${encodeURIComponent(serial)}`;
        ws = TOKEN ? new WebSocket(url, [`scrcpy-webrtc.token.${TOKEN}`]) : new WebSocket(url);
    }
    ws.binaryType = "arraybuffer";
    ws.onopen = () => {
        // negotiate WebRTC immediately, in parallel with scrcpy startup
        tryWebRtc();
        restoreSession(serial);
    };

    ws.onmessage = async (ev) => {
        if (typeof ev.data === "string") {
            const msg = JSON.parse(ev.data);
            if (msg.type === "meta") {
                // may legitimately be 0 until the first configuration packet
                // is parsed — the follow-up "size" message carries it then
                streamW = msg.width || 0;
                streamH = msg.height || 0;
                startDecoder(msg.codec);
                $("btn-shell").hidden = !msg.shell;
                if (msg.preset) qualitySelect.value = msg.auto ? "auto" : msg.preset;
                if (path !== "rtc") setBadge("WS");
                // the server only sets audio=false when it had to auto-disable
                // it after the device's codec crashed; reflect that in the UI
                if (msg.audio === false) {
                    audioEnabled = false;
                    rememberSession({ audioEnabled });
                    paintSoundButton();
                    setBadge(t.audioAutoDisabled, "warn");
                }
                sendWs({ t: "encoder", list: true }); // this device's encoders
            } else if (msg.type === "encoders") {
                if (msg.error) setBadge(t.encoderListFailed(msg.error), "error");
                else {
                    fillEncoders(msg.list ?? [], msg.current ?? "");
                    fillAudioEncoders(msg.list ?? [], msg.currentAudio ?? "");
                }
            } else if (msg.type === "encoder") {
                setActiveEncoder(msg.name);
                if (!msg.active) setBadge(t.encoderSwitched(msg.name || t.qAuto));
            } else if (msg.type === "audio-encoder") {
                setActiveAudioEncoder(msg.name);
                // a codec the audio track can't decode is worth saying out loud
                if (!msg.active) {
                    setBadge(msg.codec && msg.codec !== "opus"
                        ? t.audioEncoderWarning(msg.codec)
                        : t.audioEncoderSwitched(msg.name || t.qAuto));
                }
            } else if (msg.type === "quality") {
                // server auto-downshifted under congestion
                setBadge(t.downshift(PRESET_NAMES[msg.preset] ?? msg.preset), "p2p");
            } else if (msg.type === "clipboard") {
                lastClipFromDevice = msg.text;
                navigator.clipboard?.writeText(msg.text).catch(() => {});
            } else if (msg.type === "size") {
                streamW = msg.width;
                streamH = msg.height;
                lastSize = `${msg.width}×${msg.height}`;
                if (path !== "rtc") setBadge(`${lastSize} · WS`);
                // stream rotated — re-run the follow rule
                autoRotate();
            } else if (msg.type === "error") {
                setBadge(t.error(msg.message), "error");
            } else if (msg.t === "rtc-offer") {
                acceptRtcOffer(msg.sdp, msg.iceServers).catch((e) => {
                    console.warn("webrtc setup failed:", e);
                    teardownRtc();
                });
            } else if (msg.t === "rtc-ice") {
                pc?.addIceCandidate(msg.candidate).catch(() => {});
            } else if (msg.t?.startsWith("shell-")) {
                handleShellMessage(msg);
            }
            return;
        }
        if (!writer) return;
        const buf = new Uint8Array(ev.data);
        const dv = new DataView(ev.data);
        const data = buf.subarray(10);
        const packet = buf[0] === 0
            ? { type: "configuration", data }
            : { type: "data", keyframe: buf[1] === 1, pts: dv.getBigUint64(2), data };
        try {
            await writer.write(packet);
        } catch (e) {
            setBadge(t.decodeError(e.message), "error");
        }
    };
    ws.onclose = (e) => {
        if (currentSerial !== serial) return;
        setBadge(e.code === 4001 ? t.unauthorizedHint : t.disconnected, "error");
    };
}

function startDecoder(codec) {
    let renderer;
    try {
        renderer = new WebGLVideoFrameRenderer();
    } catch {
        renderer = new BitmapVideoFrameRenderer();
    }
    decoder = new WebCodecsVideoDecoder({ codec, renderer });
    writer = decoder.writable.getWriter();

    // replace only the previous canvas — the WebRTC <video> may already be
    // in the stage (negotiation runs in parallel with scrcpy startup)
    canvas?.remove();
    canvas = renderer.canvas;
    canvas.className = "screen";
    // a quality change restarts the stream mid-session; if P2P is already
    // showing, the fresh canvas must stay hidden underneath
    if (path === "rtc") canvas.style.display = "none";
    stageEl.appendChild(canvas);
    // a rotated view must adopt the fresh canvas (the old one was removed)
    autoRotate();
    attachInput(canvas);
}

function tryWebRtc() {
    if (typeof RTCPeerConnection === "undefined") {
        if (SIGNAL) setBadge(t.p2pFailed, "error");
        return;
    }
    sendWs({ t: "rtc-start" });
}

async function acceptRtcOffer(sdp, extraIce) {
    pc = new RTCPeerConnection({ iceServers: [...ICE_SERVERS, ...(extraIce ?? [])] });
    pc.onicecandidate = (e) => {
        if (e.candidate) sendWs({ t: "rtc-ice", candidate: e.candidate.toJSON() });
    };
    pc.onconnectionstatechange = () => {
        if (pc && ["failed", "disconnected", "closed"].includes(pc.connectionState)) {
            teardownRtc();
        }
    };
    pc.ondatachannel = (e) => {
        if (e.channel.label === "shell") {
            shellDc = e.channel;
            shellDc.onmessage = (m) => handleShellMessage(JSON.parse(m.data));
        } else {
            dc = e.channel;
        }
    };
    pc.ontrack = (e) => {
        // minimize receive-side latency where supported
        try {
            e.receiver.playoutDelayHint = 0;
            e.receiver.jitterBufferTarget = 0;
        } catch {}
        // Both audio and video tracks attach to the same <video> element.
        // Rationale: Chrome's autoplay policy treats <video> and <audio>
        // elements differently. A muted <video> element is always allowed
        // to autoplay, but a muted <audio> element with a WebRTC MediaStream
        // can still reject play() with NotAllowedError on some versions.
        // Routing audio through <video> lets the autoplay exemption apply
        // uniformly across browsers.
        if (!videoEl) {
            videoEl = document.createElement("video");
            videoEl.className = "screen-video";
            videoEl.autoplay = true;
            videoEl.muted = muted;  // start in the user's remembered state
            videoEl.playsInline = true;
            videoEl.srcObject = new MediaStream();
            stageEl.appendChild(videoEl);
            attachInput(videoEl);
            autoRotate();
            watchRtcStats();
            // fires as soon as the track's dimensions change (device rotated),
            // earlier than the server-side sizeChanged round-trip
            videoEl.addEventListener("resize", autoRotate);
        }
        videoEl.srcObject.addTrack(e.track);
        videoEl.play().catch((err) => console.warn("video.play() rejected:", err));
    };
    await pc.setRemoteDescription({ type: "offer", sdp });
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    sendWs({ t: "rtc-answer", sdp: answer.sdp });
}

// switch paths based on real decode progress from getStats(); keeps a live
// readout in the status badge so failures are diagnosable
function watchRtcStats() {
    let lastFrames = 0;
    let lastTime = performance.now();
    let ticksWithoutDecode = 0;
    clearInterval(statsTimer);
    statsTimer = setInterval(async () => {
        if (!pc) return;
        const stats = await pc.getStats();
        let inbound = null;
        stats.forEach((s) => {
            if (s.type === "inbound-rtp" && s.kind === "video") inbound = s;
        });
        if (!inbound) return;
        const { packetsReceived = 0, framesDecoded = 0, framesReceived = 0, pliCount = 0 } = inbound;
        if (path !== "rtc") {
            if (framesDecoded > 0 && videoEl) {
                path = "rtc";
                if (canvas) canvas.style.display = "none";
                videoEl.play?.().catch(() => {});
                sendWs({ t: "video-path", mode: "rtc" });
            } else {
                ticksWithoutDecode++;
                if (ticksWithoutDecode === 10) {
                    console.warn("webrtc: no decoded frames after 5s", inbound);
                    console.warn(`p2p stalled: pkts=${packetsReceived} recv=${framesReceived} dec=${framesDecoded} pli=${pliCount}`);
                }
                if (ticksWithoutDecode >= 20) {
                    console.warn("webrtc: giving up, staying on WebSocket path");
                    teardownRtc();
                    // no WS video exists on the signaling path — say why
                    // the screen is black instead of silently showing it
                    if (SIGNAL) setBadge(t.p2pFailed, "error");
                }
            }
            return;
        }
        const now = performance.now();
        const fps = Math.round(((framesDecoded - lastFrames) * 1000) / (now - lastTime));
        lastFrames = framesDecoded;
        lastTime = now;
        const size = videoEl?.videoWidth ? `${videoEl.videoWidth}×${videoEl.videoHeight}` : lastSize;
        setBadge(`${size} · P2P ${fps}fps`, "p2p");
    }, 500);
}

// normalized [0,1] coordinates; accounts for letterboxing in <video>
function norm(el, e) {
    const rect = el.getBoundingClientRect();
    if (viewRotation) {
        // inside the rotator the element fills its box exactly (no
        // letterbox) — but the box is rotated, so un-rotate the point
        const u = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
        const v = Math.min(1, Math.max(0, (e.clientY - rect.top) / rect.height));
        // rotate(90) cw: video x = visual y, video y = 1 - visual x
        if (viewRotation === 90) return { x: v, y: 1 - u };
        if (viewRotation === 180) return { x: 1 - u, y: 1 - v };
        return { x: 1 - v, y: u }; // 270
    }
    let { width, height } = rect;
    let ox = 0;
    let oy = 0;
    if (el.videoWidth) {
        const scale = Math.min(rect.width / el.videoWidth, rect.height / el.videoHeight);
        width = el.videoWidth * scale;
        height = el.videoHeight * scale;
        ox = (rect.width - width) / 2;
        oy = (rect.height - height) / 2;
    }
    return {
        x: Math.min(1, Math.max(0, (e.clientX - rect.left - ox) / width)),
        y: Math.min(1, Math.max(0, (e.clientY - rect.top - oy) / height)),
    };
}

function attachInput(el) {
    let down = false;
    el.onpointerdown = (e) => {
        el.setPointerCapture(e.pointerId);
        down = true;
        sendControl({ t: "touch", a: "down", ...norm(el, e) });
        e.preventDefault();
    };
    el.onpointermove = (e) => {
        if (down) sendControl({ t: "touch", a: "move", ...norm(el, e) });
    };
    const up = (e) => {
        if (down) sendControl({ t: "touch", a: "up", ...norm(el, e) });
        down = false;
    };
    el.onpointerup = up;
    el.onpointercancel = up;
    el.onwheel = (e) => {
        e.preventDefault();
        sendControl({ t: "scroll", ...norm(el, e), dx: -e.deltaX / 100, dy: -e.deltaY / 100 });
    };
    el.oncontextmenu = (e) => e.preventDefault();
}

// ── device shell ───────────────────────────────────────────────
let term = null;
let fitAddon = null;
let shellReady = false;

function sendShell(obj) {
    // mirrors sendControl: prefer the dedicated P2P channel
    if (shellDc?.readyState === "open") shellDc.send(JSON.stringify(obj));
    else sendWs(obj);
}

async function openShell() {
    if (!currentSerial) return;
    shellPanel.hidden = false;
    // shrink the layout instead of covering it, so the screen stays visible
    document.body.classList.add("with-shell");
    try {
        await loadXterm();
    } catch (e) {
        $("shell-title").textContent = `${t.shell}: ${e.message}`;
        return;
    }
    if (!term) {
        term = new Terminal({
            fontSize: 13,
            cursorBlink: true,
            theme: { background: "#0f1115", foreground: "#e6e8ec" },
        });
        fitAddon = new FitAddon();
        term.loadAddon(fitAddon);
        term.open($("terminal"));
        term.onData((d) => sendShell({ t: "shell-in", d }));
        term.onResize(({ rows, cols }) => sendShell({ t: "shell-resize", rows, cols }));
        // panel geometry changes on rotation / window resize
        new ResizeObserver(() => {
            if (!shellPanel.hidden) fitAddon?.fit();
        }).observe($("terminal"));
    }
    fitAddon.fit();
    term.focus();
    if (!shellReady) {
        sendShell({ t: "shell-start", rows: term.rows, cols: term.cols });
    }
}

function closeShell() {
    shellPanel.hidden = true;
    document.body.classList.remove("with-shell");
}

function handleShellMessage(msg) {
    if (msg.t === "shell-ready") {
        shellReady = true;
    } else if (msg.t === "shell-out") {
        term?.write(Uint8Array.from(atob(msg.d), (c) => c.charCodeAt(0)));
    } else if (msg.t === "shell-exit") {
        shellReady = false;
        term?.write(`\r\n\x1b[90m[${t.shellExited(msg.code)}]\x1b[0m\r\n`);
    } else if (msg.t === "shell-error") {
        shellReady = false;
        term?.write(`\r\n\x1b[31m${msg.message}\x1b[0m\r\n`);
    }
}

// physical keyboard passthrough (desktop); form fields keep their own keys
const isFormField = (t) => ["INPUT", "SELECT", "TEXTAREA"].includes(t.tagName);
window.onkeydown = (e) => {
    if (isFormField(e.target) || !currentSerial) return;
    if (e.key === "Backspace") sendControl({ t: "key", code: 67, a: "down" });
    else if (e.key === "Enter") sendControl({ t: "key", code: 66, a: "down" });
    else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey) sendControl({ t: "text", text: e.key });
};
window.onkeyup = (e) => {
    if (isFormField(e.target) || !currentSerial) return;
    if (e.key === "Backspace") sendControl({ t: "key", code: 67, a: "up" });
    else if (e.key === "Enter") sendControl({ t: "key", code: 66, a: "up" });
};

// on-screen keyboard bar (mobile: summons the local IME, forwards to device)
function sendKbdText() {
    const text = kbdInput.value;
    if (text) {
        sendControl({ t: "text", text });
        kbdInput.value = "";
    } else {
        sendControl({ t: "key", code: 66, a: "down" });
        sendControl({ t: "key", code: 66, a: "up" });
    }
}
$("btn-kbd").onclick = () => {
    setSettingsOpen(false); // the sheet would cover the bar it just opened
    kbdBar.hidden = !kbdBar.hidden;
    if (!kbdBar.hidden) kbdInput.focus();
};
$("kbd-send").onclick = sendKbdText;
kbdInput.onkeydown = (e) => {
    if (e.key === "Enter") {
        e.preventDefault();
        sendKbdText();
    } else if (e.key === "Backspace" && !kbdInput.value) {
        sendControl({ t: "key", code: 67, a: "down" });
        sendControl({ t: "key", code: 67, a: "up" });
    }
};

for (const [id, code] of [["btn-back", 4], ["btn-home", 3], ["btn-recents", 187], ["btn-power", 26]]) {
    $(id).onclick = () => {
        sendControl({ t: "key", code, a: "down" });
        sendControl({ t: "key", code, a: "up" });
        setSettingsOpen(false); // get out of the way so the result is visible
    };
}

$("btn-fullscreen").onclick = () => {
    setSettingsOpen(false);
    if (document.fullscreenElement) document.exitFullscreen();
    else document.documentElement.requestFullscreen?.();
};

// ── view rotation: follow the device + manual correction ───────
// The stream adapts to device rotation on its own, but when the device goes
// landscape while the browser window is portrait, the content shrinks into a
// small bar. So rotation follows the *content*: when content and stage are
// "crossed", the view rotates by `bias` so the user can just turn their phone
// sideways (and once the browser window itself rotates, the view settles back
// to 0). The bias is remembered per content orientation: frame sizes can't
// tell 90° from 270° (reverse-landscape), so a wrong guess is corrected by
// tapping 🔄 once — and sticks for the next rotation.
let viewRotation = 0;
let rotationBias = { landscape: 90, portrait: 0, ...session.rotationBias };

// Authoritative stream size, as reported by the server (parsed out of the
// H.264/H.265 configuration). Element sizes are NOT trustworthy early on: a
// freshly created <canvas> is 300×150 by default, which reads as "landscape"
// and used to flip the view 90° depending on a timing race at page load.
let streamW = 0;
let streamH = 0;

// natural size of whatever is currently displayed
function contentSize() {
    if (streamW && streamH) return { W: streamW, H: streamH };
    if (path === "rtc" && videoEl?.videoWidth) {
        return { W: videoEl.videoWidth, H: videoEl.videoHeight };
    }
    // ignore a canvas that still carries the HTML default size / no size yet
    if (canvas?.width > 0 && canvas?.height > 0 &&
        !(canvas.width === 300 && canvas.height === 150)) {
        return { W: canvas.width, H: canvas.height };
    }
    return null;
}

function applyViewRotation() {
    document.getElementById("rotator")?.remove();
    const els = [canvas, videoEl].filter(Boolean);
    if (!viewRotation || !els.length) {
        // native layout: canvas in flow, <video> absolutely covering the stage
        for (const el of els) {
            el.style.cssText = "";
            if (el === canvas) {
                if (path === "rtc") el.style.display = "none";
            }
            stageEl.appendChild(el);
        }
        return;
    }
    const size = contentSize();
    if (!size) { viewRotation = 0; return; }
    const { W, H } = size;
    const sw = stageEl.clientWidth;
    const sh = stageEl.clientHeight;
    // the rotator keeps the content's aspect ratio (no letterboxing inside),
    // and is scaled so that the *rotated* visual box fits the stage
    const k = viewRotation % 180 === 90
        ? Math.min(sw / H, sh / W)
        : Math.min(sw / W, sh / H);
    const rot = document.createElement("div");
    rot.id = "rotator";
    rot.style.cssText =
        `position:absolute;left:50%;top:50%;` +
        `width:${Math.round(W * k)}px;height:${Math.round(H * k)}px;` +
        `transform:translate(-50%,-50%) rotate(${viewRotation}deg);`;
    for (const el of els) {
        el.style.cssText =
            "position:absolute;inset:0;width:100%;height:100%;object-fit:contain;";
        if (el === canvas && path === "rtc") el.style.display = "none";
        rot.appendChild(el);
    }
    stageEl.appendChild(rot);
}

function paintRotateButton() {
    $("btn-rotate").textContent = viewRotation ? String(viewRotation) + "°" : "🔄";
}
// re-evaluate the follow rule; only acts when the answer actually changes
function autoRotate() {
    const size = contentSize();
    if (!size) return;
    const contentLandscape = size.W >= size.H;
    const stageLandscape = stageEl.clientWidth >= stageEl.clientHeight;
    const want = contentLandscape
        ? (stageLandscape ? 0 : rotationBias.landscape)
        : rotationBias.portrait;
    if (want !== viewRotation) {
        viewRotation = want;
        applyViewRotation();
        paintRotateButton();
    }
}

$("btn-rotate").onclick = () => {
    viewRotation = (viewRotation + 90) % 360;
    // remember the correction for this content orientation, so the next
    // device rotation auto-applies it (90 vs 270 can't be auto-detected)
    const size = contentSize();
    if (size) {
        rotationBias[size.W >= size.H ? "landscape" : "portrait"] = viewRotation;
        rememberSession({ rotationBias });
    }
    applyViewRotation();
    paintRotateButton();
};
window.addEventListener("resize", autoRotate);
document.addEventListener("fullscreenchange", autoRotate);
// the stage has no size until layout settles; re-evaluate whenever it changes
// (this also catches phone rotation, where `resize` can fire late)
if (typeof ResizeObserver !== "undefined") {
    new ResizeObserver(() => autoRotate()).observe(stageEl);
}

$("btn-shell").onclick = () => {
    setSettingsOpen(false);
    if (shellPanel.hidden) openShell().catch((e) => console.error("shell:", e));
    else closeShell();
};
$("shell-close").onclick = closeShell;

function setSettingsOpen(open) {
    settingsSheet.hidden = !open;
    settingsBackdrop.hidden = !open;
    // sheet + backdrop already cover the entire viewport; disabling pointer
    // events on the stage keeps any pointer that escapes the sheet (e.g. when
    // a finger slides out of a button across a select dropdown) from being
    // forwarded as a touch to the device
    stageEl.style.pointerEvents = open ? "none" : "";
}
$("settings-close").onclick = () => setSettingsOpen(false);
settingsBackdrop.onclick = () => setSettingsOpen(false);

// ── the settings button is also a drag handle ──
// a drag repositions it (and is remembered); a plain tap opens the sheet
const FAB_POS_KEY = "scrcpy-webrtc-fab-pos";
const fab = $("btn-settings");

function placeFab(x, y) {
    const w = fab.offsetWidth || 48;
    const h = fab.offsetHeight || 48;
    // never let it leave the viewport — a lost button would be unrecoverable
    const cx = Math.min(Math.max(4, x), Math.max(4, window.innerWidth - w - 4));
    const cy = Math.min(Math.max(4, y), Math.max(4, window.innerHeight - h - 4));
    fab.style.setProperty("--fab-x", `${cx}px`);
    fab.style.setProperty("--fab-y", `${cy}px`);
    fab.classList.add("placed");
    return [cx, cy];
}

try {
    const saved = JSON.parse(localStorage.getItem(FAB_POS_KEY) || "null");
    if (Array.isArray(saved)) placeFab(saved[0], saved[1]);
} catch {} // corrupt value — just fall back to the default corner

// rotation / window resize can push a saved position off screen
window.addEventListener("resize", () => {
    if (fab.classList.contains("placed")) placeFab(parseFloat(fab.style.getPropertyValue("--fab-x")), parseFloat(fab.style.getPropertyValue("--fab-y")));
});

// the move/up listeners sit on window: the pointer routinely leaves the 48px
// button, and we must keep following it until it is released
let fabDrag = null;
fab.addEventListener("pointerdown", (e) => {
    if (!e.isPrimary || (e.pointerType === "mouse" && e.button !== 0)) return;
    const r = fab.getBoundingClientRect();
    fabDrag = { id: e.pointerId, dx: e.clientX - r.left, dy: e.clientY - r.top, sx: e.clientX, sy: e.clientY, moved: false };
    fab.dataset.dragged = "0";
    fab.setPointerCapture(e.pointerId);
});
window.addEventListener("pointermove", (e) => {
    if (fabDrag?.id !== e.pointerId) return;
    if (!fabDrag.moved) {
        // require a real drag, so a shaky tap still opens the sheet
        if (Math.hypot(e.clientX - fabDrag.sx, e.clientY - fabDrag.sy) < 6) return;
        fabDrag.moved = true;
        fab.classList.add("dragging");
    }
    [fabDrag.x, fabDrag.y] = placeFab(e.clientX - fabDrag.dx, e.clientY - fabDrag.dy);
});
const endFabDrag = (e) => {
    if (fabDrag?.id !== e.pointerId) return;
    const { moved, x, y } = fabDrag;
    fabDrag = null;
    fab.classList.remove("dragging");
    if (!moved) return;
    fab.dataset.dragged = "1"; // swallow the click that follows this gesture
    localStorage.setItem(FAB_POS_KEY, JSON.stringify([x, y]));
};
window.addEventListener("pointerup", endFabDrag);
window.addEventListener("pointercancel", endFabDrag);

fab.onclick = () => {
    if (fab.dataset.dragged === "1") {
        fab.dataset.dragged = "0";
        return;
    }
    setSettingsOpen(settingsSheet.hidden);
};

deviceSelect.onchange = () => {
    if (deviceSelect.value) {
        connect(deviceSelect.value);
        setSettingsOpen(false); // picking a device is the whole point of the sheet
    }
};
qualitySelect.onchange = () => {
    rememberSession({ quality: qualitySelect.value });
    sendWs({ t: "quality", preset: qualitySelect.value });
};
encoderSelect.onchange = () => {
    rememberEncoder(currentSerial, { video: encoderSelect.value });
    sendWs({ t: "encoder", name: encoderSelect.value });
};
audioEncoderSelect.onchange = () => {
    const codec = audioEncoderSelect.selectedOptions[0]?.dataset.codec ?? "";
    rememberEncoder(currentSerial, { audio: audioEncoderSelect.value, audioCodec: codec });
    sendWs({ t: "audio-encoder", name: audioEncoderSelect.value, codec });
};

// ── custom scrcpy options ────────────────────────────────
// hand-tuned overrides that beat both the quality preset and the dedicated
// encoder fields above; empty fields fall back to whatever the preset chose
const OPT_FIELDS = [
    { key: "maxSize", id: "opt-max-size", kind: "number" },
    // Mbps in the UI, bps on the wire — scrcpy-server's option is in bps and
    // users reason in Mbps, so convert at the boundary
    { key: "videoBitRate", id: "opt-video-bit-rate", kind: "mbps" },
    { key: "maxFps", id: "opt-max-fps", kind: "number" },
    { key: "videoCodec", id: "opt-video-codec", kind: "select" },
    { key: "videoEncoder", id: "opt-video-encoder", kind: "string" },
    { key: "audioEncoder", id: "opt-audio-encoder", kind: "string" },
    { key: "audioBitRate", id: "opt-audio-bit-rate", kind: "number" },
    { key: "screenOffTimeout", id: "opt-screen-off-timeout", kind: "number" },
];
const BUILTIN_KEYS = new Set(OPT_FIELDS.map((f) => f.key));

function readCustomOptions() {
    const out = {};
    for (const f of OPT_FIELDS) {
        const el = $(f.id);
        if (!el) continue;
        const v = el.value.trim();
        if (!v) continue;
        if (f.kind === "number") {
            const n = Number(v);
            if (!Number.isFinite(n)) continue;
            out[f.key] = n;
        } else if (f.kind === "mbps") {
            const n = Number(v);
            if (!Number.isFinite(n) || n <= 0) continue;
            out[f.key] = Math.round(n * 1_000_000);
        } else {
            out[f.key] = v;
        }
    }
    for (const row of $("opt-extra-list").querySelectorAll(".opt-extra-row")) {
        const k = row.querySelector(".opt-k").value.trim();
        const v = row.querySelector(".opt-v").value.trim();
        if (!k) continue;
        const n = Number(v);
        if (v !== "" && Number.isFinite(n) && String(n) === v) out[k] = n;
        else out[k] = v;
    }
    return out;
}

function writeCustomOptions(obj) {
    obj = obj || {};
    for (const f of OPT_FIELDS) {
        const el = $(f.id);
        if (!el) continue;
        if (f.key in obj) {
            const v = obj[f.key];
            el.value = f.kind === "mbps" && typeof v === "number"
                ? String(Math.round((v / 1_000_000) * 10) / 10)
                : String(v);
        } else {
            el.value = "";
        }
    }
    const list = $("opt-extra-list");
    list.innerHTML = "";
    for (const [k, v] of Object.entries(obj)) {
        if (BUILTIN_KEYS.has(k)) continue;
        addExtraRow(k, v);
    }
    updateCustomOptionsCount();
}

function addExtraRow(k = "", v = "") {
    const list = $("opt-extra-list");
    const row = document.createElement("div");
    row.className = "opt-extra-row";
    const ki = document.createElement("input");
    ki.className = "opt-k";
    ki.placeholder = "key";
    ki.value = k;
    const vi = document.createElement("input");
    vi.className = "opt-v";
    vi.placeholder = "value";
    vi.value = String(v);
    const rm = document.createElement("button");
    rm.type = "button";
    rm.className = "mini";
    rm.textContent = "✕";
    rm.onclick = () => { row.remove(); updateCustomOptionsCount(); };
    row.append(ki, vi, rm);
    list.appendChild(row);
}

function updateCustomOptionsCount() {
    const n = Object.keys(readCustomOptions()).length;
    const tag = $("custom-options-count");
    if (!tag) return;
    tag.hidden = n === 0;
    tag.textContent = n === 1 ? "(1 项)" : `(${n} 项)`;
}

function sendCustomOptions() {
    const opts = readCustomOptions();
    // the audio toggle owns the `audio` field — make sure extra-row edits
    // can't shadow it
    if (!audioEnabled) opts.audio = false;
    else delete opts.audio;
    rememberSession({ customOptions: opts, audioEnabled });
    sendWs({ t: "options", options: opts });
    updateCustomOptionsCount();
}

writeCustomOptions(session.customOptions);
$("opt-apply").onclick = sendCustomOptions;
$("opt-reset").onclick = () => { writeCustomOptions(null); sendCustomOptions(); };
$("opt-extra-add").onclick = () => addExtraRow();
for (const f of OPT_FIELDS) {
    const el = $(f.id);
    if (el) el.addEventListener("change", updateCustomOptionsCount);
}

// shows the remembered state before the first click
$("btn-mute").textContent = muted ? "🔇" : "🔊";
function paintSoundButton() {
    const b = $("btn-mute");
    // device audio off takes precedence in the icon — that's the unusual state
    if (!audioEnabled) {
        b.textContent = "🚫";
        b.classList.add("off");
        b.title = "设备音频已关闭（长按恢复）";
    } else {
        b.textContent = muted ? "🔇" : "🔊";
        b.classList.remove("off");
        b.title = muted ? "已静音（单击发声，长按关闭设备音频）"
                        : "有声（单击静音，长按关闭设备音频）";
    }
}
paintSoundButton();

// one button, two gestures:
//   tap     → toggle browser-side muted (videoEl.muted)
//   long-press (≥600ms) → toggle device-side audio (server restart)
let soundPressTimer = null;
function cancelSoundPress() {
    if (soundPressTimer) {
        clearTimeout(soundPressTimer);
        soundPressTimer = null;
    }
}
$("btn-mute").addEventListener("pointerdown", () => {
    cancelSoundPress();
    soundPressTimer = setTimeout(() => {
        soundPressTimer = null;
        audioEnabled = !audioEnabled;
        rememberSession({ audioEnabled });
        paintSoundButton();
        const opts = readCustomOptions();
        if (!audioEnabled) opts.audio = false;
        else delete opts.audio;
        writeCustomOptions(opts);
        sendCustomOptions();
    }, 600);
});
$("btn-mute").addEventListener("pointerup", () => {
    if (!soundPressTimer) return; // long-press already fired
    cancelSoundPress();
    muted = !muted;
    // not persisted: every new tab/session begins silent so the browser's
    // autoplay policy is satisfied without a NotAllowedError on first play
    paintSoundButton();
    // both audio and video tracks live on videoEl — toggle its muted
    // property to switch audio playback
    if (videoEl) {
        videoEl.muted = muted;
        videoEl.play?.().catch(() => {});
    }
});
$("btn-mute").addEventListener("pointerleave", cancelSoundPress);
$("btn-mute").addEventListener("pointercancel", cancelSoundPress);

// clipboard sync: browser -> device on tab focus (permission permitting)
window.addEventListener("focus", async () => {
    if (!currentSerial || !navigator.clipboard?.readText) return;
    try {
        const text = await navigator.clipboard.readText();
        if (text && text !== lastClipFromDevice && text !== lastClipToDevice) {
            lastClipToDevice = text;
            sendControl({ t: "clipboard", text });
        }
    } catch {} // permission denied — ignore silently
});

$("refresh").onclick = loadDevices;
loadDevices();
