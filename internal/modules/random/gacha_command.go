package random

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"regexp"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	gachaUsage       = "Usage: /gacha <option,...>\nOptions are 5* by default; prefix 4* or 3* to lower one, e.g. /gacha Pizza, 4* Pho, 3* Rice"
	genshinUsage     = "Usage: /genshin <option,...>\nOptions are 5* by default; prefix 4* or 3* to lower one, e.g. /genshin Pizza, 4* Pho, 3* Rice"
	gachaPlaceholder = "Wishing..."
	gachaFilename    = "gacha.mp4"

	gachaMinRarity     = 3
	gachaMaxRarity     = 5
	gachaDefaultRarity = gachaMaxRarity
)

// gachaRarityTag matches a leading "3*", "4*", or "5*" rarity prefix.
var gachaRarityTag = regexp.MustCompile(`^([345])\s*\*\s*`)

type gachaOption struct {
	Label  string
	Rarity int
}

// parseGachaOptions splits comma-separated options and strips each one's
// rarity prefix. Unprefixed options are 5★; options left blank after the
// prefix is removed are dropped.
func parseGachaOptions(arg string) []gachaOption {
	parts := splitWheelOptions(arg)
	out := make([]gachaOption, 0, len(parts))
	for _, part := range parts {
		option := gachaOption{Label: part, Rarity: gachaDefaultRarity}
		if m := gachaRarityTag.FindStringSubmatchIndex(part); m != nil {
			option.Label = strings.TrimSpace(part[m[1]:])
			option.Rarity = int(part[m[2]] - '0')
		}
		if option.Label != "" {
			out = append(out, option)
		}
	}
	return out
}

// gachaResultText renders an option as its stars followed by its label.
func gachaResultText(option gachaOption) string {
	return strings.Repeat("★", option.Rarity) + " " + option.Label
}

func gachaCommand() modules.Command {
	return newGachaCommand("gacha", modules.VisibilityPublic,
		"Wish for one comma-separated option as a gacha card pack; 5* by default, prefix 4* or 3*",
		gachaUsage, gachaStyleCardPack)
}

// genshinCommand wishes with the Genshin-style meteor animation. It is
// unlisted: anyone can run it, but it stays out of the command menu and /help.
func genshinCommand() modules.Command {
	return newGachaCommand("genshin", modules.VisibilityUnlisted,
		"Wish for one comma-separated option, Genshin style; 5* by default, prefix 4* or 3*",
		genshinUsage, gachaStyleGenshin)
}

// newGachaCommand builds a wish command that picks one option and renders it
// in style, falling back to a text reply when rendering fails.
func newGachaCommand(name string, visibility modules.Visibility, description, usage string, style gachaStyle) modules.Command {
	return modules.Command{
		Name:        name,
		Visibility:  visibility,
		Description: description,
		Parameters:  "<option,...>",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			options := parseGachaOptions(chathelper.ArgAfterCommand(update.Message.Text))
			if len(options) == 0 {
				return chathelper.Reply(ctx, b, update.Message, usage)
			}
			// Every option is equally likely; the rarity tag only styles the
			// wish animation and the result text.
			winner := rand.IntN(len(options))
			results := make([]string, len(options))
			for i, option := range options {
				results[i] = gachaResultText(option)
			}

			placeholder := sendRenderPlaceholder(ctx, b, update.Message, gachaPlaceholder)
			animation, err := renderGachaAnimation(ctx, style, options[winner].Label, options[winner].Rarity)
			if err != nil {
				if !errors.Is(err, errRendererNotConfigured) {
					log.Warn("gacha remote render failed", "command", name, "err", err)
				}
				return replaceWheelPlaceholder(ctx, b, update.Message, placeholder, results[winner])
			}
			_, err = b.SendAnimation(ctx, &bot.SendAnimationParams{
				ChatID:          update.Message.Chat.ID,
				MessageThreadID: update.Message.MessageThreadID,
				Animation: &models.InputFileUpload{
					Filename: gachaFilename,
					Data:     bytes.NewReader(animation.Data),
				},
				Duration:  animation.Duration,
				Width:     animation.Width,
				Height:    animation.Height,
				Caption:   wheelResultCaption(results, winner),
				ParseMode: models.ParseModeHTML,
			})
			if err != nil {
				log.Warn("gacha send animation failed", "command", name, "chat", update.Message.Chat.ID, "err", err)
				return replaceWheelPlaceholder(ctx, b, update.Message, placeholder, results[winner])
			}
			if err := chathelper.DeleteMessage(ctx, b, update.Message, placeholder); err != nil {
				log.Warn("gacha placeholder delete failed", "command", name, "chat", update.Message.Chat.ID, "err", err)
			}
			return nil
		},
	}
}
