// shims/fetch-scrcpy-server.mjs
//
// Drop-in for the @yume-chan/fetch-scrcpy-server package, kept for any
// future code path that still references it. In the native fpk we read
// server.bin directly from the sibling file, so this shim exposes the
// same `BIN` URL the upstream package would.
//
// Works in both ESM and CJS bundle output: in CJS `__dirname` is set, in
// ESM `import.meta.url` is set (esbuild's `define` rewrites that to the
// CJS-compatible expression when bundling).

import { fileURLToPath, pathToFileURL } from "node:url";
import { dirname, join } from "node:path";
import { existsSync } from "node:fs";

const here = (typeof __dirname === "string" && __dirname)
    ? __dirname
    : dirname(fileURLToPath(import.meta.url));
const BIN_PATH = join(here, "server.bin");

if (!existsSync(BIN_PATH)) {
    console.error(
        `scrcpy server binary not found at ${BIN_PATH}.\n` +
        "The native package should ship server.bin next to server.cjs.",
    );
    process.exit(1);
}

export const BIN = pathToFileURL(BIN_PATH);
export const VERSION = "3.1";
