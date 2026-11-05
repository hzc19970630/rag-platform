package observability

import "net/http"

// Handler is the whole surface of the dedicated Prometheus listener. Process
// counters carry no tenant data, so this listener is protected by network
// placement instead of tenant authentication; keeping it separate from the API
// router avoids weakening the operator gate.
func Handler() http.Handler {
	return HandlerFor(Default())
}

// HandlerFor serves the registry built by the composition root. cmd/api and
// cmd/worker share this registry with the application services so a scrape sees
// the same counters the request path recorded.
func HandlerFor(registry *Registry) http.Handler {
	if registry == nil {
		registry = Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := registry.WritePrometheus(w); err != nil {
			http.Error(w, "render metrics: "+err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}
