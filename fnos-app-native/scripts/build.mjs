// scripts/build.mjs — produce a single self-contained CJS bundle for the
// native (no-Docker) fnOS package.
//
// Source: fnos-app-native/src/server.mjs (a fork of the workspace's
// upstream server.mjs with the adb refactor applied — see README).
//
// Patches applied at bundle time to server.mjs (via esbuild plugin):
//   1. drop the dev-only dynamic import("@yume-chan/fetch-scrcpy-server")
//      fallback inside loadServerBin. Function becomes sync.
//   2. change `const serverBin = await loadServerBin()` → sync call,
//      removing the module's top-level await so it can be bundled to CJS.
//   3. change `--version` block's `await readFile` → readFileSync.
//   4. inject readFileSync into the existing node:fs import.
//
// `import.meta.url` is rewritten to a CJS-friendly expression via
// `banner` + `define`, so file-URL semantics work in the bundled CJS
// output (Node has no native import.meta in CJS).

import { build } from "esbuild";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { existsSync, readFileSync } from "node:fs";

const here = dirname(fileURLToPath(import.meta.url));
const root = dirname(here);
const buildDir = join(root, "build");
const out = join(buildDir, "server.cjs");
const serverEntry = join(root, "src", "server.mjs");
const shim = join(root, "shims", "fetch-scrcpy-server.mjs");

if (!existsSync(serverEntry)) {
    console.error(`server entry not found: ${serverEntry}`);
    process.exit(1);
}

const patchServerMjs = {
    name: "patch-server-mjs",
    setup(build) {
        const norm = (p) => p.replace(/\\/g, "/");
        const target = norm(serverEntry);
        build.onLoad({ filter: /\.mjs$/ }, async (args) => {
            if (norm(args.path) !== target) return null;
            let src = readFileSync(args.path, "utf8");

            // 1) add readFileSync to the node:fs import (keeps existsSync)
            src = src.replace(
                'import { existsSync } from "node:fs";',
                'import { existsSync, readFileSync } from "node:fs";',
            );

            // 2) drop the dev dynamic-import fallback inside loadServerBin,
            //    make the function sync, and switch await readFile → readFileSync.
            src = src.replace(
                /async function loadServerBin\(\) \{[\s\S]*?\n\}/,
                `function loadServerBin() {
    // server.bin is shipped next to this module. The dynamic-import
    // fallback was only useful for dev checkouts via npx fetch-scrcpy-server;
    // a packaged install always has the local file (npm run prepack copies
    // it next to server.mjs), so we don't need the runtime dep anymore.
    const bundled = join(PKG_DIR, "server.bin");
    if (existsSync(bundled)) return new Uint8Array(readFileSync(bundled));
    console.error("scrcpy server binary not found at " + bundled + "; run: npx fetch-scrcpy-server 3.1");
    process.exit(1);
}`,
            );

            // 3) drop the top-level `await` on the loadServerBin call
            src = src.replace(
                "const serverBin = await loadServerBin();",
                "const serverBin = loadServerBin();",
            );

            // 4) the --version block uses await readFile; switch to sync
            src = src.replace(
                /await readFile\(join\(dirname\(fileURLToPath\(import\.meta\.url\)\), "package\.json"\), "utf8"\)/,
                'readFileSync(join(dirname(fileURLToPath(import.meta.url)), "package.json"), "utf8")',
            );

            return { contents: src, loader: "js" };
        });
    },
};

await build({
    entryPoints: [serverEntry],
    outfile: out,
    bundle: true,
    format: "cjs",
    platform: "node",
    target: ["node20"],
    plugins: [patchServerMjs],
    alias: {
        "@yume-chan/fetch-scrcpy-server": shim,
    },
    define: {
        "import.meta.url": "__meta_url",
        "import.meta.dirname": "__meta_dirname",
        "import.meta.filename": "__meta_filename",
    },
    banner: {
        // esbuild rewrites every `import.meta.url` to the identifier
        // __meta_url, declared here once. The shape matches Node's
        // ESM `import.meta.url` (a file:// URL) so downstream code that
        // does dirname(fileURLToPath(import.meta.url)) keeps working.
        js:
            'var __meta_url = require("url").pathToFileURL(__filename).href;' +
            'var __meta_dirname = __dirname;' +
            'var __meta_filename = __filename;',
    },
    external: [],
    sourcemap: false,
    minify: false,
    legalComments: "none",
    logLevel: "info",
});

// ── proxy bundle (CJS, gateway unix-socket listener) ───────────────
const proxyEntry = join(root, "proxy.mjs");
const proxyOut = join(buildDir, "proxy.cjs");if (!existsSync(proxyEntry)) {
    console.error(`proxy entry not found: ${proxyEntry}`);
    process.exit(1);
}
await build({
    entryPoints: [proxyEntry],
    outfile: proxyOut,
    bundle: true,
    format: "cjs",
    platform: "node",
    target: ["node20"],
    external: [],
    sourcemap: false,
    minify: false,
    legalComments: "none",
    logLevel: "info",
    // proxy.mjs is a pure library (exports startProxy only). When executed
    // as `node proxy.cjs` nothing would run and the process would exit
    // immediately — silently, since everything is wrapped in try/catch.
    // Append the CLI bootstrap here so the bundle self-starts from the
    // env vars cmd/main exports (GATEWAY_SOCKET / GATEWAY_PREFIX / etc.).
    footer: {
        js: `
// ── CLI bootstrap (appended by scripts/build.mjs) ──
if (require.main === module) {
    startProxy({
        socket: process.env.GATEWAY_SOCKET,
        prefix: process.env.GATEWAY_PREFIX,
        target: "http://127.0.0.1:" + (process.env.INNER_PORT || "18010"),
        webDir: process.env.WEB_DIR,
        logFile: process.env.LOG_FILE,
    });
}
`,
    },
});

console.log(`[OK] ${out}`);
console.log(`[OK] ${proxyOut}`);

// ── sync bundles into package/app/ ────────────────────────────────
// build-fpk.mjs stages package/ verbatim, so the fpk only ships what's
// in package/app/. Copy the fresh bundles there (and verify), otherwise
// the package keeps a stale copy from a previous manual copy step.
import { copyFileSync } from "node:fs";
const pkgApp = join(root, "package", "app");
if (!existsSync(pkgApp)) {
    console.error(`package/app not found: ${pkgApp}`);
    process.exit(1);
}
for (const [src, name] of [[out, "server.cjs"], [proxyOut, "proxy.cjs"]]) {
    copyFileSync(src, join(pkgApp, name));
    console.log(`[OK] ${join(pkgApp, name)} (synced from build/)`);
}

// ── ensure server.bin ships in package/app/ ───────────────────────
// server.cjs calls loadServerBin() at module top level, which reads the
// sibling server.bin and process.exit(1)s if it's missing. Without this
// file the app dies instantly on start and fnOS reports "gateway socket
// not created within 15s". Sync it from the fetched npm package (run
// `npm run fetch-server` at the workspace root to download it first).
import { statSync } from "node:fs";
const pkgServerBin = join(pkgApp, "server.bin");
const fetchedServerBin = resolve(root, "..", "node_modules", "@yume-chan", "fetch-scrcpy-server", "server.bin");
if (existsSync(fetchedServerBin)) {
    copyFileSync(fetchedServerBin, pkgServerBin);
    console.log(`[OK] ${pkgServerBin} (synced from fetch-scrcpy-server, ${statSync(pkgServerBin).size} B)`);
} else if (existsSync(pkgServerBin)) {
    console.log(`[OK] ${pkgServerBin} (kept existing, ${statSync(pkgServerBin).size} B)`);
} else {
    console.error(`[FAIL] server.bin missing. Neither ${fetchedServerBin} nor ${pkgServerBin} exists.`);
    console.error(`       Run \`npm run fetch-server\` at the workspace root, then rebuild.`);
    process.exit(1);
}
