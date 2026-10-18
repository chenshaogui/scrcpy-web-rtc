// scripts/build-fpk.mjs — assemble the fnOS Docker-app .fpk artifact.
//
// The Docker variant is arch-independent (manifest declares platform=all): the
// container image carries the architecture, and the fpk just ships the compose
// file + build context. So this produces ONE fpk, not an x86/arm pair like the
// native variant does.
//
// Steps:
//   1. Stage package/* into dist/stage/
//   2. Run fnpack build --directory dist/stage
//   3. Move the resulting scrcpy-remote-docker.fpk to dist/scrcpy-remote-docker_<ver>_all.fpk
//
// Reuses the fnpack binary cached by fnos-app-native (../fnos-app-native/.tools
// /fnpack.exe) when present, otherwise downloads it.
//
// Run:   node scripts/build-fpk.mjs

import { spawnSync } from "node:child_process";
import {
    existsSync, mkdirSync, readFileSync, readdirSync, copyFileSync,
    statSync, rmSync, writeFileSync, createWriteStream,
} from "node:fs";
import { rename } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { pipeline } from "node:stream/promises";

const here = dirname(fileURLToPath(import.meta.url));
const root = dirname(here);
const distDir = join(root, "dist");
const packageSrc = join(root, "package");
const toolsDir = join(root, ".tools");

const FNPACK_VERSION = "1.2.3";

function fnpackTarget() {
    if (process.platform !== "win32") return null;
    const arch = process.arch === "arm64" ? "arm64" : "amd64";
    return `windows-${arch}`;
}

async function findFnpack() {
    if (process.env.FNPACK) return resolve(process.env.FNPACK);
    // prefer the native variant's cache so both packages share one binary
    const shared = resolve(root, "..", "fnos-app-native", ".tools", "fnpack.exe");
    if (existsSync(shared)) return shared;
    const local = join(toolsDir, "fnpack.exe");
    if (existsSync(local)) return local;
    const inPath = spawnSync("fnpack", ["--help"], { encoding: "utf8" });
    if (!inPath.error && inPath.status === 0) return "fnpack";
    const target = fnpackTarget();
    if (!target) throw new Error(`fnpack is not available for ${process.platform}-${process.arch}`);
    const url = `https://static2.fnnas.com/fnpack/fnpack-${FNPACK_VERSION}-${target}`;
    mkdirSync(dirname(local), { recursive: true });
    process.stdout.write(`[fnpack] downloading ${url} → ${local}\n`);
    const res = await fetch(url);
    if (!res.ok) throw new Error(`failed to download fnpack: ${res.status}`);
    await pipeline(res.body, createWriteStream(local));
    return local;
}

function copyDirSync(src, dest) {
    mkdirSync(dest, { recursive: true });
    const entries = readdirSync(src, { withFileTypes: true });
    if (entries.length === 0) {
        writeFileSync(join(dest, ".keep"), "");
        return;
    }
    for (const e of entries) {
        if (e.isDirectory()) copyDirSync(join(src, e.name), join(dest, e.name));
        else copyFileSync(join(src, e.name), join(dest, e.name));
    }
}

async function stage() {
    const stageDir = join(distDir, "stage");
    if (existsSync(stageDir)) rmSync(stageDir, { recursive: true, force: true });
    mkdirSync(stageDir, { recursive: true });
    for (const entry of readdirSync(packageSrc, { withFileTypes: true })) {
        const src = join(packageSrc, entry.name);
        const dest = join(stageDir, entry.name);
        if (entry.isDirectory()) copyDirSync(src, dest);
        else copyFileSync(src, dest);
    }
    return stageDir;
}

async function main() {
    const fnpack = await findFnpack();
    mkdirSync(distDir, { recursive: true });

    const stageDir = await stage();
    process.stdout.write(`[build] staged at ${stageDir}\n`);

    // fnpack writes the fpk into the cwd, named after the manifest `appname`.
    const r = spawnSync(fnpack, ["build", "--directory", stageDir], {
        cwd: root,
        stdio: "inherit",
    });
    if (r.status !== 0) throw new Error(`fnpack exited with status ${r.status}`);

    const manifestData = readFileSync(join(packageSrc, "manifest"), "utf8");
    const appname = (manifestData.match(/^appname=(.*)$/m) || [, "scrcpy-remote-docker"])[1].trim();
    const version = (manifestData.match(/^version=(.*)$/m) || [, "0.0.0"])[1].trim();
    const built = join(root, `${appname}.fpk`);
    if (!existsSync(built)) throw new Error(`fnpack succeeded but no ${appname}.fpk was found`);

    const finalPath = join(distDir, `${appname}_${version}_all.fpk`);
    if (existsSync(finalPath)) rmSync(finalPath, { force: true });
    await rename(built, finalPath);
    process.stdout.write(`[build] → ${finalPath} (${statSync(finalPath).size} B)\n`);

    rmSync(stageDir, { recursive: true, force: true });
    process.stdout.write("\n[build] done\n");
}

main().catch((e) => {
    process.stderr.write(`[build] ${e.stack || e.message || e}\n`);
    process.exit(1);
});
