// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	// Headers is the column order for a row-list ("Items") value. In the
	// simplified data model an Items value carries its own column order, so a
	// flow wires ONE port instead of parallel rows + headers ports. Empty for
	// non-row values, and for row values whose order can be derived from the
	// keys (rows.DeriveHeaders). Folds the former `headers` port onto the value.
	Headers []string `json:"headers,omitempty"`
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

	// ScratchRoot is the absolute path of this run's ephemeral scratch
	// directory — distinct from the persistent WorkspaceRoot. File drops
	// resolve a `scratch://` path against it; whatever lands there is
	// reclaimed when the run reaches a terminal state (per CleanupPolicy).
	// Empty when the sandbox provider doesn't support scratch or the
	// run has no ID. Set by the engine before Execute.
	ScratchRoot string `json:"scratch_root,omitempty"`

	// Tenant carries the principal's tenant for module-side accounting
	// (e.g. quota checks). Set by the engine before Execute.
	Tenant string `json:"tenant,omitempty"`

	// Language is the flow's output language (Graph.Language) — the language a
	// step writes WORDS in when it emits any. Empty means English. Set by the
	// engine before Execute, like Tenant.
	Language string `json:"language,omitempty"`

	// QuotaLimit is the tenant's byte budget at the time the job
	// started. Zero means unlimited.
	QuotaLimit int64 `json:"quota_limit,omitempty"`

	// QuotaUsed is the tenant's byte usage at job-start. Modules treat
	// (QuotaLimit - QuotaUsed) as their remaining budget and refuse
	// writes that would exceed it.
	QuotaUsed int64 `json:"quota_used,omitempty"`

	// ApprovalURL is the absolute URL an external approver hits to
	// resume a paused node. Populated by the engine for modules whose
	// manifest declares AwaitsApproval = true; empty otherwise. The
	// module is expected to emit it as part of its pending Result so
	// downstream notification nodes (email, Slack, etc.) can deliver
	// the link to a human.
	ApprovalURL string `json:"approval_url,omitempty"`
}

// IdempotencyKey returns a stable per-node-record identifier suitable
// as the value of an `Idempotency-Key` HTTP header on outbound POST/PUT
// calls. The same Job (= same JobRecord.ID) produces the same key
// across retries — when a worker re-Executes a failed job, the record
// ID stays the same, so the receiving service can dedupe by storing
// the key and rejecting repeat requests.
//
// Format: "dazyflow:<job_id>". Slack, Stripe, GitHub, and most modern
// REST APIs honor this header convention; APIs that don't recognize
// it ignore the header without erroring.
func (j Job) IdempotencyKey() string {
	return "dazyflow:" + j.ID
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries technical context (type signatures, library
	// error strings, stack-trace-like info) that helps a developer
	// debug but would confuse a non-technical user reading Message.
	// UIs should hide it behind a "Details" expander; the Message
	// field alone must be actionable. Optional.
	Details string `json:"details,omitempty"`
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
	// StatusAwaiting is the sentinel a module returns to tell the
	// worker "park this node and free me — I'm waiting for an
	// external resume." The worker translates this into a
	// JobStatusAwaiting record rather than a terminal write.
	StatusAwaiting = "awaiting"
)

type Progress struct {
	JobID   string         `json:"job_id"`
	NodeID  string         `json:"node_id"`
	Percent *float64       `json:"percent,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}
