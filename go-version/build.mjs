// build.mjs — prepare static assets + scrcpy server binary, then compile the
// self-contained Go server. Run from the go-version directory (or anywhere):
//
//   node go-version/build.mjs
//
// The Go binary needs two runtime assets next to it:
//   public/index.html + public/app.js   (the frontend, shared with the Node app)
//   server.bin                          (scrcpy-server v3.1 jar)
import { cpSync, mkdirSync, existsSync, copyFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..");
const outDir = join(here, "dist");

mkdirSync(outDir, { recursive: true });

// 1. Frontend (shared with the Node implementation).
const publicSrc = join(repoRoot, "public");
const publicDst = join(outDir, "public");
if (existsSync(publicSrc)) {
    cpSync(publicSrc, publicDst, { recursive: true });
    console.log(`[OK] frontend -> ${publicDst}`);
} else {
    console.error(`[FAIL] frontend not found: ${publicSrc} (run npm run build at repo root first)`);
    process.exit(1);
}

// 2. scrcpy-server binary.
const candidates = [
    join(repoRoot, "server.bin"),
    join(repoRoot, "node_modules", "@yume-chan", "fetch-scrcpy-server", "server.bin"),
];
let copied = false;
for (const src of candidates) {
    if (existsSync(src)) {
        copyFileSync(src, join(outDir, "server.bin"));
        console.log(`[OK] server.bin <- ${src}`);
        copied = true;
        break;
    }
}
if (!copied) {
    console.error("[FAIL] server.bin not found; run: npm run fetch-server (at repo root)");
    process.exit(1);
}

// 3. Compile.
const exe = process.platform === "win32" ? "scrcpy-webrtc.exe" : "scrcpy-webrtc";
const goArgs = ["build", "-o", join(outDir, exe), "./cmd/scrcpy-webrtc"];
console.log(`[build] go ${goArgs.join(" ")}`);
const env = { ...process.env, GOPROXY: process.env.GOPROXY || "https://goproxy.cn,direct" };
const result = spawnSync("go", goArgs, { cwd: here, stdio: "inherit", env });
if (result.status !== 0) {
    console.error("[FAIL] go build failed");
    process.exit(result.status ?? 1);
}
console.log(`[OK] ${join(outDir, exe)}`);
