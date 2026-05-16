// Package tracing provides OpenTelemetry setup for the factory platform.
//
// All services use the same setup: initialize a TracerProvider that exports
// to an OTLP collector, configured via standard OTEL env vars:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT  - collector URL (e.g. "http://otel-collector:4318")
//	OTEL_SERVICE_NAME            - service name (e.g. "factory-dispatcher")
//
// If OTEL_EXPORTER_OTLP_ENDPOINT is not set, tracing is disabled (noop).
package tracing

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Init sets up the global TracerProvider. Returns a shutdown function.
// If OTEL_EXPORTER_OTLP_ENDPOINT is not set, returns a noop shutdown.
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		slog.Error("failed to create OTLP exporter", "error", err)
		return func(context.Context) error { return nil }
	}

	res, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	slog.Info("tracing enabled", "endpoint", endpoint, "service", serviceName)

	return tp.Shutdown
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}
