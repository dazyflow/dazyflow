package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/daemon"
	"go.opentelemetry.io/otel"
)

func TestTracingConfigured(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	if daemon.TracingConfigured() {
		t.Error("no OTLP endpoint → TracingConfigured should be false")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	if !daemon.TracingConfigured() {
		t.Error("endpoint set → TracingConfigured should be true")
	}
}

func TestSetupTracing_NoEndpointIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	prev := otel.GetTracerProvider()

	shutdown, enabled, err := daemon.SetupTracing(context.Background(), "test", "v0")
	if err != nil {
		t.Fatalf("SetupTracing: %v", err)
	}
	if enabled {
		t.Error("no endpoint → enabled should be false")
	}
	if otel.GetTracerProvider() != prev {
		t.Error("global TracerProvider must be left untouched when disabled")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown returned error: %v", err)
	}
}

func TestSetupTracing_WithEndpointInstallsProvider(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) }) // don't leak global state
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

	shutdown, enabled, err := daemon.SetupTracing(context.Background(), "test", "v0")
	if err != nil {
		t.Fatalf("SetupTracing: %v", err)
	}
	if !enabled {
		t.Fatal("endpoint set → enabled should be true")
	}
	if otel.GetTracerProvider() == prev {
		t.Error("global TracerProvider should have been replaced with the OTLP provider")
	}
	// Shutdown must not hang even with no collector listening (lazy conn).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = shutdown(ctx) // ignore export error — nothing to flush, no collector
}
