// Package web holds the Wordle Daily page: plain HTML, CSS and JavaScript
// with no build step, embedded into the binary.
package web

import "embed"

// FS holds index.html, app.css and app.js.
//
//go:embed index.html app.css app.js
var FS embed.FS
