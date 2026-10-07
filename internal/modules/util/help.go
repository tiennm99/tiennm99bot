package util

import (
	"context"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
)

const repoURL = "https://github.com/tiennm99/tiennm99bot"

var supportFooter = fmt.Sprintf(
	`Enjoying the bot? Support me by starring the repo: <a href="%s">%s</a>`,
	repoURL, repoURL,
)

// helpMessageLimit is Telegram's message length cap. Telegram counts text
// after HTML tags are parsed out, so measuring the HTML source is conservative.
const helpMessageLimit = 4096

// RenderHelp produces the full body of /help: each module's public commands
// grouped under a bold module name, followed by the support footer. It is
// RenderHelpMessages joined back into one string.
//
// Exposed (capitalised) so tests can assert on the string without spinning up
// a bot context.
func RenderHelp(reg *modules.Registry) string {
	return strings.Join(RenderHelpMessages(reg), "\n\n")
}

// RenderHelpMessages renders /help as one or more messages, each within
// Telegram's length cap. Messages break only between module sections, and the
// support footer ends the last one.
// Modules in MODULES-env order. Modules with no visible commands are omitted.
// Protected and private commands are hidden; authorization-specific commands
// stay discoverable only through operator knowledge, not the public help/menu.
func RenderHelpMessages(reg *modules.Registry) []string {
	if reg == nil {
		return []string{"no commands registered\n\n" + supportFooter}
	}

	byModule := make(map[string][]modules.Command, len(reg.Modules))

	for _, c := range reg.PublicCommands() {
		byModule[ownerOf(reg, c.Name)] = append(byModule[ownerOf(reg, c.Name)], c)
	}

	var sections []string
	for _, mod := range reg.Modules {
		es := byModule[mod.Name]
		if len(es) == 0 {
			continue
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "<b>%s</b>", html.EscapeString(mod.Name))
		for _, command := range es {
			fmt.Fprintf(&sb, "\n%s %s",
				html.EscapeString(command.InvocationSentence()),
				html.EscapeString(command.SummarySentence()))
		}
		sections = append(sections, sb.String())
	}
	if len(sections) == 0 {
		sections = []string{"no commands registered"}
	}
	return packHelpSections(append(sections, supportFooter), helpMessageLimit)
}

// packHelpSections greedily joins sections with blank lines into messages of
// at most limit runes. A section longer than limit gets a message of its own.
func packHelpSections(sections []string, limit int) []string {
	var messages []string
	current := ""
	for _, section := range sections {
		candidate := section
		if current != "" {
			candidate = current + "\n\n" + section
		}
		if current != "" && utf8.RuneCountInString(candidate) > limit {
			messages = append(messages, current)
			current = section
			continue
		}
		current = candidate
	}
	return append(messages, current)
}

// ownerOf finds the module that registered the named command. Linear scan
// (modules are few; commands per module are few). Returns "" if not found —
// callers treat that as "skip".
func ownerOf(reg *modules.Registry, cmdName string) string {
	for _, m := range reg.Modules {
		for _, c := range m.Commands {
			if c.Name == cmdName {
				return m.Name
			}
		}
	}
	return ""
}

// helpCommand returns /help — pure renderer over the registry.
func helpCommand(reg *modules.Registry) modules.Command {
	return modules.Command{
		Name:        "help",
		Visibility:  modules.VisibilityPublic,
		Description: "Show all available commands",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			for _, text := range RenderHelpMessages(reg) {
				if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID:             update.Message.Chat.ID,
					MessageThreadID:    update.Message.MessageThreadID,
					Text:               text,
					ParseMode:          models.ParseModeHTML,
					LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
				}); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
