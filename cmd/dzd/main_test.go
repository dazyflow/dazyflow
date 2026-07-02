// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

func TestProductionConfigProblems(t *testing.T) {
	const strongKey = "c3Ryb25nLTMyLWJ5dGUta2V5LWZvci10ZXN0aW5nLW9rIQ=="
	const safeDSN = "postgres://dazyflow:s3cret@db:5432/dazyflow?sslmode=require"
	const noTLSDSN = "postgres://dazyflow:s3cret@db:5432/dazyflow" // strong password, sslmode unset
	const defaultDSN = "postgres://dazyflow:dazyflow@db:5432/dazyflow?sslmode=disable"

	cases := []struct {
		name      string
		dsn       string
		masterKey string
		wantCount int
	}{
		{"empty dsn skips dsn checks", "", strongKey, 0},
		{"safe dsn + key is clean", safeDSN, strongKey, 0},
		{"verify-full is clean", "postgres://dazyflow:s3cret@db/dazyflow?sslmode=verify-full", strongKey, 0},
		{"keyword-form sslmode is clean", "host=db user=dazyflow password=s3cret sslmode=require", strongKey, 0},
		{"missing sslmode flagged", noTLSDSN, strongKey, 1},
		{"sslmode=disable flagged", "postgres://dazyflow:s3cret@db/dazyflow?sslmode=disable", strongKey, 1},
		{"default password + disable flagged", defaultDSN, strongKey, 2},
		{"missing master key flagged", safeDSN, "", 1},
		{"all three insecure flagged", defaultDSN, "", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := productionConfigProblems(tc.dsn, tc.masterKey)
			if len(got) != tc.wantCount {
				t.Errorf("problems = %v (n=%d), want %d", got, len(got), tc.wantCount)
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("DZ_TEST_INT", "42")
	if got := envInt("DZ_TEST_INT", 7); got != 42 {
		t.Errorf("set valid = %d, want 42", got)
	}
	t.Setenv("DZ_TEST_INT", "not-a-number")
	if got := envInt("DZ_TEST_INT", 7); got != 7 {
		t.Errorf("set invalid = %d, want default 7", got)
	}
	if got := envInt("DZ_TEST_UNSET_VAR", 9); got != 9 {
		t.Errorf("unset = %d, want default 9", got)
	}
}

func TestRegisterRemotes_Errors(t *testing.T) {
	cat := engine.NewRemoteCatalog()
	if err := registerRemotes(cat, "  ", true); err != nil {
		t.Errorf("empty spec: %v", err)
	}
	if err := registerRemotes(cat, "no-equals-sign", true); err == nil {
		t.Error("malformed remote spec: want error")
	}
}

func TestRegisterMCPServers_Errors(t *testing.T) {
	cat := mcp.NewCatalog()
	if err := registerMCPServers(cat, ""); err != nil {
		t.Errorf("empty spec: %v", err)
	}
	for _, bad := range []string{"noequals", "name="} {
		if err := registerMCPServers(cat, bad); err == nil {
			t.Errorf("registerMCPServers(%q): want error", bad)
		}
	}
}
