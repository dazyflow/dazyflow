// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// The redaction boundary for the Support feature. Redaction BY CONSTRUCTION: the
// SupportBundle types physically cannot hold a raw param value or a run's output
// payload, and a bundle is built from core.Graph plus a RunSnapshot rather than by
// serializing the raw structs. The final scrub pass is belt-and-suspenders for a
// token pasted into free text.

type RedactMode string

const (
	RedactStructureOnly RedactMode = "structure_only"
	// Still redacts anything secret-shaped and still drops run payloads.
	RedactStructurePlusValues RedactMode = "structure_plus_values"
)

func (m RedactMode) effective() RedactMode {
	if m == RedactStructurePlusValues {
		return RedactStructurePlusValues
	}
	return RedactStructureOnly
}

// ASCII, so it never perturbs JSON escaping.
const redactedSecretMarker = "[redacted-secret]"

type RunSnapshot struct {
	RunID      string
	Status     JobStatus
	Error      *JobError
	EnqueuedAt *time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Nodes      []NodeRunSnapshot
}

type NodeRunSnapshot struct {
	NodeID     string
	Status     JobStatus
	Error      *JobError
	Attempt    int
	StartedAt  *time.Time
	FinishedAt *time.Time
	Output     map[string]Ref
}

type SupportBundle struct {
	Mode     RedactMode      `json:"mode"`
	Flow     BundleFlow      `json:"flow"`
	Nodes    []BundleNode    `json:"nodes"`
	Edges    []Edge          `json:"edges"` // safe verbatim — pure wiring
	Triggers []BundleTrigger `json:"triggers,omitempty"`
	Run      *BundleRun      `json:"run,omitempty"`
	Issues   []LintIssue     `json:"issues,omitempty"` // safe by design (ids/fields, never values)
}

type BundleFlow struct {
	ID                string     `json:"id"`
	Tenant            string     `json:"tenant"`
	Workspace         string     `json:"workspace"`
	Name              string     `json:"name,omitempty"`
	Icon              string     `json:"icon,omitempty"`
	Description       string     `json:"description,omitempty"`
	Visibility        Visibility `json:"visibility,omitempty"`
	Owner             string     `json:"owner,omitempty"`
	Disabled          bool       `json:"disabled,omitempty"`
	TimeoutSeconds    int        `json:"timeout_seconds,omitempty"`
	NotifiesOnFailure bool       `json:"notifies_on_failure,omitempty"`
}

type BundleNode struct {
	ID             string         `json:"id"`
	Module         string         `json:"module"`
	Disabled       bool           `json:"disabled,omitempty"`
	Breakpoint     bool           `json:"breakpoint,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Position       *Position      `json:"position,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	Env            map[string]any `json:"env,omitempty"`
}

type BundleTrigger struct {
	Type            string   `json:"type"`
	Cron            string   `json:"cron,omitempty"`
	TZ              string   `json:"tz,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
	PublicForm      bool     `json:"public_form,omitempty"`
	FormFields      []string `json:"form_fields,omitempty"`
	FormTitle       string   `json:"form_title,omitempty"`
	HasSecret       bool     `json:"has_secret,omitempty"` // a bearer existed; value dropped
}

type BundleRun struct {
	RunID      string          `json:"run_id"`
	Status     JobStatus       `json:"status"`
	Error      *JobError       `json:"error,omitempty"`
	EnqueuedAt *time.Time      `json:"enqueued_at,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Nodes      []BundleNodeRun `json:"nodes,omitempty"`
}

type BundleNodeRun struct {
	NodeID     string               `json:"node_id"`
	Status     JobStatus            `json:"status"`
	Error      *JobError            `json:"error,omitempty"`
	Attempt    int                  `json:"attempt,omitempty"`
	StartedAt  *time.Time           `json:"started_at,omitempty"`
	FinishedAt *time.Time           `json:"finished_at,omitempty"`
	Output     map[string]BundleRef `json:"output,omitempty"`
}

type BundleRef struct {
	MIME        string   `json:"mime,omitempty"`
	HasValue    bool     `json:"has_value,omitempty"`
	Shape       string   `json:"shape,omitempty"`
	HeaderCount int      `json:"header_count,omitempty"`
	Headers     []string `json:"headers,omitempty"`
}

func BuildSupportBundle(g Graph, run *RunSnapshot, issues []LintIssue, mode RedactMode) SupportBundle {
	mode = mode.effective()

	b := SupportBundle{
		Mode: mode,
		Flow: BundleFlow{
			ID:                g.ID,
			Tenant:            g.Tenant,
			Workspace:         g.Workspace,
			Name:              g.Name,
			Icon:              g.Icon,
			Description:       g.Description,
			Visibility:        g.EffectiveVisibility(),
			Owner:             g.Owner,
			Disabled:          g.Disabled,
			TimeoutSeconds:    g.TimeoutSeconds,
			NotifiesOnFailure: g.FailureNotify != nil && (g.FailureNotify.Webhook != "" || g.FailureNotify.Email != ""),
		},
		Edges:  append([]Edge(nil), g.Edges...),
		Issues: issues,
	}

	b.Nodes = make([]BundleNode, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		b.Nodes = append(b.Nodes, BundleNode{
			ID:             n.ID,
			Module:         n.Module,
			Disabled:       n.Disabled,
			Breakpoint:     n.Breakpoint,
			TimeoutSeconds: n.TimeoutSeconds,
			Position:       n.Position,
			Params:         redactParams(n.Params, mode),
			Env:            redactEnv(n.Env, mode),
		})
	}

	for _, t := range g.Triggers {
		b.Triggers = append(b.Triggers, BundleTrigger{
			Type:            t.Type,
			Cron:            t.Cron,
			TZ:              t.TZ,
			IntervalSeconds: t.IntervalSeconds,
			PublicForm:      t.PublicForm,
			FormFields:      append([]string(nil), t.FormFields...),
			FormTitle:       t.FormTitle,
			HasSecret:       t.Secret != "",
		})
	}

	if run != nil {
		br := &BundleRun{
			RunID:      run.RunID,
			Status:     run.Status,
			Error:      redactError(run.Error),
			EnqueuedAt: run.EnqueuedAt,
			StartedAt:  run.StartedAt,
			FinishedAt: run.FinishedAt,
		}
		for _, nr := range run.Nodes {
			bnr := BundleNodeRun{
				NodeID:     nr.NodeID,
				Status:     nr.Status,
				Error:      redactError(nr.Error),
				Attempt:    nr.Attempt,
				StartedAt:  nr.StartedAt,
				FinishedAt: nr.FinishedAt,
			}
			if len(nr.Output) > 0 {
				bnr.Output = make(map[string]BundleRef, len(nr.Output))
				for port, ref := range nr.Output {
					bnr.Output[port] = redactRef(ref, mode)
				}
			}
			br.Nodes = append(br.Nodes, bnr)
		}
		b.Run = br
	}

	// A JSON round-trip is the simplest way to reach EVERY string, and the
	// known-secret patterns hold no JSON metacharacters, so replacing in place keeps
	// the JSON valid.
	return scrubBundleSecrets(b)
}

func redactError(e *JobError) *JobError {
	if e == nil {
		return nil
	}
	return &JobError{Code: e.Code, Message: e.Message}
}

func redactRef(r Ref, mode RedactMode) BundleRef {
	br := BundleRef{
		MIME:        r.MIME,
		HasValue:    r.Inline != nil,
		Shape:       shapeOf(r.Inline),
		HeaderCount: len(r.Headers),
	}
	if mode == RedactStructurePlusValues && len(r.Headers) > 0 {
		br.Headers = append([]string(nil), r.Headers...)
	}
	return br
}

func redactParams(params map[string]any, mode RedactMode) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = redactValue(k, v, mode)
	}
	return out
}

func redactEnv(env map[string]string, mode RedactMode) map[string]any {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]any, len(env))
	for k, v := range env {
		out[k] = redactValue(k, v, mode)
	}
	return out
}

// A string holding a ${scheme.path} template is a REFERENCE, kept verbatim: its
// diagnostic value is naming what it points at, and the name is not the secret.
// keyLeaf is threaded into slice elements, so an array under a secret-named key is
// redacted element-wise.
func redactValue(keyLeaf string, v any, mode RedactMode) any {
	switch t := v.(type) {
	case string:
		if templatePattern.MatchString(t) {
			return t // reference template — keep verbatim
		}
		if mode == RedactStructurePlusValues &&
			!secretKeyName.MatchString(keyLeaf) &&
			!knownSecretValue.MatchString(t) {
			return t // non-secret literal, values mode: keep
		}
		return map[string]any{"__redacted": "string", "len": len(t)}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, cv := range t {
			out[k] = redactValue(k, cv, mode)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, cv := range t {
			out[i] = redactValue(keyLeaf, cv, mode)
		}
		return out
	case nil:
		if mode == RedactStructurePlusValues {
			return nil
		}
		return map[string]any{"__redacted": "null"}
	default:
		if mode == RedactStructurePlusValues && !secretKeyName.MatchString(keyLeaf) {
			return t
		}
		return map[string]any{"__redacted": shapeOf(t)}
	}
}

func shapeOf(v any) string {
	switch v.(type) {
	case nil:
		return ""
	case string:
		return "string"
	case bool:
		return "bool"
	case float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "value"
	}
}

type SupportBundleRecord struct {
	ID        string     `json:"id"`
	Tenant    string     `json:"tenant"`
	FlowID    string     `json:"flow_id"`
	RunID     string     `json:"run_id,omitempty"` // optional — the run the bundle captured
	Mode      RedactMode `json:"mode"`
	Payload   []byte     `json:"payload"` // redacted SupportBundle JSON — never raw
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
}

// Accepting only a SupportBundle — which cannot hold a raw value — is what
// guarantees the stored Payload is redacted.
func NewSupportBundleRecord(id, createdBy string, createdAt time.Time, b SupportBundle) (SupportBundleRecord, error) {
	payload, err := json.Marshal(b)
	if err != nil {
		return SupportBundleRecord{}, err
	}
	rec := SupportBundleRecord{
		ID:        id,
		Tenant:    b.Flow.Tenant,
		FlowID:    b.Flow.ID,
		Mode:      b.Mode,
		Payload:   payload,
		CreatedBy: createdBy,
		CreatedAt: createdAt,
	}
	if b.Run != nil {
		rec.RunID = b.Run.RunID
	}
	return rec, nil
}

type BundleStore interface {
	Create(ctx context.Context, rec SupportBundleRecord) error
	Get(ctx context.Context, id string) (SupportBundleRecord, error)
	ListForTenant(ctx context.Context, tenant string) ([]SupportBundleRecord, error)
}

func scrubBundleSecrets(b SupportBundle) SupportBundle {
	raw, err := json.Marshal(b)
	if err != nil {
		return b
	}
	scrubbed := knownSecretValue.ReplaceAll(raw, []byte(redactedSecretMarker))
	if bytes.Equal(raw, scrubbed) {
		return b
	}
	var out SupportBundle
	if err := json.Unmarshal(scrubbed, &out); err != nil {
		return b
	}
	return out
}
