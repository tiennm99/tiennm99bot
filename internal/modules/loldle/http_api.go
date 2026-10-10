package loldle

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/loldle/web"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

// requestsPerMinute caps one player's API calls, every endpoint together.
const requestsPerMinute = 60

// maxNameBytes caps a guessed champion name in a request.
const maxNameBytes = 64

// apiError is the JSON error body every API failure returns.
type apiError struct {
	status  int
	Code    string `json:"error"`
	Message string `json:"message"`
}

var (
	errBadRequestAPI    = apiError{http.StatusBadRequest, "bad_request", "Bad request."}
	errBadTokenAPI      = apiError{http.StatusUnauthorized, "bad_token", "This game link is not valid. Open the game again from the chat."}
	errExpiredAPI       = apiError{http.StatusUnauthorized, "expired", "This game link has expired. Open the game again from the chat."}
	errBadModeAPI       = apiError{http.StatusForbidden, "bad_mode", "This card plays the daily champion. Send /loldle for unlimited rounds."}
	errNewPuzzleAPI     = apiError{http.StatusConflict, "new_puzzle", "A new daily champion is up."}
	errNewRoundAPI      = apiError{http.StatusConflict, "new_round", "A new round has started."}
	errDataChangedAPI   = apiError{http.StatusConflict, "new_round", "Champion data changed, so a new round has started."}
	errUnknownAPI       = apiError{http.StatusUnprocessableEntity, "unknown", "Champion not found."}
	errAmbiguousAPI     = apiError{http.StatusUnprocessableEntity, "ambiguous", "Several champions match. Pick one from the list."}
	errDuplicateAPI     = apiError{http.StatusUnprocessableEntity, "duplicate", "You already guessed that champion."}
	errFinishedAPI      = apiError{http.StatusUnprocessableEntity, "finished", "You have already finished today's champion."}
	errRoundFinishedAPI = apiError{http.StatusUnprocessableEntity, "finished", "This round is over. Start a new game."}
	errTooFastAPI       = apiError{http.StatusTooManyRequests, "rate_limited", "Too many requests. Wait a moment and try again."}
	errInternalAPI      = apiError{http.StatusInternalServerError, "internal", "Something went wrong. Try again later."}
)

// Page modes, as the view reports them.
const (
	modeDaily     = "daily"
	modeUnlimited = "unlimited"
)

// cellView is one attribute of a guess: the guessed champion's own value
// and how it compares with the answer. It never carries the answer's value.
type cellView struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Result string `json:"result"`        // correct | partial | wrong
	Dir    string `json:"dir,omitempty"` // up | down, on a wrong release year
}

// rowView is one guess on the board.
type rowView struct {
	Name  string     `json:"name"`
	ID    string     `json:"id"`
	Marks string     `json:"marks"`
	Cells []cellView `json:"cells"`
}

// champView is the answer, shown once the game is over.
type champView struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// columnView names one attribute column.
type columnView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// statsView is the player's record in the card's mode, shown once the game
// is over.
type statsView struct {
	Played int   `json:"played"`
	WinPct int   `json:"win_pct"`
	Cur    int   `json:"cur"`
	Max    int   `json:"max"`
	Dist   []int `json:"dist"`
}

// view is the state the page renders. The answer, the stats and the share
// text are present only once the game is over. A daily view carries the
// puzzle's number, date and the next puzzle's start; an unlimited one the
// round's number.
type view struct {
	Mode    string       `json:"mode"`
	Num     int          `json:"num,omitempty"`
	Date    string       `json:"date,omitempty"`
	Seq     int          `json:"seq,omitempty"`
	Player  string       `json:"player"`
	Max     int          `json:"max"`
	Columns []columnView `json:"columns"`
	Guesses []rowView    `json:"guesses"`
	Status  string       `json:"status"`
	GaveUp  bool         `json:"gave_up,omitempty"`
	Answer  *champView   `json:"answer,omitempty"`
	Stats   *statsView   `json:"stats,omitempty"`
	NextAt  int64        `json:"next_at,omitempty"`
	Share   string       `json:"share,omitempty"`
	// Abandoned is the champion of the unlimited round a New game gave up
	// as a loss, so the page can show it before the next round.
	Abandoned *champView `json:"abandoned,omitempty"`
}

type stateRequest struct {
	Token string `json:"token"`
}

// guessRequest is a guess on the board the page shows: Num names a daily
// puzzle, Seq an unlimited round.
type guessRequest struct {
	Token string `json:"token"`
	Num   int    `json:"num"`
	Seq   int    `json:"seq"`
	Name  string `json:"name"`
}

// newRequest starts the next unlimited round after round Seq.
type newRequest struct {
	Token string `json:"token"`
	Seq   int    `json:"seq"`
}

// handler serves the page, its assets, the champion list and the JSON API
// under routePrefix. Images may come from Data Dragon, which serves the
// champion icons, and from nowhere else.
func (s *service) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+routePrefix+"{$}", htmlgame.ServeAsset(web.FS, "index.html"))
	mux.HandleFunc("GET "+routePrefix+"app.js", htmlgame.ServeAsset(web.FS, "app.js"))
	mux.HandleFunc("GET "+routePrefix+"app.css", htmlgame.ServeAsset(web.FS, "app.css"))
	mux.HandleFunc("GET "+routePrefix+"champions.json", s.serveChampions)
	mux.HandleFunc("POST "+routePrefix+"api/state", s.apiState)
	mux.HandleFunc("POST "+routePrefix+"api/guess", s.apiGuess)
	mux.HandleFunc("POST "+routePrefix+"api/new", s.apiNew)
	return htmlgame.SecurityHeaders(routePrefix, mux, ddragonOrigin)
}

// serveChampions is the search list: every champion's name and icon id,
// nothing that hints at an answer.
func (s *service) serveChampions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(s.championList)
}

func writeError(w http.ResponseWriter, e apiError) { htmlgame.WriteJSON(w, e.status, e) }

// authorize verifies the token and the player's request budget, writing
// the error itself when either fails.
func (s *service) authorize(w http.ResponseWriter, token string, now time.Time) (guessgame.Claims, bool) {
	c, err := s.verifyToken(token, now)
	switch {
	case errors.Is(err, htmlgame.ErrTokenExpired):
		writeError(w, errExpiredAPI)
		return guessgame.Claims{}, false
	case err != nil:
		writeError(w, errBadTokenAPI)
		return guessgame.Claims{}, false
	}
	if !s.limiter.Allow(c.UserID, now) {
		writeError(w, errTooFastAPI)
		return guessgame.Claims{}, false
	}
	return c, true
}

// cardMax is the length of an unlimited round started from the token's
// card: its chat's /loldle_setmax, or the default for an inline card.
func (s *service) cardMax(ctx context.Context, c guessgame.Claims) (int, error) {
	if c.InlineID != "" {
		return MaxGuesses, nil
	}
	return s.maxFor(ctx, strconv.FormatInt(c.ChatID, 10))
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
	ctx := r.Context()
	if c.Unlimited() {
		maxGuesses, err := s.cardMax(ctx, c)
		var st guessgame.RoundState
		if err == nil {
			st, err = s.rounds.Load(ctx, c.UserID, maxGuesses)
		}
		s.writeRound(w, c, st, err)
		return
	}
	o, err := s.LoadState(ctx, c)
	if err != nil {
		log.Error("loldle state failed", "err", err)
		writeError(w, errInternalAPI)
		return
	}
	htmlgame.WriteJSON(w, http.StatusOK, s.view(c, o))
}

func (s *service) apiGuess(w http.ResponseWriter, r *http.Request) {
	var req guessRequest
	if htmlgame.DecodeJSON(w, r, &req) != nil || len(req.Name) > maxNameBytes {
		writeError(w, errBadRequestAPI)
		return
	}
	c, ok := s.authorize(w, req.Token, s.cfg.now())
	if !ok {
		return
	}
	ctx := r.Context()
	if c.Unlimited() {
		maxGuesses, err := s.cardMax(ctx, c)
		var st guessgame.RoundState
		if err == nil {
			st, err = s.rounds.Guess(ctx, c.UserID, req.Seq, req.Name, maxGuesses)
		}
		if errors.Is(err, errTargetGone) {
			// The round's answer left the data: replace it, uncounted.
			if _, err = s.rounds.Replace(ctx, c.UserID, maxGuesses); err == nil {
				writeError(w, errDataChangedAPI)
				return
			}
		}
		s.writeRound(w, c, st, err)
		return
	}
	o, err := s.SubmitGuess(ctx, c, req.Num, req.Name)
	switch {
	case errors.Is(err, guessgame.ErrNewPuzzle):
		writeError(w, errNewPuzzleAPI)
	case errors.Is(err, guessgame.ErrFinished):
		writeError(w, errFinishedAPI)
	case guessError(w, err):
	case err != nil:
		log.Error("loldle guess failed", "err", err)
		writeError(w, errInternalAPI)
	default:
		htmlgame.WriteJSON(w, http.StatusOK, s.view(c, o))
	}
}

// apiNew starts the player's next unlimited round, giving up the current
// one. A stale seq answers with the current round, so a double click
// starts one round.
func (s *service) apiNew(w http.ResponseWriter, r *http.Request) {
	var req newRequest
	if htmlgame.DecodeJSON(w, r, &req) != nil || req.Seq < 1 {
		writeError(w, errBadRequestAPI)
		return
	}
	c, ok := s.authorize(w, req.Token, s.cfg.now())
	if !ok {
		return
	}
	if !c.Unlimited() {
		writeError(w, errBadModeAPI)
		return
	}
	maxGuesses, err := s.cardMax(r.Context(), c)
	var st guessgame.RoundState
	if err == nil {
		st, err = s.rounds.New(r.Context(), c.UserID, req.Seq, maxGuesses)
	}
	s.writeRound(w, c, st, err)
}

// guessError writes the API error of a refused guess and reports whether
// err was one.
func guessError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errUnknownChampion):
		writeError(w, errUnknownAPI)
	case errors.Is(err, errAmbiguous):
		writeError(w, errAmbiguousAPI)
	case errors.Is(err, errDuplicate):
		writeError(w, errDuplicateAPI)
	default:
		return false
	}
	return true
}

// writeRound answers an unlimited request.
func (s *service) writeRound(w http.ResponseWriter, c guessgame.Claims, st guessgame.RoundState, err error) {
	switch {
	case errors.Is(err, guessgame.ErrNewRound):
		writeError(w, errNewRoundAPI)
	case errors.Is(err, guessgame.ErrFinished):
		writeError(w, errRoundFinishedAPI)
	case guessError(w, err):
	case err != nil:
		log.Error("loldle unlimited request failed", "err", err)
		writeError(w, errInternalAPI)
	default:
		htmlgame.WriteJSON(w, http.StatusOK, s.roundView(c, st))
	}
}

// view renders a daily game for the page. The answer leaves the server
// only once the game is over.
func (s *service) view(c guessgame.Claims, o guessgame.Outcome) view {
	v := view{
		Mode:    modeDaily,
		Num:     o.Num,
		Date:    s.PuzzleDate(o.Num),
		Player:  c.Name,
		Max:     MaxGuesses,
		Columns: columns(),
		Guesses: s.rowViews(o.P.Guesses),
		Status:  o.P.Status,
		NextAt:  s.PuzzleStart(o.Num + 1).Unix(),
	}
	if v.Status == "" {
		v.Status = guessgame.StatusPlaying
	}
	if !o.P.Finished() {
		return v
	}
	v.Answer = s.champView(o.Answer)
	st := o.Stats
	v.Stats = statsOf(st.Played, st.Wins, guessgame.DisplayStreak(st, o.Num), st.MaxStreak, st.Dist)
	v.Share = s.ShareText(o.P)
	return v
}

// roundView renders an unlimited round for the page. The answer leaves the
// server only once the round is over.
func (s *service) roundView(c guessgame.Claims, st guessgame.RoundState) view {
	rd := st.Round
	v := view{
		Mode:    modeUnlimited,
		Seq:     rd.Seq,
		Player:  c.Name,
		Max:     rd.MaxGuesses,
		Columns: columns(),
		Guesses: s.rowViews(rd.Guesses),
		Status:  rd.Status,
		GaveUp:  rd.GaveUp,
	}
	if st.Abandoned != nil {
		v.Abandoned = s.champView(st.Abandoned.Target)
	}
	if !rd.Finished() {
		return v
	}
	v.Answer = s.champView(rd.Target)
	v.Stats = statsOf(st.Stats.Played, st.Stats.Wins, st.Stats.CurStreak, st.Stats.MaxStreak, st.Stats.Dist)
	return v
}

func columns() []columnView {
	out := make([]columnView, len(classicAttributes))
	for i, a := range classicAttributes {
		out[i] = columnView{Key: a.Key, Label: a.Label}
	}
	return out
}

// rowViews renders the guesses newest last, as stored; the page orders
// them.
func (s *service) rowViews(gs []guessgame.Guess) []rowView {
	out := make([]rowView, len(gs))
	for i, g := range gs {
		row := rowView{Name: g.Word, Marks: g.Marks, Cells: make([]cellView, 0, len(classicAttributes))}
		if c := findChampionByExactName(s.cfg.champions, g.Word); c != nil {
			row.ID = c.ID
		}
		for _, r := range s.rowsOf(g) {
			row.Cells = append(row.Cells, cellView{Key: r.Key, Value: r.GuessValue, Result: r.Result, Dir: r.Direction})
		}
		out[i] = row
	}
	return out
}

func (s *service) champView(name string) *champView {
	v := &champView{Name: name}
	if c := findChampionByExactName(s.cfg.champions, name); c != nil {
		v.ID, v.Title = c.ID, c.Title
	}
	return v
}

func statsOf(played, wins, cur, maxStreak int, dist []int) *statsView {
	pct := 0
	if played > 0 {
		pct = (wins*100 + played/2) / played
	}
	return &statsView{Played: played, WinPct: pct, Cur: cur, Max: maxStreak, Dist: dist}
}
