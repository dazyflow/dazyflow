package daemon

import (
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestSecretProviderSchemesAndConstructors(t *testing.T) {
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	if es.Scheme() != "secret" {
		t.Errorf("encrypted scheme = %q", es.Scheme())
	}

	aws := NewAwsSecretsProviderForStore(es, 5*time.Second)
	if aws.Scheme() != "aws" {
		t.Errorf("aws scheme = %q", aws.Scheme())
	}
	gcp := NewGcpSecretsProviderForStore(es, 5*time.Second)
	if gcp.Scheme() != "gcp" {
		t.Errorf("gcp scheme = %q", gcp.Scheme())
	}
	vault := NewVaultProviderForStore(es, 5*time.Second)
	if vault.Scheme() != "vault" {
		t.Errorf("vault scheme = %q", vault.Scheme())
	}
}

func TestTruncateForError(t *testing.T) {
	short := []byte("hello")
	if got := truncateForError(short); got != "hello" {
		t.Errorf("short = %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	if got := truncateForError(long); len(got) != 200 {
		t.Errorf("long truncated to %d bytes, want 200", len(got))
	}
}

func TestRunViewHelpers(t *testing.T) {
	if durationMS(nil, nil) != 0 {
		t.Error("durationMS(nil,nil) != 0")
	}
	start := time.Unix(0, 0)
	end := start.Add(1500 * time.Millisecond)
	if got := durationMS(&start, &end); got != 1500 {
		t.Errorf("durationMS = %d, want 1500", got)
	}

	if resultError(nil) != nil {
		t.Error("resultError(nil) != nil")
	}
	je := &core.JobError{Code: "boom", Message: "x"}
	if resultError(&core.Result{Error: je}) != je {
		t.Error("resultError did not return the embedded error")
	}

	// newSSETerminalView maps the terminal event fields.
	v := newSSETerminalView(&TerminalEvent{JobID: "r1", Status: core.JobStatusFailed, Error: je})
	if v.RunID != "r1" || v.Status != core.JobStatusFailed || v.Error != je {
		t.Errorf("sse view = %+v", v)
	}

	// newRunView falls back to EnqueuedAt for duration when StartedAt is nil.
	enq := time.Unix(100, 0)
	fin := enq.Add(2 * time.Second)
	rv := newRunView(core.JobRecord{
		ID: "r2", Tenant: "t", Workspace: "ws", GraphID: "g",
		Status: core.JobStatusSucceeded, EnqueuedAt: enq, FinishedAt: &fin,
	})
	if rv.FlowID != "t/ws/g" || rv.DurationMS != 2000 {
		t.Errorf("run view = %+v", rv)
	}
}
