// Package blacklist implements a per-thread dictionary of forbidden text and a
// companion list of exceptions that rescue false positives.
//
// The module is passive on purpose. It never reads ordinary chat messages and
// never deletes, warns or restricts anyone: the lists are inert until
// /blacklist_check (or /blacklist with an argument) asks about a specific text. Enforcement would need a
// message-level hook the dispatcher does not have, privacy mode disabled in
// BotFather, and group-admin delete rights — all deliberately out of scope.
// Check is a pure function, so a future hook could call it unchanged.
//
// Scope is one thread: (Chat.ID, MessageThreadID), the same pair the lol module
// keys subscriptions by. Entries added in one forum topic are invisible in the
// next, and a DM is simply the thread (user's chat ID, 0). Within a thread the
// lists are world-writable, the same trust model the alias module uses: the
// list belongs to the conversation, so the permission to edit it does too.
package blacklist

import (
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Entry is one stored rule.
//
// The storage key holds the normalized form, so Text carries what the adder
// actually typed — the only place the original casing and spacing survive, and
// what /blacklist_rules shows.
type Entry struct {
	Text      string `bson:"text"`      // as typed, for echoing back
	OwnerID   int64  `bson:"ownerId"`   // who added it
	CreatedAt int64  `bson:"createdAt"` // unix millis
}

// Store is the module's typed view over its collection. Both lists live in it,
// separated by the key prefixes in scope.go.
type Store = storage.DocStore[Entry]

// state holds what the handlers share.
type state struct {
	store Store
}

// New is the module Factory.
func New(deps modules.Deps) modules.Module {
	s := &state{store: storage.Typed[Entry](deps.Store)}
	return modules.Module{
		Commands: []modules.Command{
			{
				// The whole module behind one short name: bare it lists, with
				// an argument it checks.
				Name:        "blacklist",
				Visibility:  modules.VisibilityPublic,
				Description: "Check a text, or list both",
				Parameters:  "[text...]",
				Handler:     s.handleShort,
			},
			{
				Name:        "blacklist_add",
				Visibility:  modules.VisibilityPublic,
				Description: "Blacklist a text or a reply",
				Parameters:  "[text...]",
				Handler:     s.handleAdd(listBlack),
			},
			{
				Name:        "blacklist_del",
				Visibility:  modules.VisibilityPublic,
				Description: "Remove from the blacklist",
				Parameters:  "<text...>",
				Handler:     s.handleDel(listBlack),
			},
			{
				Name:        "whitelist_add",
				Visibility:  modules.VisibilityPublic,
				Description: "Whitelist a text or a reply",
				Parameters:  "[text...]",
				Handler:     s.handleAdd(listWhite),
			},
			{
				Name:        "whitelist_del",
				Visibility:  modules.VisibilityPublic,
				Description: "Remove from the whitelist",
				Parameters:  "<text...>",
				Handler:     s.handleDel(listWhite),
			},
			{
				Name:        "blacklist_rules",
				Visibility:  modules.VisibilityPublic,
				Description: "List both lists here",
				Handler:     s.handleRules,
			},
			{
				Name:        "blacklist_check",
				Visibility:  modules.VisibilityPublic,
				Description: "Check if a text is blacklisted",
				Parameters:  "<text...>",
				Handler:     s.handleCheck,
			},
			{
				Name:        "whitelist_rnd",
				Visibility:  modules.VisibilityPublic,
				Description: "Random whitelist entry",
				Handler:     s.handleWhitelistRandom,
			},
		},
	}
}
