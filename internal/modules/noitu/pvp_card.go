package noitu

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// pvpCommand sends a game card that the chat's members play together.
	pvpCommand = "noitupvp"

	cardKeyPrefix = "pvp:"
	// cardTTL is how long a card stays a room after its last Play press;
	// after that the daily cleanup forgets it and it plays solo.
	cardTTL = 30 * 24 * time.Hour
	// cardTouchEvery throttles the Play-time refresh of a card's age to one
	// write a day.
	cardTouchEvery = 24 * time.Hour
	// cardCleanupSchedule runs at 03:30 ICT.
	cardCleanupSchedule = "30 20 * * *"

	msgPvPNeedsGroup = "Chơi nối từ cùng nhau cần một nhóm. Hãy dùng /noitupvp trong nhóm, hoặc /noitu để chơi với bot."
	msgPvPFail       = "Không tạo được phòng nối từ. Thử lại sau nhé."
	msgCardLookup    = "Không mở được trò chơi lúc này. Thử lại sau nhé."
)

// pvpCard records a game message /noitupvp sent. A Play press on it opens the
// card's room; any other game message, a forwarded copy of a card included,
// plays against the bot.
type pvpCard struct {
	ChatID    int64 `bson:"chatId"`
	MessageID int   `bson:"messageId"`
	ThreadID  int   `bson:"threadId"`  // forum topic; 0 outside forums
	TouchedAt int64 `bson:"touchedAt"` // unix seconds of creation or a recent Play
}

func cardKey(chatID int64, messageID int) string {
	return cardKeyPrefix + strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID)
}

// handlePvPCommand sends the game and registers it as a room card. Like
// /noitu it works even when the game is disabled; Play explains why. Rooms
// are for a group's members, so private chats and channels are refused.
func (s *service) handlePvPCommand(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg.Chat.Type != models.ChatTypeGroup && msg.Chat.Type != models.ChatTypeSupergroup {
		return chathelper.Reply(ctx, b, msg, msgPvPNeedsGroup)
	}
	if s.cfg.cards == nil {
		_ = chathelper.Reply(ctx, b, msg, msgPvPFail)
		return errors.New("noitu: no storage for pvp cards")
	}
	sent, err := b.SendGame(ctx, &bot.SendGameParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: msg.MessageThreadID,
		GameShorName:    ShortName, // the library's field name is misspelled
	})
	if err != nil {
		_ = chathelper.Reply(ctx, b, msg, msgSendGameFail)
		return err
	}
	card := pvpCard{ChatID: msg.Chat.ID, MessageID: sent.ID, ThreadID: msg.MessageThreadID, TouchedAt: s.cfg.now().Unix()}
	if err := s.cfg.cards.Put(ctx, cardKey(card.ChatID, card.MessageID), card); err != nil {
		// An unregistered card would silently play solo; take it back.
		if _, derr := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: card.ChatID, MessageID: card.MessageID}); derr != nil {
			log.Warn("noitu: delete unregistered pvp card failed", "err", derr)
		}
		_ = chathelper.Reply(ctx, b, msg, msgPvPFail)
		return err
	}
	return nil
}

// lookupCard reports whether the chat message is a registered card, and
// refreshes its age at most once a day so a card in use never expires.
func (s *service) lookupCard(ctx context.Context, chatID int64, messageID int) (pvpCard, bool, error) {
	if s.cfg.cards == nil {
		return pvpCard{}, false, nil
	}
	key := cardKey(chatID, messageID)
	card, _, err := s.cfg.cards.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return pvpCard{}, false, nil
	}
	if err != nil {
		return pvpCard{}, false, err
	}
	now := s.cfg.now().Unix()
	if now-card.TouchedAt >= int64(cardTouchEvery/time.Second) {
		card.TouchedAt = now
		if err := s.cfg.cards.Put(ctx, key, card); err != nil {
			log.Warn("noitu: refresh pvp card failed", "err", err)
		}
	}
	return card, true, nil
}

// cleanupCards forgets cards nobody played for cardTTL.
func (s *service) cleanupCards(ctx context.Context, _ modules.Deps) error {
	if s.cfg.cards == nil {
		return nil
	}
	docs, err := s.cfg.cards.Scan(ctx, cardKeyPrefix)
	if err != nil {
		return err
	}
	cutoff := s.cfg.now().Add(-cardTTL).Unix()
	var errs []error
	for _, d := range docs {
		if d.Val.TouchedAt < cutoff {
			if err := s.cfg.cards.Delete(ctx, d.ID); err != nil && !errors.Is(err, storage.ErrNotFound) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
