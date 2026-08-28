// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// nodeOutcome captures a RunNode call's result, plus whether it panicked or
// ran past a watchdog (a hang).
type nodeOutcome struct {
	result   core.Result
	err      error
	panicVal any
	stack    string
	timedOut bool
}

// runNodeSafely drives e.RunNode in a goroutine with panic recovery and a
// watchdog, so an adversarial graph that crashes or hangs the template/secret
// pipeline is reported as a test failure rather than taking down the binary.
func runNodeSafely(e *Engine, g core.Graph, nodeID string, prior map[string]core.Result, budget time.Duration) nodeOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	done := make(chan struct{})
	var out nodeOutcome
	go func() {
		defer func() {
			if p := recover(); p != nil {
				out.panicVal = p
				out.stack = string(debug.Stack())
			}
			close(done)
		}()
		out.result, out.err = e.RunNode(ctx, g, "run-adv", nodeID, "rec-adv", prior, nil)
	}()
	select {
	case <-done:
	case <-time.After(budget + 8*time.Second):
		out.timedOut = true
	}
	return out
}

// sinkDrop is a no-op that simply succeeds — used as the downstream node whose
// params carry adversarial templates, so the interesting work is the engine's
// pre-Execute template resolution, not the drop itself.
func sinkDrop() NativeDrop {
	return NativeDrop{
		Manifest: core.Manifest{
			ID:       "sink",
			Summary:  "Test sink.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Inputs:   []core.Port{{Port: "in"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK}, nil
		},
	}
}

// TestRunNode_AdversarialUpstreamTemplates proves the ${upstream.…} resolver
// never panics or hangs, whatever path syntax or payload shape an upstream
// node produced — malformed paths degrade to a clean error, pathological
// payloads resolve in bounded time.
func TestRunNode_AdversarialUpstreamTemplates(t *testing.T) {
	e := newEngineWith(t, sinkDrop())

	// A deeply nested and a very wide upstream payload — JSON-stringified
	// when a template references the whole value.
	deep := any("leaf")
	for i := 0; i < 2000; i++ {
		deep = map[string]any{"k": deep}
	}
	wide := make([]any, 100000)
	for i := range wide {
		wide[i] = i
	}
	prior := map[string]core.Result{
		"src": {Status: core.StatusOK, Output: map[string]core.Ref{
			"out":  {Inline: map[string]any{"name": "ok", "list": []any{1, 2, 3}}},
			"deep": {Inline: deep},
			"wide": {Inline: wide},
		}},
	}

	templates := []string{
		"${upstream.}",                                           // empty path
		"${upstream.src}",                                        // node only, no port
		"${upstream.nope.out}",                                   // unknown node
		"${upstream.src.missing}",                                // unknown port
		"${upstream.src.out.name}",                               // valid nested
		"${upstream.src.out.list[0]}",                            // valid index
		"${upstream.src.out.list[999999999]}",                    // out of range
		"${upstream.src.out.list[-1]}",                           // negative
		"${upstream.src.out.list[abc]}",                          // non-numeric index
		"${upstream.src.out.list[[[}",                            // unclosed bracket
		"${upstream.src.out.name.deeper.more}",                   // descend into a string
		"${upstream.src.deep}",                                   // huge JSON stringify
		"${upstream.src.wide}",                                   // 100k-element stringify
		"${upstream.src.out" + strings.Repeat(".x", 10000) + "}", // 10k-segment path
		"prefix ${upstream.src.out.name} and ${upstream.src.out.list[0]} suffix",
	}

	for _, tmpl := range templates {
		tmpl := tmpl
		t.Run(short(tmpl), func(t *testing.T) {
			g := core.Graph{
				ID: "g", Tenant: "t",
				Nodes: []core.Node{{ID: "dst", Module: "sink", Params: map[string]any{"x": tmpl}}},
			}
			out := runNodeSafely(e, g, "dst", prior, 2*time.Second)
			if out.panicVal != nil {
				t.Fatalf("PANIC resolving %q: %v\n%s", tmpl, out.panicVal, out.stack)
			}
			if out.timedOut {
				t.Fatalf("HANG resolving %q", tmpl)
			}
			// Either it resolved (StatusOK) or it surfaced a structured
			// error — never a crash.
			if out.err == nil && out.result.Status == "" {
				t.Errorf("empty status for %q", tmpl)
			}
		})
	}
}

// echoSecretDrop deliberately leaks its resolved `token` param into every
// output shape we can think of: a nested inline value, an array element, the
// external Ref string, a map KEY, and both error fields. RunNode must scrub
// the plaintext from all of them.
func echoSecretDrop() NativeDrop {
	return NativeDrop{
		Manifest: core.Manifest{
			ID:       "echo_secret",
			Summary:  "Test secret echo.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Inputs:   []core.Port{{Port: "in"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			tok, _ := job.Params["token"].(string)
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusError,
				Output: map[string]core.Ref{
					"out": {
						Ref: "audit-log-" + tok + ".txt",
						Inline: map[string]any{
							"echoed":   tok,
							"prose":    "called the api with " + tok + " just now",
							"nested":   []any{"safe", map[string]any{"deep": tok}},
							tok:        "secret-used-as-a-map-key",
							"safe_key": "nothing to see",
						},
					},
				},
				Error: &core.JobError{
					Code:    "boom",
					Message: "auth failed using " + tok,
					Details: "Authorization: Bearer " + tok,
				},
			}, nil
		},
	}
}

// TestRunNode_SecretRedactionDefenseInDepth runs the full RunNode pipeline
// (resolve ${secret.api} → Execute echoes it everywhere → redact) and asserts
// the plaintext secret survives nowhere in the persisted Result — including
// when a malicious drop uses it as a map key.
func TestRunNode_SecretRedactionDefenseInDepth(t *testing.T) {
	const secret = "sk_live_aVeryRealLookingSecretValue_01234"
	e := newEngineWith(t, echoSecretDrop())
	e.Secrets = newProviders(stubProvider{scheme: "secret", values: map[string]string{"api": secret}})

	g := core.Graph{
		ID: "g", Tenant: "acme",
		Nodes: []core.Node{{ID: "n", Module: "echo_secret", Params: map[string]any{"token": "${secret.api}"}}},
	}
	out := runNodeSafely(e, g, "n", nil, 2*time.Second)
	if out.panicVal != nil {
		t.Fatalf("PANIC: %v\n%s", out.panicVal, out.stack)
	}
	blob, _ := json.Marshal(out.result)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("secret plaintext survived RunNode redaction:\n%s", blob)
	}
}

// TestRunNode_TransformedSecretIsNotRedacted documents a deliberate limit:
// substring redaction cannot catch a secret a drop has *transformed* (e.g.
// base64-encoded), because the plaintext no longer appears. This is by design
// — such cases are the save-time secret-to-output lint's job, not the runtime
// scrubber's. The test pins the boundary so a future reader knows it's
// intentional, not an oversight.
func TestRunNode_TransformedSecretIsNotRedacted(t *testing.T) {
	const secret = "sk_live_aVeryRealLookingSecretValue_01234"
	e := newEngineWith(t, NativeDrop{
		Manifest: core.Manifest{
			ID:       "b64_secret",
			Summary:  "Test transformed secret.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Inputs:   []core.Port{{Port: "in"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			tok, _ := job.Params["token"].(string)
			// Reverse the string — a stand-in for any transform (base64,
			// hashing, chunking) that the substring scrubber can't see through.
			r := []rune(tok)
			for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
				r[i], r[j] = r[j], r[i]
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Inline: string(r)}}}, nil
		},
	})
	e.Secrets = newProviders(stubProvider{scheme: "secret", values: map[string]string{"api": secret}})
	g := core.Graph{ID: "g", Tenant: "acme",
		Nodes: []core.Node{{ID: "n", Module: "b64_secret", Params: map[string]any{"token": "${secret.api}"}}}}
	out := runNodeSafely(e, g, "n", nil, 2*time.Second)
	if out.panicVal != nil {
		t.Fatalf("PANIC: %v", out.panicVal)
	}
	// The transformed value is present (redaction can't see it) but the
	// raw plaintext is NOT — the transform is what protects it here.
	blob, _ := json.Marshal(out.result)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("raw secret leaked (transform should have hidden it): %s", blob)
	}
}

func short(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}
