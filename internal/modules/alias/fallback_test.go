package alias_test

import (
	"context"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/alias"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// The headline of the fallback: a saved name becomes its own command.
func TestFallback_SavedNameWorksAsItsOwnCommand(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("cheer", &models.Message{Sticker: &models.Sticker{FileID: "sticker-id"}}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/cheer"))

	call, ok := callTo(rb, "sendSticker")
	if !ok {
		t.Fatalf("no sendSticker call; /cheer did not resolve: %+v", rb.Sent())
	}
	if got := call.Form["sticker"]; got != "sticker-id" {
		t.Errorf("sticker = %q, want the saved file_id", got)
	}
}

// Groups send /cmd@botname, and the fallback must normalise that the same way
// the command matcher does.
func TestFallback_ToleratesAtBotnameSuffix(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("cheer", &models.Message{Text: "yay"}))

	rb.Reset()
	// The fixture builder stops the entity at '@', but real Telegram includes
	// the whole "/cheer@tiennm99bot" in it — which is the case the stripping
	// exists for, so the entity is widened here to match the wire format.
	upd := testutil.NewPrivateMessage(7, "/cheer@tiennm99bot")
	upd.Message.Entities[0].Length = len(upd.Message.Text)
	rb.Bot.ProcessUpdate(context.Background(), upd)

	if got := rb.LastSent().Text(); got != "yay" {
		t.Errorf("reply = %q, want the alias to resolve despite the @suffix", got)
	}
}

// Mobile keyboards autocapitalise; /Cheer must reach the same alias.
func TestFallback_IsCaseInsensitive(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("cheer", &models.Message{Text: "yay"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/CHEER"))

	if got := rb.LastSent().Text(); got != "yay" {
		t.Errorf("reply = %q, want case-folded resolution", got)
	}
}

// Silence on a miss is deliberate: the fallback sees every unrecognised command
// in every chat, so replying would turn typos into noise and would confirm
// which names are taken.
func TestFallback_UnknownNameIsSilent(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/pign"))

	if calls := rb.Sent(); len(calls) != 0 {
		t.Errorf("unknown command produced output: %+v", calls)
	}
}

// The rule the user asked for: behaviour in code wins over an alias, always.
// Registered commands are installed before the fallback, and the bot library
// returns the first matching handler.
func TestFallback_NeverShadowsARegisteredCommand(t *testing.T) {
	var realRan bool
	realModule := func(_ modules.Deps) modules.Module {
		return modules.Module{Commands: []modules.Command{{
			Name:        "cheer",
			Visibility:  modules.VisibilityPublic,
			Description: "the real thing",
			Handler: func(_ context.Context, _ *bot.Bot, _ *models.Update) error {
				realRan = true
				return nil
			},
		}}}
	}

	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"alias", "real"},
		map[string]modules.Factory{"alias": alias.New, "real": realModule},
		storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})

	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/cheer"))
	if !realRan {
		t.Error("the registered /cheer handler did not run")
	}
}

// Refusing at assignment time is the companion to dispatch order: an alias
// named after a real command would only ever be reachable via /insert.
func TestAlias_RefusesNameOfARegisteredCommand(t *testing.T) {
	rb := installAlias(t)

	// /aliases is one of this module's own commands.
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("aliases", &models.Message{Text: "hijack"}))

	rb.AssertSentText(t, "already a command of mine")

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert aliases"))
	rb.AssertSentText(t, "Nothing is saved")
}

func TestUnalias_DeletesAndThenNameIsFree(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("temp", &models.Message{Text: "content"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/unalias temp"))
	rb.AssertSentText(t, "Deleted <code>temp</code>")

	// Gone from /insert, from the list, and from the bare-command path.
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/insert temp"))
	rb.AssertSentText(t, "Nothing is saved")

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/aliases"))
	rb.AssertSentText(t, "No aliases saved yet")

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/temp"))
	if calls := rb.Sent(); len(calls) != 0 {
		t.Errorf("deleted alias still answered as a command: %+v", calls)
	}
}

func TestUnalias_UnknownNameIsReported(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/unalias ghost"))

	rb.AssertSentText(t, "Nothing is saved")
}

// Deleting is open to anyone, matching the overwrite rule — the namespace is
// shared, so the permission model is too.
func TestUnalias_AnyoneMayDelete(t *testing.T) {
	rb := installAlias(t)
	rb.Bot.ProcessUpdate(context.Background(),
		aliasCmd("shared", &models.Message{Text: "content"}))

	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(99, "/unalias shared"))
	rb.AssertSentText(t, "Deleted")
}
