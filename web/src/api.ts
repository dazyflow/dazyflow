import type {
  APIKeySummary,
  AuditEvent,
  FlowSummary,
  Graph,
  IssuedAPIKey,
  InvitationDetails,
  AdminOAuthProvider,
  InvitationSummary,
  LintIssue,
  Manifest,
  Revision,
  MemberSummary,
  OAuthProviderStatus,
  GoogleAccountsResponse,
  OrgAuthConfig,
  OrgProfile,
  Role,
  TemplateSummary,
  FileEntry,
  JobRecord,
  JobStatus,
  PendingApproval,
  RunLogEntry,
  RunSummary,
  RunView,
  NodeRunView,
  ScheduleEntry,
  PublishInfo,
  GitCredential,
  ReferenceGroups,
  ResourceDef,
  SecretManagerStatus,
  SecretManagerConfig,
  AwsSecretManagerStatus,
  AwsSecretManagerConfig,
  GcpSecretManagerStatus,
  GcpSecretManagerConfig,
  BillingInfo,
  ServiceInfo,
  UsageCounters,
  UserSummary,
  VersionStatus,
  WhoAmI,
  WorkspaceLimits,
} from "./types";

// API_BASE: dev defaults to relative "/api/v1" (proxied by Vite to the
// daemon); prod builds can hardcode an absolute URL via VITE_API_BASE.
const API_BASE = (import.meta.env.VITE_API_BASE ?? "") + "/api/v1";

export class APIError extends Error {
  status: number;
  // code is the snake_case enum from the new ErrorEnvelope (e.g.
  // "drop_not_found", "permission_denied"). Empty string on legacy
  // routes that still emit the {"error":"<string>"} shape.
  code: string;
  constructor(status: number, message: string, code: string = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

// onUnauthorized is a process-wide hook fired whenever an *authenticated*
// request (one that carried a credential) comes back 401 — i.e. the
// daemon no longer accepts the token, so the session has expired or been
// revoked mid-use. The AuthProvider registers a handler that tears down
// local session state, surfaces the "session expired" message, and
// bounces to /signin; without it, a stale request anywhere in the app
// would leak the raw "auth: invalid credential" string into that
// component's own error UI. Null until the provider mounts.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(handler: (() => void) | null): void {
  onUnauthorized = handler;
}

// notifyUnauthorized fires the handler for an authenticated 401. Anonymous
// endpoints (signin/signup, static template assets) must NOT call this: a
// 401 there is a wrong-password / not-found case, not an expired session.
function notifyUnauthorized(): void {
  if (onUnauthorized) onUnauthorized();
}

// parseAPIErrorBody handles both the legacy `{"error":"<string>"}`
// shape and the new `{"error":{"code","message","details","doc"}}`
// envelope. New spec-aligned routes (catalog, /me) emit the envelope;
// older routes still return the string. Returns the fallback when the
// body is unparseable or empty.
function parseAPIErrorBody(text: string, fallback: string): { message: string; code: string } {
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed.error === "string") {
      return { message: parsed.error, code: "" };
    }
    if (parsed && parsed.error && typeof parsed.error === "object") {
      const msg = typeof parsed.error.message === "string" ? parsed.error.message : fallback;
      const code = typeof parsed.error.code === "string" ? parsed.error.code : "";
      return { message: msg, code };
    }
  } catch {
    /* fall through with raw text */
  }
  return { message: text || fallback, code: "" };
}

async function request<T>(
  token: string | null,
  method: string,
  path: string,
  body?: unknown,
  // signalUnauthorized defaults true: a 401 on a token-bearing request
  // triggers the global session-expired handler. signOut passes false —
  // a 401 there just means the session was already gone, which is the
  // intended end state, not an event to surface as "session expired".
  opts?: { signalUnauthorized?: boolean },
): Promise<T> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body) headers["Content-Type"] = "application/json";
  const res = await fetch(API_BASE + path, {
    method,
    headers,
    credentials: "include",
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const { message, code } = parseAPIErrorBody(await res.text(), res.statusText);
    if (res.status === 401 && token && opts?.signalUnauthorized !== false) {
      notifyUnauthorized();
    }
    throw new APIError(res.status, message, code);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export type SignInResponse = {
  // On a normal sign-in the server returns a session token + identity.
  // When the account has 2FA enabled it instead returns
  // { totp_required: true, challenge } and the other fields are absent —
  // the caller must finish via api.totpVerify before it has a session.
  token?: string;
  subject?: string;
  tenant?: string;
  workspace?: string;
  expires_at?: string;
  totp_required?: boolean;
  challenge?: string;
};

// TOTPStatus mirrors GET /me/totp. enrolled_at + recovery_codes_left are
// only meaningful when enabled.
export type TOTPStatus = {
  enabled: boolean;
  enrolled_at?: string;
  recovery_codes_left?: number;
};

// TOTPSetup mirrors POST /me/totp/setup — the provisioning data shown
// while enrolling. qr_png_data_url is a ready-to-render <img> source;
// secret_base32 is the manual fallback for apps that can't scan.
export type TOTPSetup = {
  otp_auth_url: string;
  secret_base32: string;
  qr_png_data_url?: string;
};

// runViewToRecord / nodeViewToRecord adapt the public API's clean run/node
// wire shapes (RunView / NodeRunView, snake_case) into the JobRecord shape
// the run-detail components already consume. Keeping the translation here
// means the API stays clean while the component layer is untouched.
function runViewToRecord(r: RunView): JobRecord {
  return {
    ID: r.id,
    Kind: "graph",
    GraphRunID: "",
    GraphID: r.graph_id ?? "",
    NodeID: "*",
    Status: r.status,
    EnqueuedAt: r.enqueued_at ?? null,
    StartedAt: r.started_at ?? null,
    FinishedAt: r.finished_at ?? null,
    Result: r.error ? { status: r.status, error: r.error } : undefined,
  };
}

function nodeViewToRecord(runID: string, n: NodeRunView): JobRecord {
  return {
    ID: `${runID}:${n.node_id}`,
    Kind: "node",
    GraphRunID: runID,
    GraphID: "",
    NodeID: n.node_id,
    Status: n.status,
    StartedAt: n.started_at ?? null,
    FinishedAt: n.finished_at ?? null,
    Attempt: n.attempts,
    Result:
      n.outputs || n.error
        ? { status: n.status, output: n.outputs, error: n.error }
        : undefined,
    Job: n.inputs ? { Input: n.inputs } : undefined,
  };
}

// SecretScope mirrors the daemon's secret scoping: tenant (the organization,
// shared by every flow) or flow (only the named flow resolves it). Set when a
// secret is saved; ${secret.NAME} resolves flow → organization.
export type SecretScope = "tenant" | "flow";

// secretQuery builds the ?scope=&flow= query for the secret endpoints. Tenant
// scope and no flow yield an empty string, so existing callers are unchanged.
function secretQuery(scope?: SecretScope, flow?: string): string {
  const p = new URLSearchParams();
  if (scope && scope !== "tenant") p.set("scope", scope);
  if (flow) p.set("flow", flow);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const api = {
  // uploadWorkspaceFile sends a single file to a workspace sandbox via
  // multipart/form-data — used by the workspace-path widget in the
  // node param editor. `destPath` is optional; the daemon defaults to
  // the file's name (with browser-supplied directories stripped).
  uploadWorkspaceFile: async (
    token: string,
    tenant: string,
    workspace: string,
    file: File,
    destPath?: string,
  ): Promise<{ path: string; size: number }> => {
    const form = new FormData();
    form.append("file", file);
    if (destPath) form.append("path", destPath);
    const res = await fetch(
      API_BASE + `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files`,
      {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
        credentials: "include",
        body: form,
      },
    );
    if (!res.ok) {
      const text = await res.text();
      let message = text;
      try {
        const parsed = JSON.parse(text);
        if (parsed.error) message = parsed.error;
      } catch {
        // raw text
      }
      if (res.status === 401) notifyUnauthorized();
      throw new APIError(res.status, message || res.statusText);
    }
    return res.json();
  },

  // uploadWorkspaceFileProgress is the upload path used by the background
  // uploader: XMLHttpRequest (not fetch) because only XHR exposes
  // upload.onprogress for a real percentage, and it can be aborted via the
  // passed AbortSignal (cancel button).
  uploadWorkspaceFileProgress: (
    token: string,
    tenant: string,
    workspace: string,
    file: File,
    destPath: string,
    opts?: { onProgress?: (fraction: number) => void; signal?: AbortSignal },
  ): Promise<{ path: string; size: number }> =>
    new Promise((resolve, reject) => {
      const form = new FormData();
      form.append("file", file);
      if (destPath) form.append("path", destPath);
      const xhr = new XMLHttpRequest();
      xhr.open(
        "POST",
        API_BASE +
          `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files`,
      );
      xhr.withCredentials = true;
      xhr.setRequestHeader("Authorization", `Bearer ${token}`);
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) opts?.onProgress?.(e.loaded / e.total);
      };
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText));
          } catch {
            resolve({ path: destPath, size: file.size });
          }
        } else {
          if (xhr.status === 401) notifyUnauthorized();
          const { message } = parseAPIErrorBody(xhr.responseText, xhr.statusText);
          reject(new APIError(xhr.status, message));
        }
      };
      xhr.onerror = () => reject(new APIError(0, "network error"));
      xhr.onabort = () => reject(new DOMException("aborted", "AbortError"));
      if (opts?.signal) {
        if (opts.signal.aborted) {
          xhr.abort();
          return;
        }
        opts.signal.addEventListener("abort", () => xhr.abort());
      }
      xhr.send(form);
    }),

  // --- Workspace file manager (browse/manage the persistent sandbox) ---

  listWorkspaceFiles: (
    token: string,
    tenant: string,
    workspace: string,
    path: string,
  ): Promise<{ path: string; entries: FileEntry[] }> => {
    const q = path ? "?path=" + encodeURIComponent(path) : "";
    return request(
      token,
      "GET",
      `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files/list${q}`,
    );
  },

  workspaceFileUsage: (
    token: string,
    tenant: string,
    workspace: string,
  ): Promise<{ used: number; limit: number }> =>
    request(
      token,
      "GET",
      `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files/usage`,
    ),

  deleteWorkspaceFile: (
    token: string,
    tenant: string,
    workspace: string,
    path: string,
  ): Promise<{ path: string; deleted: boolean }> =>
    request(
      token,
      "DELETE",
      `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files?path=${encodeURIComponent(path)}`,
    ),

  mkdirWorkspaceDir: (
    token: string,
    tenant: string,
    workspace: string,
    path: string,
  ): Promise<{ path: string; created: boolean }> =>
    request(
      token,
      "POST",
      `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files/mkdir`,
      { path },
    ),

  renameWorkspaceFile: (
    token: string,
    tenant: string,
    workspace: string,
    from: string,
    to: string,
  ): Promise<{ from: string; to: string }> =>
    request(
      token,
      "POST",
      `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files/rename`,
      { from, to },
    ),

  // downloadWorkspaceFile fetches one file as a Blob. The endpoint requires
  // the bearer token, so a plain anchor href can't be used — the caller
  // turns the Blob into an object URL to trigger the browser save.
  downloadWorkspaceFile: async (
    token: string,
    tenant: string,
    workspace: string,
    path: string,
  ): Promise<Blob> => {
    const res = await fetch(
      API_BASE +
        `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files/download?path=${encodeURIComponent(path)}`,
      {
        method: "GET",
        headers: { Authorization: `Bearer ${token}` },
        credentials: "include",
      },
    );
    if (!res.ok) {
      const { message } = parseAPIErrorBody(await res.text(), res.statusText);
      if (res.status === 401) notifyUnauthorized();
      throw new APIError(res.status, message);
    }
    return res.blob();
  },

  signIn: (email: string, password: string) =>
    request<SignInResponse>(null, "POST", "/auth/signin", { email, password }),
  // signUp returns the same shape as signIn — the server issues a
  // session immediately so the UI can land the user on the welcome
  // page without an extra round trip.
  signUp: (email: string, password: string) =>
    request<SignInResponse>(null, "POST", "/auth/signup", { email, password }),
  // Email verification: verifyEmail consumes the emailed link's
  // email+token pair (no auth — the click can land in any browser);
  // resendVerification re-mints + re-sends for the signed-in user.
  verifyEmail: (email: string, verifyToken: string) =>
    request<{ verified: boolean }>(null, "POST", "/auth/verify-email", {
      email,
      token: verifyToken,
    }),
  resendVerification: (token: string) =>
    request<{ sent: boolean; already_verified?: boolean }>(
      token,
      "POST",
      "/me/verification/resend",
    ),
  // Template gallery: index lives at /templates/index.json under the
  // web app's static assets (NOT /api/v1/...). Each template entry
  // points at its own graph file, fetched lazily when the user
  // clicks "Use this template" so the gallery page loads fast even
  // with dozens of templates.
  listTemplates: async (): Promise<{ templates: TemplateSummary[] }> => {
    const res = await fetch("/templates/index.json", { credentials: "same-origin" });
    if (!res.ok) throw new APIError(res.status, await res.text());
    return res.json();
  },
  loadTemplateGraph: async (graphFile: string): Promise<Graph> => {
    const res = await fetch(graphFile, { credentials: "same-origin" });
    if (!res.ok) throw new APIError(res.status, await res.text());
    return res.json();
  },
  signOut: (token: string | null) =>
    request<void>(token, "POST", "/auth/signout", undefined, {
      signalUnauthorized: false,
    }),
  whoami: (token: string | null) => request<WhoAmI>(token, "GET", "/me"),

  // listReferences returns everything a param on `node` can reference in a
  // flow — secrets, upstream node outputs, trigger/form fields, resources —
  // as ready-to-insert ${…} tokens. Powers the param editor's insert-
  // reference picker. node is optional (omit to list all nodes).
  listReferences: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    node?: string,
  ) =>
    request<{ flow: string; node: string; groups: ReferenceGroups }>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/references${
        node ? `?node=${encodeURIComponent(node)}` : ""
      }`,
    ),

  // listInputFields returns the record fields the node feeding `node`'s
  // `port` (default "rows") emits — e.g. a Google Form's structural keys or
  // a hosted form's declared fields — so the Sheets mapping editor can
  // suggest them. Empty when nothing is wired in or the producer isn't a
  // known row source.
  listInputFields: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    node: string,
    port?: string,
  ) =>
    request<{
      source: { node_id: string; module: string; label?: string } | null;
      fields: string[];
    }>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/input-fields?node=${encodeURIComponent(node)}${
        port ? `&port=${encodeURIComponent(port)}` : ""
      }`,
    ),

  // listAccountResources lists a connected account's selectable items of a
  // kind (e.g. provider "google", kind "spreadsheets" or "forms") so a param
  // can offer a dropdown instead of an opaque ID. account defaults to
  // "default" server-side. A 502 means "not connected" / provider error —
  // the picker falls back to manual entry.
  // extra carries dependent params (e.g. spreadsheet_id for the tabs kind),
  // forwarded to the lister as query params.
  listAccountResources: (
    token: string,
    provider: string,
    kind: string,
    account?: string,
    extra?: Record<string, string>,
  ) => {
    const qs = new URLSearchParams({ kind });
    if (account) qs.set("account", account);
    for (const [k, v] of Object.entries(extra ?? {})) qs.set(k, v);
    return request<{ resources: { id: string; name: string }[] }>(
      token,
      "GET",
      `/oauth/${encodeURIComponent(provider)}/resources?${qs.toString()}`,
    );
  },

  // serviceInfo fetches the public GET /api/v1 descriptor. No token
  // required — path "" resolves to API_BASE ("/api/v1"). The UI uses the
  // build block for the account-menu version footer.
  serviceInfo: () => request<ServiceInfo>(null, "GET", ""),

  // --- TOTP 2FA ---------------------------------------------------------
  // totpVerify is leg 2 of sign-in: it redeems the challenge from leg 1
  // plus a code (or recovery code) and, on success, returns the same
  // payload as a normal sign-in (token + identity). Unauthenticated.
  totpVerify: (challenge: string, code: string, recoveryCode: string) =>
    request<SignInResponse>(null, "POST", "/auth/totp", {
      challenge,
      code,
      recovery_code: recoveryCode,
    }),
  // getTOTPStatus reads the caller's current 2FA state for the Settings
  // card. Returns { enabled: false } for principals with no 2FA.
  getTOTPStatus: (token: string) =>
    request<TOTPStatus>(token, "GET", "/me/totp"),
  // totpSetup starts enrolment — mints a pending secret and returns the
  // QR + manual secret. totpConfirm finalises it and returns the
  // one-time recovery codes.
  totpSetup: (token: string) =>
    request<TOTPSetup>(token, "POST", "/me/totp/setup"),
  totpConfirm: (token: string, code: string) =>
    request<{ recovery_codes: string[] }>(token, "POST", "/me/totp/confirm", {
      code,
    }),
  // totpDisable requires the current password as a re-auth gate.
  totpDisable: (token: string, password: string) =>
    request<void>(token, "POST", "/me/totp/disable", { password }),
  totpRegenerateRecoveryCodes: (token: string) =>
    request<{ recovery_codes: string[] }>(
      token,
      "POST",
      "/me/totp/recovery-codes",
    ),
  listWorkspaces: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ workspaces: string[] }>(token, "GET", "/workspaces" + qs);
  },
  listTenants: (token: string) =>
    request<{ tenants: string[] }>(token, "GET", "/admin/tenants"),
  // adminVersion drives the System section: running build vs. the newest
  // upstream release. Platform-admin only (403 otherwise).
  adminVersion: (token: string) =>
    request<VersionStatus>(token, "GET", "/admin/version"),
  listDrops: async (token: string, query?: string) => {
    // Daemon emits both "drops" (canonical) and "modules" (legacy alias)
    // during the rename transition; accept either so older daemons keep
    // working until we ship the final cutover.
    const r = await request<{ drops?: Manifest[]; modules?: Manifest[] }>(
      token,
      "GET",
      "/drops" + (query ? `?q=${encodeURIComponent(query)}` : ""),
    );
    return { drops: r.drops ?? r.modules ?? [] };
  },
  listGraphs: async (token: string, tenant: string, workspace: string) => {
    const r = await request<{ flows?: FlowSummary[] }>(
      token,
      "GET",
      `/me/flows?tenant=${encodeURIComponent(tenant)}&workspace=${encodeURIComponent(workspace)}`,
    );
    return { graphs: r.flows ?? [] };
  },
  // ref loads a past revision (a commit hash from flowHistory); omit it
  // for the current HEAD. Used by the editor's history-preview.
  loadGraph: (token: string, tenant: string, workspace: string, id: string, ref?: string) =>
    request<Graph>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}${
        ref ? `?ref=${encodeURIComponent(ref)}` : ""
      }`,
    ),
  // flowHistory returns the flow's commit log, newest first.
  // published_commit (when present) flags which revision is currently live.
  flowHistory: (token: string, tenant: string, workspace: string, id: string) =>
    request<{ revisions: Revision[]; published_commit?: string }>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/history`,
    ),
  // getPublishedInfo reports the flow's draft-vs-live state for the
  // editor's publish control: whether anything is published, and whether
  // the draft (HEAD) differs from the published revision.
  getPublishedInfo: (token: string, tenant: string, workspace: string, id: string) =>
    request<PublishInfo>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/published`,
    ),
  // publishFlow promotes a revision to "live" — automatic triggers run it
  // while manual/test runs keep using the draft. Omit ref to publish the
  // current draft (HEAD); pass an older commit to roll back to it.
  publishFlow: (token: string, tenant: string, workspace: string, id: string, ref?: string) =>
    request<{ flow_id: string; published_commit: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/publish`,
      ref ? { ref } : {},
    ),
  // restoreFlow makes a past revision the new HEAD (a fresh commit on top).
  restoreFlow: (token: string, tenant: string, workspace: string, id: string, ref: string) =>
    request<{ commit: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/restore`,
      { ref },
    ),
  // autosave=true marks an editor autosave: the daemon coalesces
  // consecutive autosaves of the same flow into one commit so the
  // workspace history stays readable. Explicit saves omit it.
  saveGraph: (token: string, g: Graph, autosave = false) =>
    request<{
      commit: string;
      lint?: LintIssue[];
    }>(
      token,
      "PUT",
      `/me/flows/${encodeURIComponent(`${g.tenant}/${g.workspace}/${g.id}`)}${
        autosave ? "?autosave=1" : ""
      }`,
      g,
    ),
  runGraph: (token: string, tenant: string, workspace: string, id: string) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/run`,
    ),
  // setFlowEnabled pauses (enabled=false) or resumes a flow without deleting
  // it — the scheduler skips disabled flows and webhook/form endpoints reject
  // them, but manual Run still works. Idempotent; commits the flipped state.
  setFlowEnabled: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    enabled: boolean,
  ) =>
    request<{ flow_id: string; enabled: boolean; commit: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/${
        enabled ? "enable" : "disable"
      }`,
    ),
  // listSchedules returns every cron/poll trigger across the workspace's
  // flows, each with its next-run preview — backs the Schedules page.
  listSchedules: (token: string, opts: { tenant?: string; workspace?: string } = {}) => {
    const qs = new URLSearchParams();
    if (opts.tenant) qs.set("tenant", opts.tenant);
    if (opts.workspace) qs.set("workspace", opts.workspace);
    const q = qs.toString();
    return request<{ schedules: ScheduleEntry[] }>(
      token,
      "GET",
      "/me/schedules" + (q ? "?" + q : ""),
    );
  },
  // setTriggerEnabled pauses (enabled=false) or resumes a single trigger
  // node without touching the rest of the flow — finer-grained than
  // setFlowEnabled. Commits the flipped state; 409 if the flow is locked.
  setTriggerEnabled: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    nodeID: string,
    enabled: boolean,
  ) =>
    request<{ flow_id: string; node_id: string; enabled: boolean; commit: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/triggers/${encodeURIComponent(
        nodeID,
      )}/${enabled ? "enable" : "disable"}`,
    ),
  // listGitCredentials returns the org's named git credentials (names +
  // which parts are set — never the secret material). Backs the admin page
  // and the git_checkout account picker.
  listGitCredentials: (token: string) =>
    request<{ credentials: GitCredential[] }>(token, "GET", "/git/credentials"),
  // putGitCredential creates or replaces a named git credential — an SSH key
  // and/or an HTTPS access token (PAT). Validated server-side before storage.
  putGitCredential: (
    token: string,
    account: string,
    body: {
      private_key?: string;
      passphrase?: string;
      known_hosts?: string;
      token?: string;
      username?: string;
    },
  ) =>
    request<void>(
      token,
      "PUT",
      `/git/credentials/${encodeURIComponent(account)}`,
      body,
    ),
  deleteGitCredential: (token: string, account: string) =>
    request<void>(token, "DELETE", `/git/credentials/${encodeURIComponent(account)}`),
  // testTrigger fires a webhook flow with a synthetic JSON payload so a
  // user can verify it end-to-end without wiring an external caller.
  // The daemon seeds webhook_input nodes with `sample` exactly as a real
  // /trigger hit would, and returns a run_id to subscribe to.
  testTrigger: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    sample: unknown,
  ) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/test-trigger`,
      sample,
    ),
  // validateCron asks the daemon to parse a 5-field cron expression
  // using the SAME parser the scheduler uses, and returns the next
  // few fire times when it's valid. UI uses this to surface "bad
  // cron silently never fires" issues at save-time instead of after
  // the user wonders why nothing ran. tz (an IANA name) anchors the
  // expression to a real zone so the previewed fire times match when
  // the flow actually fires — pass the viewer's browser timezone.
  validateCron: (token: string, expr: string, tz?: string) =>
    request<{ valid: boolean; error?: string; next_fires?: string[] }>(
      token,
      "POST",
      "/validate/cron",
      { expr, tz },
    ),
  // sampleNode fires a partial run that ends at nodeID — the daemon
  // strips every node and edge outside nodeID's upstream chain before
  // submitting. Returns the run_id so the caller can subscribe to the
  // same SSE stream the regular Run button uses.
  sampleNode: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    nodeID: string,
  ) =>
    request<{ job_id: string; sampled_node: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/nodes/${encodeURIComponent(nodeID)}/sample`,
    ),
  // retryRun resumes a failed (or cancelled) run from where it failed:
  // the daemon reuses the outputs of the nodes that already succeeded and
  // re-executes only the failed node and its downstream. Returns the new
  // run's id. 409 if the run is still in progress.
  retryRun: (token: string, runID: string) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/me/runs/${encodeURIComponent(runID)}/retry`,
    ),
  cancelRun: (token: string, runID: string, reason?: string) =>
    request<{ status: string }>(
      token,
      "POST",
      `/me/runs/${encodeURIComponent(runID)}/cancel`,
      reason ? { reason } : {},
    ),
  // resumeRun continues a run paused at a breakpoint (#12). step=true runs
  // the next node(s) then pauses again; otherwise it runs to the next
  // breakpoint or completion.
  resumeRun: (token: string, runID: string, step = false) =>
    request<{ status: string }>(
      token,
      "POST",
      `/me/runs/${encodeURIComponent(runID)}/resume`,
      { step },
    ),
  listRuns: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    opts: { limit?: number; offset?: number; status?: JobStatus } = {},
  ) => {
    const qs = new URLSearchParams();
    qs.set("limit", String(opts.limit ?? 20));
    if (opts.offset) qs.set("offset", String(opts.offset));
    if (opts.status) qs.set("status", opts.status);
    return request<{ runs: RunSummary[] }>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/runs?${qs.toString()}`,
    );
  },
  listAllRuns: (
    token: string,
    opts: {
      limit?: number;
      offset?: number;
      status?: JobStatus;
      workspace?: string;
      tenant?: string;
    } = {},
  ) => {
    const qs = new URLSearchParams();
    qs.set("limit", String(opts.limit ?? 50));
    if (opts.offset) qs.set("offset", String(opts.offset));
    if (opts.status) qs.set("status", opts.status);
    if (opts.workspace) qs.set("workspace", opts.workspace);
    if (opts.tenant) qs.set("tenant", opts.tenant);
    return request<{ runs: RunSummary[] }>(
      token,
      "GET",
      `/me/runs?${qs.toString()}`,
    );
  },
  getJob: (token: string, jobID: string): Promise<JobRecord> =>
    request<RunView>(token, "GET", `/me/runs/${encodeURIComponent(jobID)}`).then(
      runViewToRecord,
    ),
  // Billing: plan state for the Usage page, and the two Stripe
  // redirects (Checkout to upgrade, billing portal to manage/cancel).
  getBilling: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<BillingInfo>(token, "GET", "/me/billing" + qs);
  },
  createCheckout: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ url: string }>(token, "POST", "/me/billing/checkout" + qs);
  },
  createBillingPortal: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ url: string }>(token, "POST", "/me/billing/portal" + qs);
  },
  // Usage metering: the tenant's graph-run + node-execution counts,
  // one bucket per month, newest first (current month always present).
  getUsage: (token: string, opts: { tenant?: string; months?: number } = {}) => {
    const qs = new URLSearchParams();
    if (opts.tenant) qs.set("tenant", opts.tenant);
    if (opts.months) qs.set("months", String(opts.months));
    const q = qs.toString();
    return request<{ usage: UsageCounters[] }>(
      token,
      "GET",
      "/me/usage" + (q ? "?" + q : ""),
    );
  },
  listPendingApprovals: (token: string, opts: { workspace?: string; tenant?: string } = {}) => {
    const qs = new URLSearchParams();
    if (opts.workspace) qs.set("workspace", opts.workspace);
    if (opts.tenant) qs.set("tenant", opts.tenant);
    const q = qs.toString();
    return request<{ approvals: PendingApproval[] }>(
      token,
      "GET",
      "/approvals/pending" + (q ? "?" + q : ""),
    );
  },
  listAPIKeys: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ keys: APIKeySummary[] }>(
      token,
      "GET",
      "/admin/api-keys" + qs,
    );
  },
  issueAPIKey: (
    token: string,
    params: {
      id?: string;
      subject: string;
      tenant?: string;
      workspace?: string;
      roles: Role[];
      // ISO-8601 timestamp. Omit (or undefined) = the key never
      // expires (operator default). When set, the daemon rejects the
      // key after this time and the table flips it to "expired".
      expires_at?: string;
    },
  ) => request<IssuedAPIKey>(token, "POST", "/admin/api-keys", params),

  // issueMyAPIKey is the self-issue path used by the Connect MCP modal.
  // Tenant + workspace + subject come from the caller's session — only
  // optional fields ride in the body. Server caps requested permissions
  // to a subset of the caller's own.
  issueMyAPIKey: (
    token: string,
    params: {
      id?: string;
      roles?: Role[];
      expires_at?: string;
    },
  ) => request<IssuedAPIKey>(token, "POST", "/me/api-keys", params),

  revokeAPIKey: (token: string, id: string) =>
    request<void>(token, "DELETE", `/admin/api-keys/${encodeURIComponent(id)}`),
  listUsers: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ users: UserSummary[] }>(
      token,
      "GET",
      "/admin/users" + qs,
    );
  },
  listAudit: (token: string, opts: { limit?: number; offset?: number } = {}) => {
    const qs = new URLSearchParams();
    if (opts.limit) qs.set("limit", String(opts.limit));
    if (opts.offset) qs.set("offset", String(opts.offset));
    const q = qs.toString();
    return request<{ events: AuditEvent[] }>(
      token,
      "GET",
      "/admin/audit" + (q ? "?" + q : ""),
    );
  },
  getWorkspaceLimits: (token: string) =>
    request<WorkspaceLimits>(token, "GET", "/admin/limits"),
  approveNode: (
    token: string,
    runID: string,
    nodeID: string,
    decision: "approve" | "reject",
    comment?: string,
  ) => {
    const qs = new URLSearchParams({ decision });
    if (comment) qs.set("comment", comment);
    return request<{ status: string; decision: string }>(
      token,
      "POST",
      `/approvals/${encodeURIComponent(runID)}/${encodeURIComponent(nodeID)}?${qs.toString()}`,
    );
  },
  getNodeRecord: (token: string, runID: string, nodeID: string): Promise<JobRecord> =>
    request<NodeRunView>(
      token,
      "GET",
      `/me/runs/${encodeURIComponent(runID)}/nodes/${encodeURIComponent(nodeID)}`,
    ).then((n) => nodeViewToRecord(runID, n)),
  // listRunNodes returns every per-node record for a run in one
  // round trip — the run-detail page draws its timeline from this.
  listRunNodes: (token: string, runID: string): Promise<{ nodes: JobRecord[] }> =>
    request<{ nodes: NodeRunView[] }>(
      token,
      "GET",
      `/me/runs/${encodeURIComponent(runID)}/nodes`,
    ).then((r) => ({ nodes: (r.nodes ?? []).map((n) => nodeViewToRecord(runID, n)) })),
  // listRunLogs returns a page of the run's persisted log. `after` is a
  // seq cursor (entries with seq > after), so callers append-poll a live
  // run without refetching history. 501 = the daemon has no log store.
  listRunLogs: (
    token: string,
    runID: string,
    opts: { after?: number; limit?: number } = {},
  ): Promise<{ logs: RunLogEntry[] }> => {
    const qs = new URLSearchParams();
    if (opts.after) qs.set("after", String(opts.after));
    if (opts.limit) qs.set("limit", String(opts.limit));
    const q = qs.toString();
    return request<{ logs: RunLogEntry[] }>(
      token,
      "GET",
      `/me/runs/${encodeURIComponent(runID)}/logs` + (q ? "?" + q : ""),
    );
  },
  // SSE: EventSource doesn't support headers, so we proxy through fetch
  // with ReadableStream parsing instead. Caller cancels via AbortController.
  streamJob(
    token: string,
    jobID: string,
    onEvent: (kind: string, data: unknown) => void,
    signal: AbortSignal,
  ): Promise<void> {
    return fetch(API_BASE + `/me/runs/${encodeURIComponent(jobID)}/events`, {
      method: "GET",
      headers: { Authorization: `Bearer ${token}` },
      signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
        if (res.status === 401) notifyUnauthorized();
        throw new APIError(res.status, await res.text());
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      // Parse SSE frames: `event: <name>\ndata: <json>\n\n`
      while (true) {
        const { value, done } = await reader.read();
        if (done) return;
        buffer += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          if (frame.startsWith(":")) continue; // keep-alive
          let name = "message";
          let dataLine = "";
          for (const line of frame.split("\n")) {
            if (line.startsWith("event: ")) name = line.slice(7);
            else if (line.startsWith("data: ")) dataLine = line.slice(6);
          }
          if (dataLine) {
            try {
              onEvent(name, JSON.parse(dataLine));
            } catch {
              onEvent(name, dataLine);
            }
          }
        }
      }
    });
  },
  // listProviders returns every OAuth provider the daemon is
  // configured for, plus which accounts this tenant has already
  // connected. Drives the "Connect an app" panel. 501 when no OAuth
  // client credentials are configured server-side — callers treat
  // that as "no providers" rather than an error.
  listProviders: async (token: string) => {
    const r = await request<{ providers: OAuthProviderStatus[] }>(
      token,
      "GET",
      "/oauth/providers",
    );
    // The daemon serializes an empty account set as JSON null, not [].
    // Normalize so callers can trust accounts is always an array.
    return {
      providers: (r.providers ?? []).map((p) => ({
        ...p,
        accounts: p.accounts ?? [],
        // stale_accounts is optional on the wire — absent or null when
        // every account's scopes still match. Normalise to an empty
        // array so callers can `.length` and `.includes()` without
        // optional-chain-and-default-everywhere boilerplate.
        stale_accounts: p.stale_accounts ?? [],
      })),
    };
  },
  // googleAccounts returns each connected Google account with the
  // services its grant covers — the data behind /admin/google. Org-admin
  // only (403 otherwise); 404/501 when Google OAuth isn't set up.
  googleAccounts: (token: string) =>
    request<GoogleAccountsResponse>(
      token,
      "GET",
      "/oauth/google/accounts",
    ),
  // startConnection asks the daemon for the consent URL (instead of the
  // 302-redirect authorize endpoint) so the caller can surface errors —
  // a bad account name, missing org-admin, or unconfigured provider —
  // before navigating. On success the caller does
  // window.location.assign(authorize_url). account names the connection;
  // integration requests only that service's scopes (incremental top-up).
  startConnection: (
    token: string,
    provider: string,
    opts: { account?: string; integration?: string; returnTo: string },
  ) => {
    const qs = new URLSearchParams({ return_to: opts.returnTo });
    if (opts.account) qs.set("account", opts.account);
    if (opts.integration) qs.set("integration", opts.integration);
    return request<{ authorize_url: string }>(
      token,
      "POST",
      `/me/connections/${encodeURIComponent(provider)}/authorize?${qs.toString()}`,
    );
  },
  // oauthAuthorizeUrl builds the URL the browser navigates to in order
  // to start an OAuth consent flow. It is NOT fetched — the daemon
  // 302s to the provider, so the caller does window.location.assign().
  // Auth rides on the session cookie (same-origin); the daemon bounces
  // the user back to `returnTo` with ?oauth=success|error appended.
  // integration (the Manifest.Integration label, e.g. "Google Sheets")
  // requests incremental authorization: only that service's scopes appear
  // on the consent screen. Omit it to request the provider's full scope set.
  oauthAuthorizeUrl: (provider: string, returnTo: string, account?: string, integration?: string) => {
    const qs = new URLSearchParams({ return_to: returnTo });
    if (account) qs.set("account", account);
    if (integration) qs.set("integration", integration);
    return `${API_BASE}/oauth/${encodeURIComponent(provider)}/authorize?${qs.toString()}`;
  },
  // disconnectConnection deletes a stored OAuth connection (forgets the
  // token for that provider+account) so flows stop using it. Inverse of
  // the connect flow; needs secret:write.
  disconnectConnection: (token: string, provider: string, account: string) => {
    const qs = account ? `?account=${encodeURIComponent(account)}` : "";
    return request<void>(
      token,
      "DELETE",
      `/me/connections/${encodeURIComponent(provider)}${qs}`,
    );
  },
  // listAdminOAuthProviders returns every provider Hazyflow knows
  // about (Slack, Google, GitHub, Notion) along with whether each is
  // currently configured and where the credentials came from (env vs.
  // persisted via this UI). Admin-only — 403 to a regular member.
  listAdminOAuthProviders: (token: string) =>
    request<{ providers: AdminOAuthProvider[] }>(
      token,
      "GET",
      "/admin/oauth-providers",
    ),
  // upsertAdminOAuthProvider stores client_id + client_secret for one
  // provider, encrypted at rest, and live-registers it in the registry
  // so no daemon restart is needed. Idempotent.
  upsertAdminOAuthProvider: (
    token: string,
    name: string,
    clientID: string,
    clientSecret: string,
  ) =>
    request<{ name: string; configured: boolean; updated_at: string }>(
      token,
      "PUT",
      `/admin/oauth-providers/${encodeURIComponent(name)}`,
      { client_id: clientID, client_secret: clientSecret },
    ),
  // deleteAdminOAuthProvider clears the persisted credentials and
  // unregisters the provider from the in-memory registry. Returns 204.
  deleteAdminOAuthProvider: (token: string, name: string) =>
    request<void>(
      token,
      "DELETE",
      `/admin/oauth-providers/${encodeURIComponent(name)}`,
    ),

  // listSecrets returns the NAMES of the stored credentials at a scope —
  // never the values (the daemon has no read-back endpoint by design).
  // Scope defaults to tenant; workspace uses the caller's workspace; flow
  // requires the flow (graph) id. includeConnections opts the conn.<slug>.*
  // namespace back into the tenant listing — the Apps page needs it to detect
  // which integrations are connected; the Credentials page omits it to stay clean.
  listSecrets: (
    token: string,
    scope?: SecretScope,
    flow?: string,
    includeConnections?: boolean,
  ) => {
    let q = secretQuery(scope, flow);
    if (includeConnections) q += (q ? "&" : "?") + "include=conn";
    return request<{ secrets: string[] }>(token, "GET", "/secrets" + q);
  },
  // putSecret creates or replaces a credential at a scope. Idempotent; 204
  // on success. Value is write-only from here on.
  putSecret: (token: string, name: string, value: string, scope?: SecretScope, flow?: string) =>
    request<void>(token, "PUT", `/secrets/${encodeURIComponent(name)}` + secretQuery(scope, flow), {
      value,
    }),
  deleteSecret: (token: string, name: string, scope?: SecretScope, flow?: string) =>
    request<void>(token, "DELETE", `/secrets/${encodeURIComponent(name)}` + secretQuery(scope, flow)),

  // Flow resources (${resource.NAME}) — named pointers at external content
  // (e.g. a Google Sheet) the engine fetches live. CRUD mirrors secrets'
  // scope/flow query; config is returned by list (it isn't sensitive).
  listResources: (token: string, scope?: SecretScope, flow?: string) =>
    request<{ resources: ResourceDef[]; scope: string }>(
      token,
      "GET",
      "/resources" + secretQuery(scope, flow),
    ),
  putResource: (
    token: string,
    name: string,
    type: string,
    config: Record<string, unknown>,
    scope?: SecretScope,
    flow?: string,
  ) =>
    request<void>(token, "PUT", `/resources/${encodeURIComponent(name)}` + secretQuery(scope, flow), {
      type,
      config,
    }),
  deleteResource: (token: string, name: string, scope?: SecretScope, flow?: string) =>
    request<void>(token, "DELETE", `/resources/${encodeURIComponent(name)}` + secretQuery(scope, flow)),

  // Bring-your-own secret manager (OpenBao/Vault) connection for this tenant.
  // getSecretManager returns a redacted view (no credentials); setSecretManager
  // connection-tests before saving (a 502 means "could not reach the manager").
  getSecretManager: (token: string) =>
    request<SecretManagerStatus>(token, "GET", "/secret-manager"),
  setSecretManager: (token: string, cfg: SecretManagerConfig) =>
    request<void>(token, "PUT", "/secret-manager", cfg),
  deleteSecretManager: (token: string) =>
    request<void>(token, "DELETE", "/secret-manager"),
  // AWS / GCP flavours — same contract, separate config slots, so a
  // tenant can run vault://, aws://, and gcp:// side by side.
  getSecretManagerAws: (token: string) =>
    request<AwsSecretManagerStatus>(token, "GET", "/secret-manager/aws"),
  setSecretManagerAws: (token: string, cfg: AwsSecretManagerConfig) =>
    request<void>(token, "PUT", "/secret-manager/aws", cfg),
  deleteSecretManagerAws: (token: string) =>
    request<void>(token, "DELETE", "/secret-manager/aws"),
  getSecretManagerGcp: (token: string) =>
    request<GcpSecretManagerStatus>(token, "GET", "/secret-manager/gcp"),
  setSecretManagerGcp: (token: string, cfg: GcpSecretManagerConfig) =>
    request<void>(token, "PUT", "/secret-manager/gcp", cfg),
  deleteSecretManagerGcp: (token: string) =>
    request<void>(token, "DELETE", "/secret-manager/gcp"),

  switchOrg: (token: string, tenant: string) =>
    request<{ tenant: string; workspace: string; roles: Role[] }>(
      token,
      "POST",
      "/auth/switch-org",
      { tenant },
    ),

  listMembers: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ members: MemberSummary[] }>(token, "GET", `/admin/members${qs}`);
  },
  removeMember: (token: string, email: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<void>(
      token,
      "DELETE",
      `/admin/members/${encodeURIComponent(email)}${qs}`,
    );
  },
  // updateMemberRoles replaces a member's role set (viewer/editor/admin
  // from the catalog, or a custom set). The home owner can't be changed
  // this way — the server answers 409.
  updateMemberRoles: (token: string, email: string, roles: Role[], tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ email: string; roles: Role[] }>(
      token,
      "PATCH",
      `/admin/members/${encodeURIComponent(email)}${qs}`,
      { roles },
    );
  },

  createInvitation: (
    token: string,
    body: { email: string; workspace?: string; roles?: Role[] },
  ) =>
    request<InvitationSummary & { token: string }>(token, "POST", "/admin/invitations", body),

  listInvitations: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ invitations: InvitationSummary[] }>(
      token,
      "GET",
      `/admin/invitations${qs}`,
    );
  },
  revokeInvitation: (token: string, inviteToken: string) =>
    request<void>(
      token,
      "DELETE",
      `/admin/invitations/${encodeURIComponent(inviteToken)}`,
    ),

  // viewInvitation is unauthenticated — the token IS the credential.
  viewInvitation: (inviteToken: string) =>
    request<InvitationDetails>(
      null,
      "GET",
      `/invitations/${encodeURIComponent(inviteToken)}`,
    ),
  acceptInvitation: (token: string, inviteToken: string) =>
    request<{ tenant: string; workspace: string; roles: Role[] }>(
      token,
      "POST",
      `/invitations/${encodeURIComponent(inviteToken)}/accept`,
    ),

  getOrgAuthConfig: (token: string) =>
    request<OrgAuthConfig>(token, "GET", "/admin/org/auth-config"),
  putOrgAuthConfig: (
    token: string,
    body: {
      google_client_id: string;
      google_client_secret?: string;
      google_workspace_domain?: string;
    },
  ) =>
    request<{ tenant: string; google_enabled: boolean }>(
      token,
      "PUT",
      "/admin/org/auth-config",
      body,
    ),
  deleteOrgAuthConfig: (token: string) =>
    request<void>(token, "DELETE", "/admin/org/auth-config"),

  getPublicSSOStatus: (tenant: string) =>
    request<{ google_enabled: boolean; google_workspace_domain?: string }>(
      null,
      "GET",
      `/auth/sso/${encodeURIComponent(tenant)}`,
    ),

  // getPublicAuthConfig returns deployment-level auth feature flags
  // the sign-in / sign-up pages need to render correctly. No secrets.
  getPublicAuthConfig: () =>
    request<{
      signup_enabled: boolean;
      admin_bootstrap?: boolean;
      wildcard_domain?: string;
    }>(
      null,
      "GET",
      "/auth/config",
    ),

  getOrgProfile: (token: string) =>
    request<OrgProfile>(token, "GET", "/admin/org/profile"),
  putOrgProfile: (token: string, display_name: string, icon?: string) =>
    request<OrgProfile>(token, "PUT", "/admin/org/profile", {
      display_name,
      icon: icon ?? "",
    }),
};
