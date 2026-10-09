package wordledaily

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Every document lives in the module's one collection under its own key
// prefix. JSON and BSON names match because the memory store round-trips
// through JSON and MongoDB through BSON.

const (
	statusPlaying = "playing"
	statusWon     = "won"
	statusLost    = "lost"

	maxGuesses = 6
	// maxReported bounds the cards a player's win is reported on per day.
	maxReported = 32
	// maxChats bounds the chats one player's daily result appears in.
	maxChats = 20
	// maxChatPlayers bounds one chat's daily player list.
	maxChatPlayers = 200
	// writeRetries bounds a versioned read-modify-write.
	writeRetries = 5
)

// puzzleDoc pins one day's answer once it has been resolved.
type puzzleDoc struct {
	Num       int    `json:"num" bson:"num"`
	Answer    string `json:"answer" bson:"answer"`
	CreatedAt int64  `json:"created_at" bson:"created_at"`
}

// guess is one submitted word and its marks: one letter per position, c
// (correct), p (present elsewhere) or w (wrong).
type guess struct {
	Word  string `json:"word" bson:"word"`
	Marks string `json:"marks" bson:"marks"`
}

// chatRef is a group chat (and forum topic) a player's result appears in,
// with the card they last played from there, which the live summary replies
// to.
type chatRef struct {
	ChatID   int64 `json:"chat_id" bson:"chat_id"`
	ThreadID int   `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
	CardID   int   `json:"card_id,omitempty" bson:"card_id,omitempty"`
}

// progress is one player's game on one puzzle.
type progress struct {
	Num        int       `json:"num" bson:"num"`
	UserID     int64     `json:"user_id" bson:"user_id"`
	Name       string    `json:"name" bson:"name"`
	Guesses    []guess   `json:"guesses" bson:"guesses"`
	Status     string    `json:"status" bson:"status"`
	FinishedAt int64     `json:"finished_at,omitempty" bson:"finished_at,omitempty"` // unix ms
	Reported   []string  `json:"reported,omitempty" bson:"reported,omitempty"`       // card keys the win was reported on
	Chats      []chatRef `json:"chats,omitempty" bson:"chats,omitempty"`
}

func (p *progress) finished() bool { return p.Status == statusWon || p.Status == statusLost }

// joinedChat reports whether the player's result already appears in the
// chat's topic.
func (p *progress) joinedChat(chatID int64, threadID int) bool {
	return slices.ContainsFunc(p.Chats, func(c chatRef) bool { return c.ChatID == chatID && c.ThreadID == threadID })
}

// userStats is a player's record across every puzzle.
type userStats struct {
	Played     int   `json:"played" bson:"played"`
	Wins       int   `json:"wins" bson:"wins"`
	CurStreak  int   `json:"cur_streak" bson:"cur_streak"`
	MaxStreak  int   `json:"max_streak" bson:"max_streak"`
	Dist       []int `json:"dist" bson:"dist"` // wins by guess count, index 0 = 1 guess
	LastNum    int   `json:"last_num" bson:"last_num"`
	LastWinNum int   `json:"last_win_num" bson:"last_win_num"`
}

// chatPlayer is one player listed in a chat's day.
type chatPlayer struct {
	UserID int64  `json:"user_id" bson:"user_id"`
	Name   string `json:"name" bson:"name"`
}

// chatDay is who played one puzzle in one chat topic, plus the live
// summary message the bot keeps there.
type chatDay struct {
	Num       int          `json:"num" bson:"num"`
	ChatID    int64        `json:"chat_id" bson:"chat_id"`
	ThreadID  int          `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
	Players   []chatPlayer `json:"players" bson:"players"`
	CardID    int          `json:"card_id,omitempty" bson:"card_id,omitempty"` // the card the summary replies to
	MsgID     int          `json:"msg_id,omitempty" bson:"msg_id,omitempty"`
	MsgSentAt int64        `json:"msg_sent_at,omitempty" bson:"msg_sent_at,omitempty"` // unix ms
}

// chatStreak counts the consecutive puzzles on which at least one player in
// a chat topic solved the word; a loss does not count. LastNum is the latest
// such puzzle.
type chatStreak struct {
	Streak  int `json:"streak" bson:"streak"`
	LastNum int `json:"last_num" bson:"last_num"`
}

// liveAt is the streak as of puzzle num: a streak whose last puzzle is
// older than yesterday has been broken.
func (c chatStreak) liveAt(num int) int {
	if c.LastNum >= num-1 {
		return c.Streak
	}
	return 0
}

func puzzleKey(num int) string { return "puzzle:" + strconv.Itoa(num) }

func playKey(num int, userID int64) string {
	return "play:" + strconv.Itoa(num) + ":" + strconv.FormatInt(userID, 10)
}

func statsKey(userID int64) string { return "stats:" + strconv.FormatInt(userID, 10) }

// chatTopic is "<chat>:<thread>:"; the trailing colon keeps chat -1 from
// matching chat -100 in a prefix.
func chatTopic(chatID int64, threadID int) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(threadID) + ":"
}

func chatDayKey(num int, chatID int64, threadID int) string {
	return "cday:" + strconv.Itoa(num) + ":" + chatTopic(chatID, threadID)
}

func chatStreakKey(chatID int64, threadID int) string {
	return "cstreak:" + chatTopic(chatID, threadID)
}

// loadProgress returns the player's game on puzzle num, or a fresh one.
func (s *service) loadProgress(ctx context.Context, num int, userID int64) (progress, error) {
	p, _, err := s.plays.Get(ctx, playKey(num, userID))
	switch {
	case err == nil:
		return p, nil
	case errors.Is(err, storage.ErrNotFound):
		return progress{Num: num, UserID: userID, Guesses: []guess{}, Status: statusPlaying}, nil
	default:
		return progress{}, err
	}
}

func (s *service) loadStats(ctx context.Context, userID int64) (userStats, error) {
	st, _, err := s.stats.Get(ctx, statsKey(userID))
	if errors.Is(err, storage.ErrNotFound) {
		return userStats{Dist: make([]int, maxGuesses)}, nil
	}
	if len(st.Dist) != maxGuesses {
		st.Dist = append(st.Dist, make([]int, maxGuesses)...)[:maxGuesses]
	}
	return st, err
}

// updateVersioned applies mutate to the document at key with a versioned
// write, retrying when another writer got in first. mutate gets the current
// value (zero and found=false when absent) and reports whether to write.
func updateVersioned[T any](ctx context.Context, store storage.DocStore[T], key string, mutate func(v *T, found bool) bool) error {
	for range writeRetries {
		v, version, err := store.Get(ctx, key)
		found := err == nil
		switch {
		case errors.Is(err, storage.ErrNotFound):
			version = 0
		case err != nil:
			return err
		}
		if !mutate(&v, found) {
			return nil
		}
		err = store.PutVersioned(ctx, key, version, v)
		if !errors.Is(err, storage.ErrConflict) {
			return err
		}
	}
	return storage.ErrConflict
}

// dayStore is the part of a DocStore that pruneDays needs.
type dayStore interface {
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, id string) error
}

// pruneDays deletes the per-puzzle documents (pinned answers, boards and
// chat days) older than yesterday's puzzle. Nothing reads them once the
// 07:00 recap of the day after has run, and without this they would pile up
// forever, in RAM with the memory store. Stats and group streaks are kept.
// Failures are logged; the next day's run retries them.
func (s *service) pruneDays(ctx context.Context, today int) {
	stores := []struct {
		prefix string
		store  dayStore
	}{{"puzzle:", s.puzzles}, {"play:", s.plays}, {"cday:", s.chatDays}}
	deleted := 0
	for _, d := range stores {
		keys, err := d.store.List(ctx, d.prefix)
		if err != nil {
			log.Warn("wordledaily prune list failed", "prefix", d.prefix, "err", err)
			continue
		}
		for _, key := range keys {
			num, ok := keyNum(key, d.prefix)
			if !ok || num >= today-1 {
				continue
			}
			if err := d.store.Delete(ctx, key); err != nil {
				log.Warn("wordledaily prune delete failed", "key", key, "err", err)
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		log.Info("wordledaily pruned old puzzle days", "deleted", deleted, "today", today)
	}
}

// keyNum reads the puzzle number that follows prefix in a per-puzzle key.
func keyNum(key, prefix string) (int, bool) {
	rest := strings.TrimPrefix(key, prefix)
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil
}
