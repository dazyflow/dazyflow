// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TracingConfigured reports whether OTLP trace export is requested via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
// env vars. When neither is set, spans stay on the global noop tracer
// (zero overhead) — the engine creates them (engine/tracing.go) but
// nothing exports them.
func TracingConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// SetupTracing installs a global OTLP TracerProvider so the engine's spans
// actually export, but ONLY when an OTLP endpoint is configured via the
// standard OTEL_* env vars. Otherwise it's a no-op: it leaves the global
// noop tracer in place and returns a no-op shutdown with enabled=false, so
// the default deployment pays nothing and behaves exactly as before.
//
// The returned shutdown flushes batched spans and closes the exporter;
// callers should defer it. The OTLP exporter reads its endpoint, headers,
// TLS, etc. from the standard OTEL_EXPORTER_OTLP_* env vars.
func SetupTracing(ctx context.Context, serviceName, version string) (shutdown func(context.Context) error, enabled bool, err error) {
	noop := func(context.Context) error { return nil }
	if !TracingConfigured() {
		return noop, false, nil
	}
	exp, err := otlptracegrpc.New(ctx) // endpoint + opts from OTEL_* env
	if err != nil {
		return noop, false, err
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", version),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	// W3C trace-context + baggage so spans stitch across services.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, true, nil
}
