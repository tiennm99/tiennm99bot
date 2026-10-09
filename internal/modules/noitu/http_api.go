package noitu

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/dict"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/engine"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/opponent"
)

const (
	maxBodyBytes  = 4 << 10
	maxWordRunes  = 64
	maxWordTokens = 8
)

// apiError is the JSON error body every API failure returns.
type apiError struct {
	status  int
	Code    string `json:"error"`
	Message string `json:"message"`
}

var (
	errBadRequest  = apiError{http.StatusBadRequest, "bad_request", "Yêu cầu không hợp lệ."}
	errWordTooLong = apiError{http.StatusBadRequest, "bad_request", "Từ quá dài."}
	errBadTokenAPI = apiError{http.StatusUnauthorized, "bad_token", "Liên kết trò chơi không hợp lệ. Hãy mở lại trò chơi từ Telegram."}
	errTokenExpAPI = apiError{http.StatusUnauthorized, "token_expired", "Liên kết trò chơi đã hết hạn. Hãy bấm Chơi trong Telegram để mở lại."}
	errNoSession   = apiError{http.StatusNotFound, "no_session", "Ván chơi không còn nữa. Hãy bắt đầu ván mới."}
	errGameOver    = apiError{http.StatusConflict, "game_over", "Ván chơi đã kết thúc."}
	errTooFast     = apiError{http.StatusTooManyRequests, "too_fast", "Bạn thao tác nhanh quá, thử lại sau giây lát."}
	errBusyAPI     = apiError{http.StatusServiceUnavailable, "busy", "Máy chủ đang bận, thử lại sau."}
	errInternalAPI = apiError{http.StatusInternalServerError, "internal", "Có lỗi xảy ra, thử lại sau."}
	rejectMessages = map[engine.Reason]string{
		engine.ReasonTooFewSyllables: "Từ phải có ít nhất 2 tiếng.",
		engine.ReasonNotInDictionary: "Từ này không có trong từ điển.",
		engine.ReasonWrongLink:       "Từ phải bắt đầu bằng tiếng “%s”.",
		engine.ReasonAlreadyUsed:     "Từ này đã được dùng rồi.",
		engine.ReasonTimeout:         "Hết giờ!",
	}
)

// sessionView is the game state the page renders. See docs/noitu-game.md.
type sessionView struct {
	Session       string       `json:"session"`
	Player        string       `json:"player"`
	Difficulty    string       `json:"difficulty"`
	Status        string       `json:"status"` // playing | won | lost
	EndReason     string       `json:"end_reason"`
	Current       string       `json:"current"`
	Chain         []chainEntry `json:"chain"`
	Score         int          `json:"score"`
	TurnLimitMS   int64        `json:"turn_limit_ms"`
	DeadlineMS    int64        `json:"deadline_ms"`
	ServerNowMS   int64        `json:"server_now_ms"`
	Suggestions   []string     `json:"suggestions"`
	ScoreReported string       `json:"score_reported"`
}

type moveResult struct {
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	PlayerWord string `json:"player_word"`
	BotWord    string `json:"bot_word"`
}

type moveResponse struct {
	Result moveResult  `json:"result"`
	State  sessionView `json:"state"`
}

// handler serves the page, its assets and the JSON API under routePrefix.
func (s *service) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+routePrefix+"{$}", serveAsset("index.html"))
	mux.HandleFunc("GET "+routePrefix+"app.js", serveAsset("app.js"))
	mux.HandleFunc("GET "+routePrefix+"app.css", serveAsset("app.css"))
	mux.HandleFunc("POST "+routePrefix+"api/start", s.apiStart)
	mux.HandleFunc("POST "+routePrefix+"api/move", s.apiMove)
	mux.HandleFunc("POST "+routePrefix+"api/state", s.apiState)
	mux.HandleFunc("POST "+routePrefix+"api/give-up", s.apiGiveUp)
	return securityHeaders(mux)
}

// securityHeaders applies to every response under the game prefix. The token
// travels in the page URL, so no referrer may leak it. Telegram Web embeds
// games in an iframe, so framing is deliberately not restricted.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://telegram.org; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'")
		if strings.HasPrefix(r.URL.Path, routePrefix+"api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e apiError) { writeJSON(w, e.status, e) }

// decode reads one JSON object of at most maxBodyBytes with no unknown fields
// and nothing after it.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, errBadRequest)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, errBadRequest)
		return false
	}
	return true
}

type startRequest struct {
	Token      string `json:"token"`
	Difficulty string `json:"difficulty"`
}

func (s *service) apiStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	c, err := verifyToken(s.cfg.key, req.Token, now)
	switch {
	case errors.Is(err, errTokenExpired):
		writeError(w, errTokenExpAPI)
		return
	case err != nil:
		writeError(w, errBadTokenAPI)
		return
	}
	difficulty, ok := opponent.ParseDifficulty(req.Difficulty)
	if !ok {
		writeError(w, errBadRequest)
		return
	}
	if !s.sessions.allowStart(c.UserID, now) {
		writeError(w, errTooFast)
		return
	}
	s.sweep(now)

	sess, err := s.newSession(c, difficulty, now)
	if err != nil {
		log.Error("noitu start failed", "err", err)
		writeError(w, errInternalAPI)
		return
	}
	displaced, err := s.sessions.add(sess)
	if err != nil {
		writeError(w, errBusyAPI)
		return
	}
	if displaced != nil {
		s.settle(displaced, now)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	writeJSON(w, http.StatusOK, s.view(sess, now))
}

func (s *service) newSession(c claims, d opponent.Difficulty, now time.Time) (*session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	rng := s.cfg.newRNG()
	strategy, err := opponent.New(d, rng)
	if err != nil {
		return nil, err
	}
	eng, err := engine.New(s.words, s.words.RandomOpening(rng, minOpeningOut), turnLimit, turnGrace, now)
	if err != nil {
		return nil, err
	}
	sess := &session{id: id, claims: c, difficulty: d, eng: eng, strategy: strategy, lastSeen: now}
	sess.chain = append(sess.chain, s.entry(eng.Opening(), "bot", 0))
	return sess, nil
}

func (s *service) entry(word, by string, points int) chainEntry {
	m := s.words.Meanings(word)
	if m == nil {
		m = []dict.Sense{}
	}
	return chainEntry{Word: word, By: by, Points: points, Meanings: m}
}

type sessionRequest struct {
	Session string `json:"session"`
}

type moveRequest struct {
	Session string `json:"session"`
	Word    string `json:"word"`
}

// lockedSession finds and locks the session named in a request, settling an
// expired turn first and answering no_session when the session is gone.
// timedOut reports that this call ended the game on the clock. On ok the
// caller must unlock.
func (s *service) lockedSession(w http.ResponseWriter, id string, now time.Time) (sess *session, timedOut, ok bool) {
	sess = s.sessions.get(id)
	if sess == nil {
		writeError(w, errNoSession)
		return nil, false, false
	}
	sess.mu.Lock()
	if sess.eng.Timeout(now) {
		s.finish(sess, now)
		timedOut = true
	}
	if sess.expired(now) {
		sess.mu.Unlock()
		s.sessions.remove(sess)
		writeError(w, errNoSession)
		return nil, false, false
	}
	return sess, timedOut, true
}

func (s *service) apiState(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	sess, _, ok := s.lockedSession(w, req.Session, now)
	if !ok {
		return
	}
	defer sess.mu.Unlock()
	if !sess.eng.Over() {
		sess.lastSeen = now
	}
	writeJSON(w, http.StatusOK, s.view(sess, now))
}

func (s *service) apiGiveUp(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	sess, _, ok := s.lockedSession(w, req.Session, now)
	if !ok {
		return
	}
	defer sess.mu.Unlock()
	if s.resign(sess) {
		s.finish(sess, now)
	}
	writeJSON(w, http.StatusOK, s.view(sess, now))
}

func (s *service) apiMove(w http.ResponseWriter, r *http.Request) {
	var req moveRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	sess, timedOut, ok := s.lockedSession(w, req.Session, now)
	if !ok {
		return
	}
	defer sess.mu.Unlock()
	if timedOut {
		// The move arrived after the deadline plus grace: it ended the game.
		writeJSON(w, http.StatusOK, moveResponse{Result: s.rejected(sess, engine.ReasonTimeout), State: s.view(sess, now)})
		return
	}
	if sess.eng.Over() {
		writeError(w, errGameOver)
		return
	}
	if now.Sub(sess.lastMove) < moveInterval {
		writeError(w, errTooFast)
		return
	}
	sess.lastMove, sess.lastSeen = now, now
	if wordTooLong(req.Word) {
		writeError(w, errWordTooLong)
		return
	}

	result, err := s.play(sess, req.Word, now)
	if err != nil {
		log.Error("noitu bot move failed", "err", err)
		writeError(w, errInternalAPI)
		return
	}
	writeJSON(w, http.StatusOK, moveResponse{Result: result, State: s.view(sess, now)})
}

func wordTooLong(word string) bool {
	return utf8.RuneCountInString(word) > maxWordRunes || len(strings.Fields(word)) > maxWordTokens
}

// play submits the human's word and, when it is accepted, the bot's reply.
// Caller holds sess.mu.
func (s *service) play(sess *session, word string, now time.Time) (moveResult, error) {
	mv, reason := sess.eng.Submit(engine.Human, word, now)
	if reason != engine.ReasonNone {
		if sess.eng.Over() {
			s.finish(sess, now)
		}
		return s.rejected(sess, reason), nil
	}
	sess.chain = append(sess.chain, s.entry(mv.Word, "player", mv.Points))
	result := moveResult{Accepted: true, PlayerWord: mv.Word}

	switch {
	case len(sess.chain) >= maxChain:
		sess.eng.EndMaxMoves()
	case sess.eng.BotStuck():
		// The bot has no reply: BotStuck already ended the game as a win.
	default:
		botWord, err := s.botMove(sess, now)
		if err != nil {
			return moveResult{}, err
		}
		result.BotWord = botWord
		if len(sess.chain) >= maxChain {
			sess.eng.EndMaxMoves()
		}
	}
	if sess.eng.Over() {
		s.finish(sess, now)
	}
	return result, nil
}

// botMove plays the strategy's choice. Every strategy picks from the legal
// moves, so a rejection here is a bug; fall back to the first legal move
// rather than leaving the game stuck on the bot's turn.
func (s *service) botMove(sess *session, now time.Time) (string, error) {
	word, err := sess.strategy.Choose(sess.eng)
	if err != nil {
		word = ""
	}
	mv, reason := sess.eng.Submit(engine.Bot, word, now)
	if reason != engine.ReasonNone {
		legal := sess.eng.LegalMoves()
		if len(legal) == 0 {
			return "", errors.New("noitu: bot has no move but BotStuck did not end the game")
		}
		if mv, reason = sess.eng.Submit(engine.Bot, legal[0], now); reason != engine.ReasonNone {
			return "", errors.New("noitu: engine rejected a legal bot move: " + string(reason))
		}
	}
	sess.chain = append(sess.chain, s.entry(mv.Word, "bot", mv.Points))
	return mv.Word, nil
}

func (s *service) rejected(sess *session, reason engine.Reason) moveResult {
	msg := rejectMessages[reason]
	if reason == engine.ReasonWrongLink {
		msg = strings.Replace(msg, "%s", sess.eng.Current(), 1)
	}
	return moveResult{Reason: string(reason), Message: msg}
}

// view renders the session for the page. Caller holds sess.mu.
func (s *service) view(sess *session, now time.Time) sessionView {
	v := sessionView{
		Session:       sess.id,
		Player:        sess.claims.Name,
		Difficulty:    string(sess.difficulty),
		Status:        "playing",
		EndReason:     string(sess.eng.EndReason()),
		Current:       sess.eng.Current(),
		Chain:         sess.chain,
		Score:         sess.eng.Score(),
		TurnLimitMS:   turnLimit.Milliseconds(),
		ServerNowMS:   now.UnixMilli(),
		Suggestions:   []string{},
		ScoreReported: sess.report,
	}
	if sess.eng.Over() {
		v.Status = "lost"
		if sess.eng.Winner() == engine.Human {
			v.Status = "won"
		}
		if sess.suggestions != nil {
			v.Suggestions = sess.suggestions
		}
		return v
	}
	v.DeadlineMS = sess.eng.Deadline().UnixMilli()
	return v
}
