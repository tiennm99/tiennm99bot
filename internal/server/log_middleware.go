package server

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

// statusRecorder wraps http.ResponseWriter to capture the final status
// code. http.ResponseWriter doesn't expose what was written; the middleware
// needs the status to log a per-request `req` line.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write records the implicit 200 net/http sends on a first body write, so a
// zero status means nothing has reached the client yet.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// effectiveStatus returns the recorded status code, defaulting to 200 when no
// explicit WriteHeader was called (Go's net/http implicitly writes 200 on
// the first body write).
func (r *statusRecorder) effectiveStatus() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// LogRequests wraps an http.Handler with a request log line:
//
//	{"msg":"req","method":"GET","path":"/","status":200,"ms":0}
//
// Keep the field names stable: log-based dashboards and alerts may filter on
// msg=req and status>=500.
//
// The req line is emitted from a deferred closure so a panic in a downstream
// handler still produces an observable log entry — http.Server's own recover
// only logs to stderr and never runs middleware again on the way out.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			p := recover()
			if p != nil && rec.status == 0 {
				// Nothing was written yet, so the client can still get a real 500
				// instead of net/http's implicit empty 200.
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
			rec.status = recoverPanicStatus(p, rec.status)
			log.Info("req",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.effectiveStatus(),
				"ms", time.Since(start).Milliseconds(),
			)
		}()
		next.ServeHTTP(rec, r)
	})
}

// recoverPanicStatus folds a recovered panic into the status to log: it
// returns 500 if a panic was recovered, otherwise the original status
// untouched. The panic is logged with its stack and absorbed, not re-raised,
// so the deferred req line always runs.
//
// It writes no response itself; LogRequests sends the 500 when the handler
// panicked before writing anything.
func recoverPanicStatus(rec any, currentStatus int) int {
	if rec == nil {
		return currentStatus
	}
	log.Error("middleware recovered panic",
		"panic", rec,
		"stack", string(debug.Stack()))
	return http.StatusInternalServerError
}
