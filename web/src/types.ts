// Mirror of the Go core types we touch from the UI.

export type Position = { x: number; y: number };

export type Node = {
  id: string;
  module: string;
  params: Record<string, unknown>;
  env?: Record<string, string>;
  position?: Position;
  timeout_seconds?: number;
  // Pause the run after this node completes (debugging) — see #12.
  breakpoint?: boolean;
};

export type Edge = {
  from: string;
  from_port: string;
  to: string;
  to_port: string;
  on_error?: string;
  // Editor-only routing knots the wire is drawn through (flow coords).
  // The engine ignores these; they round-trip to preserve hand-routing.
  waypoints?: { x: number; y: number }[];
};

export type GraphTrigger = {
  type: string;
  cron?: string;
  // tz is the IANA timezone (e.g. "Europe/Stockholm") a cron expression
  // is interpreted in. The editor stamps the browser's timezone here so
  // "every day at 09:00" fires at 09:00 on the user's own clock (and
  // survives DST), independent of where the daemon runs. Empty = UTC.
  tz?: string;
  secret?: string;
  // public_form opts a webhook trigger into a hosted intake form at
  // /form/<tenant>/<workspace>/<id> that visitors submit without any
  // bearer token. form_fields names the fields (defaults to
  // name/email/message); form_title overrides the page heading.
  public_form?: boolean;
  form_fields?: string[];
  form_title?: string;
};

export type Visibility = "org" | "private";

// TemplateSummary is one row in the gallery's index file. Each entry
// points at its own graph file under /templates/<id>.json so the
// gallery page loads fast (only metadata) and the graph payload is
// fetched lazily on "Use this template".
export type TemplateSummary = {
  id: string;
  title: string;
  // use_case is the plain-language, customer-facing one-liner shown as
  // the card's primary copy ("Get a Slack message when someone fills in
  // your form"). description is the original technical summary, no longer
  // surfaced on the card. Older index entries without use_case fall back
  // to description.
  use_case?: string;
  // category groups cards under a heading on the gallery page
  // ("Get notified", "Scheduled reports", …). Ungrouped entries land
  // under a catch-all bucket.
  category?: string;
  description: string;
  icon?: string;
  tags?: string[];
  graph_file: string;
  // integrations is the list of brand slugs whose mini logos render
  // on the card (e.g. ["gmail", "slack"]). Maps 1:1 to the files in
  // web/public/brands/<slug>.svg — so adding a connector with a brand
  // asset automatically makes it usable here.
  integrations?: string[];
  // no_setup flags templates that need no external accounts or
  // credentials to run — the trial-friendliest cards. The gallery
  // renders a small "No setup needed" badge so a brand-new buyer
  // can spot the templates they can fork on day one without
  // contacting their admin.
  no_setup?: boolean;
};

export type FlowSummary = {
  id: string;
  name?: string;
  icon?: string;
  description?: string;
  owner?: string;
  visibility?: Visibility;
  // run_status ("live" | "manual" | "paused") is computed server-side by
  // core.FlowRunStatusOf so the list can show the status chip without
  // fetching each full graph. Optional: older daemons omit it.
  run_status?: "live" | "manual" | "paused";
};

// Frame is an editor-only comment box grouping nodes visually. The engine
// ignores it; it round-trips so the editor can redraw the same boxes.
export type Frame = {
  id: string;
  title?: string;
  color?: string;
  x: number;
  y: number;
  width: number;
  height: number;
};

export type Graph = {
  id: string;
  version?: string;
  tenant: string;
  workspace: string;
  nodes: Node[];
  edges: Edge[];
  triggers?: GraphTrigger[];
  frames?: Frame[];
  visibility?: Visibility;
  owner?: string;
  name?: string;
  icon?: string;
  description?: string;
  timeout_seconds?: number;
  failure_notify?: FailureNotify;
  // disabled pauses all automatic firing (scheduler + webhook/form) without
  // deleting the flow — the dev "off switch". Manual Run still works.
  disabled?: boolean;
};

// FailureNotify mirrors core.FailureNotify in Go — the daemon's
// failure-notify dispatcher POSTs a payload to the webhook URL
// when a run of this graph terminates with status=failed.
export type FailureNotify = {
  webhook?: string;
};

export type Port = {
  port: string;
  label?: string;
  variadic?: boolean;
  mime?: string[];
  required?: boolean;
};

// ConnectionRequirement is one credential a drop needs to run, surfaced
// in the catalog (GET /drops) as requires_connections. kind is "oauth"
// (authorize via a provider — the Connections "Connect" flow) or
// "secret" (a value the user stores once and flows reference as
// ${secret.NAME}). `note` is the human label to show ("Anthropic API
// key (sk-ant-…)"); `name` is the provider id (oauth) or secret name
// (secret).
export type ConnectionRequirement = {
  kind: "oauth" | "secret";
  name: string;
  note?: string;
};

// ConnectionField is one input of a multi-field service connection
// (ntfy's server+token, SMTP's host/port/user/pass) — set once per
// tenant on the integration page and injected into a node's unset
// params at run time. `secret` masks the value; `key` is the param the
// drop reads. Stored as the tenant secret conn/<slug>/<key>.
export type ConnectionField = {
  key: string;
  label: string;
  secret?: boolean;
  required?: boolean;
  placeholder?: string;
};

export type Manifest = {
  id: string;
  version: string;
  label: string;
  color?: string;
  icon?: string;
  // brand_logo, when set, is the asset path (or URL) of a vendor logo
  // (e.g. "/brands/excel.svg") that the catalog and node card prefer
  // over `icon`. Set on the Go side via core.Manifest.BrandLogo.
  brand_logo?: string;
  category?: string;
  provider?: string;
  integration?: string;
  tags?: string[];
  description?: string;
  inputs?: Port[];
  outputs?: Port[];
  params_schema?: JSONSchema;
  idempotent?: boolean;
  awaits_approval?: boolean;
  submits_child_graph?: boolean;
  // requires_connections lists the credentials a drop needs before it
  // can run — drives the per-integration "Connection" configure widget.
  requires_connections?: ConnectionRequirement[];
  // connection_fields declares a multi-field service connection (endpoint
  // + credentials) configured once per tenant — drives the multi-field
  // connection card.
  connection_fields?: ConnectionField[];
};

// JSONSchema is a relaxed subset of the JSON Schema spec — enough to
// drive the inspector form for our built-in modules. Unknown features
// fall through to a raw JSON textarea.
export type JSONSchema = {
  type?: "string" | "integer" | "number" | "boolean" | "object" | "array" | "null";
  title?: string;
  description?: string;
  default?: unknown;
  // examples drives a plain-language "Example: …" hint under the field.
  // Optional; manifests that supply one give non-technical users a
  // concrete sample to copy instead of guessing the format.
  examples?: unknown[];
  enum?: unknown[];
  // enumNames supplies human-readable labels parallel to `enum` (same order
  // and length). The select shows enumNames[i] but stores enum[i]. A common
  // JSON Schema extension (react-jsonschema-form uses it too).
  enumNames?: string[];
  // string
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  // format is a JSON Schema annotation we use for UI dispatch. Recognized
  // today: "workspace-path" — renders an upload widget that puts the
  // dropped file into the active workspace and stores its sandbox path.
  format?: string;
  // number / integer
  minimum?: number;
  maximum?: number;
  // object
  properties?: Record<string, JSONSchema>;
  required?: string[];
  additionalProperties?: boolean | JSONSchema;
  // array
  items?: JSONSchema;
  minItems?: number;
  maxItems?: number;
  // composition
  oneOf?: JSONSchema[];
  // x_advanced flags a field as developer-flavored — timeouts,
  // raw-token bypasses, pagination cursors, etc. The Inspector's
  // form hides advanced fields by default and reveals them via a
  // "Show advanced" toggle. Named with an underscore so the
  // TypeScript type stays valid (JSON Schema's `x-` extension
  // convention is unrepresentable as a TS key); manifests emit
  // either spelling and the UI accepts both.
  x_advanced?: boolean;
  "x-advanced"?: boolean;
};

export type Permission =
  | "graph:run"
  | "graph:edit"
  | "graph:admin"
  | "module:register"
  | "secret:read"
  | "secret:write"
  | "organization:admin"
  | "platform:admin";

// ServiceInfo mirrors the GET /api/v1 ServiceDescriptor — the public
// discovery entry point. The UI only reads the build block (shown in the
// account-menu footer); the rest of the descriptor is for API clients.
export type ServiceInfo = {
  service: string;
  version: string; // API contract version
  build: {
    version: string; // daemon release ("dev" on an unstamped build)
    commit: string;
    date: string;
  };
};

// VersionStatus mirrors GET /api/v1/admin/version (platform-admin only).
// Powers the System section of the admin page: the running build paired
// with the newest upstream release tag, so the UI can show "up to date" vs
// "update available" and surface the CLI command to upgrade.
export type VersionStatus = {
  current: string; // running release ("dev" on an unstamped build)
  commit: string;
  date: string;
  latest?: string; // newest upstream tag; absent if the check couldn't run
  update_available: boolean;
  upgrade_command: string; // CLI hint, e.g. "make upgrade"
  check_error?: string; // set (non-fatal) when the upstream check failed
};

// ReferenceItem is one insertable ${…} token the reference picker offers,
// from GET /me/flows/{flow_id}/references. Kind-specific fields are
// optional; `token` is what gets inserted, `label` (and the per-kind
// fields) drive how it's described in the picker.
export type ReferenceItem = {
  token: string;
  label?: string;
  name?: string; // secrets, resources
  scope?: string; // secrets: flow|tenant
  node_id?: string; // upstream
  node_label?: string; // upstream
  port?: string; // upstream
  field?: string; // trigger
};

// ResourceDef mirrors a flow/org-scoped resource definition — a named
// pointer at external content (e.g. a Google Sheet) referenced in params
// as ${resource.NAME}. config is type-specific (for google_sheet:
// spreadsheet_id, range, account). Unlike a secret, config is not
// sensitive and round-trips through the API.
export type ResourceDef = {
  name: string;
  type: string;
  config: Record<string, unknown>;
};

export type ReferenceGroups = {
  secrets: ReferenceItem[];
  upstream: ReferenceItem[];
  trigger: ReferenceItem[];
  resources: ReferenceItem[];
};

export type WhoAmI = {
  subject: string;
  tenant: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  permissions: Permission[];
  // public_base_url is the daemon's externally-reachable origin (from
  // --public-base-url). Used to build correct webhook/hosted-form URLs.
  // Empty when unset — the UI falls back to a localhost hint.
  public_base_url?: string;
  // support_contact is an operator-set email or URL (e.g.
  // "support@acme.com" or "https://acme.com/help") surfaced on UI
  // surfaces that depend on server-side setup the end user can't fix
  // themselves — notably the Connections page when OAuth/secret-store
  // are off. Empty = render a generic "contact your administrator"
  // message with no link.
  support_contact?: string;
  // memberships is every org the signed-in user can act in: their home
  // org (home=true) plus any they've been invited to. Drives the org
  // switcher in the app shell.
  memberships?: OrgMembership[];
};

export type OrgMembership = {
  tenant: string;
  // display_name is the org's human-facing name (set on /admin/workspace,
  // defaulted from the email domain on signup). Empty when no profile
  // has been saved — the UI falls back to `tenant` in that case.
  display_name?: string;
  // icon is the org's logo (data: URL or icon name); drives the org
  // glyph in the tenant switcher.
  icon?: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  home: boolean;
};

export type OrgProfile = {
  tenant: string;
  display_name: string;
  // icon is an optional org logo — a data: URL (uploaded SVG/PNG) or a
  // logical icon name. Rendered as an image when it looks like one.
  icon?: string;
  updated_at?: string;
};

export type InvitationDetails = {
  email: string;
  tenant: string;
  // tenant_display is the org's display name when set, empty otherwise.
  // Lets the unauthenticated invite landing say "join Acme" instead of
  // exposing the raw usr_<hex> ID.
  tenant_display?: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  invited_by: string;
  expires_at: string;
  pending: boolean;
  accepted: boolean;
  revoked: boolean;
  expired: boolean;
};

export type InvitationSummary = {
  token: string;
  email: string;
  tenant: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  invited_by: string;
  created_at: string;
  expires_at: string;
  accepted_at?: string | null;
  revoked_at?: string | null;
  pending: boolean;
  accept_url: string;
};

export type MemberSummary = {
  email: string;
  tenant: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  invited_by?: string;
  created_at: string;
  home: boolean;
};

export type OrgAuthConfig = {
  tenant: string;
  google_enabled: boolean;
  google_client_id: string;
  google_workspace_domain: string;
  google_secret_set?: boolean;
  updated_at?: string;
};

export type JobStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "skipped"
  | "awaiting";

export type Ref = {
  mime?: string;
  ref?: string;
  data?: unknown; // serialized as Inline in Go
};

// Mirrors core.LintIssue. severity is "warn" or "error"; the UI
// treats both as non-blocking and surfaces them in a banner after
// save — even "error" findings don't reject persistence today, since
// the lint is heuristic. Tightening some rules to blocking is a
// future call.
export type LintIssue = {
  code: string;
  severity: "warn" | "error";
  message: string;
  node_ids?: string[];
};

// Revision is one entry in a flow's commit history (GET /me/flows/{id}/history).
// autosave distinguishes coalesced editor autosaves from explicit/restore
// checkpoints so the history panel can label them.
export type Revision = {
  commit: string;
  author: string;
  message: string;
  when: string;
  autosave: boolean;
};

export type JobError = {
  code: string;
  message: string;
  // Optional technical context (type signatures, library errors, etc.)
  // that helps a developer debug but would confuse a non-technical
  // user reading `message`. UI surfaces should hide it behind a
  // "Details" expander.
  details?: string;
};

export type JobResult = {
  job_id?: string;
  status?: string;
  output?: Record<string, Ref>;
  error?: JobError;
};

export type JobRecord = {
  ID: string;
  Kind: string;
  GraphRunID: string;
  GraphID: string;
  NodeID: string;
  Status: JobStatus;
  Result?: JobResult;
  StartedAt?: string | null;
  FinishedAt?: string | null;
  Attempt?: number;
  // Job is the per-node-record dispatch payload. Its `Input` map
  // carries the resolved input refs the worker passed to Execute —
  // the upstream values that flowed INTO this node. RunDetail uses
  // this to render an "Inputs" section alongside outputs.
  Job?: {
    Input?: Record<string, Ref>;
    Params?: Record<string, unknown>;
  };
};

// RunView / NodeRunView are the clean wire shapes the public API actually
// returns from GET /me/runs/{id} and /me/runs/{id}/nodes (and the SSE
// snapshot/terminal frames) — snake_case, storage-detail-free. The api
// layer maps these to the JobRecord shape the run-detail components
// consume; see api.ts (runViewToRecord / nodeViewToRecord).
export type RunView = {
  id: string;
  flow_id: string;
  graph_id?: string;
  status: JobStatus;
  enqueued_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  duration_ms?: number;
  error?: JobError;
};

export type NodeRunView = {
  node_id: string;
  status: JobStatus;
  attempts?: number;
  started_at?: string | null;
  finished_at?: string | null;
  duration_ms?: number;
  inputs?: Record<string, Ref>;
  outputs?: Record<string, Ref>;
  error?: JobError;
};

export type Role = {
  name: string;
  permissions: Permission[];
};

export type APIKeySummary = {
  id: string;
  subject: string;
  tenant: string;
  workspace: string;
  roles: Role[];
  expires_at?: string | null;
  revoked_at?: string | null;
  status: "active" | "expired" | "revoked";
};

export type IssuedAPIKey = APIKeySummary & {
  secret: string;
};

export type UserSummary = {
  subject: string;
  tenant: string;
  active_keys: number;
  revoked_keys: number;
  permissions: Permission[];
  role_names: string[];
  key_ids: string[];
  last_workspace?: string;
};

export type AuditEvent = {
  time: string;
  tenant: string;
  actor: string;
  action: string;
  target: string;
  detail?: string;
};

export type WorkspaceLimits = {
  tenant: string;
  quota?: { used_bytes?: number; limit_bytes: number };
  max_graph_nodes: number;
  max_graph_timeout_seconds: number;
};

export type PendingApproval = {
  run_id: string;
  graph_id: string;
  node_id: string;
  prompt?: string;
  url?: string;
  since: string;
  workspace: string;
};

export type RunSummary = {
  id: string;
  graph_id: string;
  status: JobStatus;
  enqueued_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  error_code?: string;
};

// OAuthProviderStatus is one entry from GET /oauth/providers: a
// registered provider plus the account names the tenant has already
// connected (empty = not connected yet). stale_accounts lists the
// subset of accounts whose stored token scope no longer covers the
// provider's current required scopes — drives the "Reconnect
// required" pill on the Connections page.
export type OAuthProviderStatus = {
  name: string;
  accounts: string[];
  stale_accounts?: string[];
};

// GoogleAccount is one connected Google account from
// GET /api/v1/oauth/google/accounts: the account name plus, per service
// (Gmail / Google Sheets / Google Forms), whether its current OAuth grant
// covers that service. Drives the /admin/google permission matrix.
export type GoogleAccount = {
  account: string;
  coverage: Record<string, boolean>;
  scopes: string[];
};

// GoogleAccountsResponse is the full payload: the service list (column
// headers, sorted) plus one entry per connected account.
export type GoogleAccountsResponse = {
  provider: string;
  services: string[];
  accounts: GoogleAccount[];
};

// AdminOAuthProvider is one row from GET /api/v1/admin/oauth-providers:
// the per-provider control panel state. configured = the registry
// currently has client_id + client_secret for it (either from env
// or persisted via this UI). has_env = configured but no persisted
// row, so saving here will override.
export type AdminOAuthProvider = {
  name: string;
  display_name: string;
  authorize_url: string;
  scopes: string[];
  setup_help: string;
  redirect_uri: string;
  configured: boolean;
  // The configured OAuth client ID (public identifier, not a secret).
  // Present when configured; the secret is never returned.
  client_id?: string;
  has_persisted: boolean;
  has_env: boolean;
  updated_at?: string;
};

// GET /api/v1/secret-manager — the tenant's BYO secret-manager (OpenBao/Vault)
// connection, redacted: never includes the token / secret_id.
export type SecretManagerStatus = {
  configured: boolean;
  address?: string;
  namespace?: string;
  mount?: string;
  auth_method?: "token" | "approle";
};

// PUT /api/v1/secret-manager body — the full connection incl. credentials.
export type SecretManagerConfig = {
  address: string;
  mount: string;
  namespace?: string;
  auth:
    | { method: "token"; token: string }
    | { method: "approle"; role_id: string; secret_id: string };
};
