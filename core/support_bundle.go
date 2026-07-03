// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// support_bundle.go is the redaction boundary for the Support feature (see
// TODO-support-tickets.md). A SupportBundle is a diagnostic snapshot of ONE
// flow — its structure, config shape, and a run's outcome — that a support
// agent can be shown WITHOUT ever seeing secrets or raw run data.
//
// The safety model is "redaction by construction": the SupportBundle types
// physically cannot hold a raw param value or a run's output payload. You build
// a bundle FROM core.Graph + a RunSnapshot; you never serialize the raw structs.
// The four danger zones are handled here — Node.Params / Node.Env values are
// redacted to shape (keys kept, reference templates kept verbatim), trigger
// bearer secrets are dropped, Result.Output payloads are dropped, and
// JobError.Details is dropped. A final secret-scrub pass over every string in
// the assembled bundle is the belt-and-suspenders catch for a token pasted into
// a free-text field (a flow name, an error message) — it reuses the same
// knownSecretValue detector the linter uses.

// RedactMode selects how aggressively literal values are stripped.
type RedactMode string

const (
	// RedactStructureOnly (the default, recommended) removes every literal
	// value — each is replaced by a shape marker ({"__redacted":"string",
	// "len":19}) — while keeping keys, structure, and reference templates.
	RedactStructureOnly RedactMode = "structure_only"
	// RedactStructurePlusValues keeps NON-secret literals (a config flag, a
	// column name) so the bundle reads more naturally, while still redacting
	// anything that looks like a secret and still dropping run payloads. Opt-in.
	RedactStructurePlusValues RedactMode = "structure_plus_values"
)

// effective resolves the empty/unknown mode to the safe default.
func (m RedactMode) effective() RedactMode {
	if m == RedactStructurePlusValues {
		return RedactStructurePlusValues
	}
	return RedactStructureOnly
}

// redactedSecretMarker replaces any known-secret pattern found by the final
// scrub pass. ASCII so it never perturbs JSON escaping.
const redactedSecretMarker = "[redacted-secret]"

// ---- Input: a run snapshot (carries RAW refs; BuildSupportBundle redacts) ---

// RunSnapshot is the raw run outcome fed INTO BuildSupportBundle. The daemon
// builds it from the graph-record + node-records; the bundle it produces holds
// only the redacted projection. Nil (see BuildSupportBundle's run param) means
// "no specific run" — a bundle of just the flow structure.
type RunSnapshot struct {
	RunID      string
	Status     JobStatus
	Error      *JobError
	EnqueuedAt *time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Nodes      []NodeRunSnapshot
}

// NodeRunSnapshot is one node's raw outcome. Output carries raw core.Ref
// (with Inline payloads) — BuildSupportBundle drops the payloads.
type NodeRunSnapshot struct {
	NodeID     string
	Status     JobStatus
	Error      *JobError
	Attempt    int
	StartedAt  *time.Time
	FinishedAt *time.Time
	Output     map[string]Ref
}

// ---- Output: the redacted bundle (no field can hold a raw value) -----------

// SupportBundle is the redacted, shareable snapshot. Every field is either safe
// structure or an already-redacted projection — there is deliberately no field
// capable of holding a raw param value or a run's output payload.
type SupportBundle struct {
	Mode     RedactMode      `json:"mode"`
	Flow     BundleFlow      `json:"flow"`
	Nodes    []BundleNode    `json:"nodes"`
	Edges    []Edge          `json:"edges"` // safe verbatim — pure wiring
	Triggers []BundleTrigger `json:"triggers,omitempty"`
	Run      *BundleRun      `json:"run,omitempty"`
	Issues   []LintIssue     `json:"issues,omitempty"` // safe by design (ids/fields, never values)
}

// BundleFlow is the flow's identity + display metadata. Name/Description are
// user free-text, so they pass through the final scrub pass.
type BundleFlow struct {
	ID             string     `json:"id"`
	Tenant         string     `json:"tenant"`
	Workspace      string     `json:"workspace"`
	Name           string     `json:"name,omitempty"`
	Icon           string     `json:"icon,omitempty"`
	Description    string     `json:"description,omitempty"`
	Visibility     Visibility `json:"visibility,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	Disabled       bool       `json:"disabled,omitempty"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty"`
	// NotifiesOnFailure records THAT a failure notification is configured,
	// without leaking the webhook URL / email (which can embed a token).
	NotifiesOnFailure bool `json:"notifies_on_failure,omitempty"`
}

// BundleNode keeps the node's identity + wiring-relevant flags, with Params/Env
// redacted (keys kept, literal values → shape, reference templates verbatim).
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

// BundleTrigger is a scrubbed trigger: the bearer secret is dropped (only its
// presence is recorded), everything else is safe schedule/form config.
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

// BundleRun is a run's redacted outcome: statuses, timings, error Code+Message
// (Details dropped), and per-node output SHAPE (Inline payloads dropped).
type BundleRun struct {
	RunID      string          `json:"run_id"`
	Status     JobStatus       `json:"status"`
	Error      *JobError       `json:"error,omitempty"`
	EnqueuedAt *time.Time      `json:"enqueued_at,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Nodes      []BundleNodeRun `json:"nodes,omitempty"`
}

// BundleNodeRun is one node's redacted run record.
type BundleNodeRun struct {
	NodeID     string               `json:"node_id"`
	Status     JobStatus            `json:"status"`
	Error      *JobError            `json:"error,omitempty"`
	Attempt    int                  `json:"attempt,omitempty"`
	StartedAt  *time.Time           `json:"started_at,omitempty"`
	FinishedAt *time.Time           `json:"finished_at,omitempty"`
	Output     map[string]BundleRef `json:"output,omitempty"`
}

// BundleRef is a run output port with its payload dropped: the MIME + a shape
// hint + column count survive; the raw value never does. There is deliberately
// no Inline field.
type BundleRef struct {
	MIME string `json:"mime,omitempty"`
	// HasValue records that a value was present (and dropped), so a support
	// agent can tell "emitted nothing" from "emitted something we redacted."
	HasValue bool `json:"has_value,omitempty"`
	// Shape is the JSON kind of the dropped value: string/number/bool/object/
	// array (empty when there was no value).
	Shape string `json:"shape,omitempty"`
	// HeaderCount is the column count for a row-list value.
	HeaderCount int `json:"header_count,omitempty"`
	// Headers (column names) are kept only in RedactStructurePlusValues mode —
	// column names are usually safe, but they're a user's field names, so the
	// default keeps only the count.
	Headers []string `json:"headers,omitempty"`
}

// BuildSupportBundle projects a flow (+ optional run + precomputed lint issues)
// into a redacted SupportBundle. It is pure: no I/O, no manifest lookups (pass
// ValidateGraphFull/LintGraph output as `issues`). Pass run=nil for a
// structure-only bundle with no run attached.
//
// (The design doc sketched `run RunSnapshot`; a pointer is used so "no run" is
// unambiguous rather than a zero-value sentinel.)
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

	// Belt-and-suspenders: scrub every string in the assembled bundle for a
	// known-secret pattern (a token pasted into a flow name, echoed in an error
	// message, or riding along inside a kept literal). A JSON round-trip is the
	// simplest way to reach EVERY string; the known-secret patterns contain no
	// JSON metacharacters, so replacing a match in-place keeps the JSON valid.
	return scrubBundleSecrets(b)
}

// redactError keeps the user-facing Code + Message and DROPS Details (which may
// embed URLs / tokens / raw data). Message itself is still swept by the final
// scrub pass. Returns nil for a nil error.
func redactError(e *JobError) *JobError {
	if e == nil {
		return nil
	}
	return &JobError{Code: e.Code, Message: e.Message}
}

// redactRef drops a run output's Inline payload, keeping only MIME + a shape
// hint + column count. Header names survive only in values mode.
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

// redactParams redacts every value in a node's Params, keeping keys.
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

// redactEnv redacts every value in a node's Env, keeping keys. Env values are
// strings; the redacted projection is map[string]any (shape markers).
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

// redactValue is the core redaction rule, applied recursively.
//
//   - A string containing a ${scheme.path} template is a REFERENCE, not a
//     literal — kept verbatim (its diagnostic value is naming what it points
//     at; the name is not the secret).
//   - Any other literal is replaced by a shape marker in structure-only mode.
//     In values mode a literal is kept UNLESS its key looks secret-shaped or the
//     value itself matches a known-secret pattern.
//   - Maps and slices recurse, preserving keys and structure.
//
// keyLeaf is the field's own key, used for the secret-key-name test; it is
// threaded down into slice elements so an array under a secret-named key is
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
		// Numbers, bools. Not secrets, but structure-only strips them too so the
		// default reveals no literal config at all. Values mode keeps them unless
		// they sit under a secret-shaped key.
		if mode == RedactStructurePlusValues && !secretKeyName.MatchString(keyLeaf) {
			return t
		}
		return map[string]any{"__redacted": shapeOf(t)}
	}
}

// shapeOf reports the JSON kind of a value, for the redacted shape marker.
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

// ---- Persistence: the stored redacted bundle -------------------------------

// SupportBundleRecord is a persisted SupportBundle: the metadata a store indexes
// on, plus the redacted bundle JSON in Payload. Payload is ALWAYS a serialized
// SupportBundle (redacted by construction) — never the raw graph/run — which
// NewSupportBundleRecord enforces by building it only from a SupportBundle.
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

// NewSupportBundleRecord wraps an already-redacted SupportBundle into a
// persistable record, deriving the indexed metadata (tenant, flow, run, mode)
// from the bundle itself so they can't drift from the payload. Because it only
// accepts a SupportBundle — which cannot hold a raw value — the stored Payload
// is guaranteed redacted.
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

// BundleStore persists SupportBundleRecords. Implementations live in daemon/
// (in-memory + Postgres), mirroring GrantStore / JobStore.
type BundleStore interface {
	// Create stores a record; a duplicate ID is an error.
	Create(ctx context.Context, rec SupportBundleRecord) error
	// Get returns the record, or ErrNotFound.
	Get(ctx context.Context, id string) (SupportBundleRecord, error)
	// ListForTenant returns every bundle record in tenant, newest first.
	ListForTenant(ctx context.Context, tenant string) ([]SupportBundleRecord, error)
}

// scrubBundleSecrets replaces any known-secret pattern anywhere in the bundle's
// serialized form. It round-trips through JSON so it reaches every string —
// including deep map values in redacted params and kept literals — without
// reflection. Best-effort: if (un)marshaling somehow fails, the original bundle
// (already redacted by construction) is returned unchanged.
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
