package main

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/mcp"
)

func TestParseSize(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"512", 512},
		{"10K", 10 * 1024},
		{"10k", 10 * 1024},
		{"2M", 2 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"1T", 1024 * 1024 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024}, // trailing B is allowed
		{"512b", 512},
	}
	for _, c := range ok {
		got, err := parseSize(c.in)
		if err != nil {
			t.Errorf("parseSize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1.5M", "M", "-"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) = nil error, want error", bad)
		}
	}
}

func TestParseQuotaSpec(t *testing.T) {
	got, err := parseQuotaSpec("acme=10MB,globex=1G")
	if err != nil {
		t.Fatalf("parseQuotaSpec: %v", err)
	}
	if got["acme"] != 10*1024*1024 || got["globex"] != 1024*1024*1024 {
		t.Errorf("got %v", got)
	}
	// Empty spec → nil map, no error.
	if m, err := parseQuotaSpec("  "); err != nil || m != nil {
		t.Errorf("empty spec = (%v, %v), want (nil, nil)", m, err)
	}
	// Trailing/empty entries are skipped.
	if m, err := parseQuotaSpec("acme=1K,"); err != nil || len(m) != 1 {
		t.Errorf("trailing comma = (%v, %v), want one entry", m, err)
	}
	for _, bad := range []string{"acme", "acme=", "acme=bad"} {
		if _, err := parseQuotaSpec(bad); err == nil {
			t.Errorf("parseQuotaSpec(%q) = nil error, want error", bad)
		}
	}
}

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
	// Empty spec is a no-op success.
	if err := registerRemotes(cat, "  "); err != nil {
		t.Errorf("empty spec: %v", err)
	}
	// Malformed entry (no '=') is an error.
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
