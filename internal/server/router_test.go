package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthHandler_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthHandler()(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body = %q, want contains 'ok'", rec.Body.String())
	}
}

func TestNew_ServesModuleRoutesAndKeepsHealth(t *testing.T) {
	_, restore := captureLogger(t)
	defer restore()
	h := New(Route{Pattern: "/games/demo/", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("demo"))
	})})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/games/demo/x", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "demo" {
		t.Fatalf("module route: status %d body %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("health: status %d body %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/games/other/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path: status %d, want 404", rec.Code)
	}
}
