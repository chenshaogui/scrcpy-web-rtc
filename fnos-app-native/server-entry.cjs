// server-entry.cjs — single-process entry for the native (no-Docker) fpk.
//
// Written as CJS so the bundled output is CJS (lets `ws` and other CJS deps
// keep their native require behaviour). The dynamic import() calls still
// work in CJS: Node's loader treats server.mjs / proxy.mjs as ESM and
// honours their top-level await.

const INNER_PORT = process.env.INNER_PORT || "8010";
process.argv = [
    process.argv[0],
    "server.cjs",
    "--port", INNER_PORT,
    "--no-page",
    "--no-signal",
    "--no-qr",
];
process.env.PORT = INNER_PORT;
// the gateway proxy serves the page from this directory; passed by cmd/main
const WEB_DIR = process.env.WEB_DIR;

(async () => {
    await import("../server.mjs");
    const { startProxy } = await import("./proxy.mjs");
    startProxy({
        socket: process.env.GATEWAY_SOCKET,
        prefix: process.env.GATEWAY_PREFIX || "/app/scrcpy-webrtc",
        target: `http://127.0.0.1:${INNER_PORT}`,
        webDir: WEB_DIR,
        logFile: process.env.LOG_FILE,
    });
})();
