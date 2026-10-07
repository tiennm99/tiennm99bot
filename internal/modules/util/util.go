// Package util implements /info, /help, /stickerid — the framework-validating
// "always on" module. /help is a pure renderer over the registry; the other
// two are debug helpers.
package util

import (
	"github.com/tiennm99/tiennm99bot/internal/modules"
)

// New is the module Factory. /help closes over deps.Registry so it renders
// the fully built registry at call time; the other handlers need no Deps.
func New(deps modules.Deps) modules.Module {
	return modules.Module{
		Commands: []modules.Command{
			infoCommand(),
			helpCommand(deps.Registry),
			stickerIDCommand(),
		},
	}
}
