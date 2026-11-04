package observability

import "net/http"

// Handler is the whole surface of the dedicated Prometheus listener. Process
// counters carry no tenant data, so this listener is protected by network
// placement instead of tenant authentication; keeping it separate from the API
// router avoids weakening the operator gate.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := Default().WritePrometheus(w); err != nil {
			http.Error(w, "render metrics: "+err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}
