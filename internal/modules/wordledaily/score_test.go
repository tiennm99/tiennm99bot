package wordledaily

import (
	"errors"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

func TestScore_WinReportsSevenMinusGuessesOncePerCard(t *testing.T) {
	h := newHarness(t)
	card := h.tokenFor(1, groupChat, 7, 0, "Alice")
	h.guess(card, 1, h.wrong(1)[0])
	h.guess(card, 1, h.wrong(2)[1])
	h.guess(card, 1, h.answer())
	calls := h.rep.snapshot()
	if len(calls) != 1 || calls[0].score != 4 || calls[0].user != 1 || calls[0].addr != (htmlgame.Address{ChatID: groupChat, MessageID: 7}) {
		t.Fatalf("reports = %+v", calls)
	}
	// Reopening the same card does not report again.
	h.state(card)
	if n := len(h.rep.snapshot()); n != 1 {
		t.Fatalf("reopen reported again: %d", n)
	}
	// Opening another card reports the day's result there, once.
	other := h.tokenFor(1, -200, 9, 0, "Alice")
	h.state(other)
	h.state(other)
	inline, _ := h.svc.signToken(guessgame.Claims{UserID: 1, InlineID: "BAAAInline", Expiry: h.clock.now().Add(guessgame.TokenTTL).Unix()})
	h.state(inline)
	calls = h.rep.snapshot()
	if len(calls) != 3 || calls[1].addr != (htmlgame.Address{ChatID: -200, MessageID: 9}) || calls[1].score != 4 ||
		calls[2].addr != (htmlgame.Address{InlineID: "BAAAInline"}) {
		t.Fatalf("reports = %+v", calls)
	}
}

func TestScore_LossIsNotReported(t *testing.T) {
	h := newHarness(t)
	tok := h.dmToken(1)
	for _, w := range h.wrong(6) {
		h.guess(tok, 1, w)
	}
	h.state(h.tokenFor(1, groupChat, 7, 0, "Alice"))
	if n := len(h.rep.snapshot()); n != 0 {
		t.Fatalf("loss reported %d times", n)
	}
}

// A score that does not beat the player's best on the card is Telegram's
// expected answer: the card counts as reported and is not tried again.
func TestScore_NotModifiedCountsAsReported(t *testing.T) {
	h := newHarness(t)
	h.rep.err = errors.New("Bad Request: BOT_SCORE_NOT_MODIFIED")
	h.win(2)
	h.state(h.dmToken(2))
	if calls := h.rep.snapshot(); len(calls) != 1 || calls[0].score != 6 {
		t.Fatalf("reports = %+v", calls)
	}
}

// A report that failed (a timeout, a 5xx) leaves the card unrecorded, so
// opening it again reports the win until Telegram accepts it.
func TestScore_FailedReportIsRetriedOnTheNextLoad(t *testing.T) {
	h := newHarness(t)
	card := h.tokenFor(1, groupChat, 7, 0, "Alice")
	h.rep.err = errors.New("context deadline exceeded")
	h.guess(card, 1, h.answer())
	h.rep.err = nil
	h.state(card)
	h.state(card)
	calls := h.rep.snapshot()
	if len(calls) != 2 || calls[1].score != 6 || calls[1].addr != (htmlgame.Address{ChatID: groupChat, MessageID: 7}) {
		t.Fatalf("reports = %+v", calls)
	}
}
