package guessgame

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	dayLength = 24 * time.Hour
	// maxCachedPuzzles bounds the in-memory answer cache: today and
	// yesterday are all a request or the push ever needs.
	maxCachedPuzzles = 4
)

// Epoch is the start of puzzle #1: 07:00 ICT on 9 October 2026, which is
// 00:00 UTC, so every puzzle day runs 07:00 to 07:00 ICT and the cron at
// 00:00 UTC pushes exactly the puzzle a player then gets. Every daily game
// numbers its puzzles from it.
var Epoch = time.Date(2026, time.October, 9, 0, 0, 0, 0, time.UTC)

// ICT is Vietnam time (UTC+7, no DST), used only to print a puzzle's date.
var ICT = time.FixedZone("ICT", 7*60*60)

// PuzzleNum is the number of the puzzle playing at now: 1 on the epoch day,
// then one more each day. Times before the epoch play #1.
func (d *Daily) PuzzleNum(now time.Time) int {
	since := now.Sub(d.epoch)
	if since < 0 {
		return 1
	}
	return int(since/dayLength) + 1
}

// PuzzleStart is when puzzle num starts; the next one starts a day later.
func (d *Daily) PuzzleStart(num int) time.Time {
	return d.epoch.Add(time.Duration(num-1) * dayLength)
}

// PuzzleDate is the ICT calendar date puzzle num belongs to.
func (d *Daily) PuzzleDate(num int) string {
	return d.PuzzleStart(num).In(ICT).Format(time.DateOnly)
}

// AnswerFor computes puzzle num's answer from the answer key: each cycle of
// len(answers) days walks one keyed permutation of the list, so no answer
// repeats within a cycle and the order cannot be guessed without the key.
// Persisted puzzles take precedence; see ResolvePuzzle.
func (d *Daily) AnswerFor(num int) string {
	n := len(d.cfg.Answers)
	idx := num - 1
	cycle, pos := idx/n, idx%n
	return d.cfg.Answers[d.perms.get(d.cfg.AnswerKey, cycle, n)[pos]]
}

// permCache keeps the permutation of the current cycle.
type permCache struct {
	mu    sync.Mutex
	cycle int
	perm  []int
}

func (c *permCache) get(key []byte, cycle, n int) []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.perm != nil && c.cycle == cycle && len(c.perm) == n {
		return c.perm
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("cycle:" + strconv.Itoa(cycle)))
	var seed [32]byte
	copy(seed[:], mac.Sum(nil))
	rng := rand.New(rand.NewChaCha8(seed))
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	rng.Shuffle(n, func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	c.cycle, c.perm = cycle, perm
	return perm
}

// puzzleCache remembers resolved answers so a guess does not read the store
// for the puzzle every time.
type puzzleCache struct {
	mu      sync.Mutex
	answers map[int]string
}

func (c *puzzleCache) get(num int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.answers[num]
	return a, ok
}

func (c *puzzleCache) put(num int, answer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answers == nil {
		c.answers = map[int]string{}
	}
	c.answers[num] = answer
	for len(c.answers) > maxCachedPuzzles {
		oldest := num
		for k := range c.answers {
			oldest = min(oldest, k)
		}
		delete(c.answers, oldest)
	}
}

// ResolvePuzzle returns puzzle num's answer. The first resolution persists
// it, and every later one reads it back, so changing the secret or the
// answer list never changes a puzzle that has already started.
func (d *Daily) ResolvePuzzle(ctx context.Context, num int) (string, error) {
	if a, ok := d.cache.get(num); ok {
		return a, nil
	}
	key := PuzzleKey(num)
	for range writeRetries {
		doc, _, err := d.Puzzles.Get(ctx, key)
		if err == nil {
			d.cache.put(num, doc.Answer)
			return doc.Answer, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return "", err
		}
		doc = PuzzleDoc{Num: num, Answer: d.AnswerFor(num), CreatedAt: d.cfg.Now().UnixMilli()}
		err = d.Puzzles.PutVersioned(ctx, key, 0, doc)
		if err == nil {
			d.cache.put(num, doc.Answer)
			return doc.Answer, nil
		}
		if !errors.Is(err, storage.ErrConflict) {
			return "", err
		}
	}
	return "", storage.ErrConflict
}
