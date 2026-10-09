package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetInlineGameScore_PostsJSON(t *testing.T) {
	var gotPath string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	if err := setInlineGameScoreAt(context.Background(), srv.URL, "tok", "AAA-inline", 42, 120); err != nil {
		t.Fatalf("setInlineGameScoreAt: %v", err)
	}
	if gotPath != "/bottok/setGameScore" {
		t.Errorf("path = %q", gotPath)
	}
	if got["inline_message_id"] != "AAA-inline" || got["user_id"].(float64) != 42 || got["score"].(float64) != 120 {
		t.Errorf("payload = %v", got)
	}
}

func TestSetInlineGameScore_ReturnsDescriptionWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: BOT_SCORE_NOT_MODIFIED"}`))
	}))
	defer srv.Close()

	err := setInlineGameScoreAt(context.Background(), srv.URL, "secret-token", "x", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "BOT_SCORE_NOT_MODIFIED") {
		t.Fatalf("err = %v, want the Telegram description", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestSetInlineGameScore_TransportErrorHidesToken(t *testing.T) {
	err := setInlineGameScoreAt(context.Background(), "http://127.0.0.1:1", "secret-token", "x", 1, 1)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v, want a token-free transport error", err)
	}
}
