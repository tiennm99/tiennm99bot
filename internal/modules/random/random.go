// Package random groups the commands that pick one option at random: /random
// (plain text pick), /wheelofnames (a spinning wheel MP4), /gacha (card-pack
// wish MP4), and the unlisted /genshin (Genshin-style meteor wish MP4) and
// /csgo (CS:GO-style case opening MP4). /gacha uses the optional renderer
// service; the others render in process with ffmpeg. Each falls back to a
// text reply when it cannot render.
package random

import "github.com/tiennm99/tiennm99bot/internal/modules"

// New is the module Factory. The commands keep no state, so deps is unused.
func New(_ modules.Deps) modules.Module {
	return modules.Module{
		Commands: []modules.Command{
			randomCommand(),
			wheelOfNamesCommand(),
			gachaCommand(),
			genshinCommand(),
			csgoCommand(),
		},
	}
}
