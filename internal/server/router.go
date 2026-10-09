// Package server is the bot's HTTP surface: a health route for the container
// monitor plus any routes modules contribute (the noitu game page and its JSON
// API), all wrapped in structured request logging. Telegram updates and crons
// never arrive over HTTP.
//
// The package does not import internal/modules: cmd/server converts the
// registry's routes into Route values, which keeps this package a leaf.
package server

import "net/http"

// Route is one extra handler mounted on the server's mux. Pattern is a
// net/http ServeMux pattern.
type Route struct {
	Pattern string
	Handler http.Handler
}

// New builds the application's HTTP handler:
//
//	GET /   → health (Coolify container monitor)
//	routes  → module handlers, e.g. /games/noitu/ when the game is enabled
//
// There is no /webhook route: Telegram updates arrive via long polling
// (cmd/server runs b.Start). There is no /cron route either: crons fire from
// the in-process scheduler (internal/cron). Without module routes the bot
// needs no public inbound ingress; a module route is reachable from outside
// only when the deployment attaches a public domain to this port.
//
// Anything else is 404. All routes pass through LogRequests so every request
// emits a structured `req` log line; that line carries the path only, never
// the query string.
func New(routes ...Route) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", HealthHandler())
	for _, r := range routes {
		mux.Handle(r.Pattern, r.Handler)
	}
	return LogRequests(mux)
}
