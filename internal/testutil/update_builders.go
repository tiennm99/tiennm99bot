// Package testutil provides shared fixtures for handler-layer integration
// tests: update builders, a recording Bot that captures outbound API calls
// instead of hitting Telegram, and helpers to assert on captured calls.
//
// Tests should construct a real *bot.Bot (via NewRecordingBot), register
// real module handlers against it, dispatch a fixture *models.Update via
// Bot.ProcessUpdate, and then inspect Sent() to verify the reply.
package testutil

import (
	"github.com/go-telegram/bot/models"
)

// NewPrivateMessage builds an Update for a 1:1 private DM. userID is both
// the chat id and the From id (private chats use the user as the chat id).
func NewPrivateMessage(userID int64, text string) *models.Update {
	return &models.Update{
		ID: 1,
		Message: &models.Message{
			ID:   1,
			Text: text,
			Chat: models.Chat{ID: userID, Type: models.ChatTypePrivate},
			From: &models.User{ID: userID, FirstName: "Test"},
			Entities: []models.MessageEntity{
				botCommandEntity(text),
			},
		},
	}
}

// NewGroupMessage builds an Update for a group chat where chatID and userID
// are distinct (group rules: subject = chat ID, sender = user ID).
func NewGroupMessage(chatID, userID int64, text string) *models.Update {
	return &models.Update{
		ID: 1,
		Message: &models.Message{
			ID:   1,
			Text: text,
			Chat: models.Chat{ID: chatID, Type: models.ChatTypeGroup, Title: "Test Group"},
			From: &models.User{ID: userID, FirstName: "Test"},
			Entities: []models.MessageEntity{
				botCommandEntity(text),
			},
		},
	}
}

// NewSupergroupMessage is the same shape as NewGroupMessage but with
// supergroup chat type — exercises the second branch in chathelper.SubjectFor.
func NewSupergroupMessage(chatID, userID int64, text string) *models.Update {
	u := NewGroupMessage(chatID, userID, text)
	u.Message.Chat.Type = models.ChatTypeSupergroup
	return u
}

// NewChannelMessage builds an Update for a channel post — no From field on
// most channel posts. Used to exercise the "no usable subject id" path.
func NewChannelMessage(chatID int64, text string) *models.Update {
	return &models.Update{
		ID: 1,
		Message: &models.Message{
			ID:   1,
			Text: text,
			Chat: models.Chat{ID: chatID, Type: models.ChatTypeChannel, Title: "Test Channel"},
			Entities: []models.MessageEntity{
				botCommandEntity(text),
			},
		},
	}
}

// botCommandEntity is what Telegram attaches when a message starts with `/`.
// The dispatcher's matchCommand reads the command name from it, so every
// fixture command-bearing message must include one.
func botCommandEntity(text string) models.MessageEntity {
	if len(text) == 0 || text[0] != '/' {
		return models.MessageEntity{Type: models.MessageEntityTypeBotCommand}
	}
	end := len(text)
	for i := 1; i < len(text); i++ {
		if text[i] == ' ' || text[i] == '@' {
			end = i
			break
		}
	}
	return models.MessageEntity{
		Type:   models.MessageEntityTypeBotCommand,
		Offset: 0,
		Length: end,
	}
}

// NewGameCallback builds the callback query Telegram sends when a user presses
// Play on a game message the bot sent into a chat: game_short_name set, no
// callback data, and the game message attached.
func NewGameCallback(userID, chatID int64, messageID int, shortName string) *models.Update {
	return &models.Update{
		ID: 1,
		CallbackQuery: &models.CallbackQuery{
			ID:   "game-cbq",
			From: models.User{ID: userID, FirstName: "Test"},
			Message: models.MaybeInaccessibleMessage{
				Type: models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{
					ID:   messageID,
					Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
				},
			},
			ChatInstance:  "chat-instance",
			GameShortName: shortName,
		},
	}
}

// NewInlineGameCallback builds the Play callback for a game message sent via
// the bot (inline mode or a t.me/<bot>?game= link): it carries
// inline_message_id instead of the message.
func NewInlineGameCallback(userID int64, inlineMessageID, shortName string) *models.Update {
	return &models.Update{
		ID: 1,
		CallbackQuery: &models.CallbackQuery{
			ID:              "game-cbq",
			From:            models.User{ID: userID, FirstName: "Test"},
			InlineMessageID: inlineMessageID,
			ChatInstance:    "chat-instance",
			GameShortName:   shortName,
		},
	}
}
