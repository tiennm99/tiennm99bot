// Package guessgame is the shared engine of the bot's HTML5 guessing games,
// Wordle and LoLdle. Each game supplies its Rules; this package runs the two
// modes a game card can open:
//
//   - daily (Daily): one shared answer a day from 07:00 ICT, server-side
//     progress, NYT-style stats, a live per-group results message, the
//     07:00 recap and push, and setGameScore on a win;
//   - unlimited (Rounds): each player's own random answer, a new round on
//     demand, and separate stats.
//
// Cards records the game messages that open unlimited mode; any other card
// plays daily. Claims is the signed token a Play press hands the page.
package guessgame

import (
	"errors"
	"strings"
)

// Game statuses, shared by daily progress and unlimited rounds.
const (
	StatusPlaying = "playing"
	StatusWon     = "won"
	StatusLost    = "lost"
)

// Rejections of a request, mapped to API errors by each game. Rules.Judge
// adds its own game-specific ones.
var (
	ErrNewPuzzle = errors.New("guessgame: the puzzle changed")
	ErrFinished  = errors.New("guessgame: the game is finished")
	ErrNewRound  = errors.New("guessgame: the round changed")
	ErrNoRound   = errors.New("guessgame: no round")
)

// Guess is one submitted guess and its marks, one byte per position or
// attribute. The stored and served form; JSON and BSON names match.
type Guess struct {
	Word  string `json:"word" bson:"word"`
	Marks string `json:"marks" bson:"marks"`
}

// Rules is what a game plugs into the engine.
type Rules interface {
	// Label names the daily game in chat messages, e.g. "Wordle Daily".
	Label() string
	// MaxGuesses is the daily game's guess budget; a daily win scores
	// MaxGuesses+1-guesses.
	MaxGuesses() int
	// Judge scores input against answer, given the guesses already on the
	// board. It returns the canonical guess, which equals answer on a win,
	// or a game-specific error that refuses the input without recording it.
	Judge(input, answer string, prior []Guess) (Guess, error)
	// Marker is the spoiler-free square shown for one mark.
	Marker(mark byte) string
	// AnswerText is the answer as chat messages show it.
	AnswerText(answer string) string
}

// EmojiRow renders a guess's marks as its spoiler-free row of squares.
func EmojiRow(r Rules, marks string) string {
	var b strings.Builder
	for i := 0; i < len(marks); i++ {
		b.WriteString(r.Marker(marks[i]))
	}
	return b.String()
}
