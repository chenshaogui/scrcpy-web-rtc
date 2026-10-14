var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: true });
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// proxy.mjs
var proxy_exports = {};
__export(proxy_exports, {
  startProxy: () => startProxy
});
module.exports = __toCommonJS(proxy_exports);
var import_node_http = require("node:http");
var import_node_net = require("node:net");
var import_node_fs = require("node:fs");
var import_node_path = require("node:path");
function startProxy({ socket, prefix, target, webDir, logFile }) {
  if (!socket) {
    process.stdout.write("[proxy] GATEWAY_SOCKET not set, skipping gateway\n");
    return null;
  }
  const PREFIX = (prefix || "").replace(/\/$/, "");
  const targetUrl = new URL(target);
  const log = (...parts) => {
    const line = `[${(/* @__PURE__ */ new Date()).toISOString()}] ${parts.join(" ")}
`;
    process.stdout.write(line);
    if (logFile) {
      try {
        (0, import_node_fs.appendFileSync)(logFile, line);
      } catch {
      }
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
    const body = `${message}
`;
    res.writeHead(code, {
      "content-type": "text/plain; charset=utf-8",
      "content-length": Buffer.byteLength(body),
      "cache-control": "no-store"
    });
    res.end(body);
  }
  function rejectUpgrade(sock, code, message) {
    const reason = code === 403 ? "Forbidden" : "Bad Gateway";
    const body = `${message}
`;
    sock.end(
      `HTTP/1.1 ${code} ${reason}\r
Content-Type: text/plain; charset=utf-8\r
Content-Length: ${Buffer.byteLength(body)}\r
Cache-Control: no-store\r
Connection: close\r
\r
` + body
    );
  }
  function serveStatic(upPath, res) {
    if (!webDir) return false;
    const safe = upPath.replace(/\/+$/, "") || "/";
    const rel = safe === "/" ? "index.html" : safe.replace(/^\/+/, "");
    if (rel.includes("..") || rel.includes("\\") || !/^[\w./-]+$/.test(rel)) {
      return false;
    }
    const file = (0, import_node_path.join)(webDir, rel);
    try {
      const st = (0, import_node_fs.statSync)(file);
      if (!st.isFile()) return false;
      res.writeHead(200, {
        "content-type": rel.endsWith(".html") ? "text/html; charset=utf-8" : "application/octet-stream",
        "content-length": st.size,
        "cache-control": "no-store"
      });
      if (res.req.method === "HEAD") {
        res.end();
        return true;
      }
      res.end((0, import_node_fs.readFileSync)(file));
      return true;
    } catch {
      return false;
    }
  }
  const server = (0, import_node_http.createServer)((req, res) => {
    const upPath = rewritePath(req.url);
    if (upPath === null) {
      log(`HTTP REJECT path=${req.url}`);
      rejectHttp(res, 404, "Not found");
      return;
    }
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
      headers: fwdHeaders
    };
    const upstream = (0, import_node_http.request)(opts, (upRes) => {
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
      if (v === void 0) continue;
      if (k.startsWith("proxy-")) continue;
      fwdHeaders[k] = v;
    }
    fwdHeaders.connection = "Upgrade";
    fwdHeaders.host = targetUrl.host;
    const upstream = (0, import_node_net.connect)(targetUrl.port || 80, targetUrl.hostname, () => {
      const lines = [`${req.method} ${upPath} HTTP/${req.httpVersion}`];
      for (const [name, value] of Object.entries(fwdHeaders)) {
        if (Array.isArray(value)) {
          for (const item of value) lines.push(`${name}: ${item}`);
        } else {
          lines.push(`${name}: ${value}`);
        }
      }
      upstream.write(`${lines.join("\r\n")}\r
\r
`);
      if (head.length > 0) upstream.write(head);
      sock.pipe(upstream);
      upstream.pipe(sock);
    });
    upstream.on("error", (e) => {
      log(`WS ERR ${upPath} ${e.message}`);
      try {
        sock.destroy(e);
      } catch {
      }
    });
    sock.on("error", () => {
      try {
        upstream.destroy();
      } catch {
      }
    });
    sock.on("close", () => {
      try {
        upstream.destroy();
      } catch {
      }
    });
  });
  server.on("clientError", (_e, sock) => {
    try {
      sock.end("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n");
    } catch {
    }
  });
  server.headersTimeout = 6e4;
  server.requestTimeout = 0;
  (0, import_node_fs.rmSync)(socket, { force: true });
  server.listen(socket, () => {
    try {
      (0, import_node_fs.chmodSync)(socket, 438);
    } catch {
    }
    log(`gateway proxy listening on unix://${socket}`);
  });
  return server;
}
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  startProxy
});

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

