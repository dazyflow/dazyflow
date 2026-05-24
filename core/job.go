package core

type CleanupPolicy string

const (
	CleanupOnNodeComplete  CleanupPolicy = "on_node_complete"
	CleanupOnGraphComplete CleanupPolicy = "on_graph_complete"
	CleanupManual          CleanupPolicy = "manual"
)

type Ref struct {
	MIME   string `json:"mime"`
	Ref    string `json:"ref,omitempty"`
	Inline any    `json:"data,omitempty"`
}

type Job struct {
	ID      string            `json:"job_id"`
	GraphID string            `json:"graph_id"`
	NodeID  string            `json:"node_id"`
	TraceID string            `json:"trace_id"`
	SpanID  string            `json:"span_id"`
	Input   map[string]Ref    `json:"input"`
	Params  map[string]any    `json:"params"`
	Output  map[string]Ref    `json:"output_hint"`
	Env     map[string]string `json:"env"`
	Cleanup CleanupPolicy     `json:"cleanup"`

	// WorkspaceRoot is the absolute filesystem path the module is
	// confined to. Filesystem-touching modules MUST treat this as their
	// only writable surface and refuse to operate when it is empty.
	// Populated by the engine from a SandboxProvider; module code does
	// not have to (and cannot reliably) sandbox itself otherwise.
	WorkspaceRoot string `json:"workspace_root,omitempty"`

	// Tenant carries the principal's tenant for module-side accounting
	// (e.g. quota checks). Set by the engine before Execute.
	Tenant string `json:"tenant,omitempty"`

	// QuotaLimit is the tenant's byte budget at the time the job
	// started. Zero means unlimited.
	QuotaLimit int64 `json:"quota_limit,omitempty"`

	// QuotaUsed is the tenant's byte usage at job-start. Modules treat
	// (QuotaLimit - QuotaUsed) as their remaining budget and refuse
	// writes that would exceed it.
	QuotaUsed int64 `json:"quota_used,omitempty"`
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *JobError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type Result struct {
	JobID  string         `json:"job_id"`
	Status string         `json:"status"`
	Output map[string]Ref `json:"output,omitempty"`
	Error  *JobError      `json:"error,omitempty"`
}

const (
	StatusOK    = "ok"
	StatusError = "error"
)

type Progress struct {
	JobID   string         `json:"job_id"`
	NodeID  string         `json:"node_id"`
	Percent *float64       `json:"percent,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}
