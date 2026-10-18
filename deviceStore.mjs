// deviceStore.mjs — adb device bookkeeping for the native (no-Docker) fpk.
//
// Drop-in replacement for the @yume-chan/adb AdbServerClient that doesn't
// need an external adb-server binary. It keeps a JSON list of "adb connect"
// targets in TRIM_PKGVAR/devices.json (path overridable via DEVICES_FILE)
// and translates the three calls server.mjs makes:
//
//   adbClient.getDevices()                  → list + probe each
//   adbClient.wireless.connect("ip:port")   → open + save + close
//   adbClient.createTransport({ serial })   → open transport on demand
//
// into direct AdbDaemonTransport.connect({ host, port }) calls. The bundled
// page keeps using the same REST API (`/api/devices`, `/api/adb-connect`)
// and the same { serial, product, model } shape, so the upstream client
// works unchanged.
//
// Trade-off vs an adb server: this only sees devices the user has
// explicitly added with the "ADB 连接" form. That matches what
// scrcpy-webrtc already advertises ("view and control WiFi-connected
// Android devices"), and it removes the only non-Node native dep.

import { existsSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { Adb } from "@yume-chan/adb";
import { connectAdbDaemonTcp, NodeAdbCredentialStore } from "./adbTcp.mjs";

const STORE_PATH =
    process.env.DEVICES_FILE ||
    (process.env.TRIM_PKGVAR
        ? join(process.env.TRIM_PKGVAR, "devices.json")
        : null);

// The shared RSA keypair used to authenticate against adb daemons.
// Persisted in TRIM_PKGVAR so the device keeps trusting us across restarts.
const CREDENTIAL_STORE = new NodeAdbCredentialStore(
    process.env.TRIM_PKGVAR ? join(process.env.TRIM_PKGVAR, "adbkey") : null,
);

function load() {
    if (!STORE_PATH) return [];
    try {
        if (!existsSync(STORE_PATH)) return [];
        return JSON.parse(readFileSync(STORE_PATH, "utf8"));
    } catch {
        return [];
    }
}

function save(list) {
    if (!STORE_PATH) return;
    try {
        mkdirSync(dirname(STORE_PATH), { recursive: true });
        writeFileSync(STORE_PATH, JSON.stringify(list, null, 2));
    } catch {
        // best-effort persistence; in-memory state is still authoritative
    }
}

function shortModel(product, model) {
    return (model || product || "").replaceAll("_", " ").trim();
}

export class DeviceStore {
    constructor() {
        this._cache = null;
    }

    _read() {
        if (this._cache === null) this._cache = load();
        return this._cache;
    }

    _write(list) {
        this._cache = list;
        save(list);
    }

    async _probe(host, port) {
        const transport = await connectAdbDaemonTcp({
            host,
            port: Number(port),
            credentialStore: CREDENTIAL_STORE,
        });
        try {
            const adb = new Adb(transport);
            const model = await adb.getProp("ro.product.model").catch(() => "");
            const product = await adb.getProp("ro.product.product").catch(() => "");
            return { model, product };
        } finally {
            try { transport.close?.(); } catch { /* ignore */ }
        }
    }

    async getDevices() {
        const list = this._read();
        const out = await Promise.all(
            list.map(async (d) => {
                try {
                    const { model, product } = await this._probe(d.host, d.port);
                    return {
                        serial: d.serial,
                        product: product || d.product || "",
                        model: shortModel(product, model) || d.model || "",
                    };
                } catch {
                    return {
                        serial: d.serial,
                        product: d.product || "",
                        model: d.model || "",
                        offline: true,
                    };
                }
            }),
        );
        return out;
    }

    async wirelessConnect(addr) {
        const [host, portRaw] = addr.split(":");
        const port = Number(portRaw);
        if (!host || !Number.isFinite(port) || port < 1 || port > 65535) {
            throw new Error(`invalid adb address: ${addr}`);
        }
        const { model, product } = await this._probe(host, port);
        const serial = `${host}:${port}`;
        const list = this._read();
        const i = list.findIndex((d) => d.serial === serial);
        const entry = {
            serial,
            host,
            port,
            model: shortModel(product, model),
            product,
            addedAt: new Date().toISOString(),
        };
        if (i >= 0) list[i] = { ...list[i], ...entry };
        else list.push(entry);
        this._write(list);
        return entry;
    }

    // drop a saved target so it disappears from /api/devices. returns true
    // if an entry was actually removed (false → unknown serial, no-op).
    wirelessDisconnect(serial) {
        const list = this._read();
        const next = list.filter((d) => d.serial !== serial);
        if (next.length === list.length) return false;
        this._write(next);
        return true;
    }

    async createTransport({ serial }) {
        const list = this._read();
        const d = list.find((x) => x.serial === serial);
        if (!d) {
            throw new Error(
                `device not found: ${serial}; add it with the "ADB 连接" form first`,
            );
        }
        return await connectAdbDaemonTcp({
            host: d.host,
            port: Number(d.port),
            credentialStore: CREDENTIAL_STORE,
        });
    }
}
