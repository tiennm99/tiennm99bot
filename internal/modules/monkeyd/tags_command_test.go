package monkeyd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// stubTags returns a fetcher yielding fixed tags and records the URL it saw.
func stubTags(tags []string, gotURL *string) tagsFetcher {
	return func(_ context.Context, novelURL string) ([]string, error) {
		if gotURL != nil {
			*gotURL = novelURL
		}
		return tags, nil
	}
}

func TestTags_RepliesWithHashtagBlock(t *testing.T) {
	var gotURL string
	rb, _ := installWith(t, 999, stubTags([]string{"Cổ Đại", "Gia Đình"}, &gotURL))

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewPrivateMessage(999, "/monkeyd_tags https://monkeydd.com/truong-an-gwem.html"))

	if want := "https://monkeydd.com/truong-an-gwem.html"; gotURL != want {
		t.Errorf("fetcher got %q, want %q", gotURL, want)
	}

	sent := rb.LastSent()
	want := "<pre>#MonkeyD #CổĐại #GiaĐình\n\nhttps://monkeydd.com/truong-an-gwem.html</pre>"
	if got := sent.Text(); got != want {
		t.Errorf("reply =\n%q\nwant\n%q", got, want)
	}
	// Without HTML parse mode Telegram would show the literal <pre> tags.
	if got := sent.Form["parse_mode"]; got != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", got)
	}
}

func TestTags_NoArgumentRepliesUsage(t *testing.T) {
	rb, _ := installWith(t, 999, stubTags(nil, nil))
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(999, "/monkeyd_tags"))

	if got := rb.LastSent().Text(); !strings.Contains(got, tagsUsage) {
		t.Errorf("reply = %q, want it to contain %q", got, tagsUsage)
	}
}

func TestTags_RejectsDisallowedHost(t *testing.T) {
	called := false
	rb, _ := installWith(t, 999, func(context.Context, string) ([]string, error) {
		called = true
		return nil, nil
	})

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewPrivateMessage(999, "/monkeyd_tags https://example.com/novel.html"))

	if called {
		t.Error("fetcher ran for a disallowed host")
	}
	if got := rb.LastSent().Text(); !strings.Contains(got, AllowedHostsHint) {
		t.Errorf("reply = %q, want it to name %q", got, AllowedHostsHint)
	}
}

// A bare hostname is normalised before the fetch, matching /monkeyd_crawl, and
// the reply quotes the normalised form rather than what was typed.
func TestTags_NormalizesURL(t *testing.T) {
	const wantURL = "https://monkeydd.com/truong-an-gwem.html"

	var gotURL string
	rb, _ := installWith(t, 999, stubTags([]string{"Cổ Đại"}, &gotURL))

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewPrivateMessage(999, "/monkeyd_tags monkeydd.com/truong-an-gwem.html"))

	if gotURL != wantURL {
		t.Errorf("fetcher got %q, want %q", gotURL, wantURL)
	}
	if got := rb.LastSent().Text(); !strings.Contains(got, wantURL) {
		t.Errorf("reply = %q, want it to carry the normalised URL", got)
	}
}

func TestTags_ReportsFetchFailure(t *testing.T) {
	rb, _ := installWith(t, 999, func(context.Context, string) ([]string, error) {
		return nil, errors.New("fetch failed: 404")
	})

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewPrivateMessage(999, "/monkeyd_tags "+testNovelURL))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "404") {
		t.Errorf("reply = %q, want it to carry the underlying error", got)
	}
	if strings.Contains(got, sourceHashtag) {
		t.Errorf("reply = %q, want no hashtag line on failure", got)
	}
}

// An empty tag list must say so rather than send a lone "#MonkeyD".
func TestTags_ReportsEmptyTagList(t *testing.T) {
	rb, _ := installWith(t, 999, stubTags(nil, nil))

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewPrivateMessage(999, "/monkeyd_tags "+testNovelURL))

	got := rb.LastSent().Text()
	if strings.Contains(got, sourceHashtag) {
		t.Errorf("reply = %q, want a plain explanation rather than a bare source hashtag", got)
	}
	if !strings.Contains(strings.ToLower(got), "no tags") {
		t.Errorf("reply = %q, want it to say no tags were listed", got)
	}
}

func TestTags_RejectsExtraArguments(t *testing.T) {
	called := false
	rb, _ := installWith(t, 999, func(context.Context, string) ([]string, error) {
		called = true
		return nil, nil
	})

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewPrivateMessage(999, "/monkeyd_tags "+testNovelURL+" extra"))

	if called {
		t.Error("fetcher ran despite extra arguments")
	}
	if got := rb.LastSent().Text(); !strings.Contains(got, tagsUsage) {
		t.Errorf("reply = %q, want usage text", got)
	}
}

// The command is public, so a non-admin in a group must get a reply.
func TestTags_AvailableToAnySender(t *testing.T) {
	rb, _ := installWith(t, 999, stubTags([]string{"Cổ Đại"}, nil))

	rb.Bot.ProcessUpdate(context.Background(),
		testutil.NewGroupMessage(-100, 12345, "/monkeyd_tags "+testNovelURL))

	if got := rb.LastSent().Text(); !strings.Contains(got, "#CổĐại") {
		t.Errorf("reply = %q, want the hashtag line", got)
	}
}
