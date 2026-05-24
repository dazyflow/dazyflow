package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/daemon"
)

func TestEnvProvider_ResolvesAndRejectsMissing(t *testing.T) {
	t.Setenv("HZ_TEST_SECRET", "value-from-env")
	p := daemon.EnvProvider{}
	got, err := p.Get(t.Context(), "HZ_TEST_SECRET")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "value-from-env" {
		t.Errorf("got %q, want value-from-env", got)
	}

	if _, err := p.Get(t.Context(), "DEFINITELY_NOT_SET_12345"); err == nil {
		t.Error("expected error for missing env var")
	}

	t.Setenv("HZ_TEST_EMPTY", "")
	if _, err := p.Get(t.Context(), "HZ_TEST_EMPTY"); err == nil {
		t.Error("expected error for empty env var")
	}
}

func TestEnvProvider_Scheme(t *testing.T) {
	p := daemon.EnvProvider{}
	if s := p.Scheme(); s != "env" {
		t.Errorf("scheme = %q", s)
	}
}

func TestBuiltinProvider_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	contents, _ := json.Marshal(map[string]string{
		"stripe.key": "sk_test_xyz",
		"smtp.pass":  "hunter2",
	})
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := daemon.NewBuiltinProviderFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	v, _ := p.Get(t.Context(), "stripe.key")
	if v != "sk_test_xyz" {
		t.Errorf("stripe.key = %q", v)
	}
	if _, err := p.Get(context.Background(), "nope"); err == nil {
		t.Error("expected error for missing key")
	}
}

func TestBuiltinProvider_BadFile(t *testing.T) {
	if _, err := daemon.NewBuiltinProviderFromFile("/nonexistent/path/here"); err == nil {
		t.Error("expected error for missing file")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("not json"), 0o600)
	_, err := daemon.NewBuiltinProviderFromFile(bad)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v", err)
	}
}

func TestBuiltinProvider_Set(t *testing.T) {
	p := daemon.NewBuiltinProvider()
	p.Set("k", "v")
	v, err := p.Get(t.Context(), "k")
	if err != nil || v != "v" {
		t.Errorf("Get after Set: v=%q err=%v", v, err)
	}
}
