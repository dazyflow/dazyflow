/**
 * Hazy Flow — JS/TS drop authoring contract.
 *
 * This is the surface a scripted drop binds to. It is intentionally
 * self-contained: it does NOT reference the DOM lib (no `fetch`/`Response`
 * globals, no `AbortSignal`), so a drop's tsconfig only needs `lib: ["es2021"]`.
 * Everything a drop can touch is handed to it in `DropContext` — there are no
 * ambient globals (`process`, `require`, `fs`, network) inside the sandbox.
 *
 * Runtime reality: authors write TypeScript; esbuild strips the types and
 * emits an ES module that runs out-of-process on the Node drop host
 * (drophost.mjs), reaching the host only through the capability broker. The
 * *types here* are erased at runtime — they exist for authoring DX and for
 * review. This contract is stable across runtimes: a published drop keeps
 * working if the runtime underneath is swapped.
 */

// ───────────────────────────────────────────────────────────── values ──────

/** Any JSON-serializable value. Everything crossing the host boundary is JSON. */
export type Json =
  | null
  | boolean
  | number
  | string
  | Json[]
  | { [key: string]: Json };

/**
 * A value plus its MIME type — mirrors the engine's `core.Ref`. Drops read
 * inputs as Refs and may return Refs to set an explicit output MIME; returning
 * a bare value lets the engine infer the MIME (JSON for objects, text for
 * strings, `application/octet-stream` for bytes).
 */
export interface Ref<T = Json> {
  readonly mime: string;
  /** Decoded inline value, or undefined for a file-backed ref (see `path`). */
  readonly value: T;
  /**
   * Sandbox path when the ref's bytes live in the filesystem (e.g. an upstream
   * file_write output) rather than inline. Read it with `ctx.files.read(path)`.
   * Absent for inline refs.
   */
  readonly path?: string;
}

// ──────────────────────────────────────────────────────────── manifest ──────

export type DropCategory =
  | "trigger"
  | "flow_control"
  | "transformation"
  | "io"
  | "network"
  | "ai"
  | "external"
  | "system";

/** A minimal JSON Schema object. Drives the params form the canvas renders. */
export type JsonSchema = { [key: string]: Json };

export interface Port {
  /** Port name, referenced by graph edges (e.g. "out", "response_body"). */
  port: string;
  label?: string;
  /** Accepted MIME types; empty = any. */
  mime?: string[];
  required?: boolean;
  /** Variadic input ports accept multiple upstream edges. */
  variadic?: boolean;
  min?: number;
  max?: number;
}

export interface ParamsExample {
  title: string;
  /** A worked params object the example would run with. */
  params: Json;
  notes?: string;
}

export interface ConnectionRequirement {
  kind: "oauth" | "secret";
  /** OAuth provider id, or the recommended secret slug. */
  name: string;
  note?: string;
}

/**
 * Author-facing manifest. A subset of the engine's `core.Manifest` — the
 * fields a drop author sets. `summary` and at least one `examples` entry are
 * REQUIRED (the engine rejects registration otherwise, same as native drops).
 */
export interface DropManifest {
  /** Stable unique id, e.g. "stripe_list_charges". */
  id: string;
  version: string;
  label: string;
  /** ~140-char, LLM-friendly one-liner. REQUIRED. */
  summary: string;
  /** At least one worked example. REQUIRED. */
  examples: ParamsExample[];

  description?: string;
  /** Vendor/service grouping in the palette ("Stripe", "Slack"). */
  integration?: string;
  category?: DropCategory;
  /** lucide-react icon name (kebab-case). */
  icon?: string;
  /** Hex accent for the canvas node. */
  color?: string;
  tags?: string[];

  inputs?: Port[];
  outputs?: Port[];
  /** JSON Schema for params — rendered as the node's form. */
  paramsSchema?: JsonSchema;

  requiresConnections?: ConnectionRequirement[];
  idempotent?: boolean;
  retryPolicy?: "never" | "exponential_backoff";
  /** v1 supports "batch"; "stream"/"trigger" are reserved. */
  executionModel?: "batch" | "stream" | "trigger";
}

// ──────────────────────────────────────────────── injected capabilities ──────

/** Guarded HTTP — the ONLY way a drop reaches the network. */
export interface FetchOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD" | "OPTIONS";
  headers?: Record<string, string>;
  /**
   * Request body. An object/array is JSON-encoded (and a JSON content-type
   * set unless you override it); a string is sent verbatim; bytes are sent raw.
   */
  body?: Json | string | Uint8Array;
  /** Appended as the query string; values are URL-encoded. */
  query?: Record<string, string | number | boolean>;
  /** Per-request timeout (ms); capped by the engine's ceiling. */
  timeoutMs?: number;
  /**
   * Opt-in status assertion. When set, a response status outside the set throws
   * DropError("http_status", …). When unset (default), every status resolves —
   * browser-fetch semantics — and you check `res.ok` yourself (which lets you
   * read an error body before deciding it failed).
   */
  expectStatus?: number[];
}

export interface FetchResponse {
  readonly status: number;
  readonly ok: boolean;
  readonly headers: Readonly<Record<string, string>>;
  json<T = Json>(): Promise<T>;
  text(): Promise<string>;
  bytes(): Promise<Uint8Array>;
}

/**
 * Issue an HTTP request. Routed through the engine's SSRF guard, the operator
 * egress allowlist, and the tenant byte-quota — a drop cannot bypass these,
 * and cannot reach loopback/private/link-local addresses. Throws `DropError`
 * with code "egress_blocked" / "ssrf_blocked" / "http_status" on failure.
 */
export type Fetch = (url: string, opts?: FetchOptions) => Promise<FetchResponse>;

/** Secret access, scoped to the slugs this drop declared in requiresConnections. */
export interface Secrets {
  /** Returns the secret value; throws "secret_denied" if not granted. */
  get(name: string): string;
  has(name: string): boolean;
}

/**
 * OAuth tokens. The host owns refresh — the returned token is always current,
 * so a drop never sees client secrets or refresh logic. `provider` matches a
 * requiresConnections entry of kind "oauth".
 */
export interface Auth {
  /**
   * Current access token for `provider` (a requiresConnections oauth entry).
   * `account` selects among multiple connected accounts; omitted → the drop's
   * `params.account`, then "default".
   */
  token(provider: string, account?: string): Promise<string>;
}

export type HashAlgo = "sha256" | "sha1" | "sha512" | "md5";
export type HmacAlgo = "sha256" | "sha1" | "sha512";

/** Signing/encoding primitives, so authors never hand-roll crypto. */
export interface Crypto {
  hmac(algo: HmacAlgo, key: string | Uint8Array, data: string | Uint8Array): Uint8Array;
  hash(algo: HashAlgo, data: string | Uint8Array): Uint8Array;
  hex(b: Uint8Array): string;
  base64(b: Uint8Array, opts?: { url?: boolean; pad?: boolean }): string;
  /** Decode a base64 (or base64url) string to bytes; padding optional. */
  base64Decode(s: string, opts?: { url?: boolean }): Uint8Array;
  /** Cryptographically secure random bytes. */
  randomBytes(n: number): Uint8Array;
  utf8(s: string): Uint8Array;
  /** Decode UTF-8 bytes back to a string (inverse of utf8). */
  utf8Decode(b: Uint8Array): string;
  /**
   * Built-in request signers for schemes too dangerous to reimplement.
   * Returns headers to merge into a fetch. (Optional — present as the menu grows.)
   */
  awsSigV4?(args: {
    region: string;
    service: string;
    accessKeyId: string;
    secretAccessKey: string;
    method: string;
    url: string;
    headers?: Record<string, string>;
    body?: string | Uint8Array;
  }): Record<string, string>;
}

/** Sandboxed filesystem — confined to the job's workspace/scratch roots. */
export interface Files {
  /**
   * A bare path is relative to the persistent workspace; a "scratch://…"
   * path lives in the run's ephemeral scratch area. Traversal is refused.
   */
  read(path: string): Promise<Uint8Array>;
  readText(path: string): Promise<string>;
  write(path: string, data: string | Uint8Array): Promise<void>;
  exists(path: string): Promise<boolean>;
}

export interface Logger {
  info(msg: string, data?: Json): void;
  warn(msg: string, data?: Json): void;
  error(msg: string, data?: Json): void;
}

/** Inputs arriving on the node's ports from upstream drops. */
export interface Inputs {
  /** Decoded value on `port`, or undefined if unwired. */
  get<T = Json>(port: string): T | undefined;
  /** The raw Ref (value + mime) on `port`. */
  ref(port: string): Ref | undefined;
  /** All refs on a variadic port, in edge order. */
  all(port: string): Ref[];
  has(port: string): boolean;
}

/** A minimal cancellation signal (no DOM dependency). Fires on timeout/abort. */
export interface CancelSignal {
  readonly aborted: boolean;
  readonly reason?: string;
  onAbort(cb: () => void): void;
}

export interface PollOptions {
  intervalMs?: number;
  timeoutMs?: number;
  maxAttempts?: number;
}

/** Everything a drop is allowed to do. No ambient globals beyond this. */
export interface DropContext<P extends Record<string, Json> = Record<string, Json>> {
  /** Validated params for this node (typed via defineDrop<P>). */
  readonly params: P;
  readonly inputs: Inputs;
  readonly secrets: Secrets;
  readonly auth: Auth;
  readonly env: Readonly<Record<string, string>>;
  readonly fetch: Fetch;
  readonly crypto: Crypto;
  readonly files: Files;
  readonly log: Logger;
  /** Aborts on engine timeout or run cancellation; honor it in long loops. */
  readonly signal: CancelSignal;

  /** Report progress to the UI (0..1 percent and/or a message). */
  progress(p: { percent?: number; message?: string; data?: Json }): void;
  /** Cooperative sleep — respects `signal`. */
  sleep(ms: number): Promise<void>;
  /**
   * Poll `fn` until it returns a non-undefined value (the "wait for the async
   * job to finish" pattern), then resolve to it. Throws "poll_timeout" if it
   * never does within the bounds.
   */
  poll<T>(fn: () => Promise<T | undefined> | (T | undefined), opts?: PollOptions): Promise<T>;
}

// ────────────────────────────────────────────────────────────── output ──────

export type PortValue = Json | Ref | Uint8Array;

/** Map of output-port name → value. Keys must match manifest.outputs. */
export type DropOutput = Record<string, PortValue>;

export type DropRun<P extends Record<string, Json> = Record<string, Json>> = (
  ctx: DropContext<P>,
) => Promise<DropOutput> | DropOutput;

export interface DropDefinition<P extends Record<string, Json> = Record<string, Json>> {
  manifest: DropManifest;
  run: DropRun<P>;
}

/**
 * Throw this for a clean, typed failure. `code` is machine-readable (surfaces
 * in run logs and retry decisions); any other thrown value becomes an
 * "internal" error with its message.
 */
export declare class DropError extends Error {
  constructor(code: string, message: string);
  readonly code: string;
}

/**
 * Identity helper that pins the params type so `ctx.params` is typed and the
 * manifest is checked at author time. A drop module's default export is its
 * DropDefinition:
 *
 *   export default defineDrop<{ limit?: number }>({
 *     manifest: { id: "stripe_list_charges", ... },
 *     async run({ params, secrets, fetch }) {
 *       const out = [];
 *       let url = `https://api.stripe.com/v1/charges?limit=${params.limit ?? 100}`;
 *       while (url) {
 *         const res = await fetch(url, {
 *           headers: { Authorization: `Bearer ${secrets.get("STRIPE_KEY")}` },
 *         });
 *         const page = await res.json<{ data: any[]; has_more: boolean }>();
 *         out.push(...page.data);
 *         url = page.has_more
 *           ? `https://api.stripe.com/v1/charges?limit=100&starting_after=${out.at(-1).id}`
 *           : "";
 *       }
 *       return { out };
 *     },
 *   });
 */
export declare function defineDrop<P extends Record<string, Json> = Record<string, Json>>(
  def: DropDefinition<P>,
): DropDefinition<P>;
