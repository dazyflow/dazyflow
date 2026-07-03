// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func bundleRec(id, tenant string, at time.Time) core.SupportBundleRecord {
	return core.SupportBundleRecord{
		ID:        id,
		Tenant:    tenant,
		FlowID:    "daily-invoice",
		Mode:      core.RedactStructureOnly,
		Payload:   []byte(`{"mode":"structure_only"}`),
		CreatedBy: "agent-a",
		CreatedAt: at,
	}
}

func TestMemBundleStore_CreateGetList(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	s := NewMemBundleStore()

	if err := s.Create(ctx, bundleRec("b1", "acme", now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(ctx, bundleRec("b2", "acme", now.Add(time.Minute))); err != nil {
		t.Fatalf("create b2: %v", err)
	}
	if err := s.Create(ctx, bundleRec("b3", "beta", now)); err != nil {
		t.Fatalf("create b3: %v", err)
	}

	got, err := s.Get(ctx, "b1")
	if err != nil || got.FlowID != "daily-invoice" {
		t.Fatalf("get b1: %+v err=%v", got, err)
	}

	list, _ := s.ListForTenant(ctx, "acme")
	if len(list) != 2 {
		t.Fatalf("want 2 acme bundles, got %d", len(list))
	}
	if list[0].ID != "b2" { // newest first
		t.Errorf("want b2 first (newest), got %s", list[0].ID)
	}
}

func TestMemBundleStore_Errors(t *testing.T) {
	ctx := context.Background()
	s := NewMemBundleStore()
	_ = s.Create(ctx, bundleRec("b1", "acme", time.Unix(1_700_000_000, 0)))

	if err := s.Create(ctx, bundleRec("b1", "acme", time.Unix(1_700_000_000, 0))); !errors.Is(err, errBundleExists) {
		t.Errorf("duplicate create should fail, got %v", err)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get missing should be ErrNotFound, got %v", err)
	}
	if err := s.Create(ctx, core.SupportBundleRecord{}); err == nil {
		t.Error("create with empty id should fail")
	}
}
