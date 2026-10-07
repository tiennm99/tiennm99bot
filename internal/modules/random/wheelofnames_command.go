package random

import (
	"bytes"
	"context"
	"errors"
	"html"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	wheelFilename              = "wheelofnames.gif"
	wheelResultCaptionMaxRunes = 900
)

func wheelOfNamesCommand() modules.Command {
	return modules.Command{
		Name:        "wheelofnames",
		Visibility:  modules.VisibilityPublic,
		Description: "Pick one comma-separated option with wheel GIF when configured",
		Parameters:  "<option,...>",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			options := splitWheelOptions(chathelper.ArgAfterCommand(update.Message.Text))
			if len(options) == 0 {
				return chathelper.Reply(ctx, b, update.Message, wheelUsage)
			}
			winner := pickWheelOption(options)
			placeholder := sendRenderPlaceholder(ctx, b, update.Message, wheelPlaceholder)
			animation, err := renderWheelOfNamesAnimation(ctx, options, winner)
			if err != nil {
				if !errors.Is(err, errRendererNotConfigured) {
					log.Warn("wheelofnames remote render failed", "err", err)
				}
				return replaceWheelPlaceholder(ctx, b, update.Message, placeholder, options[winner])
			}
			_, err = b.SendAnimation(ctx, &bot.SendAnimationParams{
				ChatID:          update.Message.Chat.ID,
				MessageThreadID: update.Message.MessageThreadID,
				Animation: &models.InputFileUpload{
					Filename: wheelFilename,
					Data:     bytes.NewReader(animation.Data),
				},
				Duration:  animation.Duration,
				Width:     animation.Width,
				Height:    animation.Height,
				Caption:   wheelResultCaption(options, winner),
				ParseMode: models.ParseModeHTML,
			})
			if err != nil {
				log.Warn("wheelofnames send animation failed", "chat", update.Message.Chat.ID, "err", err)
				return replaceWheelPlaceholder(ctx, b, update.Message, placeholder, options[winner])
			}
			// The animation carries the result, so the placeholder is retired
			// only once it is safely delivered.
			if err := chathelper.DeleteMessage(ctx, b, update.Message, placeholder); err != nil {
				log.Warn("wheelofnames placeholder delete failed", "chat", update.Message.Chat.ID, "err", err)
			}
			return nil
		},
	}
}

const wheelUsage = "Usage: /wheelofnames <option,...>"

const wheelPlaceholder = "Spinning..."

// sendRenderPlaceholder posts a holding message such as "Spinning..." and
// returns its id, or 0 when none was posted.
//
// It is skipped unless a renderer is configured: without one the winner reply
// is immediate, and a placeholder would only flash. A failed placeholder is
// non-fatal — the pick still resolves, just without the holding message.
func sendRenderPlaceholder(ctx context.Context, b *bot.Bot, msg *models.Message, text string) int {
	if _, err := rendererBaseURL(newRendererClientFromEnv().BaseURL); err != nil {
		return 0
	}
	id, err := chathelper.SendText(ctx, b, msg, text)
	if err != nil {
		log.Warn("render placeholder send failed", "chat", msg.Chat.ID, "err", err)
		return 0
	}
	return id
}

// replaceWheelPlaceholder resolves the spin in place by editing the holding
// message to the winner, falling back to a fresh reply when there is no
// placeholder to edit or the edit is rejected.
func replaceWheelPlaceholder(ctx context.Context, b *bot.Bot, msg *models.Message, placeholder int, winner string) error {
	if placeholder != 0 {
		err := chathelper.EditText(ctx, b, msg, placeholder, winner)
		if err == nil {
			return nil
		}
		log.Warn("render placeholder edit failed", "chat", msg.Chat.ID, "err", err)
	}
	return chathelper.Reply(ctx, b, msg, winner)
}

func wheelResultCaption(options []string, winner int) string {
	result := padWheelResultCaption(options, truncateWheelResultCaption(options[winner]))
	return `Result: <span class="tg-spoiler">` + html.EscapeString(result) + `</span>`
}

const wheelCaptionPad = "_"

// padWheelResultCaption centers shorter winners between underscores so every
// option renders a spoiler of equal length; otherwise the spoiler width
// reveals the winner. The longest option is returned unpadded.
func padWheelResultCaption(options []string, result string) string {
	longest := len([]rune(result))
	for _, option := range options {
		if n := len([]rune(truncateWheelResultCaption(option))); n > longest {
			longest = n
		}
	}
	missing := longest - len([]rune(result))
	left := missing / 2
	return strings.Repeat(wheelCaptionPad, left) + result + strings.Repeat(wheelCaptionPad, missing-left)
}

func truncateWheelResultCaption(result string) string {
	runes := []rune(result)
	if len(runes) <= wheelResultCaptionMaxRunes {
		return result
	}
	return string(runes[:wheelResultCaptionMaxRunes]) + "..."
}
