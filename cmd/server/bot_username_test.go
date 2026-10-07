package main

import (
	"context"
	"testing"

	"github.com/tiennm99/miti99bot/internal/testutil"
)

func TestResolveBotUsername(t *testing.T) {
	t.Run("configured value wins without calling getMe", func(t *testing.T) {
		rb := testutil.NewRecordingBot(t)
		if got := resolveBotUsername(context.Background(), rb.Bot, "envbot"); got != "envbot" {
			t.Errorf("got %q, want envbot", got)
		}
		for _, c := range rb.Sent() {
			if c.Method == "getMe" {
				t.Error("getMe called although BOT_USERNAME was set")
			}
		}
	})

	t.Run("falls back to getMe", func(t *testing.T) {
		rb := testutil.NewRecordingBot(t)
		rb.StubMethod("getMe", `{"id":1,"is_bot":true,"username":"apibot"}`)
		if got := resolveBotUsername(context.Background(), rb.Bot, ""); got != "apibot" {
			t.Errorf("got %q, want apibot", got)
		}
	})

	// RecordingBot answers an unstubbed method with a bare `true`, which cannot
	// decode into a User: the failure must yield "" rather than stop startup.
	t.Run("getMe failure yields empty", func(t *testing.T) {
		rb := testutil.NewRecordingBot(t)
		if got := resolveBotUsername(context.Background(), rb.Bot, ""); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestLoadConfig_BotUsername(t *testing.T) {
	t.Setenv("PORT", "8080")
	for in, want := range map[string]string{"": "", "examplebot": "examplebot", " @examplebot ": "examplebot"} {
		t.Setenv("BOT_USERNAME", in)
		if got := loadConfig().BotUsername; got != want {
			t.Errorf("BOT_USERNAME=%q: got %q, want %q", in, got, want)
		}
	}
}
