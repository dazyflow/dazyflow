package secrets

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeStore captures writes for inspection. Concurrency-safe so a
// future test that exercises parallel writes doesn't race.
type fakeStore struct {
	mu      sync.Mutex
	written map[string]string // key: tenant|name → value
	err     error             // set to simulate write failure
}

func newFakeStore() *fakeStore {
	return &fakeStore{written: map[string]string{}}
}

func (f *fakeStore) write(_ context.Context, tenant, name, value string) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written[tenant+"|"+name] = value
	return nil
}

func (f *fakeStore) get(tenant, name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.written[tenant+"|"+name]
	return v, ok
}

// withFakeWriter installs a fresh fakeStore and returns a cleanup
// hook that clears the global so tests don't leak into each other.
func withFakeWriter(t *testing.T) *fakeStore {
	t.Helper()
	prev := currentWriter()
	store := newFakeStore()
	SetSecretWriter(store.write)
	t.Cleanup(func() { SetSecretWriter(prev) })
	return store
}

func TestSecretSet_WritesValueFromParam(t *testing.T) {
	store := withFakeWriter(t)
	res, err := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "poll.cursor.daily", "value": "2026-05-26T12:00:00Z"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got, ok := store.get("acme", "poll.cursor.daily")
	if !ok {
		t.Fatal("nothing written")
	}
	if got != "2026-05-26T12:00:00Z" {
		t.Fatalf("written value=%q", got)
	}
	if name, _ := res.Output["name"].Inline.(string); name != "poll.cursor.daily" {
		t.Fatalf("name output=%+v", res.Output["name"])
	}
}

func TestSecretSet_InputPortOverridesParam(t *testing.T) {
	store := withFakeWriter(t)
	res, err := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "cursor", "value": "stale-param"},
		Input: map[string]core.Ref{
			"value": {Inline: "fresh-from-port"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got, _ := store.get("acme", "cursor")
	if got != "fresh-from-port" {
		t.Fatalf("input port should win; got %q", got)
	}
}

func TestSecretSet_InputPortAcceptsBytes(t *testing.T) {
	store := withFakeWriter(t)
	res, err := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "cursor"},
		Input: map[string]core.Ref{
			"value": {Inline: []byte("byte-value")},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got, _ := store.get("acme", "cursor")
	if got != "byte-value" {
		t.Fatalf("got=%q", got)
	}
}

func TestSecretSet_RejectsStructuredInput(t *testing.T) {
	_ = withFakeWriter(t)
	res, err := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "cursor"},
		Input: map[string]core.Ref{
			"value": {Inline: map[string]any{"id": 42}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("expected error result, got %+v", res)
	}
	if res.Error.Code != "bad_input" {
		t.Fatalf("code=%q", res.Error.Code)
	}
	// Message must be friendly (no Go type signatures), details
	// must carry the type info — this is the contract that lets
	// the UI split the two surfaces.
	if strings.Contains(res.Error.Message, "map[string]") {
		t.Fatalf("friendly message leaked Go type: %q", res.Error.Message)
	}
	if !strings.Contains(res.Error.Details, "map[string]") {
		t.Fatalf("details didn't carry Go type: %q", res.Error.Details)
	}
}

func TestSecretSet_RequiresTenant(t *testing.T) {
	_ = withFakeWriter(t)
	res, err := executeSecretSet(t.Context(), core.Job{
		Params: map[string]any{"name": "cursor", "value": "x"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("expected error, got %+v", res)
	}
	if res.Error.Code != "bad_input" {
		t.Fatalf("code=%q", res.Error.Code)
	}
}

func TestSecretSet_RequiresName(t *testing.T) {
	_ = withFakeWriter(t)
	res, _ := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"value": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_input" {
		t.Fatalf("expected bad_input, got %+v", res)
	}
}

func TestSecretSet_RejectsBadName(t *testing.T) {
	_ = withFakeWriter(t)
	for _, name := range []string{"../etc/passwd", "with space", "has/slash", "weird*char"} {
		res, _ := executeSecretSet(t.Context(), core.Job{
			Tenant: "acme",
			Params: map[string]any{"name": name, "value": "x"},
		}, nil)
		if res.Status != core.StatusError {
			t.Fatalf("name %q should be rejected", name)
		}
		if res.Error.Code != "bad_input" {
			t.Fatalf("name %q: code=%q", name, res.Error.Code)
		}
	}
}

func TestSecretSet_RequiresValue(t *testing.T) {
	_ = withFakeWriter(t)
	res, _ := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "cursor"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("expected bad_input, got %+v", res)
	}
}

func TestSecretSet_UnwiredHookIsClearError(t *testing.T) {
	// No withFakeWriter — leave the writer nil to simulate hzd
	// running without --master-key. Save & restore the global so
	// later tests in this file still pass.
	prev := currentWriter()
	SetSecretWriter(nil)
	t.Cleanup(func() { SetSecretWriter(prev) })

	res, _ := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "cursor", "value": "x"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatal("expected error")
	}
	if res.Error.Code != "not_configured" {
		t.Fatalf("code=%q (message=%q)", res.Error.Code, res.Error.Message)
	}
	// Message must point at the fix, not just say "nope".
	if !strings.Contains(res.Error.Message, "--master-key") {
		t.Fatalf("error message should mention the flag: %q", res.Error.Message)
	}
}

func TestSecretSet_WriteFailureSurfacesDetails(t *testing.T) {
	store := withFakeWriter(t)
	store.err = fmt.Errorf("simulated store failure")
	res, _ := executeSecretSet(t.Context(), core.Job{
		Tenant: "acme",
		Params: map[string]any{"name": "cursor", "value": "x"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatal("expected error")
	}
	if res.Error.Code != "write_failed" {
		t.Fatalf("code=%q", res.Error.Code)
	}
	if !strings.Contains(res.Error.Details, "simulated store failure") {
		t.Fatalf("details should carry the underlying error: %q", res.Error.Details)
	}
}

func TestSecretSet_TenantIsolation(t *testing.T) {
	store := withFakeWriter(t)
	// Same name, different tenants — must not collide.
	for _, tenant := range []string{"acme", "globex"} {
		res, _ := executeSecretSet(t.Context(), core.Job{
			Tenant: tenant,
			Params: map[string]any{"name": "cursor", "value": "v-" + tenant},
		}, nil)
		if res.Status != core.StatusOK {
			t.Fatalf("tenant %q: %+v", tenant, res.Error)
		}
	}
	if v, _ := store.get("acme", "cursor"); v != "v-acme" {
		t.Fatalf("acme got %q", v)
	}
	if v, _ := store.get("globex", "cursor"); v != "v-globex" {
		t.Fatalf("globex got %q", v)
	}
}
