package wordledaily

import (
	"errors"
	"strings"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
)

// Rejections of a guess, mapped to API errors in http_api.go and to chat
// replies in command.go.
var (
	errWordEmpty   = errors.New("wordledaily: no word")
	errWordLength  = errors.New("wordledaily: word must be 5 letters")
	errWordUnknown = errors.New("wordledaily: not in the word list")
)

// rules is Wordle for the guessgame engine. Marks are one byte per letter:
// c (correct), p (present elsewhere) or w (wrong).
type rules struct {
	dict map[string]struct{}
}

func (rules) Label() string   { return "Wordle Daily" }
func (rules) MaxGuesses() int { return maxGuesses }

// Judge accepts a dictionary word and scores it against answer.
func (r rules) Judge(input, answer string, _ []guessgame.Guess) (guessgame.Guess, error) {
	v := wordlist.Validate(r.dict, input)
	switch v.Reason {
	case wordlist.ReasonEmpty:
		return guessgame.Guess{}, errWordEmpty
	case wordlist.ReasonLength:
		return guessgame.Guess{}, errWordLength
	case wordlist.ReasonUnknown:
		return guessgame.Guess{}, errWordUnknown
	}
	return guessgame.Guess{Word: v.Word, Marks: marksOf(wordlist.Compare(v.Word, answer))}, nil
}

func (rules) Marker(mark byte) string {
	switch mark {
	case 'c':
		return wordlist.Marker(wordlist.ResultCorrect)
	case 'p':
		return wordlist.Marker(wordlist.ResultPartial)
	}
	return wordlist.Marker(wordlist.ResultWrong)
}

func (rules) AnswerText(answer string) string { return strings.ToUpper(answer) }

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
func emojiRow(marks string) string { return guessgame.EmojiRow(rules{}, marks) }
