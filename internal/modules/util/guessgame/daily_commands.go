package guessgame

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
)

// DailyTexts are a daily game's command replies.
type DailyTexts struct {
	Channel  string // the game cannot be played in a channel
	Disabled string // the game is not configured
	SendFail string // the card could not be sent
}

// DailyCommands are the handlers of a daily game's three commands: send
// today's card, subscribe to the 07:00 push, unsubscribe. enabled reports
// whether Play can open the game.
type DailyCommands struct {
	Daily   *Daily
	Enabled func() bool
	Texts   DailyTexts
}

// HandleCard sends the BotFather game in the same chat and topic. No reply
// markup: Telegram then adds the Play button itself, which is the button a
// game message requires first. The card is not recorded, so it plays daily.
func (dc DailyCommands) HandleCard(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	if msg.Chat.Type == models.ChatTypeChannel {
		return chathelper.Reply(ctx, b, msg, dc.Texts.Channel)
	}
	if !dc.Enabled() {
		return chathelper.Reply(ctx, b, msg, dc.Texts.Disabled)
	}
	if _, err := b.SendGame(ctx, &bot.SendGameParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: TopicOf(msg),
		GameShorName:    dc.Daily.cfg.GameShortName, // the library's field name is misspelled
	}); err != nil {
		_ = chathelper.Reply(ctx, b, msg, dc.Texts.SendFail)
		return err
	}
	return nil
}

// HandleSubscribe opts the chat topic into the 07:00 ICT push. Anyone in
// the chat may subscribe, like the other daily pushes.
func (dc DailyCommands) HandleSubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	if msg.Chat.Type == models.ChatTypeChannel {
		return chathelper.Reply(ctx, b, msg, dc.Texts.Channel)
	}
	// The push cron exists only when the game is enabled, so a
	// subscription would never be served.
	if !dc.Enabled() {
		return chathelper.Reply(ctx, b, msg, dc.Texts.Disabled)
	}
	d := dc.Daily
	thread := TopicOf(msg)
	d.SubscribersMu.Lock()
	defer d.SubscribersMu.Unlock()
	added, err := subscription.Add(ctx, d.Subscribers, msg.Chat.ID, thread)
	if err != nil {
		return err
	}
	scope := ScopeOf(thread)
	if added {
		return chathelper.Reply(ctx, b, msg,
			"✅ Subscribed "+scope+" to "+d.cfg.Rules.Label()+": a new puzzle and yesterday's group results every day at 07:00 ICT.\n"+
				"If you block the bot, you'll be auto-unsubscribed on the next push.")
	}
	return chathelper.Reply(ctx, b, msg, "Already subscribed in "+scope+".")
}

// HandleUnsubscribe opts the chat topic out. It works while the game is
// disabled too, so a chat can always leave.
func (dc DailyCommands) HandleUnsubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	d := dc.Daily
	if d.Subscribers == nil {
		return chathelper.Reply(ctx, b, msg, dc.Texts.Disabled)
	}
	thread := TopicOf(msg)
	d.SubscribersMu.Lock()
	defer d.SubscribersMu.Unlock()
	removed, err := subscription.Remove(ctx, d.Subscribers, msg.Chat.ID, thread)
	if err != nil {
		return err
	}
	scope := ScopeOf(thread)
	if removed {
		return chathelper.Reply(ctx, b, msg, "Unsubscribed "+scope+".")
	}
	return chathelper.Reply(ctx, b, msg, strings.ToUpper(scope[:1])+scope[1:]+" wasn't subscribed.")
}

// TopicOf is the message's forum topic, or 0 for the whole chat. Telegram
// also gives a reply chain in an ordinary supergroup a thread id;
// IsTopicMessage marks a real topic. Plays key the group results the same
// way (PlayClaims), so a subscription made by replying still gets its
// chat's recap and streak.
func TopicOf(msg *models.Message) int {
	if !msg.IsTopicMessage {
		return 0
	}
	return msg.MessageThreadID
}

// ScopeOf names a subscription's scope in replies.
func ScopeOf(thread int) string {
	if thread != 0 {
		return "this topic"
	}
	return "this chat"
}
