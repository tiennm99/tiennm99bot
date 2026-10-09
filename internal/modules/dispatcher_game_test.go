package modules_test

import (
	"context"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installGame registers one game module whose Play handler is h.
func installGame(t *testing.T, visibility modules.Visibility, h modules.CallbackHandler) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"arcade"}, map[string]modules.Factory{
		"arcade": func(modules.Deps) modules.Module {
			return modules.Module{Games: []modules.Game{{ShortName: "arcade", Visibility: visibility, Handler: h}}}
		},
	}, storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{BotOwnerID: 1})
	return rb
}

func answeredCallbacks(rb *testutil.RecordingBot) []testutil.SentCall {
	var out []testutil.SentCall
	for _, c := range rb.Sent() {
		if c.Method == "answerCallbackQuery" {
			out = append(out, c)
		}
	}
	return out
}

func TestInstall_GameCallbackReachesHandler(t *testing.T) {
	var got *models.Update
	rb := installGame(t, modules.VisibilityPublic, func(_ context.Context, _ *bot.Bot, u *models.Update) error {
		got = u
		return nil
	})
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(42, 42, 7, "arcade"))
	if got == nil || got.CallbackQuery.GameShortName != "arcade" {
		t.Fatalf("game handler not called; got %+v", got)
	}
}

func TestInstall_GameCallbackDeniedAnswersEmpty(t *testing.T) {
	called := false
	rb := installGame(t, modules.VisibilityPrivate, func(context.Context, *bot.Bot, *models.Update) error {
		called = true
		return nil
	})
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(42, 42, 7, "arcade"))
	if called {
		t.Fatal("denied user reached the game handler")
	}
	answers := answeredCallbacks(rb)
	if len(answers) != 1 || answers[0].Form["url"] != "" || answers[0].Form["text"] != "" {
		t.Fatalf("want one empty answer, got %+v", answers)
	}
}

func TestInstall_GameCallbackPanicIsAnswered(t *testing.T) {
	rb := installGame(t, modules.VisibilityPublic, func(context.Context, *bot.Bot, *models.Update) error {
		panic("game exploded")
	})
	captureLogs(t, func() {
		rb.Bot.ProcessUpdate(context.Background(), testutil.NewGameCallback(42, 42, 7, "arcade"))
	})
	if len(answeredCallbacks(rb)) != 1 {
		t.Fatalf("panicking game handler left the query unanswered; sent %+v", rb.Sent())
	}
}

func TestInstall_UnknownGameShortNameAnswersEmpty(t *testing.T) {
	called := false
	rb := installGame(t, modules.VisibilityPublic, func(context.Context, *bot.Bot, *models.Update) error {
		called = true
		return nil
	})
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewInlineGameCallback(42, "inline-1", "other"))
	if called {
		t.Fatal("a different short name reached the arcade handler")
	}
	if len(answeredCallbacks(rb)) != 1 {
		t.Fatalf("unknown game was not answered; sent %+v", rb.Sent())
	}
}
