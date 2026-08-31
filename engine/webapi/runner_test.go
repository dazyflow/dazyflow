// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/webapi"
)

// seenDispatch is what the fake dispatcher recorded.
type seenDispatch struct {
	tenant string
	tags   []string
	script string
	shell  string
	stdin  string
	called bool
}

// fakeDispatcher installs a dispatcher that records the task and answers with
// what the machine "printed". Cleared on cleanup — SetDispatcher is
// process-wide, so no test here may run in parallel with another.
func fakeDispatcher(t *testing.T, reply webapi.RunnerResult, err error) *seenDispatch {
	t.Helper()
	var seen seenDispatch
	webapi.SetDispatcher(dispatchFunc(func(ctx context.Context, req webapi.RunnerRequest, onProgress func(string)) (webapi.RunnerResult, error) {
		seen = seenDispatch{
			tenant: req.Tenant, tags: req.Tags, script: req.Script,
			shell: req.Shell, stdin: req.Stdin, called: true,
		}
		return reply, err
	}))
	t.Cleanup(func() { webapi.SetDispatcher(nil) })
	return &seen
}

type dispatchFunc func(context.Context, webapi.RunnerRequest, func(string)) (webapi.RunnerResult, error)

func (f dispatchFunc) Dispatch(ctx context.Context, req webapi.RunnerRequest, onProgress func(string)) (webapi.RunnerResult, error) {
	return f(ctx, req, onProgress)
}

// runnerDescriptor is ordersDescriptor pointed at a private address and set to
// be reached through a runner — the case the whole feature exists for, since the
// daemon refuses to dial that host at all.
func runnerDescriptor() webapi.Descriptor {
	d := ordersDescriptor()
	d.BaseURL = "https://orders.internal.acme.example"
	d.Runner = webapi.RunnerReach{Tags: []string{"linux", "dmz"}}
	return d
}

// replyJSON builds what the script prints on a successful call.
func replyJSON(t *testing.T, status int, contentType, body string) webapi.RunnerResult {
	t.Helper()
	payload := map[string]any{
		"status":   status,
		"headers":  map[string]string{"Content-Type": contentType, "X-Request-Id": "abc123"},
		"body_b64": base64.StdEncoding.EncodeToString([]byte(body)),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	return webapi.RunnerResult{Stdout: string(raw)}
}

func TestRunner_QueuesTheRequestAndReadsTheAnswerBack(t *testing.T) {
	seen := fakeDispatcher(t, replyJSON(t, 200, "application/json", `{"id":"o-1"}`), nil)
	cat := mustRegister(t, runnerDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"order_id": "o-1",
			"expand":   "lines",
			"token":    "tok-abc",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if !seen.called {
		t.Fatal("the runner dispatcher was never called")
	}
	if seen.tenant != "acme" {
		t.Errorf("tenant = %q", seen.tenant)
	}
	if strings.Join(seen.tags, ",") != "linux,dmz" {
		t.Errorf("tags = %v", seen.tags)
	}
	if seen.shell != "python" {
		// The runner agent IS python3, so this is the one interpreter
		// guaranteed to exist on a machine running one.
		t.Errorf("shell = %q, want python", seen.shell)
	}

	// The output ports are identical to the direct path's: everything below the
	// transport swap is shared code and must not learn which one ran.
	if res.Output["status"].Inline != 200 {
		t.Errorf("status port = %v", res.Output["status"].Inline)
	}
	if got := res.Output["response_body"].Inline; got != `{"id":"o-1"}` {
		t.Errorf("response_body = %v", got)
	}
	if got := res.Output["headers"].Inline.(map[string]string)["X-Request-Id"]; got != "abc123" {
		t.Errorf("headers port = %v", res.Output["headers"].Inline)
	}
}

func TestRunner_ScriptCarriesNoCredentialAndTheEnvelopeDoes(t *testing.T) {
	seen := fakeDispatcher(t, replyJSON(t, 200, "application/json", `{}`), nil)
	cat := mustRegister(t, runnerDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	if _, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "o-1", "token": "tok-secret"},
	}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// runner_tasks stores `script` and `stdin` in separate columns and both are
	// read while debugging a queue. Keeping the script constant means the token
	// has exactly ONE place to be, which is the point of putting the whole
	// envelope on stdin rather than templating the request into the program.
	if strings.Contains(seen.script, "tok-secret") {
		t.Error("the credential reached the script text")
	}
	if strings.Contains(seen.script, "orders.internal.acme.example") {
		t.Error("the script is templated per request; it is meant to be a constant")
	}
	if !strings.Contains(seen.stdin, "tok-secret") {
		t.Error("the credential did not reach the runner at all")
	}

	var env struct {
		Method   string            `json:"method"`
		URL      string            `json:"url"`
		Headers  map[string]string `json:"headers"`
		MaxBytes int               `json:"max_bytes"`
	}
	if err := json.Unmarshal([]byte(seen.stdin), &env); err != nil {
		t.Fatalf("stdin is not the JSON envelope: %v", err)
	}
	if env.Method != "GET" {
		t.Errorf("method = %q", env.Method)
	}
	if env.URL != "https://orders.internal.acme.example/orders/o-1" {
		t.Errorf("url = %q", env.URL)
	}
	if env.Headers["Authorization"] != "Bearer tok-secret" {
		t.Errorf("Authorization = %q", env.Headers["Authorization"])
	}
	// The response cap does not survive on its own: the guarded Doer is not in
	// this path, so the script has to re-impose it.
	if env.MaxBytes != webapi.DefaultMaxBodyBytes {
		t.Errorf("max_bytes = %d, want the descriptor's cap", env.MaxBytes)
	}
}

func TestRunner_NoDispatcherNamesTheCatalog(t *testing.T) {
	webapi.SetDispatcher(nil)
	cat := mustRegister(t, runnerDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "o-1", "token": "t"},
	}, nil)
	if err != nil {
		t.Fatalf("Execute returned a transport error: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want an error", res.Status)
	}
	// A deployment with no runners still runs every direct catalog, so this
	// must fail the catalog that asked for one and name it — not fail globally
	// the way an unwired Doer does.
	if !strings.Contains(res.Error.Message, "orders") {
		t.Errorf("message does not name the catalog: %q", res.Error.Message)
	}
}

func TestRunner_UnwiredDoerIsFineWhenTheCatalogUsesARunner(t *testing.T) {
	webapi.SetDoer(nil)
	fakeDispatcher(t, replyJSON(t, 200, "application/json", `{}`), nil)
	cat := mustRegister(t, runnerDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	// The guarded Doer is the daemon's own last mile. A catalog that never uses
	// it must not be held hostage to it being wired.
	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "o-1", "token": "t"},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
}

func TestRunner_FailuresAreReadable(t *testing.T) {
	cases := []struct {
		name  string
		reply webapi.RunnerResult
		err   error
		want  string
	}{{
		// The dispatcher's own message already names the machine.
		name: "nothing matched the tags",
		err:  errors.New("no runner carries all of: linux, dmz"),
		want: "no runner carries",
	}, {
		// Overwhelmingly a runner started with --allow that forbids python;
		// stderr is the only thing that explains it, so it is quoted.
		name:  "the interpreter never ran",
		reply: webapi.RunnerResult{ExitCode: 1, Stderr: `this runner is not allowed to run scripts with "python"`},
		want:  "not allowed to run scripts",
	}, {
		name:  "the call failed inside the network",
		reply: webapi.RunnerResult{Stdout: `{"error":"URLError: [Errno 111] Connection refused"}`},
		want:  "Connection refused",
	}, {
		name:  "the machine printed something else entirely",
		reply: webapi.RunnerResult{Stdout: "Traceback (most recent call last):"},
		want:  "could not read",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDispatcher(t, tc.reply, tc.err)
			cat := mustRegister(t, runnerDescriptor())
			tr := transport(t, cat, "acme", "api:orders:get_order")

			res, err := tr.Execute(context.Background(), core.Job{
				ID:     "j1",
				Params: map[string]any{"order_id": "o-1", "token": "t"},
			}, nil)
			if err != nil {
				t.Fatalf("Execute returned a transport error: %v", err)
			}
			if res.Status != core.StatusError {
				t.Fatalf("status = %q, want an error", res.Status)
			}
			if !strings.Contains(res.Error.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", res.Error.Message, tc.want)
			}
		})
	}
}

func TestRunner_ServiceSaidNoIsAnAnswerNotATransportFailure(t *testing.T) {
	fakeDispatcher(t, replyJSON(t, 404, "application/json", `{"error":"gone"}`), nil)
	cat := mustRegister(t, runnerDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	// expect_status widens what counts as success, exactly as it does for a
	// direct call — so a 404 reached through a runner can be an answer too.
	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "o-1", "token": "t", "expect_status": []any{200, 404}},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if res.Output["status"].Inline != 404 {
		t.Errorf("status port = %v", res.Output["status"].Inline)
	}
}

func TestRunnerTags_NormalizedAndBounded(t *testing.T) {
	got := webapi.NormalizeRunnerTags([]string{"Linux", " dmz ", "linux", "", "DMZ"})
	if strings.Join(got, ",") != "linux,dmz" {
		t.Errorf("NormalizeRunnerTags = %v, want [linux dmz]", got)
	}

	// A tag that skipped normalisation would match no machine, silently — the
	// matching side lower-cases. Validate refuses it rather than letting the
	// catalog register and fail at run time.
	d := runnerDescriptor()
	d.Runner.Tags = []string{"Linux"}
	if err := d.Validate(); err == nil {
		t.Error("Validate accepted an un-normalised tag")
	}

	d = runnerDescriptor()
	d.Runner.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if err := d.Validate(); err == nil {
		t.Error("Validate accepted more tags than the bound allows")
	}
}

// TestRunnerScript_ActuallyPerformsTheCall runs the REAL generated script under
// the real interpreter against a real server.
//
// Every other test here fakes the dispatcher, which proves the Go half and
// nothing about the python. That program is a string constant in a Go file: it
// is never compiled, never linted, and a typo in it fails only on a customer's
// machine, once, at run time. So it gets executed at least once in CI.
//
// Skipped where python3 is absent rather than failing — the agent requires it,
// but a Go build host is not an agent host.
func TestRunnerScript_ActuallyPerformsTheCall(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed; the generated script cannot be exercised here")
	}

	var gotMethod, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"o-9"}`))
	}))
	defer srv.Close()

	// Drive the script the way the agent does: the constant on disk, the
	// envelope on stdin.
	cat := mustRegister(t, func() webapi.Descriptor {
		d := runnerDescriptor()
		d.BaseURL = srv.URL
		return d
	}())
	tr := transport(t, cat, "acme", "api:orders:create_order")

	var script, stdin string
	webapi.SetDispatcher(dispatchFunc(func(ctx context.Context, req webapi.RunnerRequest, _ func(string)) (webapi.RunnerResult, error) {
		script, stdin = req.Script, req.Stdin
		cmd := exec.CommandContext(ctx, python, "-c", script)
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return webapi.RunnerResult{ExitCode: 1, Stdout: stdout.String(), Stderr: stderr.String()}, nil
		}
		return webapi.RunnerResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}))
	t.Cleanup(func() { webapi.SetDispatcher(nil) })

	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"sku": "ABC", "qty": 2, "token": "tok-real",
			"expect_status": []any{201},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if gotMethod != "POST" {
		t.Errorf("server saw method %q", gotMethod)
	}
	if gotAuth != "Bearer tok-real" {
		t.Errorf("server saw Authorization %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"sku"`) {
		t.Errorf("server saw body %q", gotBody)
	}
	if res.Output["status"].Inline != 201 {
		t.Errorf("status port = %v", res.Output["status"].Inline)
	}
	if got := res.Output["response_body"].Inline; got != `{"id":"o-9"}` {
		t.Errorf("response_body = %v", got)
	}
}
