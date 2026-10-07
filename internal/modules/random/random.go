// Package random groups the commands that pick one option at random: /random
// (plain text pick), /wheelofnames (wheel GIF when a renderer is configured),
// /gacha (card-pack wish MP4), and the unlisted /genshin (Genshin-style meteor
// wish MP4). The animated commands share the optional renderer service and
// fall back to a text reply without it.
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
		},
	}
}
