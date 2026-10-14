// Inlines public/app.js into the single <script> bundle block of
// public/index.html and fnos-app-native/package/app/web/index.html.
// The page keeps the bundle inline (the fnOS fpk serves web/index.html and
// can't rely on a sibling app.js), so after `npm run build` regenerates
// public/app.js we re-embed it here.
//
// The bundle is built with esbuild `--format=esm`, and current dependencies
// (@yume-chan/scrcpy-decoder-webcodecs renderers pull in scrcpy-decoder-
// tinyh264, whose worker uses `new URL("./worker.js", import.meta.url)`) emit
// `import.meta`. That is a SyntaxError inside a classic <script>, which would
// abort the whole bundle and break every UI handler. So we always normalize
// the opening tag to `<script type="module">`.
import { readFileSync, writeFileSync } from "node:fs";

const app = readFileSync("public/app.js", "utf8");

const OPEN_RE = /<script\b[^>]*>/i;
const CLOSE = "</script>";
const MODULE_OPEN = '<script type="module">';

function inlineInto(path) {
    const html = readFileSync(path, "utf8");
    const m = OPEN_RE.exec(html);
    if (!m) throw new Error(`${path}: no <script> open tag`);
    const start = m.index;
    const contentStart = start + m[0].length;
    // the bundle is the page's only script block and it is last, so anchor the
    // close tag with lastIndexOf to survive a literal </script> inside the JS.
    const end = html.lastIndexOf(CLOSE);
    if (end === -1 || end < contentStart) throw new Error(`${path}: no </script> close tag`);
    const oldLen = end - contentStart;
    const next = html.slice(0, start) + MODULE_OPEN + app + html.slice(end);
    writeFileSync(path, next, "utf8");
    const changed = next !== html;
    console.log(`${path}: tag="${m[0]}" -> "${MODULE_OPEN}", replaced ${oldLen} bytes with ${app.length} bytes (changed=${changed})`);
}

for (const p of [
    "public/index.html",
    "fnos-app-native/package/app/web/index.html",
]) {
    inlineInto(p);
}
