// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Mirror of the Go core types we touch from the UI.

export type Position = { x: number; y: number };

export type Node = {
  id: string;
  module: string;
  params: Record<string, unknown>;
  env?: Record<string, string>;
  // The step's display name, when the author has renamed it. Absent on a step
  // still called after its drop — the editor falls back to the drop's own
  // (localized) label, so storing the default would freeze it in one language.
  label?: string;
  position?: Position;
  timeout_seconds?: number;
  // Pause the run after this node completes (debugging) — see #12.
  breakpoint?: boolean;
  // Step switched off: skipped at run time, and everything downstream
  // of it is skipped too (setup-time aid).
  disabled?: boolean;
  // Non-critical step: if it fails, the run carries on and finishes its other
  // branches instead of being marked failed. For the "announce it everywhere"
  // shape — Discord being down is no reason for the Slack post not to go out.
  // The step's own dependents are still skipped; there is no output for them.
  continue_on_error?: boolean;
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
  // secrets is the multi-key bearer list (zero-downtime rotation — the
  // /trigger endpoint accepts any listed key).
  secrets?: string[];
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
  // run_status is computed server-side by core.FlowRunStatusOf (see
  // core/flowstatus.go) so the list can show the status chip without
  // fetching each full graph. "needs_publish" = a trigger is configured but
  // not yet published (e.g. a freshly AI-generated scheduled flow), so it
  // won't fire until the user publishes. Optional: older daemons omit it.
  run_status?: "live" | "manual" | "paused" | "needs_publish";
  // published is true once the flow has a published revision. Unpublished
  // flows are drafts (whatever the trigger) and are kept out of the overview's
  // health + attention stats. Optional: older daemons omit it.
  published?: boolean;
};

// DropAdjacency is one directed port-to-port co-occurrence mined from the
// workspace's own flows: the `from_port` output of module `from` was wired
// to the `to_port` input of module `to`. `flows` counts distinct graphs
// containing such an edge (the ranking signal); `edges` is the raw count.
// Feeds the editor's "Suggested" group when dragging off a port.
export type DropAdjacency = {
  from: string;
  from_port: string;
  to: string;
  to_port: string;
  flows: number;
  edges: number;
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
  // language is the flow's OUTPUT language (core.Graph.Language) — the language
  // steps write words in when they emit any, today the Date & time step's day
  // and month names. A property of the flow, not of the viewer: it decides what
  // a scheduled run sends to someone who never opens this UI. Empty = English.
  // Distinct from the UI language in account preferences, which only changes
  // what YOU see.
  language?: string;
  // disabled pauses all automatic firing (scheduler + webhook/form) without
  // deleting the flow — the dev "off switch". Manual Run still works.
  disabled?: boolean;
};

// FailureNotify mirrors core.FailureNotify in Go — the daemon's
// failure-notify dispatcher POSTs a payload to the webhook URL
// when a run of this graph terminates with status=failed.
export type FailureNotify = {
  webhook?: string;
  // Failure summary by email — needs the operator mailer (DAZYFLOW_SMTP_URL).
  email?: string;
};

export type Port = {
  port: string;
  label?: string;
  variadic?: boolean;
  mime?: string[];
  required?: boolean;
  // list marks a port that carries a LIST of records (set centrally on
  // conventionally-named ports). A list output wired into a non-list input is
  // the "you fed a whole list into a one-at-a-time step" tell — see the loop
  // hint in FlowEditor.
  list?: boolean;
  // inline_only marks an input that takes a VALUE and cannot take a file.
  // Set on every input of a tenant runner's drop: the runner is on another
  // machine, and a file reference is a path on the daemon's own disk. The
  // engine refuses such a job before dialling, so surfacing this on the port
  // is what turns a failed run into something visible while editing.
  inline_only?: boolean;
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
  // An EXAMPLE of the value ("smtp.example.com"). Lives inside the input and
  // disappears on the first keystroke, so it can only show the shape.
  placeholder?: string;
  // One line of setup guidance ("Create one in Home Assistant under Profile →
  // Long-Lived Access Tokens"), rendered under the input and still there while
  // the user types — which is when they need it. See core.ConnectionField.
  help?: string;
  // When set, the field is an enum: the UI renders a dropdown of these
  // values (plus a blank "default" choice) instead of a free-text input.
  options?: string[];
};

export type Manifest = {
  id: string;
  version: string;
  label: string;
  // subtitle is an optional short action line shown under the label (e.g.
  // label "Google Sheets", subtitle "Append rows").
  subtitle?: string;
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
  // retry_policy is "never" or "exponential_backoff". The worker refuses to
  // retry a module that declares no policy, so the editor reads it to decide
  // whether an on_error=retry connection would do anything at all.
  retry_policy?: string;
  // node_state, when present, means a node of this drop keeps per-node state
  // across runs (a dedupe cursor, a poll watermark). Drives the "Reset state"
  // context-menu action and the "keeps state" indicator on the node card.
  node_state?: { label: string; reset_hint?: string };
  awaits_approval?: boolean;
  submits_child_graph?: boolean;
  // requires_connections lists the credentials a drop needs before it
  // can run — drives the per-integration "Connection" configure widget.
  requires_connections?: ConnectionRequirement[];
  // connection_fields declares a multi-field service connection (endpoint
  // + credentials) configured once per tenant — drives the multi-field
  // connection card.
  connection_fields?: ConnectionField[];
  // connection_verifiable is set by the daemon when this drop's integration
  // has a live connection check — the Apps page then verifies credentials
  // before saving them and offers a "Test connection" button.
  connection_verifiable?: boolean;
  // disabled is set by the daemon (editor catalog only) when a platform admin
  // has switched this drop off. The editor shows it greyed-out and un-pickable
  // rather than hiding it; it can't be added to a flow.
  disabled?: boolean;
  // unavailable is set at registration when the drop's provider is reachable
  // in principle but not right now — an MCP server whose endpoint is down or
  // whose credential was rotated away.
  //
  // The manifest is still COMPLETE: ports, params schema and icon all come
  // from the last tool list the server was seen publishing. That is the whole
  // point — without it a flow wired into the step renders with a bare in/out
  // pair and looks like it lost its edges. The card shows a "needs connection"
  // banner instead, and the step cannot be picked for new work.
  unavailable?: boolean;
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
  // x_visible_when hides a field until sibling params have particular values:
  // {"format": "custom"} shows it only while the sibling `format` is "custom",
  // and an array of values means any of them. Distinct from x_advanced, which
  // hides a field behind a disclosure whatever the rest of the form says — the
  // wrong shape for a field that becomes THE field to fill in once an option
  // is picked. Read through lib/schemaFields.ts by both form renderers; a
  // hidden field keeps its stored value. A conditional field must not be in
  // `required` (the form would hide what the config checklist demands) — the
  // drop validates the pair in Execute instead, and
  // drops/param_visibility_test.go enforces both ends.
  x_visible_when?: Record<string, unknown>;
  // x_mono renders a multiline field in a monospace (code) font — for fields
  // whose value is code/markup (e.g. render_template's HTML template) rather
  // than prose (an email body), so the markup reads correctly.
  x_mono?: boolean;
  // x_cel marks a field whose value is a CEL formula (the Expression drop, the
  // calculated-column / row tools). It renders a visible footer hint under the
  // input linking to the CEL language docs, so the formula language is named
  // in the form rather than buried in a tooltip.
  x_cel?: boolean;
  // x_key_placeholder / x_value_placeholder are examples shown in the two boxes
  // of a name/value map row. The generic "key" placeholder says nothing about
  // what a particular map is keyed BY, and a map whose meaning lives only in
  // the field's help text reads as two empty boxes — the shape of thing a user
  // gives up on. Set them where the pair is not obvious from the field name
  // (render_table's column headings: a data column → the heading you want).
  x_key_placeholder?: string;
  x_value_placeholder?: string;
  // x_confirm_remove asks before deleting a row of a name/value map. Set where
  // the value costs something to reconstruct — an environment variable holding
  // a ${secret.…} reference — rather than everywhere, since a confirm on every
  // row is one people learn to click through. See DictField.
  x_confirm_remove?: boolean;
  // x_lang_param names a SIBLING param holding the language this field is
  // written in — "shell" on the runner step, "language" on Text. The field is
  // told where to look rather than knowing either name, because the two steps
  // ask the same question in their own words.
  x_lang_param?: string;
  // x_columns_source marks a row-condition filter whose column field should be
  // a dropdown populated from a data source rather than typed free-hand.
  // "collection" lists the columns of the collection named by the sibling
  // `table` param (the Collections "Find rows" drop).
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
  // The deliberately-weak support-agent role — by itself grants no access; it
  // only lets a provisioned agent request a per-flow, org-approved AccessGrant.
  | "support:agent";

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

// An email-template library entry: a reusable HTML layout shell the email
// drops wrap a body in. Built-ins are global and read-only; org templates are
// tenant-private and editable. The drop stores `id` in its params.
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
  // public_base_url is the daemon's externally-reachable origin (from
  // --public-base-url). Used to build correct webhook/hosted-form URLs.
  // Empty when unset — the UI falls back to a localhost hint.
  public_base_url?: string;
  // email_verified / verification_pending drive the "confirm your email"
  // banner. pending is false on deployments without a mailer and for
  // API-key principals.
  email_verified?: boolean;
  verification_pending?: boolean;
  // support_contact is an operator-set email or URL (e.g.
  // "support@acme.com" or "https://acme.com/help") surfaced on UI
  // surfaces that depend on server-side setup the end user can't fix
  // themselves — notably the Connections page when OAuth/secret-store
  // are off. Empty = render a generic "contact your administrator"
  // message with no link.
  support_contact?: string;
  // support_tickets_enabled is true when the native support-ticket surface is
  // wired on this deployment (DAZYFLOW_SUPPORT_ENABLED). Drives whether the UI
  // shows "Report a problem" and the Support page.
  support_tickets_enabled?: boolean;
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
  // subdomain is the org's chosen DNS label ("klahr" → klahr.dazyflow.app).
  // Empty when unclaimed. Set via the dedicated putOrgSubdomain endpoint.
  subdomain?: string;
  // wildcard_domain is the deployment's apex (e.g. "dazyflow.app") the
  // subdomain hangs off, or "" when per-org subdomains aren't enabled. The
  // editor only shows the subdomain field when this is set.
  wildcard_domain?: string;
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

// SignupInviteSummary is a platform signup-invite (platform:admin
// only): an offer for a specific email to create its own account on a
// signup-disabled deployment. The create call returns the same shape
// minus the lifecycle fields. See daemon/signup_invite.go.
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
};

// ShareLink is the workspace's public overview (TV-dashboard) link. url is
// the absolute, ready-to-open page; token is the cryptic credential embedded
// in it. Mirrors daemon.shareResponse.
export type ShareLink = {
  token: string;
  url: string;
  created_at: string;
  created_by?: string;
};

// PublicOverview is the sanitized, unauthenticated status snapshot the TV
// page polls. No IDs, no error detail — only flow names, run status, and
// aggregate counters. Mirrors daemon.PublicOverviewData.
export type PublicOverview = {
  // label is the org's display name, used to title the board. Absent when
  // the org has no display name — the UI falls back to a generic title.
  label?: string;
  // icon is the org's logo — a data: URL / image reference or a logical icon
  // name — shown beside the title. Absent when the org has no icon set.
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

export type PublicFlowState = {
  name: string;
  icon?: string;
  run_status?: "live" | "manual" | "paused" | "needs_publish";
  last_status?: JobStatus;
  last_run_at?: string;
  // next_run_at is the next scheduled fire (RFC3339 UTC), present only for
  // flows that are live on a scheduler trigger. Absent for manual/paused/
  // needs-publish/webhook-only flows.
  next_run_at?: string;
  // history is the flow's recent run outcomes, newest first — drawn as a
  // small health strip on the card.
  history?: JobStatus[];
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
  // Param paths the finding points at (e.g. "spreadsheet_id",
  // "headers.Authorization", "env.API_KEY"). The editor names these inputs the
  // way the Inspector does — by schema title — when building the visible
  // warning, so no node/module/field slugs leak into the help text. `message`
  // is the slug-bearing fallback for non-UI consumers.
  fields?: string[];
  // Data the finding needs quoted in its sentence — a language name, an
  // interpreter — so the editor can build a LOCALISED sentence instead of
  // showing the English `message`. Keys are per-code; see the rule that sets
  // them. Distinct from `fields`, which are param paths resolved against a
  // schema.
  values?: Record<string, string>;
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
  // Optional human name attached to this revision (e.g. "Black Friday
  // config"). Keyed to the commit, so it survives publishes and rollbacks.
  // Empty/absent when the revision is unlabeled.
  label?: string;
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

// RunLogEntry is one line of a run's persisted log (what the run SAID
// while executing: progress lines, node status transitions, terminal
// outcome). Mirrors daemon.RunLogEntry; seq is the resume cursor.
export type RunLogEntry = {
  seq: number;
  run_id: string;
  ts: string;
  node_id?: string;
  kind: "progress" | "status" | "terminal" | "truncated";
  // stdout | stderr on labelled progress lines (shell/git output).
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
  // WillRetry/RetryAt mirror the node view's auto-retry signal (see
  // NodeRunView). Set only while a node is queued for a future retry.
  WillRetry?: boolean;
  RetryAt?: string | null;
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
  // will_retry + retry_at: the engine will automatically try this node
  // again at retry_at (it's between attempts after a transient failure).
  // Absent on a terminal failure — that one needs the user.
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

// ScheduleEntry is one automatic-trigger schedule from GET /me/schedules:
// a cron_trigger or poll_trigger node on a flow, plus its next-run
// preview. `disabled` is the per-trigger pause; `flow_disabled` is the
// whole-flow pause (which overrides it). next_fires are RFC3339 UTC.
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

// PublishInfo is GET /me/flows/{id}/published: the draft-vs-live state.
// published=false means nothing is live yet; dirty means the draft (HEAD)
// would BEHAVE differently from the published revision (always true when
// never published).
//
// Not "the revisions differ" — the daemon excludes canvas layout, notes,
// wire routing and the pause switch (core.BehaviorEqual), the same set
// diffGraphs ignores. Moving a step is not something to publish, and the
// pill and the diff view have to agree about that.
export type PublishInfo = {
  published: boolean;
  published_commit?: string;
  head_commit?: string;
  dirty: boolean;
};

// Runner is one of the org's registered runners from GET /admin/runners: its
// own code, on its own hardware, reachable as a step in its flows.
//
// The client PRIVATE key is never returned — it is write-only, held encrypted
// server-side. The certificates ARE returned: they are public identity an admin
// may need to re-install on the runner side.
// Runner is one machine the org has registered from GET /admin/runners.
//
// There is no address and no connection state, because nothing connects TO a
// runner: an agent on that machine asks the daemon for work. "Online" is
// therefore derived from when it last asked, which is why last_seen is here and
// a status field is not.
export type Runner = {
  name: string;
  // labels a flow can target instead of the name, so a pool of interchangeable
  // machines can share work.
  labels?: string[];
  // version of the agent, reported at registration — an old agent is a
  // plausible cause of odd behaviour.
  version?: string;
  online: boolean;
  last_seen?: string;
  created_by?: string;
  created_at: string;
};

// MCPServer is one MCP endpoint the org has configured, from
// GET /admin/mcp-servers. Every tool the endpoint publishes becomes a step
// named mcp:<name>:<tool>.
//
// There is no token field and there never will be: the credential is sealed
// server-side and the read path does not select it. has_token is what the edit
// form needs instead — whether there is something stored to keep.
export type MCPServer = {
  // name is the id flows reference, derived from the label when the server was
  // created and frozen from then on: renaming it would be a NEW server, and
  // the old step ids would stop resolving.
  name: string;
  // label is the display name — what the admin typed, free of the id rules.
  // Always populated: a server saved before labels existed reports its id
  // here, so nothing on this side needs a fallback.
  label: string;
  url: string;
  auth_kind: "none" | "bearer" | "header";
  // auth_header is the header name for auth_kind "header". The name is not
  // secret; the value it carries is.
  auth_header?: string;
  has_token: boolean;
  enabled: boolean;
  // connected is the live fact — registered in the daemon answering this
  // request, right now. On a multi-replica deployment a server saved seconds
  // ago on another node may be enabled and not yet connected here.
  connected: boolean;
  // tool_ids are the step ids this server contributes.
  tool_ids?: string[];
  // instructions is what the server says about itself at handshake — prose a
  // third party wrote, for a human to read. Live-only, like connected: absent
  // when this row is registered on another replica. Render as TEXT.
  instructions?: string;
  tool_count: number;
  // last_error is why the last connection attempt failed, verbatim from the
  // endpoint where that helps ("refused the credential").
  last_error?: string;
  last_connected?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
};

// StepSourceUse is one flow that references a step source's steps, from
// GET /admin/{mcp-servers,web-apis}/{name}/usage. It exists to answer one
// question, asked at one moment: what breaks if this source is deleted.
export type StepSourceUse = {
  workspace: string;
  flow_id: string;
  name?: string;
  // steps are the referencing step ids, e.g. "mcp:mcp-test:search".
  steps: string[];
  // published is the difference between an inconvenience and an outage.
  published: boolean;
};

// StepSourceUsage is the whole answer. hidden counts flows the caller may not
// view: an admin needs the blast radius even where they may not see the title.
export type StepSourceUsage = {
  flows: StepSourceUse[];
  hidden: number;
};

// MCPServerInput is what the form submits. token empty on an edit means "keep
// the stored one".
export type MCPServerInput = {
  // label is what the admin typed. On a create the daemon derives the id from
  // it; on an edit it is the one part of the identity that can still change.
  label: string;
  // name is never sent by this app — the daemon derives the id. It stays on
  // the type because the endpoint still accepts an explicit one.
  name?: string;
  url: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header?: string;
  token?: string;
  enabled?: boolean;
};

// WebAPIOperation is one described HTTP call. Every operation becomes a step
// named api:<catalog>:<id>.
export type WebAPIOperation = {
  // id is the step-id suffix and is frozen once flows reference it: renaming it
  // is a NEW step, and the old id stops resolving.
  id: string;
  // title is the operation's display name — what captions its step in the
  // palette. Optional; the step falls back to the id, which reads like the
  // identifier it is. Not an identifier itself, so it is free to change.
  title?: string;
  method: "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE";
  // path is joined onto the catalog's base URL. {placeholders} in it must each
  // have a required path argument of the same name — the daemon refuses the save
  // otherwise, naming the placeholder.
  path: string;
  summary?: string;
  description?: string;
  args?: WebAPIArg[];
  // body_mode decides what the request carries: nothing, a JSON object built
  // from the body arguments, or whatever is wired into the step's Body pin.
  body_mode?: "none" | "json" | "raw";
  deprecated?: boolean;
};

// WebAPIArg is one argument of an operation. `in` is the field with no
// counterpart in an MCP tool: HTTP splits one call's arguments across the path,
// the query string, the headers and the body, and the daemon needs to know which
// is which to assemble the request.
export type WebAPIArg = {
  name: string;
  // type is the JSON Schema type. Scalars earn a pin on the node (up to twelve,
  // required ones first); anything else stays a param.
  in: "path" | "query" | "header" | "body";
  type?: string;
  required?: boolean;
  label?: string;
  description?: string;
};

// WebAPI is one described HTTP API the org has configured, from
// GET /admin/web-apis.
//
// Unlike MCPServer there is no has_token, because this feature stores no
// credential: the address and the token live in the org's CONNECTION for the
// integration (the Apps page), and the engine injects them into each step at run
// time.
export type WebAPI = {
  // name is the id flows reference, derived from the label at creation and
  // frozen from then on.
  name: string;
  label: string;
  base_url: string;
  // integration is the Apps-page grouping the connection attaches to.
  integration?: string;
  auth_kind: "none" | "bearer" | "header";
  // auth_header is the header name for auth_kind "header". The name is not a
  // secret; the value it carries is, and it is not stored here.
  auth_header?: string;
  operations: WebAPIOperation[];
  timeout_ms?: number;
  max_body_bytes?: number;
  enabled: boolean;
  // logo is the catalog's brand mark as a data: URI — the same image every step
  // of this catalog wears in the palette. Absent means the globe glyph.
  logo?: string;
  // logo_mode is where that mark came from. "auto" is the service's favicon,
  // guessed at save time; "custom" is an image an admin chose; "none" is the
  // plain glyph, on purpose. Always sent, so the form opens on the right choice.
  logo_mode: WebAPILogoMode;
  // registered is the live fact: this catalog is in the answering daemon's
  // engine catalog right now. It is NOT a health check — nothing was dialed —
  // and the page must not present it as one.
  registered: boolean;
  // step_ids are the ids this catalog contributed, so the page can show what was
  // gained rather than only a count.
  step_ids?: string[];
  // last_error is set only when a stored catalog could not be registered — a
  // descriptor a later release refuses. Normally empty.
  last_error?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
};

// WebAPILogoMode is where a catalog's brand mark comes from. The three are
// distinct because a guess that found nothing is retried on the next save and a
// glyph the admin chose must not be.
export type WebAPILogoMode = "auto" | "custom" | "none";

// WebAPIInput is what the form submits.
export type WebAPIInput = {
  label: string;
  // name is never sent by this app — the daemon derives the id. It stays on the
  // type because the endpoint still accepts an explicit one.
  name?: string;
  base_url: string;
  integration?: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header?: string;
  operations: WebAPIOperation[];
  timeout_ms?: number;
  max_body_bytes?: number;
  enabled: boolean;
  // logo_mode and logo are omitted to mean "leave the icon alone": a save that
  // sent them as empty would throw away an uploaded mark. logo is the image
  // itself, a data: URI, and is only read when logo_mode is "custom".
  logo_mode?: WebAPILogoMode;
  logo?: string;
};


// RunnerTarget is one machine as the flow editor sees it, from GET /runners:
// what the Run on your machine step needs to be pointed somewhere, and nothing
// about administering the fleet.
//
// Online is here because it is the difference between "this step will run" and
// "this step will wait and then fail" — worth seeing while choosing, not after
// a run.
export type RunnerTarget = {
  name: string;
  // tags is everything a step may target this machine by — its labels AND its
  // own name. The name is in there because that is how a step pins itself to
  // one machine; the editor does not have to know the rule, it just offers the
  // set.
  tags?: string[];
  online: boolean;
};

// RunnerToken is a registration token, returned once by
// POST /admin/runners/token and never retrievable again. It is the whole of the
// install: paste it into one command on the machine.
export type RunnerToken = {
  token: string;
  expires_at: string;
};

// GitCredential is one named per-org git credential from
// GET /git/credentials: the account name and which parts are set (an SSH key
// and/or an HTTPS access token). Secret material is never returned by the API;
// the non-secret username is.
export type GitCredential = {
  account: string;
  has_ssh_key: boolean;
  has_passphrase: boolean;
  has_known_hosts: boolean;
  has_token: boolean;
  username?: string;
};

// GitMirror is GET /git/mirror: where this workspace's flow repository is
// mirrored, plus how the last push went. `configured: false` is the normal
// state of a workspace with no mirror — the endpoint answers 200 with that
// rather than 404, so the panel renders the same either way.
//
// No secret material: `account` names a git credential, whose SSH key stays
// in the encrypted store and is resolved server-side at push time.
export type GitMirror = {
  configured: boolean;
  remote_url?: string;
  account?: string;
  enabled: boolean;
  // "publish" mirrors when a flow goes live; "save" also mirrors drafts.
  push_on?: "publish" | "save";
  updated_at?: string;
  updated_by?: string;
  // last_attempt_at is set whether the push worked or not; last_success_at
  // only advances on success. Both together answer "has this EVER worked"
  // and "how long have we been unmirrored".
  last_attempt_at?: string;
  last_success_at?: string;
  last_commit?: string;
  last_error?: string;
};

// MirrorPushResult is POST /git/mirror/push: what one manual push did.
// `changed: false` means the remote already matched — a success, not a no-op
// worth warning about.
export type MirrorPushResult = {
  pushed: number;
  deleted: number;
  changed: boolean;
  commit: string;
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
  // needs_reconnect lists accounts whose grant is DEAD — the token refresh
  // was definitively rejected (access revoked, password changed, grant
  // expired). Distinct from stale_accounts, which is about scopes added
  // since. This is the one that catches a provider authorized incrementally
  // (Google), where the scope check is deliberately skipped — without it a
  // dead Google account reads as connected while every run 401s.
  needs_reconnect?: string[];
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

// GET /api/v1/me/usage — one bucket per calendar month (UTC), newest
// first. The current month is always present (zeroed when idle).
export type UsageCounters = {
  period: string; // "2026-06"
  graph_runs: number;
  node_executions: number;
  // Scheduled fires the run-cap gate refused this month (0 when uncapped).
  skipped_runs: number;
};

// GET /api/v1/me/billing — plan state for the Usage page. free_runs_per_month
// is 0 when the deployment has no run gate; can_upgrade/can_manage reflect
// whether Stripe is configured (and a customer exists, respectively).
export type BillingInfo = {
  plan: "free" | "pro";
  subscription_status?: string;
  // True when the subscription is set to cancel at period end: still
  // "active" in Stripe, but won't renew — drives the "cancels on …" chip.
  cancel_at_period_end?: boolean;
  // RFC3339; renewal date, or cancellation date when cancel_at_period_end.
  // Omitted when there's no Stripe subscription.
  current_period_end?: string;
  free_runs_per_month: number;
  runs_this_month: number;
  // True when this deployment runs paid billing (Stripe configured). Distinct
  // from can_upgrade (which is also false once already pro): gates the whole
  // plan/upgrade UI so a self-host without Stripe shows usage only.
  billing_enabled: boolean;
  // False when this deployment keeps schedules/polling triggers off the
  // free plan and the tenant is free.
  polling_allowed: boolean;
  can_upgrade: boolean;
  can_manage: boolean;
};

// GET /api/v1/secret-manager/aws — redacted (the secret access key never
// comes back). PUT body is AwsSecretManagerConfig.
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

// GET /api/v1/secret-manager/gcp — redacted (the service-account key never
// comes back; client_email identifies it). PUT body is GcpSecretManagerConfig.
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

// FileEntry is one row in a workspace file-manager directory listing
// (mirrors daemon/httpfiles.go fileEntry).
export type FileEntry = {
  name: string;
  path: string; // workspace-relative path to this entry
  is_dir: boolean;
  size: number;
  mod_time: string; // RFC3339
};

// GrantStatus mirrors core.GrantStatus — the lifecycle of a support AccessGrant.
export type GrantStatus = "requested" | "approved" | "denied" | "revoked" | "expired";

// AccessGrant is one consented, time-boxed, read-only support view of a flow.
// Mirrors core.AccessGrant's JSON. The org-admin consent surface lists these.
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

// TicketStatus mirrors core.TicketStatus — the lifecycle of a support ticket.
export type TicketStatus =
  | "open"
  | "awaiting_user"
  | "awaiting_support"
  | "resolved"
  | "closed";

// Ticket mirrors core.Ticket's JSON: one support request scoped to the org that
// filed it, optionally about a flow/run with a redacted diagnostic bundle attached.
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
};

// TicketAuthorKind mirrors core.AuthorKind — who wrote a chat message.
export type TicketAuthorKind = "user" | "support" | "system";

// TicketMessage is one entry in a ticket's chat thread. Bodies are
// secret-scrubbed server-side before they are ever stored or returned.
export type TicketMessage = {
  id: string;
  ticket_id: string;
  author?: string;
  author_kind: TicketAuthorKind;
  body: string;
  bundle_id?: string;
  created_at: string;
};

// TicketView is a ticket plus its chronological thread (the get-one response).
//
// On the end-user surface the server strips the support organisation's internals
// first: `ticket.assigned_to` is absent and support messages carry no `author`
// (the customer sees "Support", not the individual who picked it up). The agent
// surface returns the record as stored.
export type TicketView = {
  ticket: Ticket;
  messages: TicketMessage[];
};

// TicketQueueSummary mirrors core.TicketQueueSummary — the support dashboard's
// counts over the WHOLE cross-org queue, unbounded by any list limit.
//
// The two halves count different sets, deliberately: by_status/total cover every
// ticket ever filed, while open/unassigned/by_assignee cover only non-terminal
// tickets, so a pile of resolved tickets can't drown the "needs a first
// responder" signal.
export type TicketQueueSummary = {
  by_status: Partial<Record<TicketStatus, number>>;
  total: number;
  open: number;
  unassigned: number;
  by_assignee: Record<string, number>;
};

// TicketQueueSummaryResponse is the summary endpoint's body. `mine` is the
// caller's own live load, resolved server-side so the dashboard never needs to
// know its own subject.
export type TicketQueueSummaryResponse = {
  summary: TicketQueueSummary;
  mine: number;
};

// TicketQueueFilter is the ownership half of the queue's filters: every ticket,
// only unclaimed ones, or only the caller's own ("me" resolves server-side).
export type TicketQueueFilter = "all" | "unassigned" | "mine";

// SupportAgentGrant is one provisioned support-agent (cross-tenant vendor
// staff), managed on the platform-admin surface. Mirrors daemon.SupportAgentGrant.
export type SupportAgentGrant = {
  email: string;
  granted_by: string;
  created_at: string;
};

// SupportBundle is the REDACTED view of a flow a support agent receives from
// GET /support/flows/{tenant}/{workspace}/{flow_id}. Mirrors core.SupportBundle:
// structure (nodes/edges/triggers) is verbatim; params carry no secret values,
// and the optional run keeps statuses + output SHAPE but never payloads.
export type RedactMode = "" | "structure_only" | "structure_plus_values";

export type SupportBundle = {
  mode: RedactMode;
  flow: BundleFlow;
  // nodes/edges are nullable on the wire, NOT just empty: Go marshals a nil
  // slice as JSON null, so a flow with no edges arrives as `"edges": null`.
  // Typed honestly so the compiler forces a guard at every consumer — an
  // unguarded `.map` here crashed the whole support view to a blank page.
  nodes: BundleNode[] | null;
  edges: Edge[] | null;
  triggers?: BundleTrigger[];
  run?: BundleRun;
  issues?: LintIssue[];
};

export type BundleFlow = {
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

export type BundleNode = {
  id: string;
  module: string;
  disabled?: boolean;
  breakpoint?: boolean;
  timeout_seconds?: number;
  position?: Position;
  params?: Record<string, unknown>;
  env?: Record<string, unknown>;
};

export type BundleTrigger = {
  type: string;
  cron?: string;
  tz?: string;
  interval_seconds?: number;
  public_form?: boolean;
  form_fields?: string[];
  form_title?: string;
  has_secret?: boolean;
};

export type BundleRun = {
  run_id: string;
  status: JobStatus;
  error?: JobError;
  enqueued_at?: string;
  started_at?: string;
  finished_at?: string;
  nodes?: BundleNodeRun[];
};

export type BundleNodeRun = {
  node_id: string;
  status: JobStatus;
  error?: JobError;
  attempt?: number;
  started_at?: string;
  finished_at?: string;
  output?: Record<string, BundleRef>;
};

// BundleRef is a run output port with its payload dropped — MIME, a shape hint,
// and (in structure_plus_values mode) column names survive; the value never does.
export type BundleRef = {
  mime?: string;
  has_value?: boolean;
  shape?: string;
  header_count?: number;
  headers?: string[];
};
