package random

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The renderer is internal to the compose network; the bot sends no credentials.
func TestRendererClient_RenderSendsNoAuthorization(t *testing.T) {
	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mp4Bytes)
	}))
	defer server.Close()

	client := rendererClient{HTTP: server.Client(), BaseURL: server.URL}
	if _, err := client.RenderGacha(context.Background(), "gacha", "alice", 5); err != nil {
		t.Fatalf("RenderGacha: %v", err)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuthorization)
	}
}

func TestRendererClient_RenderNotConfigured(t *testing.T) {
	_, err := rendererClient{}.RenderGacha(context.Background(), "gacha", "alice", 5)
	if !errors.Is(err, errRendererNotConfigured) {
		t.Fatalf("RenderGacha error = %v, want errRendererNotConfigured", err)
	}
	t.Setenv(rendererURLEnv, "")
	if rendererConfigured() {
		t.Fatal("rendererConfigured() = true with no URL")
	}
	t.Setenv(rendererURLEnv, "http://renderer:3000")
	if !rendererConfigured() {
		t.Fatal("rendererConfigured() = false with a URL")
	}
}

func TestRendererClient_RenderRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(append(append([]byte{}, mp4Bytes...), bytes.Repeat([]byte("a"), int(rendererMaxBytes))...))
	}))
	defer server.Close()

	client := rendererClient{HTTP: server.Client(), BaseURL: server.URL}
	if _, err := client.RenderGacha(context.Background(), "gacha", "alice", 5); err == nil {
		t.Fatalf("RenderGacha returned nil error")
	}
}

func TestRendererClient_DefaultHTTPClientHasTimeout(t *testing.T) {
	client := rendererClient{}
	if got := client.httpClient().Timeout; got != rendererTimeout {
		t.Fatalf("timeout = %s, want %s", got, rendererTimeout)
	}
}
