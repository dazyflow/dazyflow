// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"errors"
	"testing"
)

func TestInlineToBytes(t *testing.T) {
	if b, err := inlineToBytes([]byte("raw")); err != nil || string(b) != "raw" {
		t.Errorf("[]byte: %q %v", b, err)
	}
	if b, err := inlineToBytes("str"); err != nil || string(b) != "str" {
		t.Errorf("string: %q %v", b, err)
	}
	if b, err := inlineToBytes(map[string]any{"k": "v"}); err != nil || string(b) != `{"k":"v"}` {
		t.Errorf("map: %q %v", b, err)
	}
}

func TestSetQuotaReserver(t *testing.T) {
	t.Cleanup(func() { SetQuotaReserver(nil) }) // don't leak into other tests

	rel, err := reserveQuota("t", 100)
	if err != nil {
		t.Fatalf("nil reserver should succeed: %v", err)
	}
	rel()

	var gotTenant string
	var gotN int64
	SetQuotaReserver(func(tenant string, n int64) (func(), error) {
		gotTenant, gotN = tenant, n
		return func() {}, nil
	})
	if _, err := reserveQuota("acme", 512); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if gotTenant != "acme" || gotN != 512 {
		t.Errorf("reserver saw (%q, %d), want (acme, 512)", gotTenant, gotN)
	}

	SetQuotaReserver(func(string, int64) (func(), error) { return nil, errors.New("over") })
	if _, err := reserveQuota("acme", 1); err == nil {
		t.Error("reserve error should propagate")
	}
}
