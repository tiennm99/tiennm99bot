package deploynotify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recorder is a Sender that captures the last (chatID, text) it received and
// flags whether it was ever invoked.
type recorder struct {
	called bool
	chatID int64
	text   string
	err    error
}

func (r *recorder) send(_ context.Context, chatID int64, text string) error {
	r.called = true
	r.chatID = chatID
	r.text = text
	return r.err
}

func TestRun_SendsOnStartup(t *testing.T) {
	rec := &recorder{}
	Run(context.Background(), Config{
		OwnerID: 42,
		GitSHA:  "abc123",
		Sender:  rec.send,
	})
	if !rec.called {
		t.Fatal("startup must send the owner DM")
	}
	if rec.chatID != 42 {
		t.Errorf("chatID = %d, want 42", rec.chatID)
	}
	if !strings.Contains(rec.text, "abc123") {
		t.Errorf("message %q missing SHA", rec.text)
	}
}

func TestRun_SendsEveryStartupNoDedup(t *testing.T) {
	// No dedup: the same SHA must notify on every boot.
	for i := 0; i < 2; i++ {
		rec := &recorder{}
		Run(context.Background(), Config{OwnerID: 42, GitSHA: "abc123", Sender: rec.send})
		if !rec.called {
			t.Fatalf("run %d: same SHA must still send (no dedup)", i)
		}
	}
}

func TestRun_SendsWithUnknownSHA(t *testing.T) {
	// An unknown SHA is reported, not silenced.
	rec := &recorder{}
	Run(context.Background(), Config{OwnerID: 42, GitSHA: "unknown", Sender: rec.send})
	if !rec.called {
		t.Fatal("unknown SHA must still send")
	}
	if !strings.Contains(rec.text, "unknown") {
		t.Errorf("message %q should carry the unknown SHA", rec.text)
	}
}

func TestRun_SkipsWhenNoOwner(t *testing.T) {
	rec := &recorder{}
	Run(context.Background(), Config{OwnerID: 0, GitSHA: "abc123", Sender: rec.send})
	if rec.called {
		t.Error("zero owner must not send")
	}
}

func TestRun_SendFailureIsSwallowed(t *testing.T) {
	rec := &recorder{err: errors.New("telegram is down")}
	// Must not panic or block; just logs and returns.
	Run(context.Background(), Config{OwnerID: 42, GitSHA: "abc123", Sender: rec.send})
	if !rec.called {
		t.Fatal("sender should have been called")
	}
}

func TestRenderMessage_ContainsSHA(t *testing.T) {
	got := renderMessage("deadbeef")
	if !strings.Contains(got, "deadbeef") {
		t.Errorf("message %q missing SHA", got)
	}
	if !strings.Contains(got, "tiennm99bot") {
		t.Errorf("message %q missing bot name", got)
	}
}
