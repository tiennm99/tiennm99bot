package loldle

import (
	"errors"
	"strings"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
)

// Rejections of a guess, mapped to API errors in http_api.go and to chat
// replies in command.go.
var (
	errUnknownChampion = errors.New("loldle: champion not found")
	errAmbiguous       = errors.New("loldle: ambiguous champion")
	errDuplicate       = errors.New("loldle: champion already guessed")
	// errTargetGone is a round whose answer left champions.json after a
	// data refresh; it can no longer be scored.
	errTargetGone = errors.New("loldle: answer no longer in the data")
)

// Marks are one byte per classic attribute, in classicAttributes order:
// c (correct), p (partial), w (wrong), and for a wrong release year u (the
// answer is later) or d (earlier). A year that cannot be compared is w.
const (
	markCorrect = 'c'
	markPartial = 'p'
	markWrong   = 'w'
	markUp      = 'u'
	markDown    = 'd'
)

// rules is LoLdle for the guessgame engine. A guess's Word is the
// champion's canonical name.
type rules struct {
	champions []Champion
	// keepGone scores a guess against a pinned answer that left
	// champions.json instead of refusing it with errTargetGone. The daily
	// mode sets it: a pinned day cannot be re-picked, so its board must stay
	// playable. Unlimited rounds leave it unset and replace the round.
	keepGone bool
}

func (rules) Label() string   { return "LoLdle Daily" }
func (rules) MaxGuesses() int { return MaxGuesses }

// Judge resolves input to one champion, refuses a repeat and scores it
// against answer.
func (r rules) Judge(input, answer string, prior []guessgame.Guess) (guessgame.Guess, error) {
	target := findChampionByExactName(r.champions, answer)
	if target == nil && r.keepGone {
		return r.judgeGone(input, answer, prior)
	}
	guess, ambiguous := findChampionMatch(r.champions, input)
	switch {
	case ambiguous:
		return guessgame.Guess{}, errAmbiguous
	case guess == nil:
		return guessgame.Guess{}, errUnknownChampion
	}
	for _, p := range prior {
		if p.Word == guess.ChampionName {
			return guessgame.Guess{}, errDuplicate
		}
	}
	if target == nil {
		return guessgame.Guess{}, errTargetGone
	}
	return guessgame.Guess{Word: guess.ChampionName, Marks: marksOf(CompareChampions(guess, target))}, nil
}

// judgeGone scores a guess against an answer that is no longer in the data.
// Its attributes are unknown, so every other champion scores wrong in every
// column; naming the answer itself still wins.
func (r rules) judgeGone(input, answer string, prior []guessgame.Guess) (guessgame.Guess, error) {
	word := answer
	if normalizeName(input) != normalizeName(answer) {
		guess, ambiguous := findChampionMatch(r.champions, input)
		switch {
		case ambiguous:
			return guessgame.Guess{}, errAmbiguous
		case guess == nil:
			return guessgame.Guess{}, errUnknownChampion
		}
		word = guess.ChampionName
	}
	for _, p := range prior {
		if p.Word == word {
			return guessgame.Guess{}, errDuplicate
		}
	}
	if word == answer {
		return guessgame.Guess{Word: word, Marks: strings.Repeat(string(rune(markCorrect)), len(classicAttributes))}, nil
	}
	return guessgame.Guess{Word: word, Marks: strings.Repeat(string(rune(markWrong)), len(classicAttributes))}, nil
}

func (rules) Marker(mark byte) string {
	switch mark {
	case markCorrect:
		return "🟩"
	case markPartial:
		return "🟨"
	}
	return "🟥"
}

func (rules) AnswerText(answer string) string { return answer }

// marksOf encodes a comparison as marks.
func marksOf(rows []AttributeRow) string {
	var b strings.Builder
	for _, r := range rows {
		switch {
		case r.Result == ResultCorrect:
			b.WriteByte(markCorrect)
		case r.Result == ResultPartial:
			b.WriteByte(markPartial)
		case r.Direction == "up":
			b.WriteByte(markUp)
		case r.Direction == "down":
			b.WriteByte(markDown)
		default:
			b.WriteByte(markWrong)
		}
	}
	return b.String()
}

// markResult splits one mark into the comparison result and year arrow.
func markResult(mark byte) (result, direction string) {
	switch mark {
	case markCorrect:
		return ResultCorrect, ""
	case markPartial:
		return ResultPartial, ""
	case markUp:
		return ResultWrong, "up"
	case markDown:
		return ResultWrong, "down"
	}
	return ResultWrong, ""
}
