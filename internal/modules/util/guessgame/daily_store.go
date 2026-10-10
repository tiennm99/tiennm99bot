package guessgame

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Every document lives in the game's one collection under its own key
// prefix. JSON and BSON names match because the memory store round-trips
// through JSON and MongoDB through BSON.

const (
	// maxReported bounds the cards a player's win is reported on per day.
	maxReported = 32
	// maxChats bounds the chats one player's daily result appears in.
	maxChats = 20
	// maxChatPlayers bounds one chat's daily player list.
	maxChatPlayers = 200
	// writeRetries bounds a versioned read-modify-write.
	writeRetries = 5
)

// PuzzleDoc pins one day's answer once it has been resolved.
type PuzzleDoc struct {
	Num       int    `json:"num" bson:"num"`
	Answer    string `json:"answer" bson:"answer"`
	CreatedAt int64  `json:"created_at" bson:"created_at"`
}

// ChatRef is a group chat (and forum topic) a player's result appears in,
// with the card they last played from there, which the live summary replies
// to.
type ChatRef struct {
	ChatID   int64 `json:"chat_id" bson:"chat_id"`
	ThreadID int   `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
	CardID   int   `json:"card_id,omitempty" bson:"card_id,omitempty"`
}

// Progress is one player's game on one puzzle.
type Progress struct {
	Num        int       `json:"num" bson:"num"`
	UserID     int64     `json:"user_id" bson:"user_id"`
	Name       string    `json:"name" bson:"name"`
	Guesses    []Guess   `json:"guesses" bson:"guesses"`
	Status     string    `json:"status" bson:"status"`
	FinishedAt int64     `json:"finished_at,omitempty" bson:"finished_at,omitempty"` // unix ms
	Reported   []string  `json:"reported,omitempty" bson:"reported,omitempty"`       // card keys the win was reported on
	Chats      []ChatRef `json:"chats,omitempty" bson:"chats,omitempty"`
}

// Finished reports whether the game is won or lost.
func (p *Progress) Finished() bool { return p.Status == StatusWon || p.Status == StatusLost }

// joinedChat reports whether the player's result already appears in the
// chat's topic.
func (p *Progress) joinedChat(chatID int64, threadID int) bool {
	return slices.ContainsFunc(p.Chats, func(c ChatRef) bool { return c.ChatID == chatID && c.ThreadID == threadID })
}

// UserStats is a player's record across every puzzle.
type UserStats struct {
	Played     int   `json:"played" bson:"played"`
	Wins       int   `json:"wins" bson:"wins"`
	CurStreak  int   `json:"cur_streak" bson:"cur_streak"`
	MaxStreak  int   `json:"max_streak" bson:"max_streak"`
	Dist       []int `json:"dist" bson:"dist"` // wins by guess count, index 0 = 1 guess
	LastNum    int   `json:"last_num" bson:"last_num"`
	LastWinNum int   `json:"last_win_num" bson:"last_win_num"`
}

// ChatPlayer is one player listed in a chat's day.
type ChatPlayer struct {
	UserID int64  `json:"user_id" bson:"user_id"`
	Name   string `json:"name" bson:"name"`
}

// ChatDay is who played one puzzle in one chat topic, plus the live
// summary message the bot keeps there.
type ChatDay struct {
	Num       int          `json:"num" bson:"num"`
	ChatID    int64        `json:"chat_id" bson:"chat_id"`
	ThreadID  int          `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
	Players   []ChatPlayer `json:"players" bson:"players"`
	CardID    int          `json:"card_id,omitempty" bson:"card_id,omitempty"` // the card the summary replies to
	MsgID     int          `json:"msg_id,omitempty" bson:"msg_id,omitempty"`
	MsgSentAt int64        `json:"msg_sent_at,omitempty" bson:"msg_sent_at,omitempty"` // unix ms
}

// ChatStreak counts the consecutive puzzles on which at least one player in
// a chat topic found the answer; a loss does not count. LastNum is the
// latest such puzzle.
type ChatStreak struct {
	Streak  int `json:"streak" bson:"streak"`
	LastNum int `json:"last_num" bson:"last_num"`
}

// liveAt is the streak as of puzzle num: a streak whose last puzzle is
// older than yesterday has been broken.
func (c ChatStreak) liveAt(num int) int {
	if c.LastNum >= num-1 {
		return c.Streak
	}
	return 0
}

// PuzzleKey is the key of puzzle num's pinned answer.
func PuzzleKey(num int) string { return "puzzle:" + strconv.Itoa(num) }

// PlayKey is the key of a player's game on puzzle num.
func PlayKey(num int, userID int64) string {
	return "play:" + strconv.Itoa(num) + ":" + strconv.FormatInt(userID, 10)
}

// StatsKey is the key of a player's daily stats.
func (d *Daily) StatsKey(userID int64) string {
	return d.cfg.StatsPrefix + strconv.FormatInt(userID, 10)
}

// chatTopic is "<chat>:<thread>:"; the trailing colon keeps chat -1 from
// matching chat -100 in a prefix.
func chatTopic(chatID int64, threadID int) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(threadID) + ":"
}

// ChatDayKey is the key of who played puzzle num in a chat topic.
func ChatDayKey(num int, chatID int64, threadID int) string {
	return "cday:" + strconv.Itoa(num) + ":" + chatTopic(chatID, threadID)
}

// ChatStreakKey is the key of a chat topic's win streak.
func ChatStreakKey(chatID int64, threadID int) string {
	return "cstreak:" + chatTopic(chatID, threadID)
}

// LoadProgress returns the player's game on puzzle num, or a fresh one.
func (d *Daily) LoadProgress(ctx context.Context, num int, userID int64) (Progress, error) {
	p, _, err := d.Plays.Get(ctx, PlayKey(num, userID))
	switch {
	case err == nil:
		return p, nil
	case errors.Is(err, storage.ErrNotFound):
		return Progress{Num: num, UserID: userID, Guesses: []Guess{}, Status: StatusPlaying}, nil
	default:
		return Progress{}, err
	}
}

// LoadStats returns the player's daily stats, zero when they never played.
func (d *Daily) LoadStats(ctx context.Context, userID int64) (UserStats, error) {
	n := d.MaxGuesses()
	st, _, err := d.Stats.Get(ctx, d.StatsKey(userID))
	if errors.Is(err, storage.ErrNotFound) {
		return UserStats{Dist: make([]int, n)}, nil
	}
	if len(st.Dist) != n {
		st.Dist = append(st.Dist, make([]int, n)...)[:n]
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

// dayStore is the part of a DocStore that PruneDays needs.
type dayStore interface {
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, id string) error
}

// PruneDays deletes the per-puzzle documents (pinned answers, boards and
// chat days) older than yesterday's puzzle. Nothing reads them once the
// 07:00 recap of the day after has run, and without this they would pile up
// forever, in RAM with the memory store. Stats and group streaks are kept.
// Failures are logged; the next day's run retries them.
func (d *Daily) PruneDays(ctx context.Context, today int) {
	stores := []struct {
		prefix string
		store  dayStore
	}{{"puzzle:", d.Puzzles}, {"play:", d.Plays}, {"cday:", d.ChatDays}}
	deleted := 0
	for _, ds := range stores {
		keys, err := ds.store.List(ctx, ds.prefix)
		if err != nil {
			log.Warn(d.cfg.LogName+" prune list failed", "prefix", ds.prefix, "err", err)
			continue
		}
		for _, key := range keys {
			num, ok := keyNum(key, ds.prefix)
			if !ok || num >= today-1 {
				continue
			}
			if err := ds.store.Delete(ctx, key); err != nil {
				log.Warn(d.cfg.LogName+" prune delete failed", "key", key, "err", err)
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		log.Info(d.cfg.LogName+" pruned old puzzle days", "deleted", deleted, "today", today)
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
