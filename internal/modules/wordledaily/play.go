package wordledaily

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
)

const reportTimeout = 10 * time.Second

// Rejections of a guess, mapped to API errors in http_api.go.
var (
	errNewPuzzle   = errors.New("wordledaily: the puzzle changed")
	errFinished    = errors.New("wordledaily: today's puzzle is finished")
	errWordLength  = errors.New("wordledaily: word must be 5 letters")
	errWordUnknown = errors.New("wordledaily: not in the word list")
)

// outcome is a player's game after a request, with what the request changed.
type outcome struct {
	num     int
	answer  string
	p       progress
	stats   userStats // set once the game is over
	guessed bool      // this request added a guess
	ended   bool      // this request finished the game
	joined  bool      // this request added the card's chat to the player's chats
	report  bool      // this request must report the win on the card
}

// cardKey names the card a token was issued for, so a win is reported on
// each card once.
func cardKey(c claims) string {
	if c.InlineID != "" {
		return "i:" + c.InlineID
	}
	return "c:" + strconv.FormatInt(c.ChatID, 10) + ":" + strconv.Itoa(c.MessageID)
}

func userLockKey(userID int64) string { return "u:" + strconv.FormatInt(userID, 10) }

// marksOf encodes scored letters as c/p/w, the stored and served form.
func marksOf(scores []wordlist.LetterScore) string {
	var b strings.Builder
	for _, sc := range scores {
		switch sc.Result {
		case wordlist.ResultCorrect:
			b.WriteByte('c')
		case wordlist.ResultPartial:
			b.WriteByte('p')
		default:
			b.WriteByte('w')
		}
	}
	return b.String()
}

// emojiRow renders stored marks as the spoiler-free share row.
func emojiRow(marks string) string {
	var b strings.Builder
	for i := 0; i < len(marks); i++ {
		switch marks[i] {
		case 'c':
			b.WriteString(wordlist.Marker(wordlist.ResultCorrect))
		case 'p':
			b.WriteString(wordlist.Marker(wordlist.ResultPartial))
		default:
			b.WriteString(wordlist.Marker(wordlist.ResultWrong))
		}
	}
	return b.String()
}

// score is what a win reports with setGameScore: fewer guesses rank higher.
func score(guesses int) int { return maxGuesses + 1 - guesses }

// submitGuess plays word on today's puzzle for the token's player. num is
// the puzzle the page shows; a stale one is refused so a board never mixes
// two days.
func (s *service) submitGuess(ctx context.Context, c claims, num int, word string) (outcome, error) {
	now := s.cfg.now()
	today := s.puzzleNum(now)
	if num != today {
		return outcome{}, errNewPuzzle
	}
	answer, err := s.resolvePuzzle(ctx, today)
	if err != nil {
		return outcome{}, err
	}
	o, err := s.guessLocked(ctx, c, today, answer, word, now)
	if err != nil {
		if o.ended {
			// The refused guess repaired a finished board's stats; its
			// group bookkeeping still has to run.
			s.afterPlay(c, o)
		}
		return o, err
	}
	s.afterPlay(c, o)
	return o, nil
}

func (s *service) guessLocked(ctx context.Context, c claims, num int, answer, word string, now time.Time) (outcome, error) {
	defer s.locks.Acquire(userLockKey(c.UserID))()
	p, err := s.loadProgress(ctx, num, c.UserID)
	if err != nil {
		return outcome{}, err
	}
	if p.finished() {
		// The board is the record: a finish whose stats write failed is
		// folded into the stats now, before the guess is refused.
		o := outcome{num: num, answer: answer, p: p}
		if o.stats, o.ended, err = s.finishStats(ctx, &p); err != nil {
			return outcome{}, err
		}
		return o, errFinished
	}
	v := wordlist.Validate(s.cfg.dict, word)
	switch {
	case v.Reason == wordlist.ReasonUnknown:
		return outcome{}, errWordUnknown
	case !v.OK:
		return outcome{}, errWordLength
	}
	o := outcome{num: num, answer: answer, guessed: true}
	p.Name = c.Name
	p.Guesses = append(p.Guesses, guess{Word: v.Word, Marks: marksOf(wordlist.Compare(v.Word, answer))})
	o.joined = s.joinLocally(&p, c)
	if v.Word == answer || len(p.Guesses) >= maxGuesses {
		p.Status, p.FinishedAt, o.ended = statusLost, now.UnixMilli(), true
		if v.Word == answer {
			p.Status = statusWon
			o.report = needsReport(p, c)
		}
	}
	if err := s.plays.Put(ctx, playKey(num, c.UserID), p); err != nil {
		return outcome{}, err
	}
	if o.ended {
		// The board is saved first and is the record. If this stats write
		// fails, the next state load or guess folds the finished board in;
		// the LastNum guard keeps it from counting twice.
		if o.stats, _, err = s.finishStats(ctx, &p); err != nil {
			return outcome{}, err
		}
	}
	o.p = p
	return o, nil
}

// loadState returns today's game for the token's player. Opening a card
// also puts a player who already guessed into that card's group results,
// and reports a win on a card it was not reported on yet.
func (s *service) loadState(ctx context.Context, c claims) (outcome, error) {
	today := s.puzzleNum(s.cfg.now())
	answer, err := s.resolvePuzzle(ctx, today)
	if err != nil {
		return outcome{}, err
	}
	o, err := s.stateLocked(ctx, c, today, answer)
	if err != nil {
		return o, err
	}
	s.afterPlay(c, o)
	return o, nil
}

func (s *service) stateLocked(ctx context.Context, c claims, num int, answer string) (outcome, error) {
	defer s.locks.Acquire(userLockKey(c.UserID))()
	p, err := s.loadProgress(ctx, num, c.UserID)
	if err != nil {
		return outcome{}, err
	}
	o := outcome{num: num, answer: answer}
	if len(p.Guesses) > 0 {
		o.joined = s.joinLocally(&p, c)
	}
	if p.Status == statusWon {
		o.report = needsReport(p, c)
	}
	if o.joined {
		if c.Name != "" {
			p.Name = c.Name
		}
		if err := s.plays.Put(ctx, playKey(num, c.UserID), p); err != nil {
			return outcome{}, err
		}
	}
	if p.finished() {
		// Folds in a finished board whose stats write failed; a no-op
		// otherwise.
		if o.stats, o.ended, err = s.finishStats(ctx, &p); err != nil {
			return outcome{}, err
		}
	}
	o.p = p
	return o, nil
}

// joinLocally adds the card's group topic to the player's chats. Only group
// chats (negative IDs) have a results message; a private chat or an inline
// card has none.
func (s *service) joinLocally(p *progress, c claims) bool {
	if c.InlineID != "" || c.ChatID >= 0 || p.joinedChat(c.ChatID, c.ThreadID) || len(p.Chats) >= maxChats {
		return false
	}
	p.Chats = append(p.Chats, chatRef{ChatID: c.ChatID, ThreadID: c.ThreadID, CardID: c.MessageID})
	return true
}

// needsReport reports whether the win still has to be reported on the
// token's card. A card is recorded in Reported only once Telegram accepted
// the score, so a failed report is retried on the next state load.
func needsReport(p progress, c claims) bool {
	return !slices.Contains(p.Reported, cardKey(c)) && len(p.Reported) < maxReported
}

// recordReported notes that the win of puzzle num is on the token's card.
func (s *service) recordReported(ctx context.Context, c claims, num int) error {
	defer s.locks.Acquire(userLockKey(c.UserID))()
	p, err := s.loadProgress(ctx, num, c.UserID)
	if err != nil || p.Status != statusWon || !needsReport(p, c) {
		return err
	}
	p.Reported = append(p.Reported, cardKey(c))
	return s.plays.Put(ctx, playKey(num, c.UserID), p)
}

// finishStats folds the finished game into the player's stats, once per
// puzzle, and reports whether this call folded it. Caller holds the
// player's lock.
func (s *service) finishStats(ctx context.Context, p *progress) (userStats, bool, error) {
	st, err := s.loadStats(ctx, p.UserID)
	if err != nil {
		return userStats{}, false, err
	}
	if st.LastNum >= p.Num {
		return st, false, nil
	}
	st.Played++
	if p.Status == statusWon {
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
	if err := s.stats.Put(ctx, statsKey(p.UserID), st); err != nil {
		return userStats{}, false, err
	}
	return st, true, nil
}

// afterPlay runs a request's side effects once the player's lock is
// released: the group results bookkeeping, the live summary and the score
// report. Failures are logged; the player's game is already saved.
func (s *service) afterPlay(c claims, o outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	if o.joined {
		ref := o.p.Chats[len(o.p.Chats)-1]
		if err := s.addChatPlayer(ctx, o.num, ref, c.UserID, o.p.Name); err != nil {
			log.Warn("wordledaily chat join failed", "err", err)
		}
		if o.p.Status == statusWon && !o.ended {
			s.bumpStreak(ctx, o.num, ref)
		}
	}
	if o.ended && o.p.Status == statusWon {
		for _, ref := range o.p.Chats {
			s.bumpStreak(ctx, o.num, ref)
		}
	}
	if o.guessed || o.joined || o.ended {
		for _, ref := range o.p.Chats {
			s.touchSummary(o.num, ref.ChatID, ref.ThreadID)
		}
	}
	if o.report {
		s.reportWin(c, o.num, len(o.p.Guesses))
	}
}

// reportWin sets the score on the card in the background, then records the
// card as reported. BOT_SCORE_NOT_MODIFIED (the score does not beat the
// player's best there) counts as reported; any other failure leaves the
// card unrecorded, so the next state load from it tries again.
func (s *service) reportWin(c claims, num, guesses int) {
	addr := htmlgame.Address{ChatID: c.ChatID, MessageID: c.MessageID, InlineID: c.InlineID}
	s.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		err := s.cfg.reporter.Report(ctx, addr, c.UserID, score(guesses))
		if err != nil && !htmlgame.ScoreNotModified(err) {
			log.Warn("wordledaily score report failed", "err", err)
			return
		}
		if err := s.recordReported(ctx, c, num); err != nil {
			log.Warn("wordledaily score report record failed", "err", err)
		}
	})
}

func (s *service) async(f func()) {
	if s.cfg.async != nil {
		s.cfg.async(f)
		return
	}
	go f()
}

// addChatPlayer lists the player in the chat topic's day.
func (s *service) addChatPlayer(ctx context.Context, num int, ref chatRef, userID int64, name string) error {
	return updateVersioned(ctx, s.chatDays, chatDayKey(num, ref.ChatID, ref.ThreadID), func(cd *chatDay, found bool) bool {
		changed := !found
		if !found {
			*cd = chatDay{Num: num, ChatID: ref.ChatID, ThreadID: ref.ThreadID, Players: []chatPlayer{}}
		}
		if cd.CardID == 0 && ref.CardID != 0 {
			cd.CardID, changed = ref.CardID, true
		}
		if !slices.ContainsFunc(cd.Players, func(p chatPlayer) bool { return p.UserID == userID }) && len(cd.Players) < maxChatPlayers {
			cd.Players, changed = append(cd.Players, chatPlayer{UserID: userID, Name: name}), true
		}
		return changed
	})
}

// bumpStreak counts puzzle num toward the chat topic's streak, once.
func (s *service) bumpStreak(ctx context.Context, num int, ref chatRef) {
	err := updateVersioned(ctx, s.streaks, chatStreakKey(ref.ChatID, ref.ThreadID), func(st *chatStreak, _ bool) bool {
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
		log.Warn("wordledaily group streak update failed", "err", err)
	}
}

// displayStreak is the current streak as shown on puzzle today: a streak
// whose last win is older than yesterday is over.
func displayStreak(st userStats, today int) int {
	if st.LastWinNum < today-1 {
		return 0
	}
	return st.CurStreak
}
