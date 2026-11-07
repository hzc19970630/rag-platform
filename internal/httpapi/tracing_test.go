package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingMiddlewareContinuesUpstreamTrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	router := NewRouter(config.Config{AppEnv: "test"})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("X-Trace-ID"); got != traceID {
		t.Fatalf("X-Trace-ID = %q, want %q", got, traceID)
	}
	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatalf("no span recorded")
	}
	for _, span := range spans {
		if span.Name() == "GET /healthz" {
			if got := span.SpanContext().TraceID().String(); got != traceID {
				t.Fatalf("span trace id = %q, want %q", got, traceID)
			}
			return
		}
	}
	t.Fatalf("GET /healthz span not recorded: %+v", spans)
}
