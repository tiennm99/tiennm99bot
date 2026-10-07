package server

import "net/http"

// HealthHandler answers GET / with a stable string so the container health
// monitor can distinguish "process up" from "process listening but routing
// broken".
func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Anything other than the root path on this exact handler is a 404.
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("tiennm99bot ok\n"))
	}
}
