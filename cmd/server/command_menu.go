package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
)

const commandMenuTimeout = 8 * time.Second

// botCommandMenu builds the Telegram command menu from the loaded public
// commands, sorted by name so related commands such as /thoitiet and
// /thoitiethomnay sit together whatever their module order.
func botCommandMenu(reg *modules.Registry) []models.BotCommand {
	if reg == nil {
		return nil
	}
	var out []models.BotCommand
	for _, mod := range reg.Modules {
		for _, cmd := range mod.Commands {
			if cmd.Visibility != modules.VisibilityPublic {
				continue
			}
			out = append(out, models.BotCommand{
				Command:     cmd.Name,
				Description: cmd.TelegramMenuDescription(),
			})
		}
	}
	slices.SortFunc(out, func(a, b models.BotCommand) int { return strings.Compare(a.Command, b.Command) })
	return out
}

// registerCommandMenu publishes the menu to Telegram. It is best-effort at
// startup: callers should log failures and keep the bot running.
func registerCommandMenu(ctx context.Context, b *bot.Bot, reg *modules.Registry) (int, error) {
	cmds := botCommandMenu(reg)
	menuCtx, cancel := context.WithTimeout(ctx, commandMenuTimeout)
	defer cancel()

	ok, err := b.SetMyCommands(menuCtx, &bot.SetMyCommandsParams{Commands: cmds})
	if err != nil {
		return len(cmds), err
	}
	if !ok {
		return len(cmds), errors.New("telegram setMyCommands returned false")
	}
	return len(cmds), nil
}
