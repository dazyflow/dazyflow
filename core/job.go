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
	MIME    string   `json:"mime"`
	Ref     string   `json:"ref,omitempty"`
	Inline  any      `json:"data,omitempty"`
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

	// The module's only writable surface. A filesystem-touching module MUST refuse
	// to operate when it is empty; module code cannot reliably sandbox itself.
	WorkspaceRoot string `json:"workspace_root,omitempty"`

	ScratchRoot string `json:"scratch_root,omitempty"`

	Tenant string `json:"tenant,omitempty"`

	Language string `json:"language,omitempty"`

	QuotaLimit int64 `json:"quota_limit,omitempty"`

	QuotaUsed int64 `json:"quota_used,omitempty"`

	ApprovalURL string `json:"approval_url,omitempty"`
}

func (j Job) IdempotencyKey() string {
	return "dazyflow:" + j.ID
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Message alone must be actionable; a UI hides Details behind an expander.
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
	StatusOK       = "ok"
	StatusError    = "error"
	StatusAwaiting = "awaiting"
)

type Progress struct {
	JobID   string         `json:"job_id"`
	NodeID  string         `json:"node_id"`
	Percent *float64       `json:"percent,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}
