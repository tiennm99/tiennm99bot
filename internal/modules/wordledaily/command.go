package wordledaily

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
)

const (
	msgChannel      = "Wordle Daily can't be played in channels."
	msgSendGameFail = "Couldn't send Wordle Daily. Try again later."
)

// handleCommand (/wordledaily) sends the BotFather game in the same chat and
// topic. No reply markup: Telegram then adds the Play button itself, which
// is the button a game message requires first.
func (s *service) handleCommand(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	if msg.Chat.Type == models.ChatTypeChannel {
		return chathelper.Reply(ctx, b, msg, msgChannel)
	}
	if !s.enabled() {
		return chathelper.Reply(ctx, b, msg, msgDisabled)
	}
	if _, err := b.SendGame(ctx, &bot.SendGameParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: topicOf(msg),
		GameShorName:    ShortName, // the library's field name is misspelled
	}); err != nil {
		_ = chathelper.Reply(ctx, b, msg, msgSendGameFail)
		return err
	}
	return nil
}

// handleSubscribe (/wordledaily_subscribe) opts the chat topic into the
// 07:00 ICT push. Anyone in the chat may subscribe, like the other daily
// pushes.
func (s *service) handleSubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	if msg.Chat.Type == models.ChatTypeChannel {
		return chathelper.Reply(ctx, b, msg, msgChannel)
	}
	// The push cron exists only when the game is enabled, so a
	// subscription would never be served.
	if !s.enabled() {
		return chathelper.Reply(ctx, b, msg, msgDisabled)
	}
	thread := topicOf(msg)
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	added, err := subscription.Add(ctx, s.subscribers, msg.Chat.ID, thread)
	if err != nil {
		return err
	}
	scope := scopeOf(thread)
	if added {
		return chathelper.Reply(ctx, b, msg,
			"✅ Subscribed "+scope+" to Wordle Daily: a new puzzle and yesterday's group results every day at 07:00 ICT.\n"+
				"If you block the bot, you'll be auto-unsubscribed on the next push.")
	}
	return chathelper.Reply(ctx, b, msg, "Already subscribed in "+scope+".")
}

// handleUnsubscribe (/wordledaily_unsubscribe) opts the chat topic out. It
// works while the game is disabled too, so a chat can always leave.
func (s *service) handleUnsubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	if s.subscribers == nil {
		return chathelper.Reply(ctx, b, msg, msgDisabled)
	}
	thread := topicOf(msg)
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	removed, err := subscription.Remove(ctx, s.subscribers, msg.Chat.ID, thread)
	if err != nil {
		return err
	}
	scope := scopeOf(thread)
	if removed {
		return chathelper.Reply(ctx, b, msg, "Unsubscribed "+scope+".")
	}
	return chathelper.Reply(ctx, b, msg, strings.ToUpper(scope[:1])+scope[1:]+" wasn't subscribed.")
}

// topicOf is the message's forum topic, or 0 for the whole chat. Telegram
// also gives a reply chain in an ordinary supergroup a thread id;
// IsTopicMessage marks a real topic. Plays key the group results the same
// way (playClaims), so a subscription made by replying still gets its
// chat's recap and streak.
func topicOf(msg *models.Message) int {
	if !msg.IsTopicMessage {
		return 0
	}
	return msg.MessageThreadID
}

func scopeOf(thread int) string {
	if thread != 0 {
		return "this topic"
	}
	return "this chat"
}
