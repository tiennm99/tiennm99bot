package loldle

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
)

const (
	newRoundHint = "🆕 Send <code>/loldle</code> or <code>/loldle &lt;champion&gt;</code> to start a new round."

	msgChannel       = "LoLdle can't be played in channels."
	msgSendFail      = "Couldn't send LoLdle. Try again later."
	msgDailyChannel  = "LoLdle Daily can't be played in channels."
	msgDailySendFail = "Couldn't send LoLdle Daily. Try again later."
	msgNoStore       = "LoLdle isn't available on this server."
	msgNoPlayer      = "Cannot identify the player."
	msgNotYourself   = "Post as yourself to play: anonymous admins and posts on behalf of a chat have no round of their own."
)

// The /loldle commands play the caller's unlimited round, the one the
// /loldle card opens: each player has their own, in groups too. A chat's
// /loldle_setmax sets the length of the rounds started there.

// player returns the caller, replying itself when the command cannot be
// played here.
func (s *service) player(ctx context.Context, b *bot.Bot, msg *models.Message) (int64, bool) {
	switch {
	case msg.Chat.Type == models.ChatTypeChannel:
		_ = chathelper.Reply(ctx, b, msg, msgChannel)
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

// maxFor is the length of an unlimited round started in subject's chat.
func (s *service) maxFor(ctx context.Context, subject string) (int, error) {
	if subject == "" {
		return MaxGuesses, nil
	}
	return getMaxGuesses(ctx, s.settings, subject)
}

// current returns the caller's round to play on: their round being played,
// or a new one when the last is over, since a finished round only waits to
// be looked at.
func (s *service) current(ctx context.Context, uid int64, maxGuesses int) (guessgame.RoundState, error) {
	st, err := s.rounds.Load(ctx, uid, maxGuesses)
	if err != nil || !st.Round.Finished() {
		return st, err
	}
	return s.rounds.New(ctx, uid, st.Round.Seq, maxGuesses)
}

// handleLoldle is /loldle [champion]. Without a champion it sends the
// unlimited card, or shows the round's board when the web game is not
// configured; with one it plays that guess on the caller's round.
func (s *service) handleLoldle(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	uid, ok := s.player(ctx, b, msg)
	if !ok {
		return nil
	}
	maxGuesses, err := s.maxFor(ctx, chathelper.SubjectFor(msg))
	if err != nil {
		return err
	}
	arg := chathelper.ArgAfterCommand(msg.Text)
	if arg == "" {
		st, err := s.current(ctx, uid, maxGuesses)
		if err != nil {
			return err
		}
		if s.enabled() {
			return s.sendCard(ctx, b, msg)
		}
		header := fmt.Sprintf("Guess %d/%d. Use <code>/loldle &lt;champion&gt;</code>.", len(st.Round.Guesses), st.Round.MaxGuesses)
		return chathelper.ReplyHTML(ctx, b, msg, header+"\n\n"+renderBoard(s.boardOf(st.Round)))
	}

	st, err := s.rounds.Guess(ctx, uid, 0, arg, maxGuesses)
	if errors.Is(err, guessgame.ErrFinished) {
		// The last round is over: this guess opens the next one.
		if _, err = s.rounds.New(ctx, uid, st.Round.Seq, maxGuesses); err == nil {
			st, err = s.rounds.Guess(ctx, uid, 0, arg, maxGuesses)
		}
	}
	switch {
	case errors.Is(err, errAmbiguous):
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Ambiguous champion %q. Type the full champion name.", arg))
	case errors.Is(err, errUnknownChampion):
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Champion not found: %q.", arg))
	case errors.Is(err, errDuplicate):
		name := arg
		if c, _ := findChampionMatch(s.cfg.champions, arg); c != nil {
			name = c.ChampionName
		}
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"🔁 <b>%s</b> was already guessed this round — try another champion.", html.EscapeString(name)))
	case errors.Is(err, errTargetGone):
		// champions.json was refreshed and the answer is gone: drop the
		// round without counting it.
		if _, err := s.rounds.Replace(ctx, uid, maxGuesses); err != nil {
			return err
		}
		return chathelper.ReplyHTML(ctx, b, msg, "Champion data was updated since this round started. "+newRoundHint)
	case err != nil:
		return err
	}

	rd := st.Round
	last := rd.Guesses[len(rd.Guesses)-1]
	rendered := renderGuess(last.Word, s.rowsOf(last))
	champ := html.EscapeString(rd.Target)
	switch rd.Status {
	case guessgame.StatusWon:
		trySendSticker(ctx, b, msg, winStickers)
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"%s\n\n🎉 %s %s\n⏱ %s · 🔥 Streak: %d (%d/%d)\n%s",
			rendered, attemptFlavor(len(rd.Guesses), rd.MaxGuesses), champ, formatDuration(rd.FinishedAt-rd.StartedAt),
			st.Stats.CurStreak, len(rd.Guesses), rd.MaxGuesses, newRoundHint))
	case guessgame.StatusLost:
		trySendSticker(ctx, b, msg, loseStickers)
		return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf(
			"%s\n\n❌ Out of guesses. Answer was %s.\n%s", rendered, champ, newRoundHint))
	}
	return chathelper.ReplyHTML(ctx, b, msg, fmt.Sprintf("%s\n\nGuess %d/%d.", rendered, len(rd.Guesses), rd.MaxGuesses))
}

// handleGiveup is /loldle_giveup: it ends the caller's round as a loss and
// reveals the answer.
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
		return chathelper.ReplyHTML(ctx, b, msg, "No active round. "+newRoundHint)
	case err != nil:
		return err
	case !st.Ended:
		return chathelper.ReplyHTML(ctx, b, msg, "No active round. "+newRoundHint)
	}
	trySendSticker(ctx, b, msg, giveupStickers)
	return chathelper.ReplyHTML(ctx, b, msg,
		fmt.Sprintf("🏳️ Answer was %s.\n%s", html.EscapeString(st.Round.Target), newRoundHint))
}

// handleStats is /loldle_stats: the caller's unlimited stats.
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
		"📊 LoLdle stats for %s\nPlayed: %d\nWins: %d (%d%%)\nCurrent streak: %d\nBest streak: %d",
		guessgame.DisplayName(msg.From.FirstName, msg.From.LastName),
		st.Played, st.Wins, chathelper.WinRate(st.Wins, st.Played), st.CurStreak, st.MaxStreak))
}

// handleSetMax is /loldle_setmax <n> — owner-only (VisibilityPrivate). It
// sets the length (1..MaxGuessesCap) of the unlimited rounds started in this
// chat from now on, by command or from a card sent here; a round keeps the
// length it started with, and daily puzzles always allow MaxGuesses.
func (s *service) handleSetMax(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	subject := chathelper.SubjectFor(msg)
	if subject == "" {
		return chathelper.Reply(ctx, b, msg, "Cannot identify chat.")
	}
	if s.settings == nil {
		return chathelper.Reply(ctx, b, msg, msgNoStore)
	}
	n, err := strconv.Atoi(chathelper.ArgAfterCommand(msg.Text))
	if err != nil || !validMaxGuesses(n) {
		return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Usage: /loldle_setmax <1-%d>", MaxGuessesCap))
	}
	if err := setMaxGuesses(ctx, s.settings, subject, n); err != nil {
		return err
	}
	return chathelper.Reply(ctx, b, msg, fmt.Sprintf("✅ Loldle max guesses set to %d (applies to the next round).", n))
}

// sendCard sends the recorded unlimited card into the command's chat.
func (s *service) sendCard(ctx context.Context, b *bot.Bot, msg *models.Message) error {
	if err := s.cards.SendCard(ctx, b, msg, GameShortName); err != nil {
		_ = chathelper.Reply(ctx, b, msg, msgSendFail)
		return err
	}
	return nil
}

// rowsOf rebuilds a stored guess's comparison rows for the chat board from
// its marks and the guessed champion's own values. A champion removed from
// the data since keeps its marks with blank values.
func (s *service) rowsOf(g guessgame.Guess) []AttributeRow {
	champ := findChampionByExactName(s.cfg.champions, g.Word)
	rows := make([]AttributeRow, len(classicAttributes))
	for i, attr := range classicAttributes {
		row := attr
		row.GuessValue = attrDisplay(champ, attr)
		if i < len(g.Marks) {
			row.Result, row.Direction = markResult(g.Marks[i])
		} else {
			row.Result = ResultWrong
		}
		rows[i] = row
	}
	return rows
}

// boardOf is a round's guesses as chat board entries.
func (s *service) boardOf(rd guessgame.Round) []boardEntry {
	out := make([]boardEntry, len(rd.Guesses))
	for i, g := range rd.Guesses {
		out[i] = boardEntry{Champion: g.Word, Results: s.rowsOf(g)}
	}
	return out
}

// attrDisplay is a champion's own value of one attribute, as the boards
// show it; "—" for an unknown champion.
func attrDisplay(c *Champion, attr AttributeRow) string {
	if c == nil {
		return "—"
	}
	v := attrValue(c, attr.Key)
	if attr.Type == attrYear {
		return yearOrPlaceholder(parseYear(asString(v)))
	}
	return formatValue(v)
}

// trySendSticker sends a sticker, swallowing errors. A bad/expired file_id
// must never block the text reply that carries the round outcome.
//
// Threads MessageThreadID so stickers post in the same forum-supergroup topic
// as the inbound command (omission posts to General — same bug class as the
// Reply helper).
func trySendSticker(ctx context.Context, b *bot.Bot, msg *models.Message, pool []string) {
	id := pickSticker(pool)
	if id == "" || msg == nil {
		return
	}
	_, _ = b.SendSticker(ctx, &bot.SendStickerParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: msg.MessageThreadID,
		Sticker:         &models.InputFileString{Data: id},
	})
}
