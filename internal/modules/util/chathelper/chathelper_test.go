package chathelper

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestFetchContext(t *testing.T) {
	t.Run("reserves reply budget from parent deadline", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		child, childCancel := FetchContext(parent)
		defer childCancel()
		dl, ok := child.Deadline()
		if !ok {
			t.Fatal("child has no deadline")
		}
		// budget = parent remaining (~10s) - replyReserve (3s) ≈ 7s.
		if d := time.Until(dl); d > 8*time.Second || d < 6*time.Second {
			t.Fatalf("fetch budget = %v, want ≈7s (10s parent - 3s reserve)", d)
		}
	})

	t.Run("floors to 1s when parent deadline is within the reserve", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		child, childCancel := FetchContext(parent)
		defer childCancel()
		dl, _ := child.Deadline()
		if d := time.Until(dl); d < 900*time.Millisecond || d > 1100*time.Millisecond {
			t.Fatalf("floored budget = %v, want ≈1s", d)
		}
	})

	t.Run("no parent deadline yields a cancelable child", func(t *testing.T) {
		child, childCancel := FetchContext(context.Background())
		defer childCancel()
		if _, ok := child.Deadline(); ok {
			t.Fatal("child unexpectedly has a deadline")
		}
		childCancel()
		if child.Err() == nil {
			t.Fatal("cancel did not propagate to child")
		}
	})
}

func TestSubjectFor(t *testing.T) {
	tests := []struct {
		name string
		msg  *models.Message
		want string
	}{
		{
			name: "nil message",
			msg:  nil,
			want: "",
		},
		{
			name: "private chat with From",
			msg: &models.Message{
				Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate},
				From: &models.User{ID: 42},
			},
			want: "42",
		},
		{
			name: "private chat without From",
			msg: &models.Message{
				Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate},
			},
			want: "",
		},
		{
			name: "group chat → chat id (ignores From)",
			msg: &models.Message{
				Chat: models.Chat{ID: -100, Type: models.ChatTypeGroup},
				From: &models.User{ID: 42},
			},
			want: "-100",
		},
		{
			name: "supergroup → chat id",
			msg: &models.Message{
				Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
				From: &models.User{ID: 42},
			},
			want: "-1001",
		},
		{
			name: "channel falls through to From.ID",
			msg: &models.Message{
				Chat: models.Chat{ID: -200, Type: models.ChatTypeChannel},
				From: &models.User{ID: 7},
			},
			want: "7",
		},
		{
			name: "channel without From",
			msg: &models.Message{
				Chat: models.Chat{ID: -200, Type: models.ChatTypeChannel},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SubjectFor(tt.msg); got != tt.want {
				t.Errorf("SubjectFor: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArgAfterCommand(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"/cmd", ""},
		{"/cmd ", ""},
		{"/cmd  ", ""},
		{"/cmd word", "word"},
		{"/cmd  word  ", "word"},
		{"/cmd@bot word", "word"},
		{"/cmd two words", "two words"},
		{"/cmd  two   words  ", "two   words"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ArgAfterCommand(tt.in); got != tt.want {
				t.Errorf("ArgAfterCommand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNowMillis(t *testing.T) {
	a := NowMillis()
	b := NowMillis()
	if b < a {
		t.Errorf("NowMillis went backwards: %d → %d", a, b)
	}
	if a < 1700000000000 {
		t.Errorf("NowMillis too small (not ms-epoch?): %d", a)
	}
}

// TestReply_ForwardsMessageThreadID locks in the forum-topic fix: when an
// inbound command arrives in a forum-supergroup topic, the reply must carry
// the same message_thread_id so Telegram posts it back to that topic. Without
// this, Telegram routes the reply to the General topic — the bug this whole
// signature change exists to prevent.
func TestReply_ForwardsMessageThreadID(t *testing.T) {
	tests := []struct {
		name       string
		msg        *models.Message
		wantChat   string
		wantThread string // "" means: field must be absent from form
	}{
		{
			name: "forum topic — thread id forwarded",
			msg: &models.Message{
				Chat:            models.Chat{ID: -1001234, Type: models.ChatTypeSupergroup, IsForum: true},
				MessageThreadID: 42,
				Text:            "/cmd",
			},
			wantChat:   "-1001234",
			wantThread: "42",
		},
		{
			name: "private chat — no thread id sent",
			msg: &models.Message{
				Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate},
				Text: "/cmd",
			},
			wantChat:   "999",
			wantThread: "",
		},
		{
			name: "regular group (no topics) — no thread id sent",
			msg: &models.Message{
				Chat: models.Chat{ID: -100, Type: models.ChatTypeGroup},
				Text: "/cmd",
			},
			wantChat:   "-100",
			wantThread: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rb := testutil.NewRecordingBot(t)
			if err := Reply(context.Background(), rb.Bot, tt.msg, "hi"); err != nil {
				t.Fatalf("Reply: %v", err)
			}
			got := rb.LastSent()
			if got.Method != "sendMessage" {
				t.Fatalf("method: got %q, want sendMessage", got.Method)
			}
			if got.ChatID() != tt.wantChat {
				t.Errorf("chat_id: got %q, want %q", got.ChatID(), tt.wantChat)
			}
			gotThread := got.Form["message_thread_id"]
			if gotThread != tt.wantThread {
				t.Errorf("message_thread_id: got %q, want %q", gotThread, tt.wantThread)
			}
		})
	}
}

// TestReplyHTML_ForwardsMessageThreadID is the HTML-mode counterpart of the
// plain Reply test; same invariant, same reason.
func TestReplyHTML_ForwardsMessageThreadID(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	msg := &models.Message{
		Chat:            models.Chat{ID: -1009999, Type: models.ChatTypeSupergroup, IsForum: true},
		MessageThreadID: 7,
	}
	if err := ReplyHTML(context.Background(), rb.Bot, msg, "<b>hi</b>"); err != nil {
		t.Fatalf("ReplyHTML: %v", err)
	}
	got := rb.LastSent()
	if got.Form["message_thread_id"] != "7" {
		t.Errorf("message_thread_id: got %q, want %q", got.Form["message_thread_id"], "7")
	}
	if got.Form["parse_mode"] != "HTML" {
		t.Errorf("parse_mode: got %q, want %q", got.Form["parse_mode"], "HTML")
	}
}

// TestReply_NilMessage is a defensive check — handlers occasionally inherit
// updates without a Message (channel posts routed through future code paths),
// and Reply must no-op rather than panic.
func TestReply_NilMessage(t *testing.T) {
	rb := testutil.NewRecordingBot(t)
	if err := Reply(context.Background(), rb.Bot, nil, "ignored"); err != nil {
		t.Fatalf("Reply(nil): %v", err)
	}
	if n := len(rb.Sent()); n != 0 {
		t.Errorf("Reply(nil) sent %d calls; want 0", n)
	}
}

func TestWinRate(t *testing.T) {
	tests := []struct {
		wins, played, want int
	}{
		{0, 0, 0},   // no games
		{0, 5, 0},   // 0%
		{5, 5, 100}, // 100%
		{2, 3, 67},  // round-half-up: 66.67% → 67% (NOT 66%)
		{1, 3, 33},  // 33.33% → 33%
		{1, 6, 17},  // 16.67% → 17%
		{1, 2, 50},  // exact 50%
		{3, 4, 75},  // exact 75%
		// negative played guards against caller bugs.
		{1, -1, 0},
	}
	for _, tt := range tests {
		got := WinRate(tt.wins, tt.played)
		if got != tt.want {
			t.Errorf("WinRate(%d,%d) = %d, want %d", tt.wins, tt.played, got, tt.want)
		}
	}
}

func TestMonospaceTableAlignsAndEscapesHTML(t *testing.T) {
	got := MonospaceTable([]string{"Asset", "Qty"}, [][]string{{"<TCB>", "10"}, {"BTC", "2"}})
	for _, want := range []string{"<pre>", "Asset", "&lt;TCB&gt;", "BTC    2", "</pre>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q in %q", want, got)
		}
	}
}
