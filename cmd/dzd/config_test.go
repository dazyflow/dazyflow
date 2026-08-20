// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

func TestEnvStrCov(t *testing.T) {
	t.Setenv("DZ_COV_STR", "value")
	if got := envStr("DZ_COV_STR", "def"); got != "value" {
		t.Errorf("envStr set = %q, want value", got)
	}
	t.Setenv("DZ_COV_STR", "")
	if got := envStr("DZ_COV_STR", "def"); got != "def" {
		t.Errorf("envStr empty = %q, want def", got)
	}
	if got := envStr("DZ_COV_UNSET", "fallback"); got != "fallback" {
		t.Errorf("envStr unset = %q, want fallback", got)
	}
}

func TestEnvBoolCov(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "On"}
	for _, v := range truthy {
		t.Setenv("DZ_COV_BOOL", v)
		if !envBool("DZ_COV_BOOL", false) {
			t.Errorf("envBool(%q) = false, want true", v)
		}
	}
	falsy := []string{"0", "false", "no", "OFF"}
	for _, v := range falsy {
		t.Setenv("DZ_COV_BOOL", v)
		if envBool("DZ_COV_BOOL", true) {
			t.Errorf("envBool(%q) = true, want false", v)
		}
	}
	t.Setenv("DZ_COV_BOOL", "garbage")
	if !envBool("DZ_COV_BOOL", true) {
		t.Error("envBool(garbage) should return default true")
	}
	if envBool("DZ_COV_BOOL", false) {
		t.Error("envBool(garbage) should return default false")
	}
	if !envBool("DZ_COV_UNSET_BOOL", true) {
		t.Error("envBool(unset) should return default")
	}
}

func TestEnvDurationCov(t *testing.T) {
	t.Setenv("DZ_COV_DUR", "1500ms")
	if got := envDuration("DZ_COV_DUR", time.Second); got != 1500*time.Millisecond {
		t.Errorf("envDuration valid = %v, want 1.5s", got)
	}
	t.Setenv("DZ_COV_DUR", "not-a-duration")
	if got := envDuration("DZ_COV_DUR", 3*time.Second); got != 3*time.Second {
		t.Errorf("envDuration invalid = %v, want default 3s", got)
	}
	if got := envDuration("DZ_COV_UNSET_DUR", 5*time.Second); got != 5*time.Second {
		t.Errorf("envDuration unset = %v, want default 5s", got)
	}
}

func TestDSNSSLModeCov(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"postgres://u:p@h/db?sslmode=require", "require"},
		{"postgresql://u:p@h/db?sslmode=VERIFY-FULL", "verify-full"},
		{"postgres://u:p@h/db", ""},
		{"host=db user=u sslmode=Disable", "disable"},
		{"host=db user=u password=p", ""},
		{"", ""},
		{"::::not a url::::", ""},
	}
	for _, tc := range cases {
		if got := dsnSSLMode(tc.dsn); got != tc.want {
			t.Errorf("dsnSSLMode(%q) = %q, want %q", tc.dsn, got, tc.want)
		}
	}
}

func TestValidateProductionConfig_DevModeWarnsOnly(t *testing.T) {
	// In dev mode, even an all-insecure config must NOT call log.Fatal; the
	// function should return normally after logging warnings.
	defaultDSN := "postgres://dazyflow:dazyflow@db:5432/dazyflow?sslmode=disable"
	validateProductionConfig(true, false, defaultDSN, "", "")

	// A clean config returns immediately regardless of dev flag (no problems).
	safeDSN := "postgres://dazyflow:s3cret@db:5432/dazyflow?sslmode=require"
	strongKey := "c3Ryb25nLTMyLWJ5dGUta2V5LWZvci10ZXN0aW5nLW9rIQ=="
	validateProductionConfig(false, false, safeDSN, strongKey, "")
}

func TestRegisterMCPServers_ParseErrors(t *testing.T) {
	cat := mcp.NewCatalog()
	// Whitespace-only and empty-segment specs are no-ops (no registration,
	// so no live connection attempted).
	if err := registerMCPServers(cat, "  ;  ; "); err != nil {
		t.Errorf("registerMCPServers(whitespace) = %v, want nil", err)
	}
	// Empty command after the equals sign is a parse error caught before any
	// connection attempt.
	if err := registerMCPServers(cat, "bad="); err == nil {
		t.Error("registerMCPServers(name=) should error on empty command")
	}
}

func TestRegisterRemotes_ParseErrors(t *testing.T) {
	cat := engine.NewRemoteCatalog()
	// Whitespace-only / empty-segment specs register nothing (dev or not).
	if err := registerRemotes(cat, " , , ", true); err != nil {
		t.Errorf("registerRemotes(whitespace) = %v, want nil", err)
	}
	// Missing '=' is a parse error caught before any dial (dev mode, so the
	// cleartext guard doesn't short-circuit it first).
	if err := registerRemotes(cat, "no-equals", true); err == nil {
		t.Error("registerRemotes(no-equals) should error")
	}
}

// TestRegisterRemotes_RefusesCleartextInProd pins the fail-closed guard: the
// flag-based remote spec is plaintext gRPC, so outside dev mode a non-empty
// spec must be refused before anything is dialed (no secrets on the wire).
func TestRegisterRemotes_RefusesCleartextInProd(t *testing.T) {
	cat := engine.NewRemoteCatalog()
	if err := registerRemotes(cat, "mod=10.0.0.5:9000", false); err == nil {
		t.Error("registerRemotes(prod, non-empty) = nil, want cleartext refusal")
	}
	// An empty spec is always fine — nothing to dial.
	if err := registerRemotes(cat, "", false); err != nil {
		t.Errorf("registerRemotes(prod, empty) = %v, want nil", err)
	}
}

func TestWaitForGroupCov(t *testing.T) {
	// Already-done group returns true promptly.
	var wg sync.WaitGroup
	if !waitForGroup(&wg, time.Second) {
		t.Error("waitForGroup on empty group should return true")
	}

	// A group that never completes within the timeout returns false.
	var blocked sync.WaitGroup
	blocked.Add(1)
	if waitForGroup(&blocked, 20*time.Millisecond) {
		t.Error("waitForGroup should time out and return false")
	}
	blocked.Done() // release the goroutine waiting inside.
}

func TestSetupEncryptedSecrets_EmptyKeyReturnsNil(t *testing.T) {
	if es := setupEncryptedSecrets(t.Context(), "", nil, nil); es != nil {
		t.Errorf("setupEncryptedSecrets(empty key) = %v, want nil", es)
	}
}

func TestSetupOAuth_PrereqsMissingReturnsNil(t *testing.T) {
	if reg := setupOAuth(nil, "https://example.com"); reg != nil {
		t.Error("setupOAuth(nil secrets) should return nil")
	}
	// Non-nil secrets pointer but empty base URL also yields nil.
	if reg := setupOAuth(&daemon.EncryptedSecrets{}, ""); reg != nil {
		t.Error("setupOAuth(empty base URL) should return nil")
	}
}

func TestApplyNetworkPolicy_DevModeNoFatal(t *testing.T) {
	// Empty allowlist + dev mode avoids the fatal path and the advisory log;
	// exercises the env-driven branches without touching the network.
	t.Setenv("DAZYFLOW_ALLOW_PRIVATE_EGRESS", "0")
	t.Setenv("DAZYFLOW_EGRESS_RATE_PER_MIN", "")
	applyNetworkPolicy("", true)
}
