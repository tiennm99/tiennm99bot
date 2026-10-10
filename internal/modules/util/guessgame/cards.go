package guessgame

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	cardKeyPrefix = "ucard:"
	// CardTTL is how long a card keeps opening unlimited mode after its
	// last Play press; after that the cleanup forgets it and it plays daily.
	CardTTL = 30 * 24 * time.Hour
	// cardTouchEvery throttles the Play-time refresh of a card's age to
	// one write a day.
	cardTouchEvery = 24 * time.Hour
)

// CardRecord is a game message that opens unlimited mode. Every other card
// of the game, the daily ones, the 07:00 push, an inline share and a card
// whose record expired, plays the daily puzzle.
type CardRecord struct {
	ChatID    int64 `json:"chat_id" bson:"chat_id"`
	MessageID int   `json:"message_id" bson:"message_id"`
	ThreadID  int   `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
	TouchedAt int64 `json:"touched_at" bson:"touched_at"` // unix seconds of creation or a recent Play
}

// Cards stores a game's unlimited card records in the game's collection.
type Cards struct {
	store storage.DocStore[CardRecord]
	now   func() time.Time
	name  string
}

// NewCards returns the card records over coll, or nil without storage.
// name prefixes log lines.
func NewCards(coll storage.Collection, now func() time.Time, name string) *Cards {
	if coll == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &Cards{store: storage.Typed[CardRecord](coll), now: now, name: name}
}

func cardKey(chatID int64, messageID int) string {
	return cardKeyPrefix + strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID)
}

// Record marks the chat message as an unlimited card.
func (c *Cards) Record(ctx context.Context, chatID int64, messageID, threadID int) error {
	if c == nil {
		return errors.New("guessgame: no storage for cards")
	}
	rec := CardRecord{ChatID: chatID, MessageID: messageID, ThreadID: threadID, TouchedAt: c.now().Unix()}
	return c.store.Put(ctx, cardKey(chatID, messageID), rec)
}

// Lookup reports whether the chat message is an unlimited card, and
// refreshes its age at most once a day so a card in use never expires.
func (c *Cards) Lookup(ctx context.Context, chatID int64, messageID int) (CardRecord, bool, error) {
	if c == nil {
		return CardRecord{}, false, nil
	}
	key := cardKey(chatID, messageID)
	rec, _, err := c.store.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return CardRecord{}, false, nil
	}
	if err != nil {
		return CardRecord{}, false, err
	}
	now := c.now().Unix()
	if now-rec.TouchedAt >= int64(cardTouchEvery/time.Second) {
		rec.TouchedAt = now
		if err := c.store.Put(ctx, key, rec); err != nil {
			log.Warn(c.name+" card refresh failed", "err", err)
		}
	}
	return rec, true, nil
}

// Cleanup forgets cards nobody played for CardTTL. It is a cron handler.
func (c *Cards) Cleanup(ctx context.Context, _ modules.Deps) error {
	if c == nil {
		return nil
	}
	docs, err := c.store.Scan(ctx, cardKeyPrefix)
	if err != nil {
		return err
	}
	cutoff := c.now().Add(-CardTTL).Unix()
	var errs []error
	for _, d := range docs {
		if d.Val.TouchedAt < cutoff {
			if err := c.store.Delete(ctx, d.ID); err != nil && !errors.Is(err, storage.ErrNotFound) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// SendCard sends the BotFather game into the message's chat and topic and
// records it as an unlimited card. A card that could not be recorded would
// silently play daily, so it is deleted again and the error returned.
func (c *Cards) SendCard(ctx context.Context, b *bot.Bot, msg *models.Message, game string) error {
	thread := TopicOf(msg)
	sent, err := b.SendGame(ctx, &bot.SendGameParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: thread,
		GameShorName:    game, // the library's field name is misspelled
	})
	if err != nil {
		return err
	}
	if err := c.Record(ctx, msg.Chat.ID, sent.ID, thread); err != nil {
		if _, derr := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: msg.Chat.ID, MessageID: sent.ID}); derr != nil {
			log.Warn(c.nameOr("guessgame")+" delete unrecorded card failed", "err", derr)
		}
		return err
	}
	return nil
}

func (c *Cards) nameOr(def string) string {
	if c == nil || c.name == "" {
		return def
	}
	return c.name
}

// PlayParams configures HandlePlay for one game.
type PlayParams struct {
	Enabled  bool
	Cards    *Cards
	Now      time.Time
	TokenKey []byte
	// PageURL is the page's absolute URL; the token goes in its ?t=.
	PageURL       string
	MsgDisabled   string
	MsgNoTarget   string
	MsgCardLookup string
	LogName       string
}

// HandlePlay answers a Play press with the game URL carrying a signed token,
// or with an alert when the game cannot open. A press on a recorded card
// opens unlimited mode; any other card plays daily. Every path answers the
// query, so the client never keeps spinning.
func HandlePlay(ctx context.Context, b *bot.Bot, q *models.CallbackQuery, p PlayParams) error {
	if !p.Enabled {
		return AnswerAlert(ctx, b, q.ID, p.MsgDisabled)
	}
	c, form := PlayClaims(q)
	if form == "" {
		return AnswerAlert(ctx, b, q.ID, p.MsgNoTarget)
	}
	// An inline card has no chat message to record, so it is always daily.
	if c.InlineID == "" {
		_, ok, err := p.Cards.Lookup(ctx, c.ChatID, c.MessageID)
		if err != nil {
			// Never fall back to daily silently: the player pressed an
			// unlimited card.
			_ = AnswerAlert(ctx, b, q.ID, p.MsgCardLookup)
			return err
		}
		if ok {
			c.Mode = ModeUnlimited
		}
	}
	c.Expiry = p.Now.Add(TokenTTL).Unix()
	token, err := SignToken(p.TokenKey, c)
	if err != nil {
		_ = AnswerAlert(ctx, b, q.ID, p.MsgDisabled)
		return err
	}
	log.Debug(p.LogName+" play", "address", form, "mode", c.Mode)
	_, err = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID,
		URL:             p.PageURL + "?t=" + url.QueryEscape(token),
	})
	return err
}
