// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/dazyflow/dazyflow/core"
)

func TestEngine_EmitsSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	defer otel.SetTracerProvider(prev)

	var seenTraceID string
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			seenTraceID = job.TraceID
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {Ref: "x"}}}, nil
		},
	})

	g := core.Graph{
		ID:        "g",
		Tenant:    "acme",
		Workspace: "ws1",
		Nodes:     []core.Node{{ID: "a", Module: "noop"}},
	}
	if _, err := e.Run(t.Context(), g, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) < 2 {
		t.Fatalf("expected ≥2 spans (graph + node); got %d", len(spans))
	}

	var graphSpan, nodeSpan bool
	for _, s := range spans {
		switch s.Name() {
		case "graph.run":
			graphSpan = true
			if attrValue(s, "dazyflow.tenant") != "acme" {
				t.Errorf("graph span missing tenant attr")
			}
		case "node.run":
			nodeSpan = true
			if attrValue(s, "dazyflow.node.module") != "noop" {
				t.Errorf("node span missing module attr")
			}
		}
	}
	if !graphSpan || !nodeSpan {
		t.Errorf("missing spans: graph=%v node=%v", graphSpan, nodeSpan)
	}
	if seenTraceID == "" {
		t.Errorf("expected job.TraceID to be populated from the active span")
	}
}

func attrValue(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	return recorder
}

func nodeSpans(recorder *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == "node.run" {
			out = append(out, s)
		}
	}

	return out
}

// A node that succeeds must leave no error on its span — the span status
// is what a tracing backend surfaces as a failed step.
func TestEngine_SuccessfulNodeSpanHasNoError(t *testing.T) {
	recorder := recordSpans(t)
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: "x"}},
			}, nil
		},
	})
	if _, err := e.Run(t.Context(), core.Graph{
		ID: "g", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	spans := nodeSpans(recorder)
	if len(spans) == 0 {
		t.Fatal("no node.run span recorded")
	}
	for _, s := range spans {
		if s.Status().Code == codes.Error {
			t.Errorf("successful node span marked as error: %q", s.Status().Description)
		}
	}
}

func TestEngine_ErrorResultMarksNodeSpanFailed(t *testing.T) {
	recorder := recordSpans(t)
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{
				Status: core.StatusError,
				Error:  &core.JobError{Code: "boom", Message: "exploded"},
			}, nil
		},
	})
	_, _ = e.Run(t.Context(), core.Graph{
		ID: "g", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}, nil)

	spans := nodeSpans(recorder)
	if len(spans) == 0 {
		t.Fatal("no node.run span recorded")
	}
	for _, s := range spans {
		if s.Status().Code != codes.Error {
			t.Errorf("node span status = %v, want %v for an error result", s.Status().Code, codes.Error)
		}
		if !strings.Contains(s.Status().Description, "boom") {
			t.Errorf("node span description = %q, want the result error's code in it", s.Status().Description)
		}
	}
}

// When Execute itself returns a Go error, THAT error is what the span
// must carry — not whatever error result the engine synthesises from it.
func TestEngine_ExecErrorIsRecordedOnNodeSpan(t *testing.T) {
	recorder := recordSpans(t)
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{}, errors.New("transport exploded")
		},
	})
	_, _ = e.Run(t.Context(), core.Graph{
		ID: "g", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}, nil)

	spans := nodeSpans(recorder)
	if len(spans) == 0 {
		t.Fatal("no node.run span recorded")
	}
	for _, s := range spans {
		if s.Status().Code != codes.Error {
			t.Errorf("node span status = %v, want %v when Execute errors", s.Status().Code, codes.Error)
		}
		if !strings.Contains(s.Status().Description, "transport exploded") {
			t.Errorf("node span description = %q, want Execute's own error text", s.Status().Description)
		}
	}
}
