// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


type Position = { x: number; y: number };

export type Node = {
  id: string;
  module: string;
  params: Record<string, unknown>;
  env?: Record<string, string>;
  label?: string;
  position?: Position;
  timeout_seconds?: number;
  breakpoint?: boolean;
  disabled?: boolean;
  continue_on_error?: boolean;
  collapsed?: boolean;
  locked?: boolean;
};

export type Edge = {
  from: string;
  from_port: string;
  to: string;
  to_port: string;
  on_error?: string;
  waypoints?: { x: number; y: number }[];
};

export type GraphTrigger = {
  type: string;
  cron?: string;
  // IANA timezone the cron expression is read in. Empty means UTC.
  tz?: string;
  secrets?: string[];
  public?: boolean;
  form_fields?: string[];
  form_title?: string;
};

export type Visibility = "org" | "private";

export type TemplateSummary = {
  id: string;
  title: string;
  use_case?: string;
  category?: string;
  description: string;
  icon?: string;
  tags?: string[];
  graph_file: string;
  integrations?: string[];
  no_setup?: boolean;
};

export type FlowSummary = {
  id: string;
  name?: string;
  icon?: string;
  description?: string;
  owner?: string;
  visibility?: Visibility;
  run_status?: "live" | "manual" | "paused" | "needs_publish";
  published?: boolean;
};

export type DropAdjacency = {
  from: string;
  from_port: string;
  to: string;
  to_port: string;
  flows: number;
  edges: number;
};

type Frame = {
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
  language?: string;
  disabled?: boolean;
};

type FailureNotify = {
  webhook?: string;
  email?: string;
};

export type Port = {
  port: string;
  label?: string;
  variadic?: boolean;
  // An absent max means the server-side default ceiling, not unlimited.
  min?: number;
  max?: number;
  mime?: string[];
  required?: boolean;
  list?: boolean;
  // Illustrative payload, never translated.
  example?: unknown;
  // Takes a VALUE and cannot take a file reference.
  inline_only?: boolean;
};

export type ConnectionRequirement = {
  kind: "oauth" | "secret";
  name: string;
  note?: string;
};

export type ConnectionField = {
  key: string;
  label: string;
  secret?: boolean;
  required?: boolean;
  placeholder?: string;
  help?: string;
  options?: string[];
};

export type Manifest = {
  id: string;
  version: string;
  label: string;
  subtitle?: string;
  color?: string;
  icon?: string;
  brand_logo?: string;
  category?: string;
  provider?: string;
  integration?: string;
  integration_description?: string;
  tags?: string[];
  description?: string;
  inputs?: Port[];
  outputs?: Port[];
  params_schema?: JSONSchema;
  idempotent?: boolean;
  // "never" or "exponential_backoff".
  retry_policy?: string;
  node_state?: { label: string; reset_hint?: string };
  awaits_approval?: boolean;
  submits_child_graph?: boolean;
  dynamic_ports?: boolean;
  requires_connections?: ConnectionRequirement[];
  connection_fields?: ConnectionField[];
  connection_verifiable?: boolean;
  disabled?: boolean;
  unavailable?: boolean;
};

export type JSONSchema = {
  type?:
    "string" | "integer" | "number" | "boolean" | "object" | "array" | "null";
  title?: string;
  description?: string;
  default?: unknown;
  examples?: unknown[];
  enum?: unknown[];
  // Parallel to `enum`, same order and length.
  enumNames?: string[];
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  format?: string;
  minimum?: number;
  maximum?: number;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  additionalProperties?: boolean | JSONSchema;
  items?: JSONSchema;
  minItems?: number;
  maxItems?: number;
  oneOf?: JSONSchema[];
  x_advanced?: boolean;
  "x-advanced"?: boolean;
  x_visible_when?: Record<string, unknown>;
  x_mono?: boolean;
  x_cel?: boolean;
  x_key_placeholder?: string;
  x_value_placeholder?: string;
  // Confirm before deleting a row.
  x_confirm_remove?: boolean;
  x_lang_param?: string;
  // A fixed language for a script box, when the step runs only one.
  x_lang?: string;
  x_columns_source?: "collection";
};

export type Permission =
  | "graph:run"
  | "graph:edit"
  | "graph:admin"
  | "module:register"
  | "secret:read"
  | "secret:write"
  | "organization:admin"
  | "platform:admin"
  | "support:agent";

export type ServiceInfo = {
  service: string;
  version: string; // API contract version
  build: {
    version: string; // daemon release ("dev" on an unstamped build)
    commit: string;
    date: string;
  };
};

export type VersionStatus = {
  current: string; // running release ("dev" on an unstamped build)
  commit: string;
  date: string;
  latest?: string; // newest upstream tag; absent if the check couldn't run
  update_available: boolean;
  upgrade_command: string; // CLI hint, e.g. "make upgrade"
  check_error?: string; // set (non-fatal) when the upstream check failed
};

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

export type ResourceDef = {
  name: string;
  type: string;
  config: Record<string, unknown>;
};

export type EmailTemplateSummary = {
  id: string;
  name: string;
  html: string;
  builtin: boolean;
  readOnly: boolean;
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
  public_base_url?: string;
  email_verified?: boolean;
  verification_pending?: boolean;
  support_contact?: string;
  support_tickets_enabled?: boolean;
  memberships?: OrgMembership[];
};

type OrgMembership = {
  tenant: string;
  display_name?: string;
  icon?: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  home: boolean;
};

export type OrgProfile = {
  tenant: string;
  display_name: string;
  icon?: string;
  subdomain?: string;
  wildcard_domain?: string;
  updated_at?: string;
};

export type InvitationDetails = {
  email: string;
  tenant: string;
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

export type SignupInviteSummary = {
  token: string;
  email: string;
  invited_by?: string;
  created_at?: string;
  expires_at: string;
  accepted_at?: string | null;
  revoked_at?: string | null;
  pending?: boolean;
  signup_url: string;
  email_sent?: boolean;
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
  // The column order, carried by the value itself rather than a parallel port.
  headers?: string[];
};

export type ShareLink = {
  token: string;
  url: string;
  created_at: string;
  created_by?: string;
};

export type CollectionShareLink = {
  collection: string;
  token: string;
  url: string;
  created_at: string;
  created_by?: string;
};

export type PublicCollectionData = {
  label?: string;
  icon?: string;
  collection: string;
  generated_at: string;
  columns: string[];
  rows: Record<string, unknown>[];
  total: number;
  offset: number;
};

export type PublicOverview = {
  label?: string;
  icon?: string;
  generated_at: string;
  stats: {
    runs_today: number;
    success_rate?: number; // absent until there's a finished run
    failed: number;
    running: number;
    live_flows: number;
    total_flows: number;
  };
  flows: PublicFlowState[];
};

type PublicFlowState = {
  name: string;
  icon?: string;
  run_status?: "live" | "manual" | "paused" | "needs_publish";
  last_status?: JobStatus;
  last_run_at?: string;
  next_run_at?: string;
  history?: JobStatus[];
};

export type LintIssue = {
  code: string;
  severity: "warn" | "error";
  message: string;
  node_ids?: string[];
  fields?: string[];
  values?: Record<string, string>;
};

export type Revision = {
  commit: string;
  author: string;
  message: string;
  when: string;
  autosave: boolean;
  label?: string;
};

type JobError = {
  code: string;
  message: string;
  details?: string;
};

type JobResult = {
  job_id?: string;
  status?: string;
  output?: Record<string, Ref>;
  error?: JobError;
  // Why a skipped step was skipped (core.SkipCode* in Go). Its own field
  // because a skip is not a failure — see skipReason.ts.
  skip_code?: string;
};

export type RunLogEntry = {
  seq: number;
  run_id: string;
  ts: string;
  node_id?: string;
  kind: "progress" | "status" | "terminal" | "truncated";
  stream?: string;
  message: string;
};

export type JobRecord = {
  ID: string;
  Kind: string;
  GraphRunID: string;
  GraphID: string;
  NodeID: string;
  Status: JobStatus;
  Result?: JobResult;
  EnqueuedAt?: string | null;
  StartedAt?: string | null;
  FinishedAt?: string | null;
  Attempt?: number;
  WillRetry?: boolean;
  RetryAt?: string | null;
  Job?: {
    Input?: Record<string, Ref>;
    Params?: Record<string, unknown>;
  };
};

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
  will_retry?: boolean;
  retry_at?: string | null;
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
  max_graph_edges?: number;
  max_graph_timeout_seconds: number;
};

export type PendingApproval = {
  run_id: string;
  graph_id: string;
  node_id: string;
  prompt?: string;
  context?: unknown;
  context_too_large?: boolean;
  context_order?: string[];
  url?: string;
  since: string;
  workspace: string;
};

export type DecidedApproval = {
  run_id: string;
  graph_id: string;
  node_id: string;
  prompt?: string;
  decision: "approve" | "reject" | "cancelled";
  approver?: string;
  comment?: string;
  reason?: string;
  context?: unknown;
  context_too_large?: boolean;
  context_order?: string[];
  decided_at: string;
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

export type ScheduleEntry = {
  flow_id: string;
  graph_id: string;
  flow_name?: string;
  icon?: string;
  node_id: string;
  kind: "cron" | "poll";
  cron?: string;
  tz?: string;
  interval_seconds?: number;
  disabled: boolean;
  flow_disabled: boolean;
  next_fires?: string[];
};

export type PublishInfo = {
  published: boolean;
  published_commit?: string;
  head_commit?: string;
  dirty: boolean;
};

export type Runner = {
  name: string;
  labels?: string[];
  version?: string;
  online: boolean;
  last_seen?: string;
  created_by?: string;
  created_at: string;
};

export type MCPServer = {
  name: string;
  label: string;
  url: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header?: string;
  has_token: boolean;
  enabled: boolean;
  connected: boolean;
  tool_ids?: string[];
  instructions?: string;
  tool_count: number;
  last_error?: string;
  last_connected?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
};

type StepSourceUse = {
  workspace: string;
  flow_id: string;
  name?: string;
  steps: string[];
  published: boolean;
};

export type StepSourceUsage = {
  flows: StepSourceUse[];
  hidden: number;
};

export type MCPServerInput = {
  label: string;
  name?: string;
  url: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header?: string;
  token?: string;
  enabled?: boolean;
};

export type WebAPIOperation = {
  id: string;
  title?: string;
  method: "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE";
  // {placeholders} must each have a required path argument of the same name.
  path: string;
  summary?: string;
  description?: string;
  args?: WebAPIArg[];
  body_mode?: "none" | "json" | "raw";
  deprecated?: boolean;
};

export type WebAPIArg = {
  name: string;
  in: "path" | "query" | "header" | "body";
  type?: string;
  required?: boolean;
  label?: string;
  description?: string;
};

export type WebAPI = {
  name: string;
  label: string;
  description?: string;
  base_url: string;
  integration?: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header?: string;
  operations: WebAPIOperation[];
  timeout_ms?: number;
  max_body_bytes?: number;
  enabled: boolean;
  logo?: string;
  logo_mode: WebAPILogoMode;
  // Non-empty means the calls are made from one of the org's own machines, which
  // bypasses the daemon's SSRF guard and egress allowlist.
  runner_tags: string[];
  spec_url?: string;
  registered: boolean;
  step_ids?: string[];
  last_error?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
};

export type WebAPILogoMode = "auto" | "custom" | "none";

export type WebAPIInput = {
  label: string;
  description?: string;
  name?: string;
  base_url: string;
  integration?: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header?: string;
  operations: WebAPIOperation[];
  timeout_ms?: number;
  max_body_bytes?: number;
  enabled: boolean;
  logo_mode?: WebAPILogoMode;
  logo?: string;
  runner_tags?: string[];
  spec_url?: string;
};

export type WebAPISpecRequest = {
  url?: string;
  spec?: string;
  against?: string;
};

type WebAPIImportWarning = {
  where?: string;
  reason: string;
};

type WebAPIOperationChange =
  "added" | "changed" | "removed" | "unchanged";

type WebAPIOperationDiff = {
  id: string;
  change: WebAPIOperationChange;
  title?: string;
  method?: string;
  path?: string;
  step_id?: string;
};

export type WebAPIRefreshDiff = {
  operations: WebAPIOperationDiff[];
  added: number;
  changed: number;
  removed: number;
  unchanged: number;
};

export type WebAPISpecResponse = {
  title?: string;
  description?: string;
  base_url?: string;
  operations: WebAPIOperation[];
  tags?: string[];
  operation_tags?: Record<string, string[]>;
  warnings?: WebAPIImportWarning[];
  diff?: WebAPIRefreshDiff;
  overflow?: boolean;
  max: number;
};

export type RunnerTarget = {
  name: string;
  tags?: string[];
  online: boolean;
};

export type RunnerToken = {
  token: string;
  expires_at: string;
  name?: string;
};

export type GitCredential = {
  account: string;
  has_ssh_key: boolean;
  has_passphrase: boolean;
  has_known_hosts: boolean;
  has_token: boolean;
  username?: string;
};

export type GitMirror = {
  configured: boolean;
  remote_url?: string;
  account?: string;
  enabled: boolean;
  push_on?: "publish" | "save";
  updated_at?: string;
  updated_by?: string;
  last_attempt_at?: string;
  last_success_at?: string;
  last_commit?: string;
  last_error?: string;
};

export type MirrorPushResult = {
  pushed: number;
  deleted: number;
  changed: boolean;
  commit: string;
};

export type OAuthProviderStatus = {
  name: string;
  accounts: string[];
  stale_accounts?: string[];
  needs_reconnect?: string[];
};

type GoogleAccount = {
  account: string;
  coverage: Record<string, boolean>;
  scopes: string[];
};

export type GoogleAccountsResponse = {
  provider: string;
  services: string[];
  accounts: GoogleAccount[];
};

export type AdminOAuthProvider = {
  name: string;
  display_name: string;
  authorize_url: string;
  scopes: string[];
  setup_help: string;
  redirect_uri: string;
  configured: boolean;
  client_id?: string;
  has_persisted: boolean;
  has_env: boolean;
  updated_at?: string;
};

export type SecretManagerStatus = {
  configured: boolean;
  address?: string;
  namespace?: string;
  mount?: string;
  auth_method?: "token" | "approle";
};

export type SecretManagerConfig = {
  address: string;
  mount: string;
  namespace?: string;
  auth:
    | { method: "token"; token: string }
    | { method: "approle"; role_id: string; secret_id: string };
};

export type UsageCounters = {
  period: string; // "2026-06"
  graph_runs: number;
  node_executions: number;
  skipped_runs: number;
};

export type BillingInfo = {
  plan: "free" | "pro";
  subscription_status?: string;
  cancel_at_period_end?: boolean;
  current_period_end?: string;
  free_runs_per_month: number;
  runs_this_month: number;
  billing_enabled: boolean;
  polling_allowed: boolean;
  can_upgrade: boolean;
  can_manage: boolean;
};

export type AwsSecretManagerStatus = {
  configured: boolean;
  region?: string;
  access_key_id?: string;
  endpoint?: string;
};
export type AwsSecretManagerConfig = {
  region: string;
  access_key_id: string;
  secret_access_key: string;
  endpoint?: string;
};

export type GcpSecretManagerStatus = {
  configured: boolean;
  project_id?: string;
  client_email?: string;
  endpoint?: string;
};
export type GcpSecretManagerConfig = {
  project_id: string;
  service_account_key: string;
  endpoint?: string;
};

export type FileEntry = {
  name: string;
  path: string; // workspace-relative path to this entry
  is_dir: boolean;
  size: number;
  mod_time: string; // RFC3339
};

export type GrantStatus =
  "requested" | "approved" | "denied" | "revoked" | "expired";

export type AccessGrant = {
  id: string;
  ticket_id: string;
  tenant: string;
  flow_id: string;
  agent_subject: string;
  status: GrantStatus;
  requested_at: string;
  requested_by: string;
  decided_by?: string;
  decided_at?: string;
  expires_at: string;
  revoked_at?: string;
  revoked_by?: string;
};

export type TicketStatus =
  "open" | "awaiting_user" | "awaiting_support" | "resolved" | "closed";

export type Ticket = {
  id: string;
  tenant: string;
  workspace: string;
  created_by: string;
  subject: string;
  status: TicketStatus;
  flow_id?: string;
  run_id?: string;
  bundle_id?: string;
  assigned_to?: string;
  created_at: string;
  updated_at: string;
  user_read_at?: string;
  support_read_at?: string;
};

type TicketAuthorKind = "user" | "support" | "system";

export type TicketMessage = {
  id: string;
  ticket_id: string;
  author?: string;
  author_kind: TicketAuthorKind;
  body: string;
  system_code?: string;
  bundle_id?: string;
  created_at: string;
};

export type TicketView = {
  ticket: Ticket;
  messages: TicketMessage[];
};

type TicketQueueSummary = {
  by_status: Partial<Record<TicketStatus, number>>;
  total: number;
  open: number;
  unassigned: number;
  by_assignee: Record<string, number>;
};

export type TicketQueueSummaryResponse = {
  summary: TicketQueueSummary;
  mine: number;
};

export type TicketQueueFilter = "all" | "unassigned" | "mine";

export type SupportAgentGrant = {
  email: string;
  granted_by: string;
  created_at: string;
};

type RedactMode = "" | "structure_only" | "structure_plus_values";

export type SupportBundle = {
  mode: RedactMode;
  flow: BundleFlow;
  nodes: BundleNode[] | null;
  edges: Edge[] | null;
  triggers?: BundleTrigger[];
  run?: BundleRun;
  issues?: LintIssue[];
};

type BundleFlow = {
  id: string;
  tenant: string;
  workspace: string;
  name?: string;
  icon?: string;
  description?: string;
  visibility?: Visibility;
  owner?: string;
  disabled?: boolean;
  timeout_seconds?: number;
  notifies_on_failure?: boolean;
};

type BundleNode = {
  id: string;
  module: string;
  disabled?: boolean;
  breakpoint?: boolean;
  timeout_seconds?: number;
  position?: Position;
  params?: Record<string, unknown>;
  env?: Record<string, unknown>;
};

type BundleTrigger = {
  type: string;
  cron?: string;
  tz?: string;
  interval_seconds?: number;
  form_fields?: string[];
  form_title?: string;
  has_secret?: boolean;
};

type BundleRun = {
  run_id: string;
  status: JobStatus;
  error?: JobError;
  enqueued_at?: string;
  started_at?: string;
  finished_at?: string;
  nodes?: BundleNodeRun[];
};

type BundleNodeRun = {
  node_id: string;
  status: JobStatus;
  error?: JobError;
  attempt?: number;
  started_at?: string;
  finished_at?: string;
  output?: Record<string, BundleRef>;
};

type BundleRef = {
  mime?: string;
  has_value?: boolean;
  shape?: string;
  header_count?: number;
  headers?: string[];
};
