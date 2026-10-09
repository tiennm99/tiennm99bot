package noitu

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// topCommand shows a group's leaderboard across its /noitu room games.
	topCommand = "noitutop"

	topKeyPrefix = "top:"
	// topShown is how many players the leaderboard lists; the caller's own
	// line is added below when they rank lower.
	topShown = 10
	// topWriteRetries bounds the versioned read-modify-write of one entry.
	// Two cards of one chat can end games with the same player at once.
	topWriteRetries = 5

	msgTopNeedsGroup = "Bảng xếp hạng nối từ chỉ có trong nhóm. Gửi /noitutop trong nhóm nhé."
	msgTopEmpty      = "Nhóm này chưa có ván nối từ nào. Gửi /noitu để chơi cùng nhau."
	msgTopFail       = "Không đọc được bảng xếp hạng nối từ. Thử lại sau nhé."
)

// topEntry is one player's record across every room game of one chat.
type topEntry struct {
	ChatID int64  `bson:"chatId"`
	UserID int64  `bson:"userId"`
	Name   string `bson:"name"`  // the player's latest first name
	Games  int64  `bson:"games"` // games played, as a seated player
	Wins   int64  `bson:"wins"`
	Best   int64  `bson:"best"`  // best single-game score, winner bonus included
	Total  int64  `bson:"total"` // sum of every game's score
}

// topChatPrefix scopes a scan to one chat. The trailing colon keeps chat -1
// from matching chat -100.
func topChatPrefix(chatID int64) string {
	return topKeyPrefix + strconv.FormatInt(chatID, 10) + ":"
}

func topKey(chatID, userID int64) string {
	return topChatPrefix(chatID) + strconv.FormatInt(userID, 10)
}

// recordTop adds a finished room game to the chat's leaderboard. It runs in
// the background publisher, never under a room's lock; a failure is logged
// and skips only that player.
func (s *service) recordTop(chatID int64, standings []roomStanding) {
	if s.cfg.top == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	for _, st := range standings {
		if err := s.addTopGame(ctx, chatID, st); err != nil {
			log.Warn("noitu leaderboard update failed", "err", err)
		}
	}
}

// addTopGame folds one player's game into their entry with a versioned write,
// retrying when another game updated the entry in between.
func (s *service) addTopGame(ctx context.Context, chatID int64, st roomStanding) error {
	key := topKey(chatID, st.userID)
	for range topWriteRetries {
		entry, version, err := s.cfg.top.Get(ctx, key)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			entry, version = topEntry{ChatID: chatID, UserID: st.userID}, 0
		case err != nil:
			return err
		}
		entry.Name = st.Name
		entry.Games++
		if st.won {
			entry.Wins++
		}
		score := int64(st.Score)
		entry.Best = max(entry.Best, score)
		entry.Total += score
		err = s.cfg.top.PutVersioned(ctx, key, version, entry)
		if !errors.Is(err, storage.ErrConflict) {
			return err
		}
	}
	return storage.ErrConflict
}

// sortTop orders the leaderboard: more wins first, then the higher best
// game, then fewer games (the same record in fewer games ranks higher), then
// more total points, then the user ID so the order is stable.
func sortTop(entries []topEntry) {
	slices.SortFunc(entries, func(a, b topEntry) int {
		return cmp.Or(
			cmp.Compare(b.Wins, a.Wins),
			cmp.Compare(b.Best, a.Best),
			cmp.Compare(a.Games, b.Games),
			cmp.Compare(b.Total, a.Total),
			cmp.Compare(a.UserID, b.UserID),
		)
	})
}

// handleTopCommand shows the chat's leaderboard. Rooms exist only in groups,
// so other chats are told where it works.
func (s *service) handleTopCommand(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg.Chat.Type != models.ChatTypeGroup && msg.Chat.Type != models.ChatTypeSupergroup {
		return chathelper.Reply(ctx, b, msg, msgTopNeedsGroup)
	}
	if s.cfg.top == nil {
		_ = chathelper.Reply(ctx, b, msg, msgTopFail)
		return errors.New("noitu: no storage for the leaderboard")
	}
	docs, err := s.cfg.top.Scan(ctx, topChatPrefix(msg.Chat.ID))
	if err != nil {
		_ = chathelper.Reply(ctx, b, msg, msgTopFail)
		return err
	}
	if len(docs) == 0 {
		return chathelper.Reply(ctx, b, msg, msgTopEmpty)
	}
	entries := make([]topEntry, len(docs))
	for i, d := range docs {
		entries[i] = d.Val
	}
	var caller int64
	if msg.From != nil {
		caller = msg.From.ID
	}
	return chathelper.ReplyHTML(ctx, b, msg, renderTop(entries, caller))
}

// renderTop renders the top entries as an HTML table, plus the caller's own
// line when they rank below it. MonospaceTable escapes every cell.
func renderTop(entries []topEntry, caller int64) string {
	sortTop(entries)
	row := func(rank int, e topEntry) []string {
		return []string{
			strconv.Itoa(rank), e.Name,
			strconv.FormatInt(e.Wins, 10), strconv.FormatInt(e.Games, 10),
			strconv.FormatInt(e.Best, 10), strconv.FormatInt(e.Total, 10),
		}
	}
	var rows [][]string
	for i, e := range entries {
		switch {
		case i < topShown:
			rows = append(rows, row(i+1, e))
		case caller != 0 && e.UserID == caller:
			rows = append(rows, []string{"…"}, row(i+1, e))
		}
	}
	table := chathelper.MonospaceTable([]string{"#", "Tên", "Thắng", "Ván", "Cao nhất", "Tổng"}, rows)
	return fmt.Sprintf("<b>Bảng xếp hạng nối từ</b> (%d người chơi)\n%s", len(entries), table)
}
