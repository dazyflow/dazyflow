// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// testDrop registers a no-op native drop with the given id into a fresh
// registry and returns the resolver wrapping it.
func dropGateResolver(t *testing.T, id string, gate func(ctx context.Context, dropID, tenant string) error) *NodeResolver {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       id,
			Summary:  "test drop for the killswitch resolver test",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return &NodeResolver{Native: reg, DropGate: gate}
}

func TestResolve_NoDropGate_Resolves(t *testing.T) {
	r := dropGateResolver(t, "test_drop", nil)
	if _, err := r.Resolve(context.Background(), "test_drop"); err != nil {
		t.Fatalf("Resolve without gate: %v", err)
	}
}

func TestResolve_DropGateBlocks(t *testing.T) {
	r := dropGateResolver(t, "test_drop", func(_ context.Context, dropID, _ string) error {
		if dropID == "test_drop" {
			return core.ErrOrgSuspended // any non-nil error refuses the drop
		}
		return nil
	})
	_, err := r.Resolve(context.Background(), "test_drop")
	if err == nil {
		t.Fatal("expected Resolve to be refused by the DropGate, got nil")
	}
}

// TestResolve_DropGateSeesTenant confirms the gate receives the executing
// tenant from the context the engine sets via core.WithTenant.
func TestResolve_DropGateSeesTenant(t *testing.T) {
	var seen string
	r := dropGateResolver(t, "test_drop", func(_ context.Context, _, tenant string) error {
		seen = tenant
		return nil
	})
	ctx := core.WithTenant(context.Background(), "org_abc")
	if _, err := r.Resolve(ctx, "test_drop"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if seen != "org_abc" {
		t.Fatalf("gate saw tenant %q, want org_abc", seen)
	}
}

// TestResolve_UnknownIDStillUnknown confirms an unknown id reports
// "no transport" even with a gate present (the gate runs after lookup).
func TestResolve_UnknownIDStillUnknown(t *testing.T) {
	r := dropGateResolver(t, "test_drop", func(_ context.Context, _, _ string) error { return nil })
	_, err := r.Resolve(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "no transport") {
		t.Fatalf("want no-transport error, got %v", err)
	}
}
