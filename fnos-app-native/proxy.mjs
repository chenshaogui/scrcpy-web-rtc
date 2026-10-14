// proxy.mjs — in-process unix-socket gateway + (optional) static page serving.
//
// Single process, single bundle: forwards to the inner HTTP server on
// 127.0.0.1:<INNER_PORT>. The page served at "/" comes from webDir (the
// fpk's web/index.html, with the inlined gateway-prefix-aware bundle);
// /api/* and /api/stream (WS) are forwarded to the real server.

import {
    createServer,
    request as httpRequest,
} from "node:http";
import { connect } from "node:net";
import {
    appendFileSync,
    chmodSync,
    readFileSync,
    rmSync,
    statSync,
} from "node:fs";
import { join } from "node:path";

export function startProxy({ socket, prefix, target, webDir, logFile }) {
    if (!socket) {
        process.stdout.write("[proxy] GATEWAY_SOCKET not set, skipping gateway\n");
        return null;
    }

    const PREFIX = (prefix || "").replace(/\/$/, "");
    const targetUrl = new URL(target);
    const log = (...parts) => {
        const line = `[${new Date().toISOString()}] ${parts.join(" ")}\n`;
        process.stdout.write(line);
        if (logFile) {
            try { appendFileSync(logFile, line); } catch {}
        }
    };

    log(`gateway proxy starting: sock=${socket} prefix=${PREFIX} target=${target} web=${webDir || "(none)"}`);

    function rewritePath(reqUrl) {
        if (!PREFIX) return reqUrl;
        if (reqUrl === PREFIX || reqUrl === `${PREFIX}/`) return "/";
        if (reqUrl.startsWith(`${PREFIX}/`)) return reqUrl.slice(PREFIX.length) || "/";
        return null;
    }

    function rejectHttp(res, code, message) {
        const body = `${message}\n`;
        res.writeHead(code, {
            "content-type": "text/plain; charset=utf-8",
            "content-length": Buffer.byteLength(body),
            "cache-control": "no-store",
        });
        res.end(body);
    }

    function rejectUpgrade(sock, code, message) {
        const reason = code === 403 ? "Forbidden" : "Bad Gateway";
        const body = `${message}\n`;
        sock.end(
            `HTTP/1.1 ${code} ${reason}\r\n` +
            "Content-Type: text/plain; charset=utf-8\r\n" +
            `Content-Length: ${Buffer.byteLength(body)}\r\n` +
            "Cache-Control: no-store\r\n" +
            "Connection: close\r\n\r\n" +
            body,
        );
    }

    function serveStatic(upPath, res) {
        if (!webDir) return false;
        // only serve GET/HEAD on a single, safe path
        const safe = upPath.replace(/\/+$/, "") || "/";
        const rel = safe === "/" ? "index.html" : safe.replace(/^\/+/, "");
        if (rel.includes("..") || rel.includes("\\") || !/^[\w./-]+$/.test(rel)) {
            return false;
        }
        const file = join(webDir, rel);
        try {
            const st = statSync(file);
            if (!st.isFile()) return false;
            res.writeHead(200, {
                "content-type": rel.endsWith(".html")
                    ? "text/html; charset=utf-8"
                    : "application/octet-stream",
                "content-length": st.size,
                "cache-control": "no-store",
            });
            if (res.req.method === "HEAD") { res.end(); return true; }
            res.end(readFileSync(file));
            return true;
        } catch {
            return false;
        }
    }

    const server = createServer((req, res) => {
        const upPath = rewritePath(req.url);
        if (upPath === null) {
            log(`HTTP REJECT path=${req.url}`);
            rejectHttp(res, 404, "Not found");
            return;
        }

        // non-/api/* — try the local page; fall through to upstream otherwise
        if (!upPath.startsWith("/api/")) {
            if (serveStatic(upPath, res)) {
                log(`HTTP ${req.method} ${req.url} -> static ${upPath}`);
                return;
            }
        }

        log(`HTTP ${req.method} ${req.url} -> ${upPath}`);
        const fwdHeaders = { ...req.headers };
        fwdHeaders.host = targetUrl.host;
        delete fwdHeaders["proxy-connection"];
        delete fwdHeaders.connection;

        const opts = {
            hostname: targetUrl.hostname,
            port: targetUrl.port || 80,
            path: upPath,
            method: req.method,
            headers: fwdHeaders,
        };

        const upstream = httpRequest(opts, (upRes) => {
            res.writeHead(upRes.statusCode || 502, upRes.headers);
            upRes.pipe(res);
        });
        upstream.on("error", (e) => {
            log(`HTTP ERR ${upPath} ${e.message}`);
            if (!res.headersSent) rejectHttp(res, 502, `Bad Gateway: ${e.message}`);
            else res.destroy(e);
        });
        req.on("aborted", () => upstream.destroy());
        req.on("error", () => upstream.destroy());
        req.pipe(upstream);
    });

    server.on("upgrade", (req, sock, head) => {
        const upPath = rewritePath(req.url);
        if (upPath === null) {
            log(`WS REJECT path=${req.url}`);
            rejectUpgrade(sock, 404, "Not found");
            return;
        }
        log(`WS UPGRADE ${req.url} -> ${upPath}`);

        const fwdHeaders = {};
        for (const [k, v] of Object.entries(req.headers)) {
            if (v === undefined) continue;
            if (k.startsWith("proxy-")) continue;
            fwdHeaders[k] = v;
        }
        // llhttp only classifies the request as an upgrade when both
        // `Upgrade: websocket` and `Connection: upgrade` are present.
        fwdHeaders.connection = "Upgrade";
        fwdHeaders.host = targetUrl.host;

        const upstream = connect(targetUrl.port || 80, targetUrl.hostname, () => {
            const lines = [`${req.method} ${upPath} HTTP/${req.httpVersion}`];
            for (const [name, value] of Object.entries(fwdHeaders)) {
                if (Array.isArray(value)) {
                    for (const item of value) lines.push(`${name}: ${item}`);
                } else {
                    lines.push(`${name}: ${value}`);
                }
            }
            upstream.write(`${lines.join("\r\n")}\r\n\r\n`);
            if (head.length > 0) upstream.write(head);
            sock.pipe(upstream);
            upstream.pipe(sock);
        });
        upstream.on("error", (e) => {
            log(`WS ERR ${upPath} ${e.message}`);
            try { sock.destroy(e); } catch {}
        });
        sock.on("error", () => { try { upstream.destroy(); } catch {} });
        sock.on("close", () => { try { upstream.destroy(); } catch {} });
    });

    server.on("clientError", (_e, sock) => {
        try { sock.end("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"); } catch {}
    });
    server.headersTimeout = 60_000;
    server.requestTimeout = 0;

    rmSync(socket, { force: true });
    server.listen(socket, () => {
        try { chmodSync(socket, 0o666); } catch {}
        log(`gateway proxy listening on unix://${socket}`);
    });

    return server;
}
