package main

import (
	"net/http"

	"github.com/example/rag-platform/internal/platform/observability"
)

// newMetricsHandler is the whole surface of the dedicated Prometheus listener.
// Process counters carry no tenant data, so this listener is protected by
// network placement instead of tenant authentication; keeping it separate from
// the API router avoids weakening the operator gate.
func newMetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := observability.Default().WritePrometheus(w); err != nil {
			http.Error(w, "render metrics: "+err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}
