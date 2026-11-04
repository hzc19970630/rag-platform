package observability

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/example/rag-platform"

// InitTracing installs the global tracer provider. An empty endpoint keeps
// spans local (IDs are still generated for log correlation) and starts no
// exporter; a non-empty endpoint enables OTLP/HTTP export.
func InitTracing(ctx context.Context, endpoint, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	endpoint = strings.TrimSpace(endpoint)
	if endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT must be a valid URL")
		}
	}
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1))),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", strings.TrimSpace(serviceName)),
			attribute.String("service.namespace", "rag-platform"),
		)),
	}
	if endpoint != "" {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
		if err != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// TraceID returns the current span's trace id, or an empty string when there is
// no valid span. Callers use it for logs and response headers.
func TraceID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// Propagator extracts or injects W3C trace context. HTTP middleware uses it to
// continue an upstream trace.
func Propagator() propagation.TextMapPropagator {
	return otel.GetTextMapPropagator()
}
