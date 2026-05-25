// Mirror of the Go core types we touch from the UI.

export type Position = { x: number; y: number };

export type Node = {
  id: string;
  module: string;
  params: Record<string, unknown>;
  env?: Record<string, string>;
  position?: Position;
  timeout_seconds?: number;
};

export type Edge = {
  from: string;
  from_port: string;
  to: string;
  to_port: string;
  on_error?: string;
};

export type GraphTrigger = {
  type: string;
  cron?: string;
  secret?: string;
};

export type Visibility = "org" | "private";

export type FlowSummary = {
  id: string;
  name?: string;
  icon?: string;
  description?: string;
  owner?: string;
  visibility?: Visibility;
};

export type Graph = {
  id: string;
  version?: string;
  tenant: string;
  workspace: string;
  nodes: Node[];
  edges: Edge[];
  triggers?: GraphTrigger[];
  visibility?: Visibility;
  owner?: string;
  name?: string;
  icon?: string;
  description?: string;
  timeout_seconds?: number;
};

export type Port = {
  port: string;
  label?: string;
  variadic?: boolean;
  mime?: string[];
  required?: boolean;
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
};

// JSONSchema is a relaxed subset of the JSON Schema spec — enough to
// drive the inspector form for our built-in modules. Unknown features
// fall through to a raw JSON textarea.
export type JSONSchema = {
  type?: "string" | "integer" | "number" | "boolean" | "object" | "array" | "null";
  title?: string;
  description?: string;
  default?: unknown;
  enum?: unknown[];
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
};

export type Permission =
  | "graph:run"
  | "graph:edit"
  | "graph:admin"
  | "module:register"
  | "secret:read"
  | "secret:write"
  | "tenant:admin"
  | "platform:admin";

export type WhoAmI = {
  subject: string;
  tenant: string;
  workspace: string;
  roles: { name: string; permissions: Permission[] }[];
  permissions: Permission[];
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

export type JobError = {
  code: string;
  message: string;
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
