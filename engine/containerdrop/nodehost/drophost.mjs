// hz-drophost (Node) — the in-container runtime for scripted drops.
//
// Runs ONE bundled drop: reads HZ_BROKER_SOCKET, fetches the job context from
// the broker, builds the same `ctx` capability surface the goja executor
// exposes (engine/jsdrop/executor.go), runs the drop's `export default`, and
// reports the result back through the broker. Capabilities reach the host only
// over the broker socket — the drop has no other path out.
//
// Usage: node drophost.mjs --source <bundled-drop.js> [--emit-manifest]
//
// This is the Node counterpart of the goja executor; the capability SEMANTICS
// must match it (parity test: engine/containerdrop/nodehost_parity_test.go).

import http from "node:http";
import crypto from "node:crypto";
import { readFile } from "node:fs/promises";

const SOCK = process.env.HZ_BROKER_SOCKET;

// ── broker client (HTTP/JSON over the unix socket) ────────────────────────
function brokerCall(method, path, body) {
  return new Promise((resolve, reject) => {
    const data = body == null ? null : Buffer.from(JSON.stringify(body));
    const req = http.request(
      {
        socketPath: SOCK,
        method,
        path,
        headers: data ? { "content-type": "application/json", "content-length": data.length } : {},
      },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const buf = Buffer.concat(chunks);
          if (res.statusCode !== 200) {
            let msg = res.statusMessage || `status ${res.statusCode}`;
            try {
              const e = JSON.parse(buf.toString());
              if (e && e.error) msg = e.error;
            } catch {}
            reject(new Error(msg));
            return;
          }
          resolve(buf.length ? JSON.parse(buf.toString()) : {});
        });
      },
    );
    req.on("error", reject);
    if (data) req.write(data);
    req.end();
  });
}

const broker = {
  job: () => brokerCall("GET", "/job", null),
  fetch: (req) => brokerCall("POST", "/fetch", req),
  token: (provider, account) => brokerCall("POST", "/token", { provider, account }),
  readFile: (path) => brokerCall("POST", "/files/read", { path }),
  writeFile: (path, dataB64) => brokerCall("POST", "/files/write", { path, data_b64: dataB64 }),
  exists: (path) => brokerCall("POST", "/files/exists", { path }),
  log: (level, message) => brokerCall("POST", "/log", { level, message }),
  result: (output) => brokerCall("POST", "/result", { output }),
  fail: (code, message) => brokerCall("POST", "/result", { error: { code, message } }),
};

// ── helpers ───────────────────────────────────────────────────────────────
// toBuf mirrors the executor's toBytes: string→utf8, bytes-like→bytes, number[]→bytes.
function toBuf(x) {
  if (Buffer.isBuffer(x)) return x;
  if (x instanceof Uint8Array) return Buffer.from(x);
  if (x instanceof ArrayBuffer) return Buffer.from(new Uint8Array(x));
  if (typeof x === "string") return Buffer.from(x, "utf8");
  if (Array.isArray(x)) return Buffer.from(x);
  return Buffer.from(String(x), "utf8");
}

function base64Of(buf, opts) {
  let url = false,
    pad = true;
  if (opts && typeof opts === "object") {
    if (opts.url !== undefined) url = !!opts.url;
    if (opts.pad !== undefined) pad = !!opts.pad;
  }
  if (url) {
    const u = buf.toString("base64url"); // unpadded url-safe
    if (!pad) return u;
    return u + "=".repeat((4 - (u.length % 4)) % 4);
  }
  const s = buf.toString("base64"); // padded standard
  return pad ? s : s.replace(/=+$/, "");
}

function decodeBase64Flexible(s, url) {
  s = String(s).trim();
  // Buffer.from is lenient about padding for both alphabets.
  return Buffer.from(s, url ? "base64url" : "base64");
}

function nodeHash(algo) {
  switch (algo) {
    case "sha1":
      return "sha1";
    case "sha512":
      return "sha512";
    default:
      return "sha256";
  }
}

// ── the ctx capability surface (matches engine/jsdrop/executor.go) ─────────
function buildCtx(job) {
  const secrets = job.secrets || {};
  const ctx = {
    params: job.params || {},
    env: job.env || {},

    secrets: {
      get(name) {
        if (!(name in secrets)) throw new DropError("secret_denied", `${JSON.stringify(name)} not granted`);
        return secrets[name];
      },
      has: (name) => name in secrets,
    },

    log: {
      info: (m) => void broker.log("info", String(m)),
      warn: (m) => void broker.log("warn", String(m)),
      error: (m) => void broker.log("error", String(m)),
    },
    progress: (e) => {
      if (e && e.message !== undefined) void broker.log("progress", String(e.message));
    },

    crypto: {
      hmac: (algo, key, data) => crypto.createHmac(nodeHash(algo), toBuf(key)).update(toBuf(data)).digest(),
      hash: (algo, data) => crypto.createHash(nodeHash(algo)).update(toBuf(data)).digest(),
      hex: (b) => toBuf(b).toString("hex"),
      base64: (b, opts) => base64Of(toBuf(b), opts),
      base64Decode: (s, opts) => decodeBase64Flexible(s, !!(opts && opts.url)),
      randomBytes: (n) => crypto.randomBytes(n),
      utf8: (s) => Buffer.from(String(s), "utf8"),
      utf8Decode: (b) => toBuf(b).toString("utf8"),
    },

    async fetch(url, opts) {
      opts = opts || {};
      const headers = { ...(opts.headers || {}) };
      let body = "";
      if (opts.body !== undefined && opts.body !== null) {
        if (typeof opts.body === "string") {
          body = opts.body;
        } else {
          body = JSON.stringify(opts.body);
          if (!("Content-Type" in headers)) headers["Content-Type"] = "application/json";
        }
      }
      const r = await broker.fetch({
        url,
        method: (opts.method || "GET").toUpperCase(),
        headers,
        query: opts.query || {},
        body,
        timeoutMs: opts.timeoutMs || 0,
      });
      const bytes = Buffer.from(r.body_b64 || "", "base64");
      // expectStatus is opt-in (browser-fetch semantics otherwise): mirror the
      // executor and throw when the status isn't in the supplied set.
      if (Array.isArray(opts.expectStatus) && opts.expectStatus.length && !opts.expectStatus.includes(r.status)) {
        throw new Error(`http_status: ${r.status} not in ${JSON.stringify(opts.expectStatus)}`);
      }
      return {
        status: r.status,
        ok: r.status >= 200 && r.status < 300,
        headers: r.headers || {},
        json: () => JSON.parse(bytes.toString("utf8")),
        text: () => bytes.toString("utf8"),
        bytes: () => bytes,
      };
    },

    inputs: buildInputs(job.inputs || {}),
  };

  // auth.token only when the broker has a resolver — but the broker errors if
  // unavailable, so expose it always and let the call fail like the host does.
  ctx.auth = {
    token: (provider, account) => broker.token(provider, account || "").then((r) => r.token),
  };

  // files.* — sandboxed, async (matches the executor).
  ctx.files = {
    read: async (p) => Buffer.from((await broker.readFile(p)).data_b64 || "", "base64"),
    readText: async (p) => Buffer.from((await broker.readFile(p)).data_b64 || "", "base64").toString("utf8"),
    write: (p, data) => broker.writeFile(p, toBuf(data).toString("base64")).then(() => undefined),
    exists: async (p) => !!(await broker.exists(p)).exists,
  };

  return ctx;
}

function buildInputs(inputs) {
  const refObj = (r) => ({ mime: r.mime, value: r.value === undefined ? undefined : r.value, ...(r.path ? { path: r.path } : {}) });
  const variadic = (port) => {
    const prefix = port + "[";
    return Object.keys(inputs)
      .filter((k) => k.startsWith(prefix) && k.endsWith("]"))
      .map((k) => ({ idx: parseInt(k.slice(prefix.length, -1), 10), k }))
      .filter((x) => !Number.isNaN(x.idx))
      .sort((a, b) => a.idx - b.idx)
      .map((x) => inputs[x.k]);
  };
  return {
    get: (port) => (inputs[port] && inputs[port].value !== undefined ? inputs[port].value : undefined),
    ref: (port) => (inputs[port] ? refObj(inputs[port]) : undefined),
    all: (port) => variadic(port).map(refObj),
    has: (port) => port in inputs || variadic(port).length > 0,
  };
}

// ── DropError global (matches the executor's dropErrorShim) ────────────────
globalThis.DropError = class DropError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "DropError";
    this.code = code;
  }
};

// ── main ────────────────────────────────────────────────────────────────
function arg(name) {
  const i = process.argv.indexOf(name);
  return i >= 0 ? process.argv[i + 1] : undefined;
}

async function loadDrop(sourcePath) {
  const src = await readFile(sourcePath, "utf8");
  // data: URL import is always ESM, sidestepping file-extension/package.json
  // ambiguity for `export default`.
  const mod = await import("data:text/javascript;base64," + Buffer.from(src, "utf8").toString("base64"));
  const drop = mod.default;
  if (!drop || typeof drop.run !== "function") {
    throw new Error("drop has no default export with a run() function");
  }
  return drop;
}

async function main() {
  const sourcePath = arg("--source");
  if (!sourcePath) {
    console.error("hz-drophost: --source is required");
    process.exit(2);
  }

  if (process.argv.includes("--emit-manifest")) {
    const drop = await loadDrop(sourcePath);
    process.stdout.write(JSON.stringify(drop.manifest || {}));
    return;
  }

  if (!SOCK) {
    console.error("hz-drophost: HZ_BROKER_SOCKET is not set");
    process.exit(2);
  }

  let drop;
  try {
    drop = await loadDrop(sourcePath);
  } catch (e) {
    await broker.fail("compile_error", String(e && e.message ? e.message : e));
    return;
  }

  let job;
  try {
    job = await broker.job();
  } catch (e) {
    await broker.fail("job", String(e && e.message ? e.message : e));
    return;
  }

  try {
    const out = await drop.run(buildCtx(job));
    await broker.result(out || {});
  } catch (e) {
    // Mirror the Transport: a DropError carries the drop's machine-readable
    // code; anything else is an untyped script failure.
    const code = e && e.name === "DropError" && e.code ? e.code : "script_error";
    const message = e && e.message ? e.message : String(e);
    await broker.fail(code, message);
  }
}

main().catch((e) => {
  console.error("hz-drophost fatal:", e);
  process.exit(1);
});
