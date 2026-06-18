package engine

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"git.sr.ht/~klahr/dazyflow/core"
)

// tracer is resolved lazily from the global TracerProvider; tests can swap
// it via otel.SetTracerProvider before invoking the engine. With the noop
// default this is effectively zero-cost.
func tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("git.sr.ht/~klahr/dazyflow/engine")
}

func startGraphSpan(ctx context.Context, graph core.Graph) (context.Context, trace.Span) {
	return tracer().Start(ctx, "graph.run",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("dazyflow.graph.id", graph.ID),
			attribute.String("dazyflow.graph.version", graph.Version),
			attribute.String("dazyflow.tenant", graph.Tenant),
			attribute.String("dazyflow.workspace", graph.Workspace),
			attribute.Int("dazyflow.graph.nodes", len(graph.Nodes)),
		))
}

func startNodeSpan(ctx context.Context, graph core.Graph, node core.Node) (context.Context, trace.Span) {
	return tracer().Start(ctx, "node.run",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("dazyflow.graph.id", graph.ID),
			attribute.String("dazyflow.node.id", node.ID),
			attribute.String("dazyflow.node.module", node.Module),
		))
}

func recordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
}

// jobIDsFromSpan copies the W3C TraceID/SpanID into the job so module code
// (or downstream systems) can correlate logs and remote traces.
func jobIDsFromSpan(ctx context.Context, job *core.Job) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return
	}
	job.TraceID = sc.TraceID().String()
	job.SpanID = sc.SpanID().String()
}
