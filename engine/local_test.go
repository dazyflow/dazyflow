package engine

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeModule plays the module side of the protocol over io pipes.
type fakeModule struct {
	manifest core.Manifest
	handle   func(execute map[string]any, send func(msg map[string]any)) map[string]any
}

func (m *fakeModule) run(stdin io.Reader, stdout io.Writer) {
	dec := json.NewDecoder(bufio.NewReader(stdin))
	enc := json.NewEncoder(stdout)
	send := func(msg map[string]any) { _ = enc.Encode(msg) }

	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return
		}
		switch head.Type {
		case "hello":
			envelope := map[string]any{"type": "manifest"}
			// Re-encode the manifest fields into envelope.
			mj, _ := json.Marshal(m.manifest)
			var fields map[string]any
			_ = json.Unmarshal(mj, &fields)
			for k, v := range fields {
				envelope[k] = v
			}
			send(envelope)
		case "execute":
			var exec map[string]any
			_ = json.Unmarshal(raw, &exec)
			result := m.handle(exec, send)
			send(result)
		}
	}
}

func TestRunProtocol_HappyPath(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	m := &fakeModule{
		manifest: core.Manifest{ID: "echo", Version: "1.0"},
		handle: func(exec map[string]any, send func(map[string]any)) map[string]any {
			send(map[string]any{
				"type":    "progress",
				"job_id":  exec["job_id"],
				"node_id": "n",
				"message": "working",
			})
			return map[string]any{
				"type":   "result",
				"job_id": exec["job_id"],
				"status": "ok",
				"output": map[string]any{
					"out": map[string]any{"ref": "echoed"},
				},
			}
		},
	}
	go m.run(stdinR, stdoutW)
	defer stdinW.Close()
	defer stdoutR.Close()

	progress := make(chan core.Progress, 4)
	job := core.Job{ID: "j1", NodeID: "n", Params: map[string]any{}}
	result, err := runProtocol(t.Context(), job, stdinW, stdoutR, progress, "echo")
	close(progress)
	if err != nil {
		t.Fatalf("runProtocol: %v", err)
	}
	if result.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok", result.Status)
	}
	if result.Output["out"].Ref != "echoed" {
		t.Errorf("out.ref = %q, want echoed", result.Output["out"].Ref)
	}

	var got []core.Progress
	for p := range progress {
		got = append(got, p)
	}
	if len(got) != 1 || got[0].Message != "working" {
		t.Errorf("progress events = %+v", got)
	}
}

func TestRunProtocol_ManifestIDMismatch(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	m := &fakeModule{manifest: core.Manifest{ID: "actual"}}
	go m.run(stdinR, stdoutW)
	defer stdinW.Close()
	defer stdoutR.Close()

	_, err := runProtocol(t.Context(), core.Job{ID: "j1"}, stdinW, stdoutR, nil, "expected")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("err = %v", err)
	}
}

func TestRunProtocol_ModuleReturnsError(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	m := &fakeModule{
		manifest: core.Manifest{ID: "bad"},
		handle: func(exec map[string]any, _ func(map[string]any)) map[string]any {
			return map[string]any{
				"type":   "result",
				"job_id": exec["job_id"],
				"status": "error",
				"error":  map[string]any{"code": "boom", "message": "intentional"},
			}
		},
	}
	go m.run(stdinR, stdoutW)
	defer stdinW.Close()
	defer stdoutR.Close()

	result, err := runProtocol(t.Context(), core.Job{ID: "j1"}, stdinW, stdoutR, nil, "bad")
	if err != nil {
		t.Fatalf("runProtocol unexpected err: %v", err)
	}
	if result.Status != core.StatusError {
		t.Fatalf("status = %q, want error", result.Status)
	}
	if result.Error == nil || result.Error.Code != "boom" {
		t.Errorf("error = %+v", result.Error)
	}
}
