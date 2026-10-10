package wordledaily

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
)

const (
	msgChannel      = "Wordle Daily can't be played in channels."
	msgSendGameFail = "Couldn't send Wordle Daily. Try again later."

	msgWordleChannel  = "Wordle can't be played in channels."
	msgWordleSendFail = "Couldn't send Wordle. Try again later."
	msgNoStore        = "Wordle isn't available on this server."
	msgNoPlayer       = "Cannot identify the player."
	msgNotYourself    = "Post as yourself to play: anonymous admins and posts on behalf of a chat have no round of their own."
	msgNewHint        = "🆕 New round started. Use `/wordle <word>` to guess."
)

// The /wordle commands play the caller's unlimited round, the one the
// /wordle card opens: each player has their own, in groups too.

// player returns the caller, replying itself when the command cannot be
// played here.
func (s *service) player(ctx context.Context, b *bot.Bot, msg *models.Message) (int64, bool) {
	switch {
	case msg.Chat.Type == models.ChatTypeChannel:
		_ = chathelper.Reply(ctx, b, msg, msgWordleChannel)
	case s.rounds == nil:
		_ = chathelper.Reply(ctx, b, msg, msgNoStore)
	case msg.From == nil || msg.From.ID <= 0:
		_ = chathelper.Reply(ctx, b, msg, msgNoPlayer)
	case msg.From.IsBot || msg.SenderChat != nil:
		// Telegram's stand-in senders (GroupAnonymousBot, Channel_Bot) share
		// one id across every chat, so they would share one round.
		_ = chathelper.Reply(ctx, b, msg, msgNotYourself)
	default:
		return msg.From.ID, true
	}
	return 0, false
}

// handleWordle is /wordle [word]. Without a word it sends the unlimited
// card, or shows the round's board when the web game is not configured;
// with one it plays that guess on the caller's round.
func (s *service) handleWordle(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	uid, ok := s.player(ctx, b, msg)
	if !ok {
		return nil
	}
	arg := chathelper.ArgAfterCommand(msg.Text)
	if arg == "" {
		if s.enabled() {
			return s.sendCard(ctx, b, msg)
		}
		st, err := s.rounds.Load(ctx, uid, maxGuesses)
		if err != nil {
			return err
		}
		return chathelper.Reply(ctx, b, msg, boardHeader(st.Round)+"\n\n"+renderBoard(st.Round.Guesses))
	}
	st, err := s.rounds.Guess(ctx, uid, 0, arg, maxGuesses)
	switch {
	case errors.Is(err, guessgame.ErrFinished):
		return chathelper.Reply(ctx, b, msg, roundOver(st.Round))
	case errors.Is(err, errWordEmpty):
		return chathelper.Reply(ctx, b, msg, "Please provide a 5-letter word.")
	case errors.Is(err, errWordLength):
		return chathelper.Reply(ctx, b, msg, "Word must be exactly 5 letters.")
	case errors.Is(err, errWordUnknown):
		return chathelper.Reply(ctx, b, msg, "Not in the word list.")
	case err != nil:
		return err
	}
	rd := st.Round
	last := rd.Guesses[len(rd.Guesses)-1]
	text := renderGuess(last.Word, last.Marks) + "\n\n"
	switch rd.Status {
	case guessgame.StatusWon:
		text += fmt.Sprintf("🎉 Solved in %d/%d! Streak: %d. /wordle_new for another.", len(rd.Guesses), rd.MaxGuesses, st.Stats.CurStreak)
	case guessgame.StatusLost:
		text += fmt.Sprintf("❌ Out of guesses. Answer was %s. /wordle_new to retry.", strings.ToUpper(rd.Target))
	default:
		text += fmt.Sprintf("Guess %d/%d.", len(rd.Guesses), rd.MaxGuesses)
	}
	return chathelper.Reply(ctx, b, msg, text)
}

// handleNew is /wordle_new: it gives up the caller's round (a loss once it
// has a guess), starts a fresh one and sends its card.
func (s *service) handleNew(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	uid, ok := s.player(ctx, b, msg)
	if !ok {
		return nil
	}
	st, err := s.rounds.New(ctx, uid, 0, maxGuesses)
	if err != nil {
		return err
	}
	prelude := ""
	if st.Abandoned != nil {
		prelude = fmt.Sprintf("🏳️ Previous round abandoned (auto-giveup). Answer was %s.", strings.ToUpper(st.Abandoned.Target))
	}
	if !s.enabled() {
		if prelude != "" {
			prelude += "\n\n"
		}
		return chathelper.Reply(ctx, b, msg, prelude+msgNewHint)
	}
	if prelude != "" {
		if err := chathelper.Reply(ctx, b, msg, prelude); err != nil {
			return err
		}
	}
	return s.sendCard(ctx, b, msg)
}

// handleGiveup is /wordle_giveup: it ends the caller's round as a loss and
// reveals the answer. On a finished round it only echoes the answer.
func (s *service) handleGiveup(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	uid, ok := s.player(ctx, b, msg)
	if !ok {
		return nil
	}
	st, err := s.rounds.GiveUp(ctx, uid)
	switch {
	case errors.Is(err, guessgame.ErrNoRound):
		return chathelper.Reply(ctx, b, msg, "No active round. /wordle_new to start one.")
	case err != nil:
		return err
	}
	answer := strings.ToUpper(st.Round.Target)
	switch {
	case st.Ended:
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("🏳️ Answer was %s. /wordle_new for another.", answer))
	case st.Round.Status == guessgame.StatusWon:
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Already solved — %s.", answer))
	case st.Round.GaveUp:
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Already gave up — %s.", answer))
	}
	return chathelper.Reply(ctx, b, msg, roundOver(st.Round))
}

// handleStats is /wordle_stats: the caller's unlimited stats.
func (s *service) handleStats(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	uid, ok := s.player(ctx, b, msg)
	if !ok {
		return nil
	}
	st, err := s.rounds.LoadStats(ctx, uid)
	if err != nil {
		return err
	}
	return chathelper.Reply(ctx, b, msg, fmt.Sprintf(
		"📊 Wordle stats for %s\nPlayed: %d\nWins: %d (%d%%)\nCurrent streak: %d\nBest streak: %d",
		guessgame.DisplayName(msg.From.FirstName, msg.From.LastName),
		st.Played, st.Wins, chathelper.WinRate(st.Wins, st.Played), st.CurStreak, st.MaxStreak))
}

// sendCard sends the recorded unlimited card into the command's chat.
func (s *service) sendCard(ctx context.Context, b *bot.Bot, msg *models.Message) error {
	if err := s.cards.SendCard(ctx, b, msg, GameShortName); err != nil {
		_ = chathelper.Reply(ctx, b, msg, msgWordleSendFail)
		return err
	}
	return nil
}

func roundOver(rd guessgame.Round) string {
	return fmt.Sprintf("Current round is over. Use /wordle_new to start another. Answer was %s.", strings.ToUpper(rd.Target))
}

// boardHeader is the status line above the in-chat board.
func boardHeader(rd guessgame.Round) string {
	switch {
	case rd.Status == guessgame.StatusWon:
		return fmt.Sprintf("🎉 Solved in %d/%d. /wordle_new for another.", len(rd.Guesses), rd.MaxGuesses)
	case rd.GaveUp:
		return fmt.Sprintf("🏳️ Gave up. Answer was %s. /wordle_new for another.", strings.ToUpper(rd.Target))
	case rd.Status == guessgame.StatusLost:
		return fmt.Sprintf("❌ Out of guesses. Answer was %s. /wordle_new to retry.", strings.ToUpper(rd.Target))
	}
	return fmt.Sprintf("Guess %d/%d. Use `/wordle <word>`.", len(rd.Guesses), rd.MaxGuesses)
}

// renderGuess formats one guess as the NYT share pattern: the word on one
// line, its emoji row below.
//
//	CRANE
//	🟩🟨⬜🟩🟩
func renderGuess(word, marks string) string {
	return strings.ToUpper(word) + "\n" + emojiRow(marks)
}

// renderBoard joins the round's guesses, blank-line separated.
func renderBoard(guesses []guessgame.Guess) string {
	if len(guesses) == 0 {
		return "No guesses yet. Reply with `/wordle <word>`."
	}
	rows := make([]string, len(guesses))
	for i, g := range guesses {
		rows[i] = renderGuess(g.Word, g.Marks)
	}
	return strings.Join(rows, "\n\n")
}
