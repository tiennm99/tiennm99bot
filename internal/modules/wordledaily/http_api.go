package wordledaily

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordle/wordlist"
	"github.com/tiennm99/tiennm99bot/internal/modules/wordledaily/web"
)

// requestsPerMinute caps one player's API calls, both endpoints together.
const requestsPerMinute = 60

// apiError is the JSON error body every API failure returns.
type apiError struct {
	status  int
	Code    string `json:"error"`
	Message string `json:"message"`
}

var (
	errBadRequestAPI = apiError{http.StatusBadRequest, "bad_request", "Bad request."}
	errBadTokenAPI   = apiError{http.StatusUnauthorized, "bad_token", "This game link is not valid. Open the game again from the chat."}
	errExpiredAPI    = apiError{http.StatusUnauthorized, "expired", "This game link has expired. Open the game again from the chat."}
	errNewPuzzleAPI  = apiError{http.StatusConflict, "new_puzzle", "A new puzzle has started."}
	errLengthAPI     = apiError{http.StatusUnprocessableEntity, "length", "Word must be exactly 5 letters."}
	errUnknownAPI    = apiError{http.StatusUnprocessableEntity, "unknown", "Not in the word list."}
	errFinishedAPI   = apiError{http.StatusUnprocessableEntity, "finished", "You have already finished today's puzzle."}
	errTooFastAPI    = apiError{http.StatusTooManyRequests, "rate_limited", "Too many requests. Wait a moment and try again."}
	errInternalAPI   = apiError{http.StatusInternalServerError, "internal", "Something went wrong. Try again later."}
)

// guessView is one row of the board: the word and its c/p/w marks.
type guessView struct {
	Word  string `json:"word"`
	Marks string `json:"marks"`
}

// statsView is the player's record, shown once today's game is over.
type statsView struct {
	Played int   `json:"played"`
	WinPct int   `json:"win_pct"`
	Cur    int   `json:"cur"`
	Max    int   `json:"max"`
	Dist   []int `json:"dist"`
}

// view is the state the page renders. The answer, the stats and the share
// text are present only once the game is over.
type view struct {
	Num     int         `json:"num"`
	Date    string      `json:"date"`
	Player  string      `json:"player"`
	Max     int         `json:"max"`
	Len     int         `json:"len"`
	Guesses []guessView `json:"guesses"`
	Status  string      `json:"status"`
	Answer  string      `json:"answer,omitempty"`
	Stats   *statsView  `json:"stats,omitempty"`
	NextAt  int64       `json:"next_at"`
	Share   string      `json:"share,omitempty"`
}

type stateRequest struct {
	Token string `json:"token"`
}

type guessRequest struct {
	Token string `json:"token"`
	Num   int    `json:"num"`
	Word  string `json:"word"`
}

// handler serves the page, its assets and the JSON API under routePrefix.
func (s *service) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+routePrefix+"{$}", htmlgame.ServeAsset(web.FS, "index.html"))
	mux.HandleFunc("GET "+routePrefix+"app.js", htmlgame.ServeAsset(web.FS, "app.js"))
	mux.HandleFunc("GET "+routePrefix+"app.css", htmlgame.ServeAsset(web.FS, "app.css"))
	mux.HandleFunc("POST "+routePrefix+"api/state", s.apiState)
	mux.HandleFunc("POST "+routePrefix+"api/guess", s.apiGuess)
	return htmlgame.SecurityHeaders(routePrefix, mux)
}

func writeError(w http.ResponseWriter, e apiError) { htmlgame.WriteJSON(w, e.status, e) }

// authorize verifies the token and the player's request budget, writing
// the error itself when either fails.
func (s *service) authorize(w http.ResponseWriter, token string, now time.Time) (claims, bool) {
	c, err := s.verifyToken(token, now)
	switch {
	case errors.Is(err, htmlgame.ErrTokenExpired):
		writeError(w, errExpiredAPI)
		return claims{}, false
	case err != nil:
		writeError(w, errBadTokenAPI)
		return claims{}, false
	}
	if !s.limiter.Allow(c.UserID, now) {
		writeError(w, errTooFastAPI)
		return claims{}, false
	}
	return c, true
}

func (s *service) apiState(w http.ResponseWriter, r *http.Request) {
	var req stateRequest
	if htmlgame.DecodeJSON(w, r, &req) != nil {
		writeError(w, errBadRequestAPI)
		return
	}
	c, ok := s.authorize(w, req.Token, s.cfg.now())
	if !ok {
		return
	}
	o, err := s.loadState(r.Context(), c)
	if err != nil {
		log.Error("wordledaily state failed", "err", err)
		writeError(w, errInternalAPI)
		return
	}
	htmlgame.WriteJSON(w, http.StatusOK, s.view(c, o))
}

func (s *service) apiGuess(w http.ResponseWriter, r *http.Request) {
	var req guessRequest
	if htmlgame.DecodeJSON(w, r, &req) != nil || len(req.Word) > 32 {
		writeError(w, errBadRequestAPI)
		return
	}
	c, ok := s.authorize(w, req.Token, s.cfg.now())
	if !ok {
		return
	}
	o, err := s.submitGuess(r.Context(), c, req.Num, req.Word)
	switch {
	case errors.Is(err, errNewPuzzle):
		writeError(w, errNewPuzzleAPI)
	case errors.Is(err, errWordLength):
		writeError(w, errLengthAPI)
	case errors.Is(err, errWordUnknown):
		writeError(w, errUnknownAPI)
	case errors.Is(err, errFinished):
		writeError(w, errFinishedAPI)
	case err != nil:
		log.Error("wordledaily guess failed", "err", err)
		writeError(w, errInternalAPI)
	default:
		htmlgame.WriteJSON(w, http.StatusOK, s.view(c, o))
	}
}

// view renders a game for the page. The answer leaves the server only once
// the game is over.
func (s *service) view(c claims, o outcome) view {
	v := view{
		Num:     o.num,
		Date:    s.puzzleDate(o.num),
		Player:  c.Name,
		Max:     maxGuesses,
		Len:     wordlist.WordLength,
		Guesses: make([]guessView, len(o.p.Guesses)),
		Status:  o.p.Status,
		NextAt:  s.puzzleStart(o.num + 1).Unix(),
	}
	if v.Status == "" {
		v.Status = statusPlaying
	}
	for i, g := range o.p.Guesses {
		v.Guesses[i] = guessView{Word: strings.ToUpper(g.Word), Marks: g.Marks}
	}
	if !o.p.finished() {
		return v
	}
	v.Answer = strings.ToUpper(o.answer)
	st := o.stats
	pct := 0
	if st.Played > 0 {
		pct = (st.Wins*100 + st.Played/2) / st.Played
	}
	v.Stats = &statsView{Played: st.Played, WinPct: pct, Cur: displayStreak(st, o.num), Max: st.MaxStreak, Dist: st.Dist}
	v.Share = shareText(o.p)
	return v
}

// shareText is the NYT-style spoiler-free result.
func shareText(p progress) string {
	var b strings.Builder
	b.WriteString("Wordle Daily #" + strconv.Itoa(p.Num) + " " + resultLine(p) + "\n")
	for _, g := range p.Guesses {
		b.WriteString("\n" + emojiRow(g.Marks))
	}
	return b.String()
}
