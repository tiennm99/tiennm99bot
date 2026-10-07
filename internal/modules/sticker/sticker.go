// Package sticker implements /addsticker: it appends a replied sticker, image,
// video or GIF to one shared, env-configured Telegram sticker pack.
//
// One command, and no storage at all. AddStickerToSet takes the *set owner's*
// user ID rather than the caller's, so there is nothing per-user to record, key
// or lock — the module holds only the conversion pipeline that turns whatever
// was replied to into something Telegram will accept.
package sticker

import (
	"github.com/tiennm99/miti99bot/internal/modules"
)

// CollectionName is this module's registry key. Exported so main can hand the
// same handle to Build and to any startup task, matching lol/coin/stock.
const CollectionName = "sticker"

// New is the module Factory. Only Deps.BotUsername is used: the command reads
// its pack from the environment and keeps no state, so the collection this
// module is handed goes unused.
func New(deps modules.Deps) modules.Module {
	return modules.Module{
		Commands: []modules.Command{
			addStickerCommand(deps.BotUsername),
		},
	}
}
