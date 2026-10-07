package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logger "github.com/tiennm99/tiennm99bot/internal/log"
)

// captureLogger swaps the package-level logger for one writing to buf and
// returns a restore func. Tests must defer the restore.
func captureLogger(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	prev := logger.Default()
	buf := &bytes.Buffer{}
	logger.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return buf, func() { logger.SetDefault(prev) }
}

func decodeReqLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "req" {
			return rec
		}
	}
	t.Fatalf("no req line found in:\n%s", buf.String())
	return nil
}

func TestLogRequests_LogsMethodPathStatus(t *testing.T) {
	buf, restore := captureLogger(t)
	defer restore()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	rec := httptest.NewRecorder()
	LogRequests(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhook", nil))

	got := decodeReqLine(t, buf)
	if got["method"] != "POST" {
		t.Errorf("method = %v, want POST", got["method"])
	}
	if got["path"] != "/webhook" {
		t.Errorf("path = %v, want /webhook", got["path"])
	}
	if got["status"].(float64) != float64(http.StatusCreated) {
		t.Errorf("status = %v, want 201", got["status"])
	}
	if _, ok := got["ms"]; !ok {
		t.Errorf("missing ms field")
	}
}

func TestLogRequests_DefaultStatus200WhenNotSet(t *testing.T) {
	buf, restore := captureLogger(t)
	defer restore()

	// Inner handler writes a body but never calls WriteHeader explicitly.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	rec := httptest.NewRecorder()
	LogRequests(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	got := decodeReqLine(t, buf)
	// Even though our recorder didn't see WriteHeader, our middleware
	// should report 200 — Go's net/http implicitly writes 200 on first
	// body write.
	if got["status"].(float64) != float64(http.StatusOK) {
		t.Errorf("status = %v, want 200 (implicit)", got["status"])
	}
}

func TestLogRequests_PreservesInnerBehavior(t *testing.T) {
	_, restore := captureLogger(t)
	defer restore()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brewing"))
	})
	rec := httptest.NewRecorder()
	LogRequests(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status code = %d, want 418", rec.Code)
	}
	if rec.Body.String() != "brewing" {
		t.Errorf("body = %q, want 'brewing'", rec.Body.String())
	}
}

func TestLogRequests_PanicBeforeWriteReturns500(t *testing.T) {
	buf, restore := captureLogger(t)
	defer restore()

	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	rec := httptest.NewRecorder()
	LogRequests(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("client status = %d, want 500", rec.Code)
	}
	if got := decodeReqLine(t, buf); got["status"].(float64) != float64(http.StatusInternalServerError) {
		t.Errorf("logged status = %v, want 500", got["status"])
	}
}
