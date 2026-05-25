// Mirror of the Go core types we touch from the UI.

export type Position = { x: number; y: number };

export type Node = {
  id: string;
  module: string;
  params: Record<string, unknown>;
  env?: Record<string, string>;
  position?: Position;
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

export type Graph = {
  id: string;
  version?: string;
  tenant: string;
  workspace: string;
  nodes: Node[];
  edges: Edge[];
  triggers?: GraphTrigger[];
};

export type Port = {
  port: string;
  label?: string;
  variadic?: boolean;
  mime?: string[];
};

export type Manifest = {
  id: string;
  version: string;
  label: string;
  color?: string;
  icon?: string;
  category?: string;
  provider?: string;
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
  | "tenant:admin";

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

export type JobRecord = {
  ID: string;
  Kind: string;
  GraphRunID: string;
  GraphID: string;
  NodeID: string;
  Status: JobStatus;
};
