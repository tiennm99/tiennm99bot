package modules

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var commandNameRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// gameShortNameRe mirrors BotFather's rule for a game's short name.
var gameShortNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,64}$`)

const telegramCommandDescriptionMaxRunes = 256

func validateCommand(c Command) error {
	if !commandNameRe.MatchString(c.Name) {
		return fmt.Errorf("command name %q must match %s", c.Name, commandNameRe)
	}
	switch c.Visibility {
	case VisibilityPublic, VisibilityProtected, VisibilityPrivate, VisibilityUnlisted:
	default:
		return fmt.Errorf("command %q: unknown visibility %d", c.Name, c.Visibility)
	}
	if strings.TrimSpace(c.Description) == "" {
		return fmt.Errorf("command %q: description is required", c.Name)
	}
	if strings.ContainsAny(c.Description, "\r\n") {
		return fmt.Errorf("command %q: description must be single-line", c.Name)
	}
	if strings.ContainsAny(c.Parameters, "\r\n") {
		return fmt.Errorf("command %q: parameters must be single-line", c.Name)
	}
	if c.Visibility == VisibilityPublic && utf8.RuneCountInString(c.TelegramMenuDescription()) > telegramCommandDescriptionMaxRunes {
		return fmt.Errorf("command %q: Telegram menu description exceeds %d characters", c.Name, telegramCommandDescriptionMaxRunes)
	}
	if c.Handler == nil {
		return fmt.Errorf("command %q: handler is nil", c.Name)
	}
	return nil
}

func validateCron(c Cron) error {
	if c.Name == "" {
		return fmt.Errorf("cron: name is required")
	}
	if c.Handler == nil {
		return fmt.Errorf("cron %q: handler is nil", c.Name)
	}
	return nil
}

func validateCallback(c Callback) error {
	if strings.TrimSpace(c.Prefix) == "" {
		return fmt.Errorf("callback: prefix is required")
	}
	if len(c.Prefix) > 32 || strings.ContainsAny(c.Prefix, "\r\n\x00") {
		return fmt.Errorf("callback prefix %q is invalid", c.Prefix)
	}
	switch c.Visibility {
	case VisibilityPublic, VisibilityProtected, VisibilityPrivate, VisibilityUnlisted:
	default:
		return fmt.Errorf("callback prefix %q: unknown visibility %d", c.Prefix, c.Visibility)
	}
	if c.Handler == nil {
		return fmt.Errorf("callback prefix %q: handler is nil", c.Prefix)
	}
	return nil
}

func validVisibility(v Visibility) bool {
	switch v {
	case VisibilityPublic, VisibilityProtected, VisibilityPrivate, VisibilityUnlisted:
		return true
	}
	return false
}

func validateGame(g Game) error {
	if !gameShortNameRe.MatchString(g.ShortName) {
		return fmt.Errorf("game short name %q must match %s", g.ShortName, gameShortNameRe)
	}
	if !validVisibility(g.Visibility) {
		return fmt.Errorf("game %q: unknown visibility %d", g.ShortName, g.Visibility)
	}
	if g.Handler == nil {
		return fmt.Errorf("game %q: handler is nil", g.ShortName)
	}
	return nil
}

// validateRoute keeps a module's HTTP routes inside its own /games/<module>/
// subtree. Method and host patterns are refused so the prefix check below sees
// the path itself.
func validateRoute(module string, r Route) error {
	if r.Handler == nil {
		return fmt.Errorf("route %q: handler is nil", r.Pattern)
	}
	if r.Pattern == "" || !strings.HasPrefix(r.Pattern, "/") || strings.ContainsAny(r.Pattern, " \t\r\n") {
		return fmt.Errorf("route %q: pattern must be a path starting with / (no method or host)", r.Pattern)
	}
	prefix := "/games/" + module + "/"
	if !strings.HasPrefix(r.Pattern, prefix) {
		return fmt.Errorf("route %q: pattern must start with %s", r.Pattern, prefix)
	}
	return nil
}
