package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/stock"
	moduleutil "github.com/tiennm99/tiennm99bot/internal/modules/util"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestBotCommandMenu_ListsLoadedPublicCommandsByName(t *testing.T) {
	reg := &modules.Registry{
		Modules: []modules.Module{
			{
				Name: "beta",
				Commands: []modules.Command{
					{Name: "beta_public", Description: "Beta public", Parameters: "<value>", Visibility: modules.VisibilityPublic},
					{Name: "beta_private", Description: "Beta private", Visibility: modules.VisibilityPrivate},
					{Name: "beta_unlisted", Description: "Beta unlisted", Visibility: modules.VisibilityUnlisted},
				},
			},
			{
				Name: "alpha",
				Commands: []modules.Command{
					{Name: "alpha_public", Description: "Alpha public", Visibility: modules.VisibilityPublic},
					{Name: "alpha_protected", Description: "Alpha protected", Visibility: modules.VisibilityProtected},
				},
			},
		},
	}

	got := botCommandMenu(reg)
	want := []models.BotCommand{
		{Command: "alpha_public", Description: "Alpha public."},
		{Command: "beta_public", Description: "<value>. Beta public."},
	}
	if len(got) != len(want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("commands[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCommandDiscovery_AllPublicCommandsHaveSafeMetadata(t *testing.T) {
	reg, err := modules.Build(nil, factories(), storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	expectedParameters := map[string]string{
		"amlich":               "[date]",
		"duonglich":            "<date> [nhuan]",
		"coin_price":           "<coin>",
		"coin_topup":           "<usd_amount>",
		"coin_buy":             "<coin> <usd_to_spend>",
		"coin_sell":            "<coin> <usd_to_receive>",
		"gold_topup":           "<vnd_amount>",
		"gold_buy":             "<luong>",
		"gold_sell":            "<luong>",
		"gacha":                "<option,...>",
		"lol":                  "[date]",
		"loldle":               "[champion]",
		"monkeyd_crawl":        "<url> [font_size]",
		"monkeyd_tags":         "<url>",
		"noitu":                "",
		"noitubot":             "",
		"noitutop":             "",
		"random":               "<option,...>",
		"addsticker":           "[emoji...]",
		"alias":                "<name>",
		"aliases":              "",
		"blacklist":            "[text...]",
		"blacklist_add":        "[text...]",
		"blacklist_del":        "<text...>",
		"blacklist_rules":      "",
		"blacklist_check":      "<text...>",
		"whitelist_add":        "[text...]",
		"whitelist_del":        "<text...>",
		"whitelist_rnd":        "",
		"unalias":              "<name>",
		"insert":               "<name>",
		"stats":                "[users | user <username> | cmd <command_name>]",
		"stock_events":         "<ticker> [days]",
		"stock_info":           "<ticker>",
		"stock_price":          "<ticker>",
		"stock_topup":          "<vnd_amount>",
		"stock_buy":            "<quantity> <ticker>",
		"stock_sell":           "<quantity> <ticker>",
		"stock_cash_dividend":  "<vnd_per_share> <ticker>",
		"stock_share_dividend": "<ratio(owned:new)> <ticker>",
		"thoitiet":             "[location... | lat,long]",
		"thoitiethomnay":       "[location... | lat,long]",
		"thoitietngaymai":      "[location... | lat,long]",
		"thoitiettuannay":      "[location... | lat,long]",
		"trongtruonghop":       "[target...]",
		"tth":                  "[target...]",
		"wheelofnames":         "<option,...>",
		"wordle":               "[word]",
	}

	menu := botCommandMenu(reg)
	if len(menu) != len(reg.PublicCommands()) {
		t.Fatalf("menu commands = %d, public commands = %d", len(menu), len(reg.PublicCommands()))
	}
	// This map lists the commands whose parameter strings are pinned; commands
	// absent from it are not asserted (the lookup yields "" for them).
	seen := map[string]bool{}
	for _, command := range reg.PublicCommands() {
		seen[command.Name] = true
		if got := command.Parameters; got != expectedParameters[command.Name] {
			t.Errorf("/%s parameters = %q, want %q", command.Name, got, expectedParameters[command.Name])
		}
		description := command.TelegramMenuDescription()
		if strings.Contains(description, "Eg:") {
			t.Errorf("/%s native menu description contains an example: %q", command.Name, description)
		}
		if strings.ContainsAny(description, "\r\n") {
			t.Errorf("/%s native menu description is multiline: %q", command.Name, description)
		}
		if utf8.RuneCountInString(description) > telegramCommandDescriptionMaxRunesForTest {
			t.Errorf("/%s native menu description exceeds Telegram limit: %d", command.Name, utf8.RuneCountInString(description))
		}
	}

	// Every expectation must correspond to a registered command.
	//
	// Without this, an entry whose command stopped being registered simply
	// stopped being checked: the forward loop only visits commands that exist,
	// so removing a whole module from factories() left the suite green. That is
	// also what makes the parameterless entries above load-bearing rather than
	// decorative — "" == "" asserts nothing on its own, but the command having
	// to exist at all does.
	for name := range expectedParameters {
		if !seen[name] {
			t.Errorf("/%s is expected but not registered — a module dropped out of factories()", name)
		}
	}

	for i, help := range moduleutil.RenderHelpMessages(reg) {
		if utf8.RuneCountInString(help) > telegramMessageMaxRunesForTest {
			t.Fatalf("/help message %d source is %d characters, exceeds conservative Telegram limit %d", i+1, utf8.RuneCountInString(help), telegramMessageMaxRunesForTest)
		}
	}
}

const (
	telegramCommandDescriptionMaxRunesForTest = 256
	telegramMessageMaxRunesForTest            = 4096
)

func TestBotCommandMenu_StockDividendContracts(t *testing.T) {
	mod := stock.New(modules.Deps{Store: storage.NewMemoryProvider().Collection("stock")})
	mod.Name = "stock"
	got := botCommandMenu(&modules.Registry{Modules: []modules.Module{mod}})

	commands := make(map[string]string, len(got))
	for _, command := range got {
		commands[command.Command] = command.Description
		if len(command.Description) > 256 {
			t.Fatalf("description for %s exceeds Telegram limit", command.Command)
		}
	}
	for _, name := range []string{"stock_cash_dividend", "stock_share_dividend"} {
		if commands[name] == "" {
			t.Fatalf("stock menu missing %s: %v", name, commands)
		}
	}
	if _, exists := commands["stock_dividend"]; exists {
		t.Fatalf("retired stock_dividend remains in public menu: %v", commands)
	}
	if _, exists := commands["stock_bonus"]; exists {
		t.Fatalf("stock_bonus remains in public menu: %v", commands)
	}
}

func TestRegisterCommandMenu_CallsTelegramSetMyCommands(t *testing.T) {
	reg := &modules.Registry{
		Modules: []modules.Module{{
			Name: "demo",
			Commands: []modules.Command{{
				Name:        "demo",
				Description: "Demo command",
				Visibility:  modules.VisibilityPublic,
			}},
		}},
	}
	rb := testutil.NewRecordingBot(t)

	n, err := registerCommandMenu(context.Background(), rb.Bot, reg)
	if err != nil {
		t.Fatalf("registerCommandMenu: %v", err)
	}
	if n != 1 {
		t.Fatalf("registered count = %d, want 1", n)
	}

	call := rb.LastSent()
	if call.Method != "setMyCommands" {
		t.Fatalf("method = %q, want setMyCommands", call.Method)
	}
	var cmds []models.BotCommand
	if err := json.Unmarshal([]byte(call.Form["commands"]), &cmds); err != nil {
		t.Fatalf("decode commands form field: %v; raw=%q", err, call.Form["commands"])
	}
	if len(cmds) != 1 || cmds[0].Command != "demo" || cmds[0].Description != "Demo command." {
		t.Fatalf("commands payload = %+v, want demo command", cmds)
	}
}
