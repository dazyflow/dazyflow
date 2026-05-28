package main

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/mcp"
)

func TestEnvInt(t *testing.T) {
	t.Setenv("HZ_TEST_INT", "42")
	if got := envInt("HZ_TEST_INT", 7); got != 42 {
		t.Errorf("set valid = %d, want 42", got)
	}
	t.Setenv("HZ_TEST_INT", "not-a-number")
	if got := envInt("HZ_TEST_INT", 7); got != 7 {
		t.Errorf("set invalid = %d, want default 7", got)
	}
	if got := envInt("HZ_TEST_UNSET_VAR", 9); got != 9 {
		t.Errorf("unset = %d, want default 9", got)
	}
}

func TestRegisterRemotes_Errors(t *testing.T) {
	cat := engine.NewRemoteCatalog()
	if err := registerRemotes(cat, "  "); err != nil {
		t.Errorf("empty spec: %v", err)
	}
	if err := registerRemotes(cat, "no-equals-sign"); err == nil {
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
