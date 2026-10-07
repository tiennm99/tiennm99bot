package stats

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// telegramMaxLen leaves margin below Telegram's 4096-character message limit.
// It is compared against the byte length, which is never shorter than the
// character count, so the check errs on the safe side.
const telegramMaxLen = 4000

const statsUsage = `Usage:
/stats
/stats users
/stats user <username>
/stats cmd <command_name>`

// row is one rendered line of a /stats view: a "/command" or "@username"
// label and its count.
type row struct {
	display string
	n       int64
}

func statsCommand(c *counter) modules.Command {
	return modules.Command{
		Name:        "stats",
		Visibility:  modules.VisibilityPublic,
		Description: "Show command usage statistics",
		Parameters:  "[users | user <username> | cmd <command_name>]",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			text := renderStats(ctx, c, parseSubargs(update))
			return chathelper.Reply(ctx, b, update.Message, text)
		},
	}
}

// renderStats dispatches to the requested view. Exposed as a pure function
// (no *bot.Bot) so tests can assert on output without driving a recording bot
// through the full command-routing path.
func renderStats(ctx context.Context, c *counter, args string) string {
	fields := strings.Fields(args)
	switch {
	case len(fields) == 0:
		return viewTopCommands(ctx, c)
	case fields[0] == "users":
		return viewTopUsers(ctx, c)
	case fields[0] == "user":
		if len(fields) < 2 {
			return statsUsage
		}
		return viewUserCommands(ctx, c, strings.TrimPrefix(fields[1], "@"))
	case fields[0] == "cmd":
		if len(fields) < 2 {
			return statsUsage
		}
		cmd := strings.TrimPrefix(fields[1], "/")
		if cmd == "" {
			return statsUsage
		}
		return viewCmdUsers(ctx, c, cmd)
	default:
		return statsUsage
	}
}

// parseSubargs returns the message text after the /stats command token,
// stripped of the @botname suffix and leading whitespace. Mirrors the
// entity-stripping logic in dispatcher.matchCommand so /stats@tiennm99bot users
// behaves like /stats users in groups.
func parseSubargs(update *models.Update) string {
	msg := update.Message
	for _, e := range msg.Entities {
		if e.Type != models.MessageEntityTypeBotCommand {
			continue
		}
		end := e.Offset + e.Length
		if e.Offset < 0 || end > len(msg.Text) || e.Length < 1 {
			continue
		}
		return strings.TrimLeft(msg.Text[end:], " \t")
	}
	return ""
}

func viewTopCommands(ctx context.Context, c *counter) string {
	rows, err := c.store.TopCommands(ctx, topK)
	if err != nil {
		log.Error("stats: top commands failed", "err", err)
		return "Could not load stats. Try again later."
	}
	if len(rows) == 0 {
		return "No command stats yet."
	}
	return renderTopN("Command usage:", rows, topK)
}

func viewTopUsers(ctx context.Context, c *counter) string {
	rows, err := c.store.TopUsers(ctx, topK)
	if err != nil {
		log.Error("stats: top users failed", "err", err)
		return "Could not load stats. Try again later."
	}
	if len(rows) == 0 {
		return "No user stats yet."
	}
	return renderTopN("Top users:", rows, topK)
}

func viewUserCommands(ctx context.Context, c *counter, username string) string {
	rows, found, err := c.store.CommandsByUser(ctx, username, topK)
	if err != nil {
		log.Error("stats: commands by user failed", "username", username, "err", err)
		return "Could not load stats. Try again later."
	}
	if !found {
		return fmt.Sprintf("User @%s not found.", username)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No commands recorded for @%s.", username)
	}
	return renderTopN(fmt.Sprintf("Commands by @%s:", username), rows, topK)
}

func viewCmdUsers(ctx context.Context, c *counter, cmd string) string {
	rows, err := c.store.UsersByCommand(ctx, cmd, topK)
	if err != nil {
		log.Error("stats: users by command failed", "command", cmd, "err", err)
		return "Could not load stats. Try again later."
	}
	if len(rows) == 0 {
		return fmt.Sprintf("Command /%s has no users yet.", cmd)
	}
	return renderTopN(fmt.Sprintf("Users of /%s:", cmd), rows, topK)
}

func sortRows(rows []row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].display < rows[j].display
	})
}

func renderTopN(header string, rows []row, k int) string {
	if k > 0 && len(rows) > k {
		rows = rows[:k]
	}
	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteByte('\n')
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s: %d\n", r.display, r.n)
	}
	output := strings.TrimSuffix(sb.String(), "\n")
	if len(output) > telegramMaxLen {
		cutoff := strings.LastIndexByte(output[:telegramMaxLen], '\n')
		if cutoff <= 0 {
			cutoff = telegramMaxLen
		}
		output = output[:cutoff] + "\n…(truncated)"
	}
	return output
}
