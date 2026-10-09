package noitu

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/engine"
)

const (
	maxRooms       = 500
	maxChatRooms   = 3  // live rooms per chat, so one group cannot fill maxRooms
	maxRoomMembers = 32 // MaxSeats players plus watchers
	// memberStaleTTL drops a lobby member whose page stopped polling, so a
	// closed page does not hold a seat, and a lobby nobody polls is dropped
	// with its last member. A running game never drops anyone: an absent
	// player loses their turns on the clock instead.
	memberStaleTTL = time.Minute
	// roomStartInterval is the shortest gap between two games in one room.
	// Starts also count against the host's startsPerMinute.
	roomStartInterval = 3 * time.Second
	// winnerBonus is added to the winner's points.
	winnerBonus = 50
	noSeat      = -1
)

// Room statuses shown on the page.
const (
	roomLobby   = "lobby"
	roomPlaying = "playing"
)

// roomMember is one person who joined a room through the card's Play button.
// secret is their bearer credential; it is only ever sent to them.
type roomMember struct {
	secret   string
	userID   int64
	name     string
	lastSeen time.Time
	lastMove time.Time
}

// roomEntry is one word of a room's chain. Seat indexes the game's players;
// the opening word is the bot's, with seat -1.
type roomEntry struct {
	chainEntry
	Seat int    `json:"seat"`
	Name string `json:"name,omitempty"`
}

// roomStanding is one player's line in a finished game's table.
type roomStanding struct {
	userID    int64
	Name      string `json:"name"`
	Rank      int    `json:"rank"`
	Score     int    `json:"score"` // word points plus the winner bonus
	Bonus     int    `json:"bonus"`
	Words     int    `json:"words"`
	OutReason string `json:"out_reason"`
	You       bool   `json:"you"`
	won       bool
}

// roomReport is one score to report on the card.
type roomReport struct {
	claims claims
	score  int
}

// roomResult is the last finished game, shown in the lobby until the next
// game starts.
type roomResult struct {
	Winner        string         `json:"winner"`
	Words         int            `json:"words"` // words played, opening excluded
	EndReason     string         `json:"end_reason"`
	Standings     []roomStanding `json:"standings"`
	ScoreReported string         `json:"score_reported"`
}

// room is the live game of one /noitu card. Its mutex guards every field
// below it; the room store's mutex is never taken while holding it.
type room struct {
	id   string
	card claims // ChatID, MessageID and ThreadID of the card
	// closed marks a room the sweep dropped. It is atomic so the store can
	// replace a closed room without taking the room's mutex.
	closed atomic.Bool

	mu        sync.Mutex
	version   int64
	members   []*roomMember // join order; members[0] hosts the lobby
	game      int           // games started, so the page can tell chains apart
	match     *engine.Match // nil before the first game; kept after one ends
	seats     []*roomMember // the match's players, by seat
	chain     []roomEntry
	result    *roomResult
	lastStart time.Time
}

// playing reports whether a game is in progress. Caller holds r.mu.
func (r *room) playing() bool { return r.match != nil && !r.match.Over() }

func (r *room) changed() { r.version++ }

// member finds the member holding secret. Caller holds r.mu.
func (r *room) member(secret string) *roomMember {
	for _, m := range r.members {
		if m.secret == secret {
			return m
		}
	}
	return nil
}

// seatOf is m's seat in the current or last game, noSeat when m watches.
// Caller holds r.mu.
func (r *room) seatOf(m *roomMember) int {
	for i, s := range r.seats {
		if s == m {
			return i
		}
	}
	return noSeat
}

// expired reports whether the room can be dropped: no game running and
// nobody left, every member having stopped polling. Caller holds r.mu.
func (r *room) expired() bool {
	return !r.playing() && len(r.members) == 0
}

// roomStore indexes live rooms by card and by id.
type roomStore struct {
	mu     sync.Mutex
	byCard map[string]*room
	byID   map[string]*room
}

func newRoomStore() *roomStore {
	return &roomStore{byCard: map[string]*room{}, byID: map[string]*room{}}
}

func roomCardKey(c claims) string { return cardKey(c.ChatID, c.MessageID) }

// open returns the card's room, creating it when there is none, or only a
// closed one the sweep has not removed yet, and the room caps allow.
func (st *roomStore) open(card claims) (*room, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := roomCardKey(card)
	if r := st.byCard[key]; r != nil {
		if !r.closed.Load() {
			return r, nil
		}
		st.removeLocked(r)
	}
	if len(st.byID) >= maxRooms {
		return nil, errBusy
	}
	inChat := 0
	for _, r := range st.byID {
		if r.card.ChatID == card.ChatID && !r.closed.Load() {
			inChat++
		}
	}
	if inChat >= maxChatRooms {
		return nil, errChatRoomsAPI
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	r := &room{
		id:      id,
		card:    claims{ChatID: card.ChatID, MessageID: card.MessageID, ThreadID: card.ThreadID},
		version: 1,
	}
	st.byCard[key] = r
	st.byID[id] = r
	return r, nil
}

func (st *roomStore) get(id string) *room {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.byID[id]
}

func (st *roomStore) remove(r *room) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.removeLocked(r)
}

func (st *roomStore) removeLocked(r *room) {
	if st.byID[r.id] == r {
		delete(st.byID, r.id)
	}
	if key := roomCardKey(r.card); st.byCard[key] == r {
		delete(st.byCard, key)
	}
}

func (st *roomStore) all() []*room {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]*room, 0, len(st.byID))
	for _, r := range st.byID {
		out = append(out, r)
	}
	return out
}

// sweepRooms settles expired turns in rooms nobody is polling, so their
// games still end and report, and drops idle rooms.
func (s *service) sweepRooms(now time.Time) {
	for _, r := range s.rooms.all() {
		r.mu.Lock()
		s.settleRoom(r, now)
		drop := r.expired()
		if drop {
			r.closed.Store(true)
		}
		r.mu.Unlock()
		if drop {
			s.rooms.remove(r)
		}
	}
}

// settleRoom catches the room up with the clock: every turn that ran out
// eliminates its player, and lobby members who stopped polling leave.
// Caller holds r.mu.
func (s *service) settleRoom(r *room, now time.Time) {
	if r.playing() {
		timedOut := false
		for r.match.Timeout(now) {
			timedOut = true
		}
		if timedOut {
			r.changed()
			if r.match.Over() {
				s.endRoomGame(r, now)
			}
		}
	}
	if r.playing() {
		return
	}
	kept := r.members[:0]
	for _, m := range r.members {
		if now.Sub(m.lastSeen) <= memberStaleTTL {
			kept = append(kept, m)
		}
	}
	if len(kept) != len(r.members) {
		clear(r.members[len(kept):])
		r.members = kept
		r.changed()
	}
}

// join adds the user to the room, or returns their existing membership so a
// reloaded page or a second Play press keeps its place. Caller holds r.mu.
func (r *room) join(c claims, now time.Time) (*roomMember, error) {
	name := c.Name
	if name == "" {
		name = "Người chơi"
	}
	for _, m := range r.members {
		if m.userID == c.UserID {
			m.lastSeen = now
			if m.name != name {
				m.name = name
				r.changed()
			}
			return m, nil
		}
	}
	if len(r.members) >= maxRoomMembers {
		return nil, errRoomFullAPI
	}
	secret, err := newSessionID()
	if err != nil {
		return nil, err
	}
	m := &roomMember{secret: secret, userID: c.UserID, name: name, lastSeen: now}
	r.members = append(r.members, m)
	r.changed()
	return m, nil
}

// startGame seats the first MaxSeats members in join order and opens a
// match. Caller holds r.mu and has checked the lobby and the host.
func (s *service) startGame(r *room, now time.Time) error {
	seats := r.members[:min(len(r.members), engine.MaxSeats)]
	if len(seats) < engine.MinSeats {
		return errNotEnoughAPI
	}
	rng := s.cfg.newRNG()
	match, err := engine.NewMatch(s.words, len(seats), s.words.RandomOpening(rng, minOpeningOut), turnLimit, turnGrace, now)
	if err != nil {
		return err
	}
	r.match = match
	r.seats = append([]*roomMember(nil), seats...)
	r.chain = []roomEntry{{chainEntry: s.entry(match.Opening(), "bot", 0), Seat: noSeat}}
	r.result = nil
	r.game++
	r.lastStart = now
	for _, m := range r.seats {
		m.lastMove = time.Time{}
	}
	r.changed()
	return nil
}

// playRoomWord submits seat's word. Caller holds r.mu and has checked that a
// game runs.
func (s *service) playRoomWord(r *room, seat int, word string, now time.Time) moveResult {
	mv, reason := r.match.Submit(seat, word, now)
	if reason != engine.ReasonNone {
		if r.match.Over() || reason == engine.ReasonTimeout {
			r.changed()
		}
		if r.match.Over() {
			s.endRoomGame(r, now)
		}
		return rejectedFor(reason, r.match.Current())
	}
	r.chain = append(r.chain, roomEntry{chainEntry: s.entry(mv.Word, "player", mv.Points), Seat: seat, Name: r.seats[seat].name})
	if len(r.chain) >= maxChain {
		r.match.EndMaxMoves()
	}
	r.changed()
	if r.match.Over() {
		s.endRoomGame(r, now)
	}
	return moveResult{Accepted: true, PlayerWord: mv.Word}
}

// resignRoom takes seat out of the running game. Caller holds r.mu.
func (s *service) resignRoom(r *room, seat int, now time.Time) {
	if !r.match.Resign(seat, now) {
		return
	}
	r.changed()
	if r.match.Over() {
		s.endRoomGame(r, now)
	}
}

// endRoomGame records the finished game's table, returns the room to the
// lobby, then posts the result and reports every positive score in the
// background. Caller holds r.mu.
func (s *service) endRoomGame(r *room, now time.Time) {
	words := make([]int, len(r.seats))
	for _, e := range r.chain {
		if e.Seat >= 0 {
			words[e.Seat]++
		}
	}
	res := &roomResult{
		Winner:    r.seats[r.match.Winner()].name,
		Words:     len(r.chain) - 1,
		EndReason: string(r.match.EndReason()),
	}
	var reports []roomReport
	for rank, seat := range r.match.Standings() {
		m := r.seats[seat]
		st := roomStanding{
			userID:    m.userID,
			Name:      m.name,
			Rank:      rank + 1,
			Score:     r.match.Score(seat),
			Words:     words[seat],
			OutReason: string(r.match.OutReason(seat)),
		}
		// A game nobody played a word in (everyone left at once) earns
		// nothing: no bonus, no score report and no announcement.
		if seat == r.match.Winner() && res.Words > 0 {
			st.Bonus = winnerBonus
			st.Score += winnerBonus
			st.won = true
		}
		res.Standings = append(res.Standings, st)
		if st.Score > 0 {
			reports = append(reports, roomReport{claims: claims{UserID: m.userID, ChatID: r.card.ChatID, MessageID: r.card.MessageID}, score: st.Score})
		}
	}
	res.ScoreReported = reportPending
	if len(reports) == 0 || s.cfg.reporter == nil {
		res.ScoreReported = reportSkipped
	}
	r.result = res
	for _, m := range r.members {
		// Back in the lobby, the clock for leaving starts now, not at the
		// last poll before a long game.
		if m.lastSeen.Before(now) {
			m.lastSeen = now
		}
	}
	r.changed()
	if res.Words == 0 {
		return
	}
	text := fmt.Sprintf("Ván nối từ kết thúc: %s thắng sau %d từ!", res.Winner, res.Words)
	go s.publishRoomResult(r, res, text, reports, res.ScoreReported == reportPending) //nolint:gosec // G118: the report must outlive the request that ended the game
}

// publishRoomResult records the game on the group leaderboard, announces the
// result in the chat and reports the scores one by one. Publishing holds the
// card's lock, so two games on one card never edit its game message at the
// same time. No step is fatal to the room. res.Standings is never modified
// after endRoomGame, so it is read here without the room's lock.
func (s *service) publishRoomResult(r *room, res *roomResult, text string, reports []roomReport, report bool) {
	defer s.publishing.Acquire(roomCardKey(r.card))()
	s.recordTop(r.card.ChatID, res.Standings)
	if s.cfg.announcer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		if err := s.cfg.announcer.Announce(ctx, r.card, text); err != nil {
			log.Warn("noitu room result message failed", "err", err)
		}
		cancel()
	}
	if !report {
		return
	}
	status := reportOK
	for _, rep := range reports {
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		err := s.cfg.reporter.Report(ctx, rep.claims, rep.score)
		cancel()
		if err != nil && !scoreNotModified(err) {
			status = reportFailed
			log.Warn("noitu room setGameScore failed", "err", err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	res.ScoreReported = status
	if r.result == res {
		r.changed()
	}
}
