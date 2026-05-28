import type {
  APIKeySummary,
  AuditEvent,
  FlowSummary,
  Graph,
  IssuedAPIKey,
  InvitationDetails,
  InvitationSummary,
  LintIssue,
  Manifest,
  MemberSummary,
  OAuthProviderStatus,
  OrgAuthConfig,
  OrgProfile,
  Role,
  TemplateSummary,
  JobRecord,
  JobStatus,
  PendingApproval,
  RunSummary,
  UserSummary,
  WhoAmI,
  WorkspaceLimits,
} from "./types";

// API_BASE: dev defaults to relative "/api/v1" (proxied by Vite to the
// daemon); prod builds can hardcode an absolute URL via VITE_API_BASE.
const API_BASE = (import.meta.env.VITE_API_BASE ?? "") + "/api/v1";

export class APIError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(
  token: string | null,
  method: string,
  path: string,
  body?: unknown,
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
    const text = await res.text();
    let message = text;
    try {
      const parsed = JSON.parse(text);
      if (parsed.error) message = parsed.error;
    } catch {
      // fall through with raw text
    }
    throw new APIError(res.status, message || res.statusText);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export type SignInResponse = {
  token: string;
  subject: string;
  tenant: string;
  workspace: string;
  expires_at: string;
};

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
      throw new APIError(res.status, message || res.statusText);
    }
    return res.json();
  },
  signIn: (email: string, password: string) =>
    request<SignInResponse>(null, "POST", "/auth/signin", { email, password }),
  // signUp returns the same shape as signIn — the server issues a
  // session immediately so the UI can land the user on the welcome
  // page without an extra round trip.
  signUp: (email: string, password: string) =>
    request<SignInResponse>(null, "POST", "/auth/signup", { email, password }),
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
    request<void>(token, "POST", "/auth/signout"),
  whoami: (token: string | null) => request<WhoAmI>(token, "GET", "/whoami"),
  listWorkspaces: (token: string, tenant?: string) => {
    const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
    return request<{ workspaces: string[] }>(token, "GET", "/workspaces" + qs);
  },
  listTenants: (token: string) =>
    request<{ tenants: string[] }>(token, "GET", "/admin/tenants"),
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
  listGraphs: (token: string, tenant: string, workspace: string) =>
    request<{ graphs: FlowSummary[] }>(
      token,
      "GET",
      `/graphs?tenant=${encodeURIComponent(tenant)}&workspace=${encodeURIComponent(workspace)}`,
    ),
  loadGraph: (token: string, tenant: string, workspace: string, id: string) =>
    request<Graph>(
      token,
      "GET",
      `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}`,
    ),
  saveGraph: (token: string, g: Graph) =>
    request<{
      commit: string;
      graph_id: string;
      lint?: LintIssue[];
    }>(
      token,
      "PUT",
      `/graphs/${encodeURIComponent(g.tenant)}/${encodeURIComponent(g.workspace)}/${encodeURIComponent(g.id)}`,
      g,
    ),
  runGraph: (token: string, tenant: string, workspace: string, id: string) =>
    request<{ job_id: string }>(
      token,
      "POST",
      `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/run`,
    ),
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
      `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/test-trigger`,
      sample,
    ),
  // validateCron asks the daemon to parse a 5-field cron expression
  // using the SAME parser the scheduler uses, and returns the next
  // few fire times when it's valid. UI uses this to surface "bad
  // cron silently never fires" issues at save-time instead of after
  // the user wonders why nothing ran.
  validateCron: (token: string, expr: string) =>
    request<{ valid: boolean; error?: string; next_fires?: string[] }>(
      token,
      "POST",
      "/validate/cron",
      { expr },
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
      `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/nodes/${encodeURIComponent(nodeID)}/sample`,
    ),
  cancelRun: (token: string, runID: string, reason?: string) =>
    request<{ status: string }>(
      token,
      "POST",
      `/runs/${encodeURIComponent(runID)}/cancel`,
      reason ? { reason } : {},
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
      `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/runs?${qs.toString()}`,
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
      `/runs?${qs.toString()}`,
    );
  },
  getJob: (token: string, jobID: string) =>
    request<JobRecord>(token, "GET", `/jobs/${encodeURIComponent(jobID)}`),
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
  getNodeRecord: (token: string, runID: string, nodeID: string) =>
    request<JobRecord>(
      token,
      "GET",
      `/jobs/${encodeURIComponent(runID)}/nodes/${encodeURIComponent(nodeID)}`,
    ),
  // listRunNodes returns every per-node record for a run in one
  // round trip — the run-detail page draws its timeline from this.
  listRunNodes: (token: string, runID: string) =>
    request<{ nodes: JobRecord[] }>(
      token,
      "GET",
      `/jobs/${encodeURIComponent(runID)}/nodes`,
    ),
  // streamChat opens the agentic chat against POST /chat/stream and
  // forwards each SSE event to the caller. messages is the full
  // conversation so far (the server is stateless across requests);
  // signal cancels mid-stream when the user clicks Stop or types a
  // new message.
  streamChat(
    token: string,
    messages: { role: "user" | "assistant"; content: unknown }[],
    onEvent: (kind: string, data: any) => void,
    signal: AbortSignal,
  ): Promise<void> {
    return fetch(API_BASE + "/chat/stream", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ messages }),
      signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
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
  // SSE: EventSource doesn't support headers, so we proxy through fetch
  // with ReadableStream parsing instead. Caller cancels via AbortController.
  streamJob(
    token: string,
    jobID: string,
    onEvent: (kind: string, data: unknown) => void,
    signal: AbortSignal,
  ): Promise<void> {
    return fetch(API_BASE + `/jobs/${encodeURIComponent(jobID)}/events`, {
      method: "GET",
      headers: { Authorization: `Bearer ${token}` },
      signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
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
      })),
    };
  },
  // oauthAuthorizeUrl builds the URL the browser navigates to in order
  // to start an OAuth consent flow. It is NOT fetched — the daemon
  // 302s to the provider, so the caller does window.location.assign().
  // Auth rides on the session cookie (same-origin); the daemon bounces
  // the user back to `returnTo` with ?oauth=success|error appended.
  oauthAuthorizeUrl: (provider: string, returnTo: string, account?: string) => {
    const qs = new URLSearchParams({ return_to: returnTo });
    if (account) qs.set("account", account);
    return `${API_BASE}/oauth/${encodeURIComponent(provider)}/authorize?${qs.toString()}`;
  },
  // listSecrets returns the NAMES of the tenant's stored credentials —
  // never the values (the daemon has no read-back endpoint by design).
  listSecrets: (token: string) =>
    request<{ secrets: string[] }>(token, "GET", "/secrets"),
  // putSecret creates or replaces a credential. Idempotent; 204 on
  // success. Value is write-only from here on.
  putSecret: (token: string, name: string, value: string) =>
    request<void>(token, "PUT", `/secrets/${encodeURIComponent(name)}`, {
      value,
    }),
  deleteSecret: (token: string, name: string) =>
    request<void>(token, "DELETE", `/secrets/${encodeURIComponent(name)}`),

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
    request<{ signup_enabled: boolean }>(null, "GET", "/auth/config"),

  getOrgProfile: (token: string) =>
    request<OrgProfile>(token, "GET", "/admin/org/profile"),
  putOrgProfile: (token: string, display_name: string) =>
    request<OrgProfile>(token, "PUT", "/admin/org/profile", { display_name }),
};
