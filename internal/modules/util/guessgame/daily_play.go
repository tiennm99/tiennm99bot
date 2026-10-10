package guessgame

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

const reportTimeout = 10 * time.Second

// Outcome is a player's daily game after a request, with what the request
// changed.
type Outcome struct {
	Num    int
	Answer string
	P      Progress
	Stats  UserStats // set once the game is over

	guessed bool // this request added a guess
	ended   bool // this request finished the game
	joined  bool // this request added the card's chat to the player's chats
	report  bool // this request must report the win on the card
}

// cardKeyOf names the card a token was issued for, so a win is reported on
// each card once.
func cardKeyOf(c Claims) string {
	if c.InlineID != "" {
		return "i:" + c.InlineID
	}
	return "c:" + strconv.FormatInt(c.ChatID, 10) + ":" + strconv.Itoa(c.MessageID)
}

func userLockKey(userID int64) string { return "u:" + strconv.FormatInt(userID, 10) }

// SubmitGuess plays input on today's puzzle for the token's player. num is
// the puzzle the page shows; a stale one is refused so a board never mixes
// two days. Rules.Judge errors pass through unchanged.
func (d *Daily) SubmitGuess(ctx context.Context, c Claims, num int, input string) (Outcome, error) {
	now := d.cfg.Now()
	today := d.PuzzleNum(now)
	if num != today {
		return Outcome{}, ErrNewPuzzle
	}
	answer, err := d.ResolvePuzzle(ctx, today)
	if err != nil {
		return Outcome{}, err
	}
	o, err := d.guessLocked(ctx, c, today, answer, input, now)
	if err != nil {
		if o.ended {
			// The refused guess repaired a finished board's stats; its
			// group bookkeeping still has to run.
			d.afterPlay(c, o)
		}
		return o, err
	}
	d.afterPlay(c, o)
	return o, nil
}

func (d *Daily) guessLocked(ctx context.Context, c Claims, num int, answer, input string, now time.Time) (Outcome, error) {
	defer d.Locks.Acquire(userLockKey(c.UserID))()
	p, err := d.LoadProgress(ctx, num, c.UserID)
	if err != nil {
		return Outcome{}, err
	}
	if p.Finished() {
		// The board is the record: a finish whose stats write failed is
		// folded into the stats now, before the guess is refused.
		o := Outcome{Num: num, Answer: answer, P: p}
		if o.Stats, o.ended, err = d.finishStats(ctx, &p); err != nil {
			return Outcome{}, err
		}
		return o, ErrFinished
	}
	g, err := d.cfg.Rules.Judge(input, answer, p.Guesses)
	if err != nil {
		return Outcome{}, err
	}
	o := Outcome{Num: num, Answer: answer, guessed: true}
	p.Name = c.Name
	p.Guesses = append(p.Guesses, g)
	o.joined = d.joinLocally(&p, c)
	if g.Word == answer || len(p.Guesses) >= d.MaxGuesses() {
		p.Status, p.FinishedAt, o.ended = StatusLost, now.UnixMilli(), true
		if g.Word == answer {
			p.Status = StatusWon
			o.report = needsReport(p, c)
		}
	}
	if err := d.Plays.Put(ctx, PlayKey(num, c.UserID), p); err != nil {
		return Outcome{}, err
	}
	if o.ended {
		// The board is saved first and is the record. If this stats write
		// fails, the next state load or guess folds the finished board in;
		// the LastNum guard keeps it from counting twice.
		if o.Stats, _, err = d.finishStats(ctx, &p); err != nil {
			return Outcome{}, err
		}
	}
	o.P = p
	return o, nil
}

// LoadState returns today's game for the token's player. Opening a card
// also puts a player who already guessed into that card's group results,
// and reports a win on a card it was not reported on yet.
func (d *Daily) LoadState(ctx context.Context, c Claims) (Outcome, error) {
	today := d.PuzzleNum(d.cfg.Now())
	answer, err := d.ResolvePuzzle(ctx, today)
	if err != nil {
		return Outcome{}, err
	}
	o, err := d.stateLocked(ctx, c, today, answer)
	if err != nil {
		return o, err
	}
	d.afterPlay(c, o)
	return o, nil
}

func (d *Daily) stateLocked(ctx context.Context, c Claims, num int, answer string) (Outcome, error) {
	defer d.Locks.Acquire(userLockKey(c.UserID))()
	p, err := d.LoadProgress(ctx, num, c.UserID)
	if err != nil {
		return Outcome{}, err
	}
	o := Outcome{Num: num, Answer: answer}
	if len(p.Guesses) > 0 {
		o.joined = d.joinLocally(&p, c)
	}
	if p.Status == StatusWon {
		o.report = needsReport(p, c)
	}
	if o.joined {
		if c.Name != "" {
			p.Name = c.Name
		}
		if err := d.Plays.Put(ctx, PlayKey(num, c.UserID), p); err != nil {
			return Outcome{}, err
		}
	}
	if p.Finished() {
		// Folds in a finished board whose stats write failed; a no-op
		// otherwise.
		if o.Stats, o.ended, err = d.finishStats(ctx, &p); err != nil {
			return Outcome{}, err
		}
	}
	o.P = p
	return o, nil
}

// joinLocally adds the card's group topic to the player's chats. Only group
// chats (negative IDs) have a results message; a private chat or an inline
// card has none.
func (d *Daily) joinLocally(p *Progress, c Claims) bool {
	if c.InlineID != "" || c.ChatID >= 0 || p.joinedChat(c.ChatID, c.ThreadID) || len(p.Chats) >= maxChats {
		return false
	}
	p.Chats = append(p.Chats, ChatRef{ChatID: c.ChatID, ThreadID: c.ThreadID, CardID: c.MessageID})
	return true
}

// needsReport reports whether the win still has to be reported on the
// token's card. A card is recorded in Reported only once Telegram accepted
// the score, so a failed report is retried on the next state load.
func needsReport(p Progress, c Claims) bool {
	return !slices.Contains(p.Reported, cardKeyOf(c)) && len(p.Reported) < maxReported
}

// recordReported notes that the win of puzzle num is on the token's card.
func (d *Daily) recordReported(ctx context.Context, c Claims, num int) error {
	defer d.Locks.Acquire(userLockKey(c.UserID))()
	p, err := d.LoadProgress(ctx, num, c.UserID)
	if err != nil || p.Status != StatusWon || !needsReport(p, c) {
		return err
	}
	p.Reported = append(p.Reported, cardKeyOf(c))
	return d.Plays.Put(ctx, PlayKey(num, c.UserID), p)
}

// finishStats folds the finished game into the player's stats, once per
// puzzle, and reports whether this call folded it. Caller holds the
// player's lock.
func (d *Daily) finishStats(ctx context.Context, p *Progress) (UserStats, bool, error) {
	st, err := d.LoadStats(ctx, p.UserID)
	if err != nil {
		return UserStats{}, false, err
	}
	if st.LastNum >= p.Num {
		return st, false, nil
	}
	st.Played++
	if p.Status == StatusWon {
		st.Wins++
		st.Dist[len(p.Guesses)-1]++
		if st.LastWinNum == p.Num-1 {
			st.CurStreak++
		} else {
			st.CurStreak = 1
		}
		st.MaxStreak = max(st.MaxStreak, st.CurStreak)
		st.LastWinNum = p.Num
	} else {
		st.CurStreak = 0
	}
	st.LastNum = p.Num
	if err := d.Stats.Put(ctx, d.StatsKey(p.UserID), st); err != nil {
		return UserStats{}, false, err
	}
	return st, true, nil
}

// afterPlay runs a request's side effects once the player's lock is
// released: the group results bookkeeping, the live summary and the score
// report. Failures are logged; the player's game is already saved.
func (d *Daily) afterPlay(c Claims, o Outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	if o.joined {
		ref := o.P.Chats[len(o.P.Chats)-1]
		if err := d.addChatPlayer(ctx, o.Num, ref, c.UserID, o.P.Name); err != nil {
			log.Warn(d.cfg.LogName+" chat join failed", "err", err)
		}
		if o.P.Status == StatusWon && !o.ended {
			d.bumpStreak(ctx, o.Num, ref)
		}
	}
	if o.ended && o.P.Status == StatusWon {
		for _, ref := range o.P.Chats {
			d.bumpStreak(ctx, o.Num, ref)
		}
	}
	if o.guessed || o.joined || o.ended {
		for _, ref := range o.P.Chats {
			d.TouchSummary(o.Num, ref.ChatID, ref.ThreadID)
		}
	}
	if o.report {
		d.reportWin(c, o.Num, len(o.P.Guesses))
	}
}

// reportWin sets the score on the card in the background, then records the
// card as reported. BOT_SCORE_NOT_MODIFIED (the score does not beat the
// player's best there) counts as reported; any other failure leaves the
// card unrecorded, so the next state load from it tries again.
func (d *Daily) reportWin(c Claims, num, guesses int) {
	if d.cfg.Reporter == nil {
		return
	}
	addr := htmlgame.Address{ChatID: c.ChatID, MessageID: c.MessageID, InlineID: c.InlineID}
	d.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		err := d.cfg.Reporter.Report(ctx, addr, c.UserID, d.Score(guesses))
		if err != nil && !htmlgame.ScoreNotModified(err) {
			log.Warn(d.cfg.LogName+" score report failed", "err", err)
			return
		}
		if err := d.recordReported(ctx, c, num); err != nil {
			log.Warn(d.cfg.LogName+" score report record failed", "err", err)
		}
	})
}

// addChatPlayer lists the player in the chat topic's day.
func (d *Daily) addChatPlayer(ctx context.Context, num int, ref ChatRef, userID int64, name string) error {
	return updateVersioned(ctx, d.ChatDays, ChatDayKey(num, ref.ChatID, ref.ThreadID), func(cd *ChatDay, found bool) bool {
		changed := !found
		if !found {
			*cd = ChatDay{Num: num, ChatID: ref.ChatID, ThreadID: ref.ThreadID, Players: []ChatPlayer{}}
		}
		if cd.CardID == 0 && ref.CardID != 0 {
			cd.CardID, changed = ref.CardID, true
		}
		if !slices.ContainsFunc(cd.Players, func(p ChatPlayer) bool { return p.UserID == userID }) && len(cd.Players) < maxChatPlayers {
			cd.Players, changed = append(cd.Players, ChatPlayer{UserID: userID, Name: name}), true
		}
		return changed
	})
}

// bumpStreak counts puzzle num toward the chat topic's streak, once.
func (d *Daily) bumpStreak(ctx context.Context, num int, ref ChatRef) {
	err := updateVersioned(ctx, d.Streaks, ChatStreakKey(ref.ChatID, ref.ThreadID), func(st *ChatStreak, _ bool) bool {
		switch {
		case st.LastNum >= num:
			return false
		case st.LastNum == num-1:
			st.Streak++
		default:
			st.Streak = 1
		}
		st.LastNum = num
		return true
	})
	if err != nil {
		log.Warn(d.cfg.LogName+" group streak update failed", "err", err)
	}
}

// DisplayStreak is the current streak as shown on puzzle today: a streak
// whose last win is older than yesterday is over.
func DisplayStreak(st UserStats, today int) int {
	if st.LastWinNum < today-1 {
		return 0
	}
	return st.CurStreak
}

// ResultLine is "3/6" for a win, "X/6" for a loss.
func (d *Daily) ResultLine(p Progress) string {
	if p.Status == StatusWon {
		return strconv.Itoa(len(p.Guesses)) + "/" + strconv.Itoa(d.MaxGuesses())
	}
	return "X/" + strconv.Itoa(d.MaxGuesses())
}

// ShareText is the NYT-style spoiler-free result of a finished game.
func (d *Daily) ShareText(p Progress) string {
	text := d.cfg.Rules.Label() + " #" + strconv.Itoa(p.Num) + " " + d.ResultLine(p) + "\n"
	for _, g := range p.Guesses {
		text += "\n" + EmojiRow(d.cfg.Rules, g.Marks)
	}
	return text
}
