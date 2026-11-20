// scripts/build-fpk.mjs — build the Go fpk packages (arm64 + amd64).
//
// Steps per target:
//   1. Cross-compile scrcpy-webrtc + gateway with GOOS=linux (CGO disabled).
//   2. Stage fnos-app-go/package/* into dist/<arch>-stage/, inject the two
//      binaries, server.bin (scrcpy-server v3.1) and the inlined web page.
//   3. Set platform=arm or platform=x86 in the staged manifest.
//   4. Run fnpack build --directory stage.
//   5. Move the resulting .fpk to dist/scrcpy-remote-go_<ver>_<arch>.fpk.
//
// Run:   node scripts/build-fpk.mjs
//   or:  node scripts/build-fpk.mjs --only arm64
//        node scripts/build-fpk.mjs --only amd64
import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, readFileSync, writeFileSync, existsSync, copyFileSync, statSync, rmSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = dirname(here);          // fnos-app-go
const repoRoot = resolve(root, ".."); // repo
const goDir = join(repoRoot, "go-version");
const packageSrc = join(root, "package");
const distDir = join(root, "dist");
const toolsDir = join(root, ".tools");
const FNPACK_VERSION = "1.2.3";

const TARGETS = [
    { arch: "arm64", goarch: "arm64", fnpackName: "arm" },
    { arch: "amd64", goarch: "amd64", fnpackName: "x86" },
];

function findFnpack() {
    if (process.env.FNPACK) return resolve(process.env.FNPACK);
    // Reuse the fnpack.exe already downloaded by fnos-app-native.
    const shared = resolve(repoRoot, "fnos-app-native", ".tools", "fnpack.exe");
    if (existsSync(shared)) return shared;
    const local = join(toolsDir, "fnpack.exe");
    if (existsSync(local)) return local;
    const inPath = spawnSync("fnpack", ["--help"], { encoding: "utf8" });
    if (!inPath.error && inPath.status === 0) return "fnpack";
    throw new Error("fnpack not found; place fnpack.exe in fnos-app-go/.tools/ or fnos-app-native/.tools/");
}

function run(cmd, args, opts = {}) {
    const r = spawnSync(cmd, args, { stdio: "inherit", ...opts });
    if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited ${r.status}`);
}

function copyDirSync(src, dest) {
    mkdirSync(dest, { recursive: true });
    const entries = readdirSyncSafe(src);
    if (entries.length === 0) {
        writeFileSync(join(dest, ".keep"), "");
        return;
    }
    for (const e of entries) {
        if (e.isDirectory()) copyDirSync(join(src, e.name), join(dest, e.name));
        else copyFileSync(join(src, e.name), join(dest, e.name));
    }
}
function readdirSyncSafe(p) { return readdirSync(p, { withFileTypes: true }); }

// Locate a runtime asset, trying a few candidates.
function firstExisting(paths) {
    for (const p of paths) if (existsSync(p)) return p;
    return null;
}

function buildBinaries(target, binDir) {
    mkdirSync(binDir, { recursive: true });
    const env = {
        ...process.env,
        GOPROXY: process.env.GOPROXY || "https://goproxy.cn,direct",
        CGO_ENABLED: "0",
        GOOS: "linux",
        GOARCH: target.goarch,
    };
    console.log(`[go] building linux/${target.goarch} binaries...`);
    run("go", ["build", "-trimpath", "-ldflags=-s -w",
        "-o", join(binDir, "scrcpy-webrtc"), "./cmd/scrcpy-webrtc"],
        { cwd: goDir, env });
    run("go", ["build", "-trimpath", "-ldflags=-s -w",
        "-o", join(binDir, "gateway"), "./cmd/gateway"],
        { cwd: goDir, env });
    console.log(`[go] scrcpy-webrtc ${statSync(join(binDir, "scrcpy-webrtc")).size} B, gateway ${statSync(join(binDir, "gateway")).size} B`);
}

function stagePackage(target, binDir) {
    const stage = join(distDir, `${target.arch}-stage`);
    rmSync(stage, { recursive: true, force: true });
    mkdirSync(stage, { recursive: true });

    for (const e of readdirSyncSafe(packageSrc)) {
        const src = join(packageSrc, e.name);
        const dst = join(stage, e.name);
        if (e.isDirectory()) copyDirSync(src, dst);
        else copyFileSync(src, dst);
    }

    const appDir = join(stage, "app");
    mkdirSync(appDir, { recursive: true });

    // Go binaries.
    copyFileSync(join(binDir, "scrcpy-webrtc"), join(appDir, "scrcpy-webrtc"));
    copyFileSync(join(binDir, "gateway"), join(appDir, "gateway"));

    // scrcpy-server jar.
    const serverBin = firstExisting([
        join(repoRoot, "server.bin"),
        join(repoRoot, "node_modules", "@yume-chan", "fetch-scrcpy-server", "server.bin"),
    ]);
    if (!serverBin) throw new Error("server.bin not found; run `npm run fetch-server` at repo root");
    copyFileSync(serverBin, join(appDir, "server.bin"));

    // Inlined frontend page (index.html with bundled app.js). Gateway serves
    // it from web/; the Go server only needs the API paths.
    const webDir = join(appDir, "web");
    mkdirSync(webDir, { recursive: true });
    const indexHtml = firstExisting([
        join(repoRoot, "public", "index.html"),
        join(repoRoot, "fnos-app-native", "package", "app", "web", "index.html"),
    ]);
    if (!indexHtml) throw new Error("index.html not found");
    const html = readFileSync(indexHtml, "utf8");
    if (!html.includes("</head>")) throw new Error("index.html has no </head>");
    if (!html.includes("</body>")) throw new Error("index.html has no </body>");
    for (const id of ["opt-video-encoder", "opt-audio-encoder"]) {
        if (!new RegExp(`<select\\b[^>]*\\bid=["']${id}["']`, "i").test(html)) {
            throw new Error(`index.html #${id} must be a <select>`);
        }
    }
    copyFileSync(indexHtml, join(webDir, "index.html"));

    // Icons: copy from the native package's ui/images.
    const iconsSrc = join(repoRoot, "fnos-app-native", "package", "app", "ui", "images");
    if (existsSync(iconsSrc)) {
        const iconsDst = join(stage, "app", "ui", "images");
        mkdirSync(iconsDst, { recursive: true });
        for (const f of readdirSyncSafe(iconsSrc)) {
            if (!f.isDirectory()) copyFileSync(join(iconsSrc, f.name), join(iconsDst, f.name));
        }
    }
    // Package-level icons (fnpack may use these).
    for (const icon of ["ICON.PNG", "ICON_256.PNG", "icon_256.png", "icon_64.png"]) {
        const candidates = [
            join(repoRoot, "fnos-app-native", "package", icon),
            join(iconsSrc, icon),
        ];
        const found = firstExisting(candidates);
        if (found) copyFileSync(found, join(stage, icon));
    }

    // Set platform in staged manifest.
    const manifestPath = join(stage, "manifest");
    const data = readFileSync(manifestPath, "utf8")
        .replace(/^platform=.*$/m, `platform=${target.fnpackName}`);
    writeFileSync(manifestPath, data);

    return stage;
}

function main() {
    const onlyArg = process.argv.slice(2).find((a) => a === "--only" || a.startsWith("--only=")) || "";
    const only = onlyArg === "--only"
        ? (process.argv[process.argv.indexOf(onlyArg) + 1] || "")
        : onlyArg.replace(/^--only=/, "");
    const onlyClean = only.trim();
    const fnpack = findFnpack();
    mkdirSync(distDir, { recursive: true });

    const manifest = readFileSync(join(packageSrc, "manifest"), "utf8");
    const appname = (manifest.match(/^appname=(.*)$/m) || [, "scrcpy-remote-go"])[1].trim();
    const version = (manifest.match(/^version=(.*)$/m) || [, "0.0.0"])[1].trim();

    for (const target of TARGETS) {
        if (onlyClean && onlyClean !== target.arch && onlyClean !== target.fnpackName) continue;
        console.log(`\n[build] === ${target.arch} (platform=${target.fnpackName}) ===`);

        const binDir = join(distDir, `bin-${target.arch}`);
        buildBinaries(target, binDir);
        const stage = stagePackage(target, binDir);

        // fnpack writes <appname>.fpk into cwd; run from fnos-app-go so any
        // previous build is overwritten.
        run(fnpack, ["build", "--directory", stage], { cwd: root });
        const built = join(root, `${appname}.fpk`);
        if (!existsSync(built)) throw new Error(`fnpack succeeded but no ${appname}.fpk found`);

        const finalName = `${appname}_${version}_${target.fnpackName}.fpk`;
        const finalPath = join(distDir, finalName);
        rmSync(finalPath, { force: true });
        cpSync(built, finalPath);
        rmSync(built, { force: true });
        console.log(`[build] -> ${finalPath} (${statSync(finalPath).size} B)`);
        rmSync(stage, { recursive: true, force: true });
        rmSync(binDir, { recursive: true, force: true });
    }
    console.log("\n[build] done");
}

main();
