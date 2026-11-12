package random

import (
	"bytes"
	"context"
	"math/rand/v2"
	"regexp"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/cardpack"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/caseopen"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/genshin"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	gachaUsage       = "Usage: /gacha <option,...>\nOptions are 5* by default; prefix 4* or 3* to lower one, e.g. /gacha Pizza, 4* Pho, 3* Rice"
	genshinUsage     = "Usage: /genshin <option,...>\nOptions are 5* by default; prefix 4* or 3* to lower one, e.g. /genshin Pizza, 4* Pho, 3* Rice"
	csgoUsage        = "Usage: /csgo <option,...>\nOptions are 5* (gold) by default; prefix 4* (purple) or 3* (blue) to lower one, e.g. /csgo Pizza, 4* Pho, 3* Rice"
	gachaPlaceholder = "Wishing..."
	casePlaceholder  = "Opening..."
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

// gachaCommand tears open a card pack whose card shows the winner, rendered
// in process by the cardpack package.
func gachaCommand() modules.Command {
	return newGachaCommand("gacha", modules.VisibilityPublic,
		"Wish for one comma-separated option as a gacha card pack; 5* by default, prefix 4* or 3*",
		gachaUsage, gachaPlaceholder, renderCardPackAnimation)
}

// genshinCommand wishes with the Genshin-style meteor animation, rendered in
// process by the genshin package. It is unlisted: anyone can run it, but it
// stays out of the command menu and /help.
func genshinCommand() modules.Command {
	return newGachaCommand("genshin", modules.VisibilityUnlisted,
		"Wish for one comma-separated option, Genshin style; 5* by default, prefix 4* or 3*",
		genshinUsage, gachaPlaceholder, renderGenshinAnimation)
}

// csgoCommand opens a case: the options fill a spinning reel of item cards
// that stops on the winner. Rendered in process by the caseopen package and
// unlisted, like /genshin.
func csgoCommand() modules.Command {
	return newGachaCommand("csgo", modules.VisibilityUnlisted,
		"Open a case of comma-separated options, CS:GO style; 5* by default, prefix 4* or 3*",
		csgoUsage, casePlaceholder, renderCaseAnimation)
}

// renderedAnimation is a rendered clip plus the metadata Telegram's
// sendAnimation wants.
type renderedAnimation struct {
	Data     []byte
	Duration int
	Width    int
	Height   int
}

// renderCardPack is the in-process card-pack renderer; tests swap it out.
var renderCardPack = cardpack.Render

func renderCardPackAnimation(ctx context.Context, options []gachaOption, winner int) (renderedAnimation, error) {
	data, err := renderCardPack(ctx, options[winner].Label, options[winner].Rarity, rand.Uint64())
	if err != nil {
		return renderedAnimation{}, err
	}
	return renderedAnimation{
		Data:     data,
		Duration: cardpack.DurationSeconds,
		Width:    cardpack.Width,
		Height:   cardpack.Height,
	}, nil
}

// renderCase is the in-process case renderer; tests swap it out.
var renderCase = caseopen.Render

func renderCaseAnimation(ctx context.Context, options []gachaOption, winner int) (renderedAnimation, error) {
	items := make([]caseopen.Item, len(options))
	for i, option := range options {
		items[i] = caseopen.Item{Label: option.Label, Rarity: option.Rarity}
	}
	data, err := renderCase(ctx, items, winner, rand.Uint64())
	if err != nil {
		return renderedAnimation{}, err
	}
	return renderedAnimation{
		Data:     data,
		Duration: caseopen.DurationSeconds,
		Width:    caseopen.Width,
		Height:   caseopen.Height,
	}, nil
}

// renderGenshin is the in-process Genshin renderer; tests swap it out.
var renderGenshin = genshin.Render

func renderGenshinAnimation(ctx context.Context, options []gachaOption, winner int) (renderedAnimation, error) {
	data, err := renderGenshin(ctx, options[winner].Label, options[winner].Rarity)
	if err != nil {
		return renderedAnimation{}, err
	}
	return renderedAnimation{
		Data:     data,
		Duration: genshin.DurationSeconds,
		Width:    genshin.Width,
		Height:   genshin.Height,
	}, nil
}

// wishRenderer draws the animation that lands on options[winner].
type wishRenderer func(ctx context.Context, options []gachaOption, winner int) (renderedAnimation, error)

// newGachaCommand builds a wish command that picks one option and renders it,
// falling back to a text reply when rendering fails.
func newGachaCommand(name string, visibility modules.Visibility, description, usage, placeholderText string, render wishRenderer) modules.Command {
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

			placeholder := sendPlaceholder(ctx, b, update.Message, placeholderText)
			animation, err := render(ctx, options, winner)
			if err != nil {
				log.Warn("gacha render failed", "command", name, "err", err)
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
