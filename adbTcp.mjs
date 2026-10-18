// adbTcp.mjs — direct "adb connect <host>:<port>" support for @yume-chan/adb 2.x.
//
// @yume-chan/adb 2.6 dropped the `AdbDaemonTransport.connect({ host, port })`
// helper: it now only ships `authenticate({ serial, connection, credentialStore })`,
// where `connection` is an AdbDaemonConnection the caller must provide.
// This module provides that missing piece for plain TCP daemons (adbd tcp
// mode / redroid): the raw ADB packet framing over a TCP socket, plus a
// Node-side credential store that persists an RSA-2048 key in PKCS#8 DER.
//
// Frame layout (little-endian, 24-byte header):
//   command u32 | arg0 u32 | arg1 u32 | payloadLength u32 | checksum u32 | magic u32
// checksum = byte sum of payload; magic = command ^ 0xFFFFFFFF.
// The transport computes checksum/magic itself before writing, but we
// recompute defensively when serializing.

import { connect as netConnect } from "node:net";
import { generateKeyPairSync } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { AdbDaemonTransport } from "@yume-chan/adb";
import { Consumable } from "@yume-chan/stream-extra";

const HEADER_SIZE = 24;

function checksum(payload) {
    let sum = 0;
    for (let i = 0; i < payload.length; i++) sum += payload[i];
    return sum >>> 0;
}

function concat(a, b) {
    const out = new Uint8Array(a.length + b.length);
    out.set(a);
    out.set(b, a.length);
    return out;
}

/**
 * Open a TCP socket to an adbd and wrap it into an AdbDaemonConnection
 * ({ readable: ReadableStream<AdbPacketData>, writable: WritableStream<Consumable<AdbPacketInit>> }).
 * Resolves once the TCP connection is established (protocol handshake is
 * done later by AdbDaemonTransport.authenticate).
 */
export function createTcpAdbDaemonConnection({ host, port, connectTimeoutMs = 10_000 } = {}) {
    return new Promise((resolve, reject) => {
        const socket = netConnect({ host, port: Number(port) });
        socket.setNoDelay(true);

        let settled = false;
        const fail = (e) => {
            if (settled) return;
            settled = true;
            socket.destroy();
            reject(e instanceof Error ? e : new Error(String(e)));
        };
        const connectTimer = setTimeout(
            () => fail(new Error(`tcp connect timeout: ${host}:${port} (${connectTimeoutMs}ms)`)),
            connectTimeoutMs,
        );
        socket.on("error", (e) => {
            clearTimeout(connectTimer);
            fail(new Error(`adb tcp connect ${host}:${port} failed: ${e.message}`));
        });

        // ---- readable: split raw bytes into ADB packets ----
        let buffer = new Uint8Array(0);
        let header = null;
        const readStream = new ReadableStream({
            start(controller) {
                socket.on("data", (chunk) => {
                    const incoming = new Uint8Array(
                        chunk.buffer, chunk.byteOffset, chunk.byteLength,
                    );
                    buffer = buffer.length === 0 ? incoming : concat(buffer, incoming);
                    for (;;) {
                        if (header === null) {
                            if (buffer.length < HEADER_SIZE) break;
                            const dv = new DataView(
                                buffer.buffer, buffer.byteOffset, buffer.byteLength,
                            );
                            header = {
                                command: dv.getUint32(0, true),
                                arg0: dv.getUint32(4, true),
                                arg1: dv.getUint32(8, true),
                                payloadLength: dv.getUint32(12, true),
                            };
                            buffer = buffer.subarray(HEADER_SIZE);
                        }
                        if (buffer.length < header.payloadLength) break;
                        const payload = buffer.subarray(0, header.payloadLength).slice();
                        buffer = buffer.subarray(header.payloadLength);
                        try {
                            controller.enqueue({
                                command: header.command,
                                arg0: header.arg0,
                                arg1: header.arg1,
                                payloadLength: header.payloadLength,
                                payload,
                            });
                        } catch {
                            return; // stream closed/cancelled
                        }
                        header = null;
                    }
                });
                const closeReader = () => {
                    try { controller.close(); } catch { /* already closed */ }
                };
                socket.on("close", closeReader);
                socket.on("end", closeReader);
                socket.on("error", (e) => {
                    try { controller.error(e); } catch { /* already errored */ }
                });
            },
            cancel() {
                socket.destroy();
            },
        });

        // ---- writable: serialize packets onto the socket ----
        const writeStream = new WritableStream({
            write(consumable) {
                return consumable.tryConsume((packet) => {
                    const payload = packet.payload || new Uint8Array(0);
                    const headerBuf = new Uint8Array(HEADER_SIZE);
                    const dv = new DataView(headerBuf.buffer);
                    dv.setUint32(0, packet.command >>> 0, true);
                    dv.setUint32(4, packet.arg0 >>> 0, true);
                    dv.setUint32(8, packet.arg1 >>> 0, true);
                    dv.setUint32(12, payload.length, true);
                    dv.setUint32(16, checksum(payload), true);
                    dv.setUint32(20, (packet.command ^ 0xffffffff) >>> 0, true);
                    if (!socket.write(Buffer.concat([headerBuf, payload]))) {
                        // honor backpressure: wait for drain before acking the write
                        return new Promise((res) => socket.once("drain", res));
                    }
                });
            },
            close() {
                socket.end();
            },
            abort(reason) {
                socket.destroy(reason instanceof Error ? reason : new Error(String(reason)));
            },
        });

        socket.on("connect", () => {
            clearTimeout(connectTimer);
            if (settled) return;
            settled = true;
            const connection = {
                readable: readStream,
                writable: writeStream,
                /** Hard-kill the underlying socket. */
                close() {
                    try { socket.destroy(); } catch { /* ignore */ }
                },
            };
            resolve(connection);
        });
    });
}

/**
 * AdbCredentialStore backed by a single RSA-2048 key persisted as
 * PKCS#8 DER at `path` (falls back to in-memory when path is null).
 */
export class NodeAdbCredentialStore {
    constructor(path) {
        this.path = path || null;
        this._key = null;
    }

    _load() {
        if (this._key) return this._key;
        if (this.path && existsSync(this.path)) {
            try {
                const buffer = readFileSync(this.path);
                if (buffer.length > 0) {
                    this._key = { buffer: new Uint8Array(buffer), name: "scrcpy-webrtc" };
                }
            } catch {
                // fall through to generate
            }
        }
        return this._key;
    }

    async *iterateKeys() {
        const key = this._load();
        if (key) yield key;
    }

    async generateKey() {
        if (this._key) return this._key;
        const { privateKey } = generateKeyPairSync("rsa", {
            modulusLength: 2048,
            publicExponent: 65537,
        });
        const der = privateKey.export({ type: "pkcs8", format: "der" });
        this._key = { buffer: new Uint8Array(der), name: "scrcpy-webrtc" };
        if (this.path) {
            try {
                mkdirSync(dirname(this.path), { recursive: true });
                writeFileSync(this.path, Buffer.from(der));
            } catch {
                // in-memory key still works for this session
            }
        }
        return this._key;
    }
}

/**
 * Authenticate against an adbd at host:port and return an AdbDaemonTransport.
 * The returned transport tears the TCP socket down once it is closed.
 *
 * @param {{ host: string, port: number|string, credentialStore: object }} opts
 */
export async function connectAdbDaemonTcp({ host, port, credentialStore }) {
    const connection = await createTcpAdbDaemonConnection({ host, port });
    try {
        const transport = await AdbDaemonTransport.authenticate({
            serial: `${host}:${port}`,
            connection,
            credentialStore,
        });
        transport.disconnected
            .then(() => connection.close())
            .catch(() => connection.close());
        return transport;
    } catch (e) {
        connection.close();
        throw e;
    }
}
