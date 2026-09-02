// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// The adapter projects graph + node records into a RunSnapshot, and the whole
// pipeline (records → snapshot → BuildSupportBundle) must not leak a run's
// output payload or an error's Details.
func TestRunSnapshotFromRecords_FeedsRedactedBundle(t *testing.T) {
	t.Parallel()
	enqueued := time.Unix(1_700_000_000, 0)
	started := enqueued.Add(time.Second)
	finished := started.Add(2 * time.Second)

	runRec := core.JobRecord{
		ID:         "run-42",
		Kind:       core.JobKindGraph,
		GraphID:    "daily-invoice",
		Tenant:     "acme",
		Status:     core.JobStatusFailed,
		EnqueuedAt: enqueued,
		StartedAt:  &started,
		FinishedAt: &finished,
		Result:     &core.Result{Status: core.StatusError, Error: &core.JobError{Code: "timeout", Message: "exceeded 30s", Details: "raw with sk_live_leaky00000000"}},
	}
	nodeRecs := []core.JobRecord{
		{
			NodeID:  "charge",
			Kind:    core.JobKindNode,
			Status:  core.JobStatusSucceeded,
			Attempt: 1,
			Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"customer_id": {MIME: "text/plain", Inline: "cus_secretPayload123"},
			}},
		},
	}

	rs := support.RunSnapshotFromRecords(runRec, nodeRecs)

	// Adapter shape checks.
	if rs.RunID != "run-42" || rs.Status != core.JobStatusFailed {
		t.Errorf("run snapshot header wrong: %+v", rs)
	}
	if rs.EnqueuedAt == nil || !rs.EnqueuedAt.Equal(enqueued) {
		t.Errorf("enqueued_at not carried: %+v", rs.EnqueuedAt)
	}
	if len(rs.Nodes) != 1 || rs.Nodes[0].NodeID != "charge" {
		t.Fatalf("node snapshot wrong: %+v", rs.Nodes)
	}
	// The snapshot carries the RAW ref (redaction happens in core, not here).
	if rs.Nodes[0].Output["customer_id"].Inline != "cus_secretPayload123" {
		t.Error("adapter must pass the raw ref through to core for redaction")
	}

	// End-to-end: through BuildSupportBundle, no payload/Details survives.
	graph := core.Graph{ID: "daily-invoice", Tenant: "acme", Workspace: "main"}
	bundle := core.BuildSupportBundle(graph, &rs, nil, core.RedactStructureOnly)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(raw)
	for _, leak := range []string{"cus_secretPayload123", "sk_live_leaky00000000", "raw with"} {
		if strings.Contains(js, leak) {
			t.Errorf("pipeline leaked %q\n%s", leak, js)
		}
	}
	// ...but the diagnostic error code+message survive.
	if !strings.Contains(js, "timeout") || !strings.Contains(js, "exceeded 30s") {
		t.Errorf("diagnostic error code/message dropped\n%s", js)
	}
}

func TestRunSnapshotFromRecords_NoResult(t *testing.T) {
	t.Parallel()
	// A node still running / with no Result must not panic and carries no output.
	rs := support.RunSnapshotFromRecords(
		core.JobRecord{ID: "run-1", EnqueuedAt: time.Unix(1_700_000_000, 0)},
		[]core.JobRecord{{NodeID: "n1", Status: core.JobStatusRunning}},
	)
	if rs.Error != nil {
		t.Errorf("no graph result → nil error, got %+v", rs.Error)
	}
	if len(rs.Nodes) != 1 || rs.Nodes[0].Output != nil || rs.Nodes[0].Error != nil {
		t.Errorf("node with no result should carry no output/error: %+v", rs.Nodes)
	}
}
