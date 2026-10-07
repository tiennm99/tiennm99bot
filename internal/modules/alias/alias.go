// Package alias implements /alias, /insert, /aliases and /unalias: a shared,
// bot-wide dictionary mapping a short name to any Telegram message the bot has
// seen. A saved name is also invocable directly as /name (through the command
// fallback) and from inline mode as "@botname <prefix>".
//
// The namespace is global on purpose — a name assigned in any chat works in
// every chat, for everyone, the same way the sticker pack /addsticker writes to
// is shared. That makes the store a plain map from name to content, with no
// chat or user component in the key.
//
// Nothing is downloaded. Every media kind is kept as the file_id Telegram
// already issued, and /insert hands that same id straight back to a send call,
// so the module stores bytes for nothing but the name and a caption.
package alias

import (
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Alias is one name-to-content binding.
//
// FileID is bot-scoped: Telegram file IDs are only valid for the bot that was
// shown them, which is no constraint here because the same bot always sends
// them back. It survives restarts and re-deploys — the id refers to a file on
// Telegram's servers, not to anything this bot holds.
type Alias struct {
	Name      string `bson:"name"`      // as typed at assignment, for echoing back
	Kind      string `bson:"kind"`      // one of the kind* constants
	FileID    string `bson:"fileId"`    // the media; empty for kindText
	Text      string `bson:"text"`      // message text for kindText, else the caption
	OwnerID   int64  `bson:"ownerId"`   // who assigned it last
	CreatedAt int64  `bson:"createdAt"` // unix millis

	// Entities carries the formatting of Text — bold, italic, code, links,
	// mentions — so an alias comes back looking like what was saved.
	//
	// Storing them works because the text is re-sent byte-identical: entity
	// offsets are relative to that text, so they stay valid. They are sent as
	// entities rather than re-rendered as HTML, which avoids having to escape
	// and re-parse content the user never wrote as markup.
	Entities []models.MessageEntity `bson:"entities,omitempty"`
}

// Store is the module's typed view over its collection.
type Store = storage.DocStore[Alias]

// state holds what the handlers share.
type state struct {
	store Store
	// reg resolves whether a name is already a real command. Captured rather
	// than snapshotted: at factory time the registry holds only the modules
	// ahead of this one in MODULES order, and by the time a handler runs it is
	// complete.
	reg *modules.Registry
}

// New is the module Factory.
func New(deps modules.Deps) modules.Module {
	s := &state{store: storage.Typed[Alias](deps.Store), reg: deps.Registry}
	return modules.Module{
		// Makes a saved name invocable directly — /cheer rather than
		// /insert cheer. Registered after every command by the dispatcher, so
		// it can never shadow one.
		Fallback: &modules.CommandFallback{
			Visibility: modules.VisibilityPublic,
			Handler:    s.handleFallback,
		},
		// "@botname <prefix>" in any chat, with previews. Requires inline mode
		// enabled in BotFather; see docs/aliases.md.
		Inline: &modules.InlineQuery{
			Visibility: modules.VisibilityPublic,
			Handler:    s.handleInline,
		},
		Commands: []modules.Command{
			{
				Name:        "alias",
				Visibility:  modules.VisibilityPublic,
				Description: "Reply to a message to save it under a name",
				Parameters:  "<name>",
				Handler:     s.handleAlias,
			},
			{
				Name:        "insert",
				Visibility:  modules.VisibilityPublic,
				Description: "Send back whatever is saved under a name",
				Parameters:  "<name>",
				Handler:     s.handleInsert,
			},
			{
				Name:        "aliases",
				Visibility:  modules.VisibilityPublic,
				Description: "List every saved alias name",
				Handler:     s.handleAliases,
			},
			{
				Name:        "unalias",
				Visibility:  modules.VisibilityPublic,
				Description: "Delete a saved alias",
				Parameters:  "<name>",
				Handler:     s.handleUnalias,
			},
		},
	}
}
