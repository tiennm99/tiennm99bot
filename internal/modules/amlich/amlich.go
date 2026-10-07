// Package amlich converts dates between dương lịch (the Gregorian calendar)
// and the Vietnamese âm lịch (lunisolar calendar): /amlich and /duonglich.
// Conversions are pure calendar math computed at UTC+7 — the offset the
// Vietnamese calendar is defined against.
package amlich

import (
	"github.com/tiennm99/tiennm99bot/internal/modules"
)

// New is the module Factory. The module is stateless — pure calendar math with
// no store access — so Deps is unused.
func New(modules.Deps) modules.Module {
	return modules.Module{
		Commands: []modules.Command{
			amlichCommand(),
			duonglichCommand(),
		},
	}
}
