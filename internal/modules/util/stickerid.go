package util

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const stickerIDUsage = "Reply to a sticker message with /stickerid to get its file_id.\n" +
	"Usage: send a sticker to me, then tap Reply on it and type /stickerid."

// stickerIDCommand returns /stickerid — private dev helper. Reply to a
// sticker, run /stickerid, get the bot-scoped file_id back. Used to populate
// loldle's congrats/lose/giveup sticker pools.
func stickerIDCommand() modules.Command {
	return modules.Command{
		Name:        "stickerid",
		Visibility:  modules.VisibilityPrivate,
		Description: "Reply to a sticker with this command to get its bot-scoped file_id",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			msg := update.Message
			if msg == nil {
				return nil
			}

			sticker := stickerFrom(msg)
			if sticker == nil {
				return chathelper.Reply(ctx, b, msg, stickerIDUsage)
			}

			setName := sticker.SetName
			if setName == "" {
				setName = "(no set)"
			}
			emoji := sticker.Emoji
			if emoji == "" {
				emoji = "—"
			}

			var sb strings.Builder
			sb.WriteString("<b>file_id</b>\n")
			fmt.Fprintf(&sb, "<code>%s</code>\n\n", html.EscapeString(sticker.FileID))
			sb.WriteString("<b>file_unique_id</b>\n")
			fmt.Fprintf(&sb, "<code>%s</code>\n\n", html.EscapeString(sticker.FileUniqueID))
			fmt.Fprintf(&sb, "set: %s · emoji: %s",
				html.EscapeString(setName), html.EscapeString(emoji))

			return chathelper.ReplyHTML(ctx, b, msg, sb.String())
		},
	}
}

// stickerFrom pulls the sticker out of the *replied-to* message.
func stickerFrom(msg *models.Message) *models.Sticker {
	if msg.ReplyToMessage == nil {
		return nil
	}
	return msg.ReplyToMessage.Sticker
}
