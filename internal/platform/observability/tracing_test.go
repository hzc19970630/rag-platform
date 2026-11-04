package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInitTracingWithoutEndpointKeepsLocalSpans(t *testing.T) {
	shutdown, err := InitTracing(context.Background(), "", "test-service")
	if err != nil {
		t.Fatalf("init tracing: %v", err)
	}
	t.Cleanup(func() {
		_ = shutdown(context.Background())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})
	ctx, span := StartSpan(context.Background(), "test.span")
	if id := TraceID(ctx); id == "" {
		t.Fatalf("local tracing must still generate a trace id")
	}
	span.End()
}

func TestInitTracingRejectsInvalidEndpoint(t *testing.T) {
	if _, err := InitTracing(context.Background(), "://bad", "test-service"); err == nil {
		t.Fatalf("invalid endpoint must fail initialization")
	}
}

func TestPropagatorExtractsTraceContext(t *testing.T) {
	shutdown, err := InitTracing(context.Background(), "", "test-service")
	if err != nil {
		t.Fatalf("init tracing: %v", err)
	}
	t.Cleanup(func() {
		_ = shutdown(context.Background())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	ctx := Propagator().Extract(context.Background(), mapCarrier{
		"traceparent": "00-" + traceID + "-00f067aa0ba902b7-01",
	})
	if got := TraceID(ctx); got != traceID {
		t.Fatalf("extracted trace id = %q, want %q", got, traceID)
	}
}

type mapCarrier map[string]string

func (m mapCarrier) Get(key string) string { return m[key] }
func (m mapCarrier) Set(key, value string) { m[key] = value }
func (m mapCarrier) Keys() []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
