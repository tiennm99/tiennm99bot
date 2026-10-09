package engine

import (
	"math/bits"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
)

// Scoring, ported from tiennm99/noitu server/internal/game/engine.go. A word
// is worth more the longer the chain it extends, the longer the word, the
// faster it was played, and the fewer words the corpus offers for the
// syllable it answered.
const (
	basePoints           = 10
	chainBonus           = 2  // per word already played
	chainBonusWords      = 15 // the chain term stops growing here
	syllableBonus        = 5  // per syllable beyond the minimum
	speedBonus           = 10 // full for an instant answer, nothing on the buzzer
	rarityBonus          = 15 // full for a syllable with a single answer
	rarityHalvingPenalty = 3  // lost per doubling of the answers available
	// MaxPointsPerWord caps one word's score. Overflow is trimmed from rarity
	// first, then speed, then syllables.
	MaxPointsPerWord = 100
)

// pointsFor scores a word about to be played: syllables is its length, link
// the syllable it answers, now when it was played.
func (b *board) pointsFor(syllables int, link string, now time.Time) int {
	parts := []int{
		basePoints,
		chainBonus * min(b.ChainLength(), chainBonusWords),
		syllableBonus * (syllables - dict.MinSyllables),
		b.speedPoints(now),
		b.rarityPoints(link),
	}
	total := 0
	for _, p := range parts {
		total += p
	}
	return min(total, MaxPointsPerWord)
}

// speedPoints pays for the share of the turn left on the clock. A move in the
// grace period is past the deadline and earns nothing.
func (b *board) speedPoints(now time.Time) int {
	remaining := b.deadline.Sub(now)
	if remaining <= 0 {
		return 0
	}
	remaining = min(remaining, b.turnLimit)
	return int(int64(speedBonus) * int64(remaining) / int64(b.turnLimit))
}

// rarityPoints pays for how few corpus words answer link, spent ones
// included: the reward is a property of the dictionary, not of this game.
func (b *board) rarityPoints(link string) int {
	options := b.dict.OutDegree(link)
	if options < 1 {
		return 0
	}
	halvings := bits.Len(uint(options)) - 1
	return max(rarityBonus-rarityHalvingPenalty*halvings, 0)
}
