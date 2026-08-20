// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
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
			got := productionConfigProblems(false, tc.dsn, tc.masterKey, "")
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

// TestProductionConfigProblems_DevKeyGuard covers the DAZYFLOW_DEV_KEY
// fail-closed rule: the flag mints a publicly-known admin bearer token, so it
// must be refused wherever the deployment doesn't look like somebody's
// laptop. Previously it was guarded only by a line in the docs.
func TestProductionConfigProblems_DevKeyGuard(t *testing.T) {
	const strongKey = "c3Ryb25nLTMyLWJ5dGUta2V5LWZvci10ZXN0aW5nLW9rIQ=="
	localDSN := "postgres://dazyflow:s3cret@localhost:5432/dazyflow?sslmode=require"
	remoteDSN := "postgres://dazyflow:s3cret@db.internal:5432/dazyflow?sslmode=require"

	cases := []struct {
		name              string
		devKey            bool
		dsn               string
		baseURL           string
		wantDevKeyProblem bool
	}{
		{"dev key off is never flagged", false, remoteDSN, "https://app.example.com", false},
		{"local dsn, no public url", true, localDSN, "", false},
		{"local dsn, localhost url", true, localDSN, "http://localhost:8080", false},
		{"loopback ip counts as local", true, "postgres://dazyflow:s3cret@127.0.0.1:5432/dazyflow?sslmode=require", "http://127.0.0.1:8080", false},
		{"public base url is flagged", true, localDSN, "https://app.example.com", true},
		{"remote database is flagged", true, remoteDSN, "", true},
		{"both signals still one problem", true, remoteDSN, "https://app.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := productionConfigProblems(tc.devKey, tc.dsn, strongKey, tc.baseURL)
			var found bool
			for _, p := range got {
				if strings.Contains(p, "DAZYFLOW_DEV_KEY") {
					found = true
				}
			}
			if found != tc.wantDevKeyProblem {
				t.Errorf("dev-key problem = %v, want %v (problems: %v)", found, tc.wantDevKeyProblem, got)
			}
		})
	}
}

func TestHostIsLocal(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"dz.localhost", true},
		{"127.0.0.1", true},
		{"127.5.5.5", true},
		{"::1", true},
		{"[::1]", true},
		{"/var/run/postgresql", true}, // unix socket
		{"db.internal", false},
		{"10.0.0.5", false},
		{"app.example.com", false},
		{"", false},
	} {
		if got := hostIsLocal(tc.host); got != tc.want {
			t.Errorf("hostIsLocal(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
