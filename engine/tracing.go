// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/dazyflow/dazyflow/core"
)

func tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("github.com/dazyflow/dazyflow/engine")
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

func jobIDsFromSpan(ctx context.Context, job *core.Job) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return
	}
	job.TraceID = sc.TraceID().String()
	job.SpanID = sc.SpanID().String()
}
