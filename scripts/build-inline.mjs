// scripts/build-inline.mjs
//
// esbuild bundle + inline the result into index.html, so the page needs
// zero sub-resource requests. fnOS's gateway serves the app at
// /app/scrcpy-webrtc (no trailing slash), where a relative
// <script src="./app.js"> would resolve to /app/app.js and 404, so the
// iframe stays blank. Inlining sidesteps that entirely.
//
// Steps:
//   1) bundle src/client.js to a self-contained IIFE in public/app.iife.js
//   2) read public/index.html and replace either the <script src="./app.js">
//      tag or an already-inlined <script> block with the fresh bundle
//   3) write the result back to public/index.html and drop the standalone
//      .js so nothing else references it
//
// Idempotent: safe to run repeatedly. After the first run the
// <script src="./app.js"> tag is gone, so subsequent runs swap the existing
// inlined block in place instead of bailing out.
//
// Usage:
//   node scripts/build-inline.mjs
//   copy public/index.html -> fnos-app/package/app/web/index.html

import { build } from "esbuild";
import { existsSync, readFileSync, writeFileSync, unlinkSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const publicDir = join(root, "public");

const outFile = join(publicDir, "app.iife.js");
const htmlFile = join(publicDir, "index.html");

await build({
    entryPoints: [join(root, "src/client.js")],
    bundle: true,
    format: "iife",
    outfile: outFile,
    minify: true,
    loader: { ".css": "text" },
    target: ["es2020"],
    logLevel: "info",
});

const js = readFileSync(outFile, "utf8");
const block = `<script>\n${js}\n</script>`;

const html = readFileSync(htmlFile, "utf8");
const tag = '<script type="module" src="./app.js"></script>';

let next = null;
if (html.includes(tag)) {
    next = html.replace(tag, block);
    console.log("[build-inline] replaced <script src> tag");
} else {
    // already inlined by a previous run: replace the existing block in place.
    // Find the first opening tag and the last closing tag so a literal
    // "</script>" inside the minified JS cannot truncate the match.
    const start = html.indexOf("<script>");
    const end = html.lastIndexOf("</script>");
    if (start !== -1 && end > start && end - start > 1000) {
        next = html.slice(0, start) + block + html.slice(end + "</script>".length);
        console.log("[build-inline] replaced existing inlined <script> block");
    }
}

if (next === null) {
    console.error(
        'could not find a <script type="module" src="./app.js"></script> tag ' +
        "nor an inlined <script> block (>1kb) in public/index.html",
    );
    process.exit(1);
}

writeFileSync(htmlFile, next);
unlinkSync(outFile);

console.log(`[build-inline] inlined ${(js.length / 1024).toFixed(1)}kb of JS into ${htmlFile}`);

// the fpk ships its own copy of the page — keep the two in step, otherwise a
// rebuild quietly packages the previous UI (this bit the 0.1.2 release)
const packaged = join(root, "fnos-app-native", "package", "app", "web", "index.html");
if (existsSync(packaged)) {
    writeFileSync(packaged, next);
    console.log(`[build-inline] synced to ${packaged}`);
}
