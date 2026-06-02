package containerdrop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
)

// stubDoer is the host HTTP client the broker mediates fetch through.
type stubDoer struct {
	status int
	body   string
	gotURL string
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.gotURL = req.URL.String()
	st := d.status
	if st == 0 {
		st = 200
	}
	b := d.body
	if b == "" {
		b = "PONG"
	}
	return &http.Response{StatusCode: st, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(b))}, nil
}

// memFS is a tiny in-memory FileStore.
type memFS struct{ m map[string][]byte }

func (f *memFS) Read(p string) ([]byte, error) {
	b, ok := f.m[p]
	if !ok {
		return nil, fmt.Errorf("not found: %s", p)
	}
	return b, nil
}
func (f *memFS) Write(p string, d []byte) error { f.m[p] = append([]byte(nil), d...); return nil }
func (f *memFS) Exists(p string) (bool, error)  { _, ok := f.m[p]; return ok, nil }

func testHost(doer *stubDoer, fs *memFS) Host {
	return Host{
		HTTP: doer,
		Token: func(_ context.Context, provider, account string) (string, error) {
			return "tok-" + provider + "-" + account, nil
		},
		Files: func(_ core.Job) jsdrop.FileStore { return fs },
	}
}

// The headline test: a drop (run in-process via the fake runner) reaches every
// capability over the real unix-socket broker, and its result flows back as a
// core.Result. Proves the run→capability→output loop without any container tech.
func TestContainerDrop_BrokerCapabilityLoop(t *testing.T) {
	doer := &stubDoer{body: "PONG"}
	fs := &memFS{m: map[string][]byte{}}

	drop := RunnerFunc(func(ctx context.Context, sock string, _ DropRef) error {
		c := NewClient(sock)
		job, err := c.Job(ctx)
		if err != nil {
			return err
		}
		status, ok, _, body, err := c.Fetch(ctx, FetchRequest{URL: "https://api.example.test/x", Method: "GET"})
		if err != nil {
			return err
		}
		secret, err := c.Secret(ctx, "API_KEY")
		if err != nil {
			return err
		}
		_, deniedErr := c.Secret(ctx, "NOT_GRANTED")
		token, err := c.Token(ctx, "google", "default")
		if err != nil {
			return err
		}
		if err := c.WriteFile(ctx, "scratch://note.txt", []byte("hi")); err != nil {
			return err
		}
		readBack, err := c.ReadFile(ctx, "scratch://note.txt")
		if err != nil {
			return err
		}
		_ = c.Log(ctx, "info", "drop ran")
		return c.Result(ctx, map[string]any{"out": map[string]any{
			"param":    job.Params["p"],
			"http_ok":  ok,
			"status":   status,
			"body":     string(body),
			"secret":   secret,
			"denied":   deniedErr != nil,
			"token":    token,
			"file":     string(readBack),
			"env_seen": job.Env["FOO"],
		}})
	})

	tr := NewTransport(core.Manifest{ID: "t"}, DropRef{ID: "t"}, drop, testHost(doer, fs))
	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"p": "hello"},
		Env:    map[string]string{"secret:API_KEY": "sk-1", "FOO": "bar"},
	}, make(chan core.Progress, 8))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%v error=%+v", res.Status, res.Error)
	}
	out, ok := res.Output["out"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("output shape: %#v", res.Output["out"])
	}
	checks := map[string]any{
		"param": "hello", "http_ok": true, "body": "PONG", "secret": "sk-1",
		"denied": true, "token": "tok-google-default", "file": "hi", "env_seen": "bar",
	}
	for k, want := range checks {
		if out[k] != want {
			t.Errorf("out[%q] = %#v, want %#v", k, out[k], want)
		}
	}
	if doer.gotURL != "https://api.example.test/x" {
		t.Errorf("fetch hit %q via the broker, want the requested URL", doer.gotURL)
	}
}

// A drop that reports a typed failure surfaces as a JobError with its code.
func TestContainerDrop_DropError(t *testing.T) {
	drop := RunnerFunc(func(ctx context.Context, sock string, _ DropRef) error {
		return NewClient(sock).Fail(ctx, "teapot", "I am a teapot")
	})
	tr := NewTransport(core.Manifest{ID: "t"}, DropRef{ID: "t"}, drop, Host{})
	res, _ := tr.Execute(context.Background(), core.Job{ID: "j"}, make(chan core.Progress, 1))
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "teapot" || res.Error.Message != "I am a teapot" {
		t.Fatalf("error result = %+v", res.Error)
	}
}

// A drop that exits without reporting a result is a clean no_result error, not a hang.
func TestContainerDrop_NoResult(t *testing.T) {
	drop := RunnerFunc(func(_ context.Context, _ string, _ DropRef) error { return nil })
	tr := NewTransport(core.Manifest{ID: "t"}, DropRef{ID: "t"}, drop, Host{})
	res, _ := tr.Execute(context.Background(), core.Job{ID: "j"}, make(chan core.Progress, 1))
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "no_result" {
		t.Fatalf("expected no_result, got %+v", res.Error)
	}
}

// A runner that fails to launch the drop surfaces as runner_error.
func TestContainerDrop_RunnerError(t *testing.T) {
	drop := RunnerFunc(func(_ context.Context, _ string, _ DropRef) error { return fmt.Errorf("boom: image pull failed") })
	tr := NewTransport(core.Manifest{ID: "t"}, DropRef{ID: "t"}, drop, Host{})
	res, _ := tr.Execute(context.Background(), core.Job{ID: "j"}, make(chan core.Progress, 1))
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "runner_error" || !strings.Contains(res.Error.Message, "image pull failed") {
		t.Fatalf("expected runner_error, got %+v", res.Error)
	}
}
