package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

func TestAuth_Permits(t *testing.T) {
	const owner int64 = 100
	const admin int64 = 200
	const stranger int64 = 999

	auth := Auth{
		BotOwnerID:   owner,
		AdminUserIDs: map[int64]bool{admin: true},
	}

	updateFrom := func(id int64) *models.Update {
		return &models.Update{Message: &models.Message{From: &models.User{ID: id}}}
	}
	callbackFrom := func(id int64) *models.Update {
		return &models.Update{CallbackQuery: &models.CallbackQuery{From: models.User{ID: id}}}
	}

	cases := []struct {
		name   string
		v      Visibility
		update *models.Update
		expect bool
	}{
		{"public-no-message", VisibilityPublic, &models.Update{}, true},
		{"public-stranger", VisibilityPublic, updateFrom(stranger), true},
		{"unlisted-stranger", VisibilityUnlisted, updateFrom(stranger), true},
		{"unlisted-no-message", VisibilityUnlisted, &models.Update{}, true},
		{"protected-owner", VisibilityProtected, updateFrom(owner), true},
		{"protected-callback-owner", VisibilityProtected, callbackFrom(owner), true},
		{"protected-admin", VisibilityProtected, updateFrom(admin), true},
		{"protected-stranger", VisibilityProtected, updateFrom(stranger), false},
		{"private-owner", VisibilityPrivate, updateFrom(owner), true},
		{"private-admin", VisibilityPrivate, updateFrom(admin), false},
		{"private-stranger", VisibilityPrivate, updateFrom(stranger), false},
		{"protected-nil-message", VisibilityProtected, &models.Update{}, false},
		{"private-nil-from", VisibilityPrivate, &models.Update{Message: &models.Message{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := auth.Permits(tc.v, tc.update)
			if got != tc.expect {
				t.Errorf("Permits(%v) = %v, want %v", tc.v, got, tc.expect)
			}
		})
	}
}

func TestMatchCommand(t *testing.T) {
	mkUpdate := func(text string, entities ...models.MessageEntity) *models.Update {
		return &models.Update{
			Message: &models.Message{
				Text:     text,
				Entities: entities,
			},
		}
	}
	cmd := func(off, length int) models.MessageEntity {
		return models.MessageEntity{Type: models.MessageEntityTypeBotCommand, Offset: off, Length: length}
	}

	cases := []struct {
		name   string
		want   string
		bot    string // own username; "" defaults to tiennm99bot, "-" means unknown
		update *models.Update
		expect bool
	}{
		{
			name:   "dm bare slash-help",
			want:   "help",
			update: mkUpdate("/help", cmd(0, 5)),
			expect: true,
		},
		{
			// The bug this fix addresses: group clients append @botname to
			// the entity. The upstream library's MatchTypeCommand misses this.
			name:   "group slash-help-at-botname",
			want:   "help",
			update: mkUpdate("/help@tiennm99bot", cmd(0, 17)),
			expect: true,
		},
		{
			name:   "group slash-help-at-botname with trailing arg",
			want:   "help",
			update: mkUpdate("/help@tiennm99bot arg", cmd(0, 17)),
			expect: true,
		},
		{
			name:   "different command no match",
			want:   "help",
			update: mkUpdate("/info", cmd(0, 5)),
			expect: false,
		},
		{
			name:   "different command with botname no match",
			want:   "help",
			update: mkUpdate("/info@tiennm99bot", cmd(0, 17)),
			expect: false,
		},
		{
			name:   "non-command entity ignored",
			want:   "help",
			update: mkUpdate("/help", models.MessageEntity{Type: models.MessageEntityTypeMention, Offset: 0, Length: 5}),
			expect: false,
		},
		{
			name:   "command not at start matches (lib parity)",
			want:   "help",
			update: mkUpdate("hi /help", cmd(3, 5)),
			expect: true,
		},
		{
			name:   "uppercase command matches",
			want:   "help",
			update: mkUpdate("/HELP", cmd(0, 5)),
			expect: true,
		},
		{
			name:   "mixed-case command matches",
			want:   "help",
			update: mkUpdate("/Help", cmd(0, 5)),
			expect: true,
		},
		{
			name:   "uppercase command with botname matches",
			want:   "help",
			update: mkUpdate("/HELP@tiennm99bot", cmd(0, 17)),
			expect: true,
		},
		{
			name:   "case folding does not match a different command",
			want:   "help",
			update: mkUpdate("/INFO", cmd(0, 5)),
			expect: false,
		},
		{
			name:   "command addressed to another bot ignored",
			want:   "help",
			update: mkUpdate("/help@otherbot", cmd(0, 14)),
			expect: false,
		},
		{
			name:   "botname suffix is case-insensitive",
			want:   "help",
			update: mkUpdate("/help@TienNM99Bot", cmd(0, 17)),
			expect: true,
		},
		{
			name:   "unknown own username accepts any suffix",
			want:   "help",
			bot:    "-",
			update: mkUpdate("/help@otherbot", cmd(0, 14)),
			expect: true,
		},
		{
			name:   "nil update",
			want:   "help",
			update: nil,
			expect: false,
		},
		{
			name:   "no message",
			want:   "help",
			update: &models.Update{},
			expect: false,
		},
		{
			name:   "no entities",
			want:   "help",
			update: mkUpdate("/help"),
			expect: false,
		},
		{
			name:   "out-of-bounds entity ignored",
			want:   "help",
			update: mkUpdate("/help", cmd(0, 999)),
			expect: false,
		},
		{
			name:   "zero-length entity ignored",
			want:   "help",
			update: mkUpdate("/help", cmd(0, 0)),
			expect: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			botUsername := tc.bot
			switch botUsername {
			case "":
				botUsername = "tiennm99bot"
			case "-":
				botUsername = ""
			}
			got := matchCommand(tc.want, botUsername, tc.update)
			if got != tc.expect {
				t.Errorf("matchCommand(%q, ...) = %v, want %v", tc.want, got, tc.expect)
			}
		})
	}
}

func TestLogCommand(t *testing.T) {
	capture := func(update *models.Update, err error) map[string]any {
		var buf bytes.Buffer
		prev := log.Default()
		log.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		defer log.SetDefault(prev)

		logCommand("ping", update, err)

		var out map[string]any
		if e := json.Unmarshal(buf.Bytes(), &out); e != nil {
			t.Fatalf("log line not JSON: %v (%q)", e, buf.String())
		}
		return out
	}

	t.Run("group success logs input, sender, chat", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{
			Text: "/ping now",
			From: &models.User{ID: 42, Username: "alice"},
			Chat: models.Chat{ID: -100, Type: models.ChatTypeSupergroup, Title: "Squad"},
		}}
		got := capture(update, nil)
		if got["level"] != "INFO" {
			t.Errorf("level = %v, want INFO", got["level"])
		}
		want := map[string]any{
			"command": "ping", "input": "/ping now", "chat_type": "supergroup",
			"chat_title": "Squad", "username": "alice",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %v, want %v", k, got[k], v)
			}
		}
		if got["user_id"].(float64) != 42 || got["chat_id"].(float64) != -100 {
			t.Errorf("ids = user %v chat %v, want 42 / -100", got["user_id"], got["chat_id"])
		}
	})

	t.Run("dm error logs at ERROR with err", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{
			Text: "/ping",
			From: &models.User{ID: 7},
			Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate},
		}}
		got := capture(update, errors.New("boom"))
		if got["level"] != "ERROR" {
			t.Errorf("level = %v, want ERROR", got["level"])
		}
		if got["err"] != "boom" {
			t.Errorf("err = %v, want boom", got["err"])
		}
		if _, hasTitle := got["chat_title"]; hasTitle {
			t.Error("DM should not log chat_title")
		}
		if _, hasUser := got["username"]; hasUser {
			t.Error("missing username should be omitted")
		}
	})
}

func TestAuth_ZeroDeniesAllGated(t *testing.T) {
	// Misconfigured deploy: zero-value Auth must deny every Protected/Private
	// command without panicking, so an unconfigured bot cannot be hijacked
	// just because an admin env var was forgotten.
	var auth Auth
	update := &models.Update{Message: &models.Message{From: &models.User{ID: 1}}}

	if !auth.Permits(VisibilityPublic, update) {
		t.Error("zero-Auth must still permit Public")
	}
	if auth.Permits(VisibilityProtected, update) {
		t.Error("zero-Auth must deny Protected")
	}
	if auth.Permits(VisibilityPrivate, update) {
		t.Error("zero-Auth must deny Private")
	}
}
