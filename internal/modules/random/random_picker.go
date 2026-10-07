package random

import (
	"context"
	"math/rand/v2"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const randomUsage = "Usage: /random <option,...>"

func splitWheelOptions(arg string) []string {
	parts := strings.Split(arg, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if option := strings.TrimSpace(part); option != "" {
			out = append(out, option)
		}
	}
	return out
}

func randomCommand() modules.Command {
	return modules.Command{
		Name:        "random",
		Visibility:  modules.VisibilityPublic,
		Description: "Pick one random comma-separated option",
		Parameters:  "<option,...>",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			options := splitWheelOptions(chathelper.ArgAfterCommand(update.Message.Text))
			if len(options) == 0 {
				return chathelper.Reply(ctx, b, update.Message, randomUsage)
			}
			return chathelper.Reply(ctx, b, update.Message, options[pickWheelOption(options)])
		},
	}
}

func pickWheelOption(options []string) int {
	return rand.N(len(options))
}
