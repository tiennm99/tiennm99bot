package noitu

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/engine"
)

var (
	errNoRoomAPI      = apiError{http.StatusNotFound, "no_room", "Phòng chơi không còn nữa. Hãy bấm Chơi trên thẻ trò chơi để vào lại."}
	errNotPvPAPI      = apiError{http.StatusBadRequest, "bad_request", "Liên kết này không mở phòng chơi nhóm."}
	errPvPSoloAPI     = apiError{http.StatusBadRequest, "bad_request", "Liên kết này mở phòng chơi nhóm, không dùng để chơi với bot."}
	errRoomFullAPI    = apiError{http.StatusConflict, "room_full", "Phòng đã đủ người."}
	errNotHostAPI     = apiError{http.StatusForbidden, "not_host", "Chỉ chủ phòng mới bắt đầu được ván chơi."}
	errNotEnoughAPI   = apiError{http.StatusConflict, "not_enough_players", "Cần ít nhất 2 người để bắt đầu."}
	errGameRunningAPI = apiError{http.StatusConflict, "game_running", "Ván chơi đang diễn ra."}
	errNoGameAPI      = apiError{http.StatusConflict, "no_game", "Ván chơi chưa bắt đầu hoặc đã kết thúc."}
	errNotInGameAPI   = apiError{http.StatusConflict, "not_in_game", "Bạn không chơi trong ván này."}
	errChatRoomsAPI   = apiError{http.StatusConflict, "too_many_rooms", "Nhóm này đang có quá nhiều phòng chơi. Hãy chơi trên một thẻ đang mở."}

	msgWatching   = "Bạn đang xem ván này."
	msgEliminated = "Bạn đã bị loại khỏi ván này."
	msgNotYetTurn = "Chưa đến lượt bạn."
)

// roomPlayerView is one player as the page shows them: a lobby member, or a
// seat of the running game.
type roomPlayerView struct {
	Name      string `json:"name"`
	You       bool   `json:"you"`
	Host      bool   `json:"host"`
	Score     int    `json:"score"`
	Alive     bool   `json:"alive"`
	OutReason string `json:"out_reason"`
}

// roomView is the room state the page renders. See docs/noitu-game.md.
type roomView struct {
	Member      string           `json:"member"`
	Version     int64            `json:"version"`
	Status      string           `json:"status"` // lobby | playing
	Game        int              `json:"game"`
	Host        bool             `json:"host"`
	Seat        int              `json:"seat"` // your seat in the running game, -1 otherwise
	Turn        int              `json:"turn"` // seat to act, -1 in the lobby
	Players     []roomPlayerView `json:"players"`
	Watchers    int              `json:"watchers"`
	MinPlayers  int              `json:"min_players"`
	MaxPlayers  int              `json:"max_players"`
	Current     string           `json:"current"`
	Chain       []roomEntry      `json:"chain"`
	TurnLimitMS int64            `json:"turn_limit_ms"`
	DeadlineMS  int64            `json:"deadline_ms"`
	ServerNowMS int64            `json:"server_now_ms"`
	Result      *roomResult      `json:"result"`
}

// roomPing answers a poll whose version is current.
type roomPing struct {
	Version     int64 `json:"version"`
	Unchanged   bool  `json:"unchanged"`
	ServerNowMS int64 `json:"server_now_ms"`
}

type roomMoveResponse struct {
	Result moveResult `json:"result"`
	State  roomView   `json:"state"`
}

type joinRequest struct {
	Token string `json:"token"`
}

type memberRequest struct {
	Member string `json:"member"`
}

type roomStateRequest struct {
	Member  string `json:"member"`
	Version int64  `json:"version"`
}

type roomMoveRequest struct {
	Member string `json:"member"`
	Word   string `json:"word"`
}

// roomReply is an answer built under the room lock and written after it is
// released, so a slow client never holds up the room.
type roomReply struct {
	status int
	body   any
}

func okReply(v any) roomReply                   { return roomReply{http.StatusOK, v} }
func errReply(e apiError) roomReply             { return roomReply{e.status, e} }
func (r roomReply) write(w http.ResponseWriter) { writeJSON(w, r.status, r.body) }

func (s *service) apiRoomJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	c, ok := s.verifyRequestToken(w, req.Token, now)
	if !ok {
		return
	}
	if !c.PvP {
		writeError(w, errNotPvPAPI)
		return
	}
	if !s.sessions.allowStart(c.UserID, now) {
		writeError(w, errTooFast)
		return
	}
	s.sweepRooms(now)
	// A sweep can close the room between open and lock; open then replaces
	// the closed room, so the second try gets a live one.
	for range 2 {
		rm, err := s.rooms.open(c)
		if err != nil {
			var apiErr apiError
			if errors.As(err, &apiErr) {
				writeError(w, apiErr)
				return
			}
			writeError(w, errBusyAPI)
			return
		}
		reply, retry := s.joinRoom(rm, c, now)
		if !retry {
			reply.write(w)
			return
		}
	}
	writeError(w, errBusyAPI)
}

// joinRoom adds c's user to rm under its lock. retry reports that the sweep
// closed rm after open returned it, so the caller must open again.
func (s *service) joinRoom(rm *room, c claims, now time.Time) (reply roomReply, retry bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.closed.Load() {
		return roomReply{}, true
	}
	s.settleRoom(rm, now)
	m, err := rm.join(c, now)
	if err != nil {
		var apiErr apiError
		if errors.As(err, &apiErr) {
			return errReply(apiErr), false
		}
		log.Error("noitu room join failed", "err", err)
		return errReply(errInternalAPI), false
	}
	return okReply(s.roomView(rm, m, now)), false
}

// verifyRequestToken checks a page token, answering the error itself.
func (s *service) verifyRequestToken(w http.ResponseWriter, token string, now time.Time) (claims, bool) {
	c, err := verifyToken(s.cfg.key, token, now)
	switch {
	case errors.Is(err, errTokenExpired):
		writeError(w, errTokenExpAPI)
		return claims{}, false
	case err != nil:
		writeError(w, errBadTokenAPI)
		return claims{}, false
	}
	return c, true
}

// withRoom finds and locks the room and member a bearer names, catches the
// room up with the clock, runs fn under the lock and writes its answer after
// unlocking. expired tells fn that the member held a turn that had run out
// before the catch-up, which therefore eliminated them. An unknown member,
// a closed room, or a member the catch-up dropped answers no_room.
func (s *service) withRoom(w http.ResponseWriter, bearer string, now time.Time, fn func(rm *room, m *roomMember, expired bool) roomReply) {
	id, secret, found := strings.Cut(bearer, ".")
	rm := s.rooms.get(id)
	if !found || rm == nil {
		writeError(w, errNoRoomAPI)
		return
	}
	func() roomReply {
		rm.mu.Lock()
		defer rm.mu.Unlock()
		m := rm.member(secret)
		if m == nil || rm.closed.Load() {
			return errReply(errNoRoomAPI)
		}
		expired := false
		if rm.playing() {
			seat := rm.seatOf(m)
			expired = seat != noSeat && rm.match.Turn() == seat && rm.match.Expired(now)
		}
		s.settleRoom(rm, now)
		// The catch-up drops members who stopped polling; they rejoin.
		if rm.member(secret) != m {
			return errReply(errNoRoomAPI)
		}
		m.lastSeen = now
		return fn(rm, m, expired)
	}().write(w)
}

func (s *service) apiRoomState(w http.ResponseWriter, r *http.Request) {
	var req roomStateRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	s.withRoom(w, req.Member, now, func(rm *room, m *roomMember, _ bool) roomReply {
		if req.Version == rm.version {
			return okReply(roomPing{Version: rm.version, Unchanged: true, ServerNowMS: now.UnixMilli()})
		}
		return okReply(s.roomView(rm, m, now))
	})
}

func (s *service) apiRoomStart(w http.ResponseWriter, r *http.Request) {
	var req memberRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	s.withRoom(w, req.Member, now, func(rm *room, m *roomMember, _ bool) roomReply {
		switch {
		case rm.playing():
			return errReply(errGameRunningAPI)
		case rm.members[0] != m:
			return errReply(errNotHostAPI)
		case !rm.lastStart.IsZero() && now.Sub(rm.lastStart) < roomStartInterval:
			return errReply(errTooFast)
		case !s.sessions.allowStart(m.userID, now):
			return errReply(errTooFast)
		}
		if err := s.startGame(rm, now); err != nil {
			var apiErr apiError
			if errors.As(err, &apiErr) {
				return errReply(apiErr)
			}
			log.Error("noitu room start failed", "err", err)
			return errReply(errInternalAPI)
		}
		return okReply(s.roomView(rm, m, now))
	})
}

func (s *service) apiRoomMove(w http.ResponseWriter, r *http.Request) {
	var req roomMoveRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	s.withRoom(w, req.Member, now, func(rm *room, m *roomMember, expired bool) roomReply {
		if expired {
			// The word came after the deadline plus grace: the clock took
			// the player out before it could count.
			return okReply(roomMoveResponse{Result: rejectedFor(engine.ReasonTimeout, ""), State: s.roomView(rm, m, now)})
		}
		if !rm.playing() {
			return errReply(errNoGameAPI)
		}
		if now.Sub(m.lastMove) < moveInterval {
			return errReply(errTooFast)
		}
		m.lastMove = now
		if wordTooLong(req.Word) {
			return errReply(errWordTooLong)
		}
		seat := rm.seatOf(m)
		var result moveResult
		switch {
		case seat == noSeat:
			result = moveResult{Reason: string(engine.ReasonNotYourTurn), Message: msgWatching}
		case !rm.match.Alive(seat):
			result = moveResult{Reason: string(engine.ReasonNotYourTurn), Message: msgEliminated}
		case rm.match.Turn() != seat:
			result = moveResult{Reason: string(engine.ReasonNotYourTurn), Message: msgNotYetTurn}
		default:
			result = s.playRoomWord(rm, seat, req.Word, now)
		}
		return okReply(roomMoveResponse{Result: result, State: s.roomView(rm, m, now)})
	})
}

func (s *service) apiRoomGiveUp(w http.ResponseWriter, r *http.Request) {
	var req memberRequest
	if !decode(w, r, &req) {
		return
	}
	now := s.cfg.now()
	s.withRoom(w, req.Member, now, func(rm *room, m *roomMember, _ bool) roomReply {
		if !rm.playing() {
			return errReply(errNoGameAPI)
		}
		seat := rm.seatOf(m)
		if seat == noSeat || !rm.match.Alive(seat) {
			return errReply(errNotInGameAPI)
		}
		s.resignRoom(rm, seat, now)
		return okReply(s.roomView(rm, m, now))
	})
}

// roomView renders the room for member m. Caller holds rm.mu. The view
// shares no mutable state with the room, so it may be encoded after the
// lock is released: the chain is only ever appended to or replaced.
func (s *service) roomView(rm *room, m *roomMember, now time.Time) roomView {
	v := roomView{
		Member:      rm.id + "." + m.secret,
		Version:     rm.version,
		Status:      roomLobby,
		Game:        rm.game,
		Host:        len(rm.members) > 0 && rm.members[0] == m,
		Seat:        noSeat,
		Turn:        noSeat,
		MinPlayers:  engine.MinSeats,
		MaxPlayers:  engine.MaxSeats,
		Chain:       rm.chain,
		TurnLimitMS: turnLimit.Milliseconds(),
		ServerNowMS: now.UnixMilli(),
	}
	if v.Chain == nil {
		v.Chain = []roomEntry{}
	}
	if rm.playing() {
		v.Status = roomPlaying
		v.Seat = rm.seatOf(m)
		v.Turn = rm.match.Turn()
		v.Current = rm.match.Current()
		v.DeadlineMS = rm.match.Deadline().UnixMilli()
		for i, p := range rm.seats {
			v.Players = append(v.Players, roomPlayerView{
				Name:      p.name,
				You:       p == m,
				Score:     rm.match.Score(i),
				Alive:     rm.match.Alive(i),
				OutReason: string(rm.match.OutReason(i)),
			})
		}
		// Members are only dropped in the lobby, so every seat is a member.
		v.Watchers = len(rm.members) - len(rm.seats)
		return v
	}
	for i, p := range rm.members[:min(len(rm.members), engine.MaxSeats)] {
		v.Players = append(v.Players, roomPlayerView{Name: p.name, You: p == m, Host: i == 0, Alive: true})
	}
	v.Watchers = max(len(rm.members)-engine.MaxSeats, 0)
	if rm.result != nil {
		res := *rm.result
		res.Standings = make([]roomStanding, len(rm.result.Standings))
		for i, st := range rm.result.Standings {
			st.You = st.userID == m.userID
			res.Standings[i] = st
		}
		v.Result = &res
	}
	return v
}
