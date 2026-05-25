package engine

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.sr.ht/~klahr/hazy-flow/core"
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
			if attrValue(s, "hazyflow.tenant") != "acme" {
				t.Errorf("graph span missing tenant attr")
			}
		case "node.run":
			nodeSpan = true
			if attrValue(s, "hazyflow.node.module") != "noop" {
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
