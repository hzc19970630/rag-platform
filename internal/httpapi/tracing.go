package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// tracingMiddleware continues an upstream W3C trace and exposes its id in
// X-Trace-ID. The span context is attached to the request so retrieval, model
// calls and logs share one trace.
func tracingMiddleware() gin.HandlerFunc {
	tracer := observability.Tracer()
	return func(c *gin.Context) {
		ctx := observability.Propagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		name := c.Request.Method + " " + c.FullPath()
		if c.FullPath() == "" {
			name = c.Request.Method + " " + c.Request.URL.Path
		}
		ctx, span := tracer.Start(ctx, name,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", c.Request.Method),
				attribute.String("http.route", c.FullPath()),
				attribute.String("http.target", c.Request.URL.Path),
			),
		)
		defer span.End()
		c.Request = c.Request.WithContext(ctx)
		if traceID := observability.TraceID(ctx); traceID != "" {
			c.Header("X-Trace-ID", traceID)
		}
		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.status_code", status))
		if len(c.Errors) > 0 {
			span.RecordError(c.Errors.Last())
			span.SetStatus(codes.Error, c.Errors.String())
			return
		}
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}
}
