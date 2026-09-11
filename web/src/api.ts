// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type {
  AccessGrant,
  Ticket,
  TicketView,
  TicketStatus,
  TicketQueueSummaryResponse,
  SupportBundle,
  SupportAgentGrant,
  APIKeySummary,
  AuditEvent,
  DropAdjacency,
  FlowSummary,
  Graph,
  IssuedAPIKey,
  InvitationDetails,
  ShareLink,
  CollectionShareLink,
  PublicCollectionData,
  PublicOverview,
  AdminOAuthProvider,
  InvitationSummary,
  SignupInviteSummary,
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
  DecidedApproval,
  RunLogEntry,
  RunSummary,
  RunView,
  NodeRunView,
  ScheduleEntry,
  PublishInfo,
  GitCredential,
  SSHCredential,
  SSHLogin,
  MCPServer,
  MCPServerInput,
  StepSourceUsage,
  WebAPI,
  WebAPIInput,
  WebAPISpecRequest,
  WebAPISpecResponse,
  Runner,
  RunnerTarget,
  RunnerToken,
  GitMirror,
  MirrorPushResult,
  ReferenceGroups,
  Ref,
  ResourceDef,
  EmailTemplateSummary,
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

const API_BASE = (import.meta.env.VITE_API_BASE ?? "") + "/api/v1";

// Marker for `token` once the session lives in an HttpOnly cookie: the value is
// never readable from JS, so requests send credentials instead of a header.
export const COOKIE_SESSION = "cookie-session";

function authHeader(token: string | null): Record<string, string> {
  return token && token !== COOKIE_SESSION
    ? { Authorization: `Bearer ${token}` }
    : {};
}

export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, message: string, code: string = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export function isErrorCode(err: unknown, code: string): boolean {
  return err instanceof APIError && err.code === code;
}

export function isHTTPStatus(err: unknown, status: number): boolean {
  return err instanceof APIError && err.status === status;
}

let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(handler: (() => void) | null): void {
  onUnauthorized = handler;
}

function notifyUnauthorized(): void {
  if (onUnauthorized) onUnauthorized();
}

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
  opts?: { signalUnauthorized?: boolean },
): Promise<T> {
  const headers: Record<string, string> = { ...authHeader(token) };
  if (body) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(API_BASE + path, {
      method,
      headers,
      credentials: "include",
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new APIError(0, "network error");
  }
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
  token?: string;
  subject?: string;
  tenant?: string;
  workspace?: string;
  expires_at?: string;
  totp_required?: boolean;
  challenge?: string;
};

export type TOTPStatus = {
  enabled: boolean;
  enrolled_at?: string;
  recovery_codes_left?: number;
};

export type TOTPSetup = {
  otp_auth_url: string;
  secret_base32: string;
  qr_png_data_url?: string;
};

export type Preferences = {
  email_on_flow_failure: boolean;
  email_on_support_reply: boolean;
  theme: "system" | "dark" | "light" | "";
  language: string;
};

export type BoardSummary = { name: string; rows: number };
export type BoardPage = {
  name: string;
  columns: string[];
  rows: Record<string, unknown>[];
  total: number;
  truncated: boolean;
};

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
    WillRetry: n.will_retry,
    RetryAt: n.retry_at ?? null,
    Result:
      n.outputs || n.error
        ? { status: n.status, output: n.outputs, error: n.error }
        : undefined,
    Job: n.inputs ? { Input: n.inputs } : undefined,
  };
}

export type SecretScope = "tenant" | "flow";

function secretQuery(scope?: SecretScope, flow?: string): string {
  const p = new URLSearchParams();
  if (scope && scope !== "tenant") p.set("scope", scope);
  if (flow) p.set("flow", flow);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const api = {
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
        headers: { ...authHeader(token) },
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
      }
      if (res.status === 401) notifyUnauthorized();
      throw new APIError(res.status, message || res.statusText);
    }
    return res.json();
  },

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
        headers: { ...authHeader(token) },
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
  signUp: (email: string, password: string, signupInvite?: string) =>
    request<SignInResponse>(null, "POST", "/auth/signup", {
      email,
      password,
      ...(signupInvite ? { signup_invite: signupInvite } : {}),
    }),
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
  requestPasswordReset: (email: string) =>
    request<{ ok: boolean }>(null, "POST", "/auth/forgot-password", { email }),
  resetPassword: (email: string, resetToken: string, password: string) =>
    request<{ ok: boolean }>(null, "POST", "/auth/reset-password", {
      email,
      token: resetToken,
      password,
    }),
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

  // A 401 here just means "not signed in", so it must not fire onUnauthorized.
  whoamiProbe: () =>
    request<WhoAmI>(COOKIE_SESSION, "GET", "/me", undefined, {
      signalUnauthorized: false,
    }),

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

  resetNodeState: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    node: string,
  ) =>
    request<{ reset: boolean; node_id: string; cleared: number }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/nodes/${encodeURIComponent(node)}/reset-state`,
    ),

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

  serviceInfo: () => request<ServiceInfo>(null, "GET", ""),

  totpVerify: (challenge: string, code: string, recoveryCode: string) =>
    request<SignInResponse>(null, "POST", "/auth/totp", {
      challenge,
      code,
      recovery_code: recoveryCode,
    }),
  getTOTPStatus: (token: string) =>
    request<TOTPStatus>(token, "GET", "/me/totp"),
  totpSetup: (token: string) =>
    request<TOTPSetup>(token, "POST", "/me/totp/setup"),
  totpConfirm: (token: string, code: string) =>
    request<{ recovery_codes: string[] }>(
      token,
      "POST",
      "/me/totp/confirm",
      { code },
      { signalUnauthorized: false },
    ),
  totpDisable: (token: string, password: string) =>
    request<void>(
      token,
      "POST",
      "/me/totp/disable",
      { password },
      { signalUnauthorized: false },
    ),
  totpRegenerateRecoveryCodes: (token: string) =>
    request<{ recovery_codes: string[] }>(
      token,
      "POST",
      "/me/totp/recovery-codes",
    ),
  exportMyData: (token: string) =>
    request<Record<string, unknown>>(token, "GET", "/me/export"),
  getPreferences: (token: string) =>
    request<Preferences>(token, "GET", "/me/preferences"),
  updatePreferences: (token: string, patch: Partial<Preferences>) =>
    request<Preferences>(token, "PUT", "/me/preferences", patch),
  listTenants: (token: string) =>
    request<{ tenants: string[] }>(token, "GET", "/admin/tenants"),
  adminVersion: (token: string) =>
    request<VersionStatus>(token, "GET", "/admin/version"),
  listDrops: async (
    token: string,
    query?: string,
    includeDisabled?: boolean,
  ) => {
    const params = new URLSearchParams();
    if (query) params.set("q", query);
    if (includeDisabled) params.set("include_disabled", "1");
    const qs = params.toString();
    const r = await request<{ drops?: Manifest[]; modules?: Manifest[] }>(
      token,
      "GET",
      "/drops" + (qs ? `?${qs}` : ""),
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
  dropSuggestions: async (token: string, tenant: string, workspace: string) => {
    const r = await request<{ items?: DropAdjacency[] }>(
      token,
      "GET",
      `/me/flows/suggestions?tenant=${encodeURIComponent(tenant)}&workspace=${encodeURIComponent(workspace)}`,
    );
    return r.items ?? [];
  },
  loadGraph: (token: string, tenant: string, workspace: string, id: string, ref?: string) =>
    request<Graph>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}${
        ref ? `?ref=${encodeURIComponent(ref)}` : ""
      }`,
    ),
  flowHistory: (token: string, tenant: string, workspace: string, id: string) =>
    request<{ revisions: Revision[]; published_commit?: string }>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/history`,
    ),
  getPublishedInfo: (token: string, tenant: string, workspace: string, id: string) =>
    request<PublishInfo>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/published`,
    ),
  publishFlow: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    ref?: string,
    label?: string,
  ) =>
    request<{ flow_id: string; published_commit: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/publish`,
      {
        ...(ref ? { ref } : {}),
        ...(label ? { label } : {}),
      },
    ),
  unpublishFlow: (token: string, tenant: string, workspace: string, id: string) =>
    request<{ flow_id: string; published: boolean }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/unpublish`,
    ),
  restoreFlow: (token: string, tenant: string, workspace: string, id: string, ref: string) =>
    request<{ commit: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/restore`,
      { ref },
    ),
  duplicateFlow: async (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    name?: string,
  ): Promise<string> => {
    const res = await request<{ flow_id: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/duplicate`,
      name ? { name } : undefined,
    );
    return res.flow_id.split("/").slice(2).join("/");
  },
  labelRevision: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    ref: string,
    label: string,
  ) =>
    request<{ flow_id: string; commit: string; label: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/label`,
      { ref, label },
    ),
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
  // Removes the whole Git history, so it cannot be undone.
  deleteGraph: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    password: string,
  ) =>
    request<void>(
      token,
      "DELETE",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}`,
      { password },
    ),
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
  listMCPServers: (token: string) =>
    request<{ servers: MCPServer[] }>(token, "GET", "/admin/mcp-servers"),
  saveMCPServer: (token: string, input: MCPServerInput, existingName?: string) =>
    existingName
      ? request<MCPServer>(
          token,
          "PUT",
          `/admin/mcp-servers/${encodeURIComponent(existingName)}`,
          input,
        )
      : request<MCPServer>(token, "POST", "/admin/mcp-servers", input),
  refreshMCPServer: (token: string, name: string) =>
    request<MCPServer>(token, "POST", `/admin/mcp-servers/${encodeURIComponent(name)}/refresh`, {}),
  mcpServerUsage: (token: string, name: string) =>
    request<StepSourceUsage>(
      token,
      "GET",
      `/admin/mcp-servers/${encodeURIComponent(name)}/usage`,
    ),
  deleteMCPServer: (token: string, name: string) =>
    request<{ deleted: string }>(token, "DELETE", `/admin/mcp-servers/${encodeURIComponent(name)}`),
  listWebAPIs: (token: string) => request<{ web_apis: WebAPI[] }>(token, "GET", "/admin/web-apis"),
  saveWebAPI: (token: string, input: WebAPIInput, existingName?: string) =>
    existingName
      ? request<WebAPI>(token, "PUT", `/admin/web-apis/${encodeURIComponent(existingName)}`, input)
      : request<WebAPI>(token, "POST", "/admin/web-apis", input),
  webAPIUsage: (token: string, name: string) =>
    request<StepSourceUsage>(
      token,
      "GET",
      `/admin/web-apis/${encodeURIComponent(name)}/usage`,
    ),
  parseWebAPISpec: (token: string, req: WebAPISpecRequest) =>
    request<WebAPISpecResponse>(token, "POST", "/admin/web-apis/spec", req),
  deleteWebAPI: (token: string, name: string) =>
    request<{ deleted: string }>(token, "DELETE", `/admin/web-apis/${encodeURIComponent(name)}`),
  listRunners: (token: string) =>
    request<{ runners: Runner[] }>(token, "GET", "/admin/runners"),
  listRunnerTargets: (token: string) =>
    request<{ runners: RunnerTarget[] }>(token, "GET", "/runners"),
  // Shown once. POST rather than GET so the token never lands in a URL or a log.
  mintRunnerToken: (token: string, name?: string) =>
    request<RunnerToken>(token, "POST", "/admin/runners/token", name ? { name } : {}),
  setRunnerLabels: (token: string, name: string, labels: string[]) =>
    request<Runner>(token, "PUT", `/admin/runners/${encodeURIComponent(name)}/labels`, {
      labels,
    }),
  deleteRunner: (token: string, name: string) =>
    request<void>(token, "DELETE", `/admin/runners/${encodeURIComponent(name)}`),
  listGitCredentials: (token: string) =>
    request<{ credentials: GitCredential[] }>(token, "GET", "/git/credentials"),
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
  listSSHCredentials: (token: string) =>
    request<{ credentials: SSHCredential[] }>(token, "GET", "/ssh/credentials"),
  putSSHCredential: (
    token: string,
    account: string,
    body: {
      host?: string;
      port?: string;
      login?: string;
      username?: string;
      password?: string;
      private_key?: string;
      passphrase?: string;
      fingerprint?: string;
      known_hosts?: string;
      directory?: string;
    },
  ) =>
    request<void>(
      token,
      "PUT",
      `/ssh/credentials/${encodeURIComponent(account)}`,
      body,
    ),
  deleteSSHCredential: (token: string, account: string) =>
    request<void>(token, "DELETE", `/ssh/credentials/${encodeURIComponent(account)}`),
  // Resolves rather than throws on a failed connection: the FIRST run of this is
  // expected to fail, because a server with no host key pinned yet refuses the
  // connection and quotes the fingerprint to paste in. That text is the point.
  verifySSHCredential: (token: string, account: string) =>
    request<{ ok: boolean; error?: string }>(
      token,
      "POST",
      `/ssh/credentials/${encodeURIComponent(account)}/verify`,
    ),
  listSSHLogins: (token: string) =>
    request<{ logins: SSHLogin[] }>(token, "GET", "/ssh/logins"),
  putSSHLogin: (
    token: string,
    name: string,
    body: {
      username: string;
      password?: string;
      private_key?: string;
      passphrase?: string;
      public_key?: string;
    },
  ) => request<void>(token, "PUT", `/ssh/logins/${encodeURIComponent(name)}`, body),
  deleteSSHLogin: (token: string, name: string) =>
    request<void>(token, "DELETE", `/ssh/logins/${encodeURIComponent(name)}`),
  // What a server offers, before a credential exists to verify. Resolves with
  // ok:false rather than throwing: an address nobody answers on is an answer.
  scanSSHHostKey: (token: string, host: string, port: string) =>
    request<{
      ok: boolean;
      error?: string;
      fingerprint?: string;
      key_type?: string;
      known_hosts?: string;
    }>(token, "POST", "/ssh/host-key", { host, port }),
  // The private half is saved with the rest of the form; the public half is
  // what goes into the server's authorized_keys.
  generateSSHKey: (token: string, comment: string) =>
    request<{ private_key: string; public_key: string }>(token, "POST", "/ssh/keypair", {
      comment,
    }),
  getGitMirror: (token: string) => request<GitMirror>(token, "GET", "/git/mirror"),
  // The remote must be an SSH URL.
  putGitMirror: (
    token: string,
    body: {
      remote_url: string;
      account: string;
      enabled: boolean;
      push_on: "publish" | "save";
    },
  ) => request<GitMirror>(token, "PUT", "/git/mirror", body),
  deleteGitMirror: (token: string) => request<GitMirror>(token, "DELETE", "/git/mirror"),
  pushGitMirror: (token: string, overwriteUnrelated = false) =>
    request<MirrorPushResult>(token, "POST", "/git/mirror/push", {
      overwrite_unrelated: overwriteUnrelated,
    }),
  // nodeID fires ONE trigger step; without it every Webhook/Form/Request step
  // in the flow is seeded, which is ambiguous the moment there are two.
  testTrigger: (
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    sample: unknown,
    nodeID?: string,
  ) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/test-trigger` +
        (nodeID ? `?node=${encodeURIComponent(nodeID)}` : ""),
      sample,
    ),
  validateCron: (token: string, expr: string, tz?: string) =>
    request<{ valid: boolean; error?: string; next_fires?: string[] }>(
      token,
      "POST",
      "/validate/cron",
      { expr, tz },
    ),

  previewRenderTemplate: (token: string, template: string, data: unknown) =>
    request<{ html?: string; error?: string }>(
      token,
      "POST",
      "/tools/render-template/preview",
      { template, data },
    ),

  previewRenderText: (
    token: string,
    params: Record<string, unknown>,
    rows: unknown,
  ) =>
    request<{ text?: string; error?: string }>(
      token,
      "POST",
      "/tools/render-text/preview",
      { ...params, rows },
    ),

  validateExpression: (token: string, expr: string) =>
    request<{
      valid: boolean;
      issue?: { message: string; line: number; column: number };
    }>(token, "POST", "/tools/expression/validate", { expr }),

  assistRenderTemplate: (
    token: string,
    description: string,
    fields: string[],
    provider?: string,
  ) =>
    request<{ template?: string; error?: string; need_connect?: boolean; provider?: string }>(
      token,
      "POST",
      "/tools/render-template/assist",
      { description, fields, provider },
    ),

  listLLMProviders: (token: string) =>
    request<{ providers: { name: string; label: string }[] }>(
      token,
      "GET",
      "/tools/llm-providers",
    ),

  streamFlowGenerate(
    token: string,
    body: { description: string; provider?: string; tz?: string; base?: unknown },
    onEvent: (kind: string, data: unknown) => void,
    signal?: AbortSignal,
  ): Promise<void> {
    return fetch(API_BASE + "/tools/flow/generate/stream", {
      method: "POST",
      headers: { ...authHeader(token), "Content-Type": "application/json" },
      credentials: "include",
      body: JSON.stringify(body),
      signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
        if (res.status === 401) notifyUnauthorized();
        throw new APIError(res.status, await res.text());
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) return;
        buffer += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          if (frame.startsWith(":")) continue;
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

  generateFlow: (token: string, description: string, provider?: string) =>
    request<{
      graph?: Graph;
      issues?: { code: string; severity: string; message: string; node_ids?: string[] }[];
      provider?: string;
      error?: string;
      need_connect?: boolean;
    }>(token, "POST", "/tools/flow/generate", { description, provider }),
  flowSamples: (token: string, tenant: string, workspace: string, id: string) =>
    request<{ flow: string; nodes: Record<string, Record<string, Ref>> }>(
      token,
      "GET",
      `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/samples`,
    ),
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
  retryRun: (token: string, runID: string) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/me/runs/${encodeURIComponent(runID)}/retry`,
    ),
  replayRun: (token: string, runID: string) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/me/runs/${encodeURIComponent(runID)}/replay`,
    ),
  cancelRun: (token: string, runID: string, reason?: string) =>
    request<{ status: string }>(
      token,
      "POST",
      `/me/runs/${encodeURIComponent(runID)}/cancel`,
      reason ? { reason } : {},
    ),
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
    opts: {
      limit?: number;
      offset?: number;
      status?: JobStatus;
      since?: string;
      until?: string;
    } = {},
  ) => {
    const qs = new URLSearchParams();
    qs.set("limit", String(opts.limit ?? 20));
    if (opts.offset) qs.set("offset", String(opts.offset));
    if (opts.status) qs.set("status", opts.status);
    if (opts.since) qs.set("since", opts.since);
    if (opts.until) qs.set("until", opts.until);
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
      since?: string;
      until?: string;
    } = {},
  ) => {
    const qs = new URLSearchParams();
    qs.set("limit", String(opts.limit ?? 50));
    if (opts.offset) qs.set("offset", String(opts.offset));
    if (opts.status) qs.set("status", opts.status);
    if (opts.workspace) qs.set("workspace", opts.workspace);
    if (opts.tenant) qs.set("tenant", opts.tenant);
    if (opts.since) qs.set("since", opts.since);
    if (opts.until) qs.set("until", opts.until);
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
  listBoards: (token: string, tenant?: string, workspace?: string) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const s = qs.toString();
    return request<{ boards: BoardSummary[] }>(
      token,
      "GET",
      "/me/boards" + (s ? `?${s}` : ""),
    );
  },
  getBoard: (
    token: string,
    name: string,
    opts: { limit?: number; offset?: number; tenant?: string; workspace?: string } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.limit) qs.set("limit", String(opts.limit));
    if (opts.offset) qs.set("offset", String(opts.offset));
    if (opts.tenant) qs.set("tenant", opts.tenant);
    if (opts.workspace) qs.set("workspace", opts.workspace);
    const s = qs.toString();
    return request<BoardPage>(
      token,
      "GET",
      `/me/boards/${encodeURIComponent(name)}` + (s ? `?${s}` : ""),
    );
  },
  clearBoard: (token: string, name: string, tenant?: string, workspace?: string) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const s = qs.toString();
    return request<void>(
      token,
      "DELETE",
      `/me/boards/${encodeURIComponent(name)}` + (s ? `?${s}` : ""),
    );
  },
  deleteBoardRow: (
    token: string,
    name: string,
    rowid: number,
    tenant?: string,
    workspace?: string,
  ) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const s = qs.toString();
    return request<void>(
      token,
      "DELETE",
      `/me/boards/${encodeURIComponent(name)}/rows/${encodeURIComponent(String(rowid))}` +
        (s ? `?${s}` : ""),
    );
  },
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
  getPlans: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<PlansInfo>(token, "GET", "/me/plans" + qs);
  },
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
  countPendingApprovals: (
    token: string,
    opts: { workspace?: string; tenant?: string } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.workspace) qs.set("workspace", opts.workspace);
    if (opts.tenant) qs.set("tenant", opts.tenant);
    const q = qs.toString();
    return request<{ count: number }>(
      token,
      "GET",
      "/approvals/pending/count" + (q ? "?" + q : ""),
    );
  },
  listDecidedApprovals: (
    token: string,
    opts: { workspace?: string; tenant?: string; limit?: number } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.workspace) qs.set("workspace", opts.workspace);
    if (opts.tenant) qs.set("tenant", opts.tenant);
    if (opts.limit) qs.set("limit", String(opts.limit));
    const q = qs.toString();
    return request<{ approvals: DecidedApproval[] }>(
      token,
      "GET",
      "/approvals/decided" + (q ? "?" + q : ""),
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
      // Omitted means the key never expires.
      expires_at?: string;
    },
  ) => request<IssuedAPIKey>(token, "POST", "/admin/api-keys", params),

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
  listRunNodes: (token: string, runID: string): Promise<{ nodes: JobRecord[] }> =>
    request<{ nodes: NodeRunView[] }>(
      token,
      "GET",
      `/me/runs/${encodeURIComponent(runID)}/nodes`,
    ).then((r) => ({ nodes: (r.nodes ?? []).map((n) => nodeViewToRecord(runID, n)) })),
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
  streamJob(
    token: string,
    jobID: string,
    onEvent: (kind: string, data: unknown) => void,
    signal: AbortSignal,
  ): Promise<void> {
    return fetch(API_BASE + `/me/runs/${encodeURIComponent(jobID)}/events`, {
      method: "GET",
      headers: { ...authHeader(token) },
      credentials: "include",
      signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
        if (res.status === 401) notifyUnauthorized();
        throw new APIError(res.status, await res.text());
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
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
  streamSystemLog(
    token: string,
    onLine: (line: string) => void,
    signal: AbortSignal,
    tail = 500,
  ): Promise<void> {
    const qs = tail !== 500 ? `?tail=${encodeURIComponent(String(tail))}` : "";
    return fetch(API_BASE + `/admin/system/log` + qs, {
      method: "GET",
      headers: { ...authHeader(token) },
      credentials: "include",
      signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
        if (res.status === 401) notifyUnauthorized();
        throw new APIError(res.status, await res.text());
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) return;
        buffer += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          if (frame.startsWith(":")) continue; // keep-alive
          let dataLine = "";
          for (const line of frame.split("\n")) {
            if (line.startsWith("data: ")) dataLine = line.slice(6);
          }
          if (dataLine) {
            try {
              onLine(JSON.parse(dataLine) as string);
            } catch {
              onLine(dataLine);
            }
          }
        }
      }
    });
  },
  watchFlow(
    token: string,
    tenant: string,
    workspace: string,
    id: string,
    onUpdated: (ev: {
      flow_id: string;
      commit: string;
      author: string;
      autosave: boolean;
    }) => void,
    signal: AbortSignal,
  ): Promise<void> {
    return fetch(
      API_BASE +
        `/me/flows/${encodeURIComponent(`${tenant}/${workspace}/${id}`)}/watch`,
      { method: "GET", headers: { ...authHeader(token) }, credentials: "include", signal },
    ).then(async (res) => {
      if (!res.ok || !res.body) {
        if (res.status === 401) notifyUnauthorized();
        throw new APIError(res.status, await res.text());
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) return;
        buffer += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          if (frame.startsWith(":")) continue; // keep-alive / open comment
          let name = "message";
          let dataLine = "";
          for (const line of frame.split("\n")) {
            if (line.startsWith("event: ")) name = line.slice(7);
            else if (line.startsWith("data: ")) dataLine = line.slice(6);
          }
          if (name === "flow_updated" && dataLine) {
            try {
              onUpdated(JSON.parse(dataLine));
            } catch {
              /* ignore malformed frame */
            }
          }
        }
      }
    });
  },
  listProviders: async (token: string) => {
    const r = await request<{ providers: OAuthProviderStatus[] }>(
      token,
      "GET",
      "/oauth/providers",
    );
    return {
      providers: (r.providers ?? []).map((p) => ({
        ...p,
        accounts: p.accounts ?? [],
        // Optional on the wire: absent from an older daemon.
        stale_accounts: p.stale_accounts ?? [],
        needs_reconnect: p.needs_reconnect ?? [],
      })),
    };
  },
  googleAccounts: (token: string) =>
    request<GoogleAccountsResponse>(
      token,
      "GET",
      "/oauth/google/accounts",
    ),
  // The daemon builds the consent URL, so the client never holds the client id.
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
  oauthAuthorizeUrl: (provider: string, returnTo: string, account?: string, integration?: string) => {
    const qs = new URLSearchParams({ return_to: returnTo });
    if (account) qs.set("account", account);
    if (integration) qs.set("integration", integration);
    return `${API_BASE}/oauth/${encodeURIComponent(provider)}/authorize?${qs.toString()}`;
  },
  disconnectConnection: (token: string, provider: string, account: string) => {
    const qs = account ? `?account=${encodeURIComponent(account)}` : "";
    return request<void>(
      token,
      "DELETE",
      `/me/connections/${encodeURIComponent(provider)}${qs}`,
    );
  },
  listAdminOAuthProviders: (token: string) =>
    request<{ providers: AdminOAuthProvider[] }>(
      token,
      "GET",
      "/admin/oauth-providers",
    ),
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
  deleteAdminOAuthProvider: (token: string, name: string) =>
    request<void>(
      token,
      "DELETE",
      `/admin/oauth-providers/${encodeURIComponent(name)}`,
    ),

  // NAMES only; values never leave the daemon.
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
  putSecret: (token: string, name: string, value: string, scope?: SecretScope, flow?: string) =>
    request<void>(token, "PUT", `/secrets/${encodeURIComponent(name)}` + secretQuery(scope, flow), {
      value,
    }),
  deleteSecret: (token: string, name: string, scope?: SecretScope, flow?: string) =>
    request<void>(token, "DELETE", `/secrets/${encodeURIComponent(name)}` + secretQuery(scope, flow)),

  connectIntegration: (token: string, slug: string, values: Record<string, string>) =>
    request<void>(token, "PUT", `/catalog/integrations/${encodeURIComponent(slug)}/connection`, {
      values,
    }),

  verifyIntegration: (token: string, slug: string) =>
    request<{ ok: boolean; error?: string }>(
      token,
      "POST",
      `/catalog/integrations/${encodeURIComponent(slug)}/verify`,
    ),

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

  listEmailTemplates: (token: string) =>
    request<{ templates: EmailTemplateSummary[] }>(token, "GET", "/email-templates"),
  putEmailTemplate: (token: string, name: string, html: string, displayName?: string) =>
    request<void>(token, "PUT", `/email-templates/${encodeURIComponent(name)}`, {
      name: displayName ?? name,
      html,
    }),
  deleteEmailTemplate: (token: string, name: string) =>
    request<void>(token, "DELETE", `/email-templates/${encodeURIComponent(name)}`),
  previewEmailTemplate: (
    token: string,
    args: { html?: string; id?: string; body?: string; subject?: string },
  ) => request<{ html: string }>(token, "POST", "/email-templates/preview", args),

  sendTestEmail: (
    token: string,
    args: { to?: string; html?: string; id?: string; subject?: string },
  ) => request<{ ok: boolean; to: string; from: string }>(token, "POST", "/email-templates/send-test", args),

  getSecretManager: (token: string) =>
    request<SecretManagerStatus>(token, "GET", "/secret-manager"),
  setSecretManager: (token: string, cfg: SecretManagerConfig) =>
    request<void>(token, "PUT", "/secret-manager", cfg),
  deleteSecretManager: (token: string) =>
    request<void>(token, "DELETE", "/secret-manager"),
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

  createOrg: (token: string, displayName: string) =>
    request<{ tenant: string; display_name: string; workspace: string }>(
      token,
      "POST",
      "/me/orgs",
      { display_name: displayName },
    ),

  exportOrg: (token: string, tenant: string) =>
    request<Record<string, unknown>>(
      token,
      "GET",
      `/admin/orgs/${encodeURIComponent(tenant)}/export`,
    ),

  deleteOrg: (token: string, tenant: string, password: string) =>
    request<Record<string, unknown>>(
      token,
      "DELETE",
      `/admin/orgs/${encodeURIComponent(tenant)}?confirm=${encodeURIComponent(tenant)}`,
      { password },
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

  listSupportGrants: (token: string) =>
    request<{ grants: AccessGrant[] }>(token, "GET", "/support/grants"),
  decideSupportGrant: (token: string, id: string, decision: "approve" | "deny") =>
    request<AccessGrant>(token, "POST", `/support/grants/${encodeURIComponent(id)}/decide`, {
      decision,
    }),
  revokeSupportGrant: (token: string, id: string) =>
    request<{ status: string }>(token, "POST", `/support/grants/${encodeURIComponent(id)}/revoke`),

  listMySupportGrants: (token: string) =>
    request<{ grants: AccessGrant[] }>(token, "GET", "/support/grants/mine"),
  requestSupportGrant: (token: string, tenant: string, flowId: string, ticketId?: string) =>
    request<AccessGrant>(token, "POST", "/support/grants", {
      tenant,
      flow_id: flowId,
      ...(ticketId ? { ticket_id: ticketId } : {}),
    }),
  viewSupportFlow: (
    token: string,
    tenant: string,
    workspace: string,
    flowId: string,
    opts?: { runId?: string; mode?: string },
  ) => {
    const params = new URLSearchParams();
    if (opts?.runId) params.set("run_id", opts.runId);
    if (opts?.mode) params.set("mode", opts.mode);
    const qs = params.toString();
    return request<SupportBundle>(
      token,
      "GET",
      `/support/flows/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(flowId)}${qs ? `?${qs}` : ""}`,
    );
  },

  createTicket: (
    token: string,
    body: { subject: string; flow_id?: string; run_id?: string; message?: string },
  ) => request<Ticket>(token, "POST", "/me/support/tickets", body),
  listMyTickets: (token: string, status?: TicketStatus) =>
    request<{ tickets: Ticket[] }>(
      token,
      "GET",
      "/me/support/tickets" + (status ? `?status=${encodeURIComponent(status)}` : ""),
    ),
  getMyTicket: (token: string, id: string) =>
    request<TicketView>(token, "GET", `/me/support/tickets/${encodeURIComponent(id)}`),
  getMyTicketBundle: (token: string, id: string) =>
    request<SupportBundle>(token, "GET", `/me/support/tickets/${encodeURIComponent(id)}/bundle`),
  postMyTicketMessage: (token: string, id: string, message: string) =>
    request<TicketView>(token, "POST", `/me/support/tickets/${encodeURIComponent(id)}/messages`, {
      message,
    }),
  setMyTicketStatus: (token: string, id: string, status: "closed" | "awaiting_support") =>
    request<TicketView>(token, "POST", `/me/support/tickets/${encodeURIComponent(id)}/status`, {
      status,
    }),

  listTicketQueue: (
    token: string,
    opts: { status?: TicketStatus; assignee?: string; unassigned?: boolean } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.status) qs.set("status", opts.status);
    if (opts.assignee) qs.set("assignee", opts.assignee);
    if (opts.unassigned) qs.set("unassigned", "true");
    const q = qs.toString();
    return request<{ tickets: Ticket[] }>(token, "GET", "/support/tickets" + (q ? `?${q}` : ""));
  },
  ticketQueueSummary: (token: string) =>
    request<TicketQueueSummaryResponse>(token, "GET", "/support/tickets/summary"),
  getSupportTicket: (token: string, id: string) =>
    request<TicketView>(token, "GET", `/support/tickets/${encodeURIComponent(id)}`),
  getSupportTicketBundle: (token: string, id: string) =>
    request<SupportBundle>(token, "GET", `/support/tickets/${encodeURIComponent(id)}/bundle`),
  postSupportTicketMessage: (token: string, id: string, message: string) =>
    request<TicketView>(token, "POST", `/support/tickets/${encodeURIComponent(id)}/messages`, {
      message,
    }),
  markMyTicketRead: (token: string, id: string) =>
    request<TicketView>(token, "POST", `/me/support/tickets/${encodeURIComponent(id)}/read`),
  markSupportTicketRead: (token: string, id: string) =>
    request<TicketView>(token, "POST", `/support/tickets/${encodeURIComponent(id)}/read`),
  setSupportTicketStatus: (token: string, id: string, status: TicketStatus) =>
    request<TicketView>(token, "POST", `/support/tickets/${encodeURIComponent(id)}/status`, {
      status,
    }),
  assignSupportTicket: (token: string, id: string, assignee: string) =>
    request<TicketView>(token, "POST", `/support/tickets/${encodeURIComponent(id)}/assign`, {
      assignee,
    }),

  platformListSupportAgents: (token: string) =>
    request<{ agents: SupportAgentGrant[] }>(token, "GET", "/admin/platform/support-agents"),
  platformGrantSupportAgent: (token: string, email: string) =>
    request<{ agents: SupportAgentGrant[] }>(token, "POST", "/admin/platform/support-agents", { email }),
  platformRevokeSupportAgent: (token: string, email: string) =>
    request<{ agents: SupportAgentGrant[] }>(
      token,
      "DELETE",
      `/admin/platform/support-agents/${encodeURIComponent(email)}`,
    ),

  createSignupInvite: (token: string, email: string) =>
    request<SignupInviteSummary>(token, "POST", "/admin/signup-invites", {
      email,
    }),
  listSignupInvites: (token: string) =>
    request<{ invites: SignupInviteSummary[] }>(
      token,
      "GET",
      "/admin/signup-invites",
    ),
  revokeSignupInvite: (token: string, inviteToken: string) =>
    request<void>(
      token,
      "DELETE",
      `/admin/signup-invites/${encodeURIComponent(inviteToken)}`,
    ),

  smtpTest: (token: string, to?: string) =>
    request<{ ok: boolean; to: string; from: string }>(
      token,
      "POST",
      "/admin/smtp-test",
      to ? { to } : {},
    ),

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

  getShare: (token: string, tenant?: string, workspace?: string) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<ShareLink>(token, "GET", `/me/share${q ? `?${q}` : ""}`);
  },
  createShare: (token: string, tenant?: string, workspace?: string) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<ShareLink>(token, "POST", `/me/share${q ? `?${q}` : ""}`);
  },
  deleteShare: (token: string, tenant?: string, workspace?: string) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<void>(token, "DELETE", `/me/share${q ? `?${q}` : ""}`);
  },
  listCollectionShares: (token: string, tenant?: string, workspace?: string) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<{ shares: CollectionShareLink[] }>(
      token,
      "GET",
      `/me/collection-shares${q ? `?${q}` : ""}`,
    );
  },
  getCollectionShare: (
    token: string,
    name: string,
    tenant?: string,
    workspace?: string,
  ) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<CollectionShareLink>(
      token,
      "GET",
      `/me/collection-shares/${encodeURIComponent(name)}${q ? `?${q}` : ""}`,
    );
  },
  createCollectionShare: (
    token: string,
    name: string,
    tenant?: string,
    workspace?: string,
  ) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<CollectionShareLink>(
      token,
      "POST",
      `/me/collection-shares/${encodeURIComponent(name)}${q ? `?${q}` : ""}`,
    );
  },
  deleteCollectionShare: (
    token: string,
    name: string,
    tenant?: string,
    workspace?: string,
  ) => {
    const qs = new URLSearchParams();
    if (tenant) qs.set("tenant", tenant);
    if (workspace) qs.set("workspace", workspace);
    const q = qs.toString();
    return request<void>(
      token,
      "DELETE",
      `/me/collection-shares/${encodeURIComponent(name)}${q ? `?${q}` : ""}`,
    );
  },
  getPublicCollection: (
    shareToken: string,
    opts: { limit?: number; offset?: number } = {},
  ) => {
    const qs = new URLSearchParams();
    if (opts.limit) qs.set("limit", String(opts.limit));
    if (opts.offset) qs.set("offset", String(opts.offset));
    const q = qs.toString();
    return request<PublicCollectionData>(
      null,
      "GET",
      `/public/collection/${encodeURIComponent(shareToken)}${q ? `?${q}` : ""}`,
    );
  },

  getPublicOverview: (shareToken: string) =>
    request<PublicOverview>(
      null,
      "GET",
      `/public/overview/${encodeURIComponent(shareToken)}`,
    ),

  getPublicSSOStatus: (tenant: string) =>
    request<{ google_enabled: boolean; google_workspace_domain?: string }>(
      null,
      "GET",
      `/auth/sso/${encodeURIComponent(tenant)}`,
    ),

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

  getMapConfig: () =>
    request<{ tile_url: string; geocoder_url: string }>(
      null,
      "GET",
      "/map/config",
    ),

  getOrgProfile: (token: string) =>
    request<OrgProfile>(token, "GET", "/admin/org/profile"),
  putOrgProfile: (token: string, display_name: string, icon?: string) =>
    request<OrgProfile>(token, "PUT", "/admin/org/profile", {
      display_name,
      icon: icon ?? "",
    }),
  putOrgSubdomain: (token: string, subdomain: string) =>
    request<{ tenant: string; subdomain: string; wildcard_domain: string }>(
      token,
      "PUT",
      "/admin/org/subdomain",
      { subdomain },
    ),
  checkSubdomainAvailable: (token: string, label: string) =>
    request<{ available: boolean; reason?: string }>(
      token,
      "GET",
      `/admin/org/subdomain/available?label=${encodeURIComponent(label)}`,
    ),
  resolveSubdomain: (label: string) =>
    request<{ tenant: string; display_name: string; icon?: string }>(
      null,
      "GET",
      `/auth/resolve-subdomain?label=${encodeURIComponent(label)}`,
    ),


  platformListUsers: (token: string) =>
    request<{ users: PlatformUser[] }>(token, "GET", "/admin/platform/users"),
  platformGetUser: (token: string, email: string) =>
    request<{ user: PlatformUser; memberships?: string[] }>(
      token,
      "GET",
      `/admin/platform/users/${encodeURIComponent(email)}`,
    ),
  platformSuspendUser: (token: string, email: string, reason: string) =>
    request<{ user: PlatformUser }>(
      token,
      "POST",
      `/admin/platform/users/${encodeURIComponent(email)}/suspend`,
      { reason },
    ),
  platformUnsuspendUser: (token: string, email: string) =>
    request<{ user: PlatformUser }>(
      token,
      "POST",
      `/admin/platform/users/${encodeURIComponent(email)}/unsuspend`,
    ),
  platformVerifyUser: (token: string, email: string) =>
    request<{ user: PlatformUser }>(
      token,
      "POST",
      `/admin/platform/users/${encodeURIComponent(email)}/verify`,
    ),
  platformBanUser: (token: string, email: string, reason: string, domain: boolean) =>
    request<{ user: PlatformUser; blocked: string }>(
      token,
      "POST",
      `/admin/platform/users/${encodeURIComponent(email)}/ban`,
      { reason, domain },
    ),
  platformGrantAdmin: (token: string, email: string) =>
    request<{ user: PlatformUser }>(
      token,
      "POST",
      `/admin/platform/users/${encodeURIComponent(email)}/platform-admin`,
    ),
  platformRevokeAdmin: (token: string, email: string) =>
    request<{ user?: PlatformUser; email?: string; platform_admin?: boolean }>(
      token,
      "DELETE",
      `/admin/platform/users/${encodeURIComponent(email)}/platform-admin`,
    ),
  platformDeleteUser: (token: string, email: string) =>
    request<Record<string, unknown>>(
      token,
      "DELETE",
      `/admin/users/${encodeURIComponent(email)}?confirm=${encodeURIComponent(email)}`,
    ),

  platformListOrgs: (token: string) =>
    request<{ orgs: PlatformOrg[] }>(token, "GET", "/admin/platform/orgs"),
  platformGetOrg: (token: string, tenant: string) =>
    request<{ org: PlatformOrg; members?: string[] }>(
      token,
      "GET",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}`,
    ),
  platformSuspendOrg: (token: string, tenant: string, reason: string) =>
    request<{ org: PlatformOrg }>(
      token,
      "POST",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}/suspend`,
      { reason },
    ),
  platformUnsuspendOrg: (token: string, tenant: string) =>
    request<{ org: PlatformOrg }>(
      token,
      "POST",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}/unsuspend`,
    ),
  platformBanOrg: (token: string, tenant: string, reason: string) =>
    request<{ org: PlatformOrg }>(
      token,
      "POST",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}/ban`,
      { reason },
    ),

  platformListDrops: (token: string) =>
    request<{ drops: PlatformDrop[] }>(token, "GET", "/admin/platform/drops"),
  platformDisableDrop: (token: string, id: string, reason: string, tenant?: string) =>
    request<void>(
      token,
      "POST",
      `/admin/platform/drops/${encodeURIComponent(id)}/disable`,
      { reason, tenant: tenant ?? "" },
    ),
  platformEnableDrop: (token: string, id: string, tenant?: string) =>
    request<void>(
      token,
      "POST",
      `/admin/platform/drops/${encodeURIComponent(id)}/enable`,
      { tenant: tenant ?? "" },
    ),

  platformListTiers: (token: string) =>
    request<{ tiers: PlatformTier[] }>(token, "GET", "/admin/platform/tiers"),
  platformSaveTier: (token: string, tier: PlatformTier) =>
    request<{ tier: PlatformTier }>(token, "POST", "/admin/platform/tiers", tier),
  platformDeleteTier: (token: string, id: string) =>
    request<void>(token, "DELETE", `/admin/platform/tiers/${encodeURIComponent(id)}`),
  platformGetEntitlement: (token: string, tenant: string) =>
    request<{ entitlement: TenantEntitlement; effective: EffectiveLimits; tiers: PlatformTier[] }>(
      token,
      "GET",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}/entitlement`,
    ),
  platformPutEntitlement: (token: string, tenant: string, ent: TenantEntitlement) =>
    request<{ entitlement: TenantEntitlement; effective: EffectiveLimits }>(
      token,
      "PUT",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}/entitlement`,
      ent,
    ),
  platformInviteMember: (token: string, tenant: string, email: string, roleName: string) =>
    request<{ token: string; email: string; tenant: string }>(
      token,
      "POST",
      `/admin/platform/orgs/${encodeURIComponent(tenant)}/invite`,
      { email, roles: [{ name: roleName, permissions: [] }] },
    ),
};

export type PlatformTier = {
  id: string;
  name: string;
  plan: string; // "free" | "pro"
  runs_per_month: number;
  disk_quota_bytes: number;
  max_graph_nodes: number;
  max_flows: number;
  max_timeout_seconds: number;
  retention_days: number;
  max_concurrency: number;
  max_members: number;
  polling_allowed?: boolean | null;
  built_in: boolean;
  updated_at?: string;
};

export type TenantEntitlement = {
  tenant: string;
  tier_id: string;
  plan_override?: string; // "", "free", "pro"
  comped?: boolean;
  trial_ends_at?: string | null;
  runs_per_month?: number | null;
  disk_quota_bytes?: number | null;
  max_graph_nodes?: number | null;
  max_flows?: number | null;
  max_timeout_seconds?: number | null;
  retention_days?: number | null;
  max_concurrency?: number | null;
  max_members?: number | null;
  polling_allowed?: boolean | null;
  notes?: string;
};

export type EffectiveLimits = {
  plan: string;
  runs_per_month: number;
  disk_quota_bytes: number;
  max_graph_nodes: number;
  max_flows: number;
  max_timeout_seconds: number;
  retention_days: number;
  max_concurrency: number;
  max_members: number;
  polling_allowed: boolean;
  tier_id?: string;
  trial_ends_at?: string | null;
  comped?: boolean;
};

export type PlanLimits = {
  runs_per_month: number;
  max_flows: number;
  max_graph_nodes: number;
  disk_quota_bytes: number;
  max_timeout_seconds: number;
  retention_days: number;
  max_concurrency: number;
  max_members: number;
  polling_allowed: boolean;
};

export type PlanOption = {
  id: string;
  name: string;
  plan: string; // "free" | "pro"
  is_current: boolean;
  is_contact?: boolean; // sales-led (Enterprise): "Contact sales", not self-serve upgrade
  limits: PlanLimits;
};

export type PlansInfo = {
  current_plan: string;
  current_tier_id: string;
  runs_this_month: number;
  can_upgrade: boolean;
  can_manage: boolean;
  plans: PlanOption[];
};

export type PlatformUser = {
  email: string;
  subject: string;
  tenant: string;
  tenant_name?: string; // resolved home-org display name ("" → use id)
  status: string; // "active" | "suspended"
  suspended_at?: string;
  suspend_reason?: string;
  created_at: string;
  verified: boolean;
  platform_admin: boolean;
  platform_admin_env: boolean;
};

export type PlatformOrg = {
  tenant: string;
  display_name: string;
  icon?: string; // data: URL logo, or empty
  subdomain?: string;
  status: string; // "active" | "suspended"
  suspended_at?: string;
  suspend_reason?: string;
  member_count: number;
};

export type PlatformDrop = {
  id: string;
  label: string;
  integration?: string;
  icon?: string;
  category?: string;
  color?: string;
  brand_logo?: string;
  globally_disabled: boolean;
  disabled_tenants?: string[];
  reason?: string;
};
