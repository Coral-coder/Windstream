// Package web embeds the browser client.
package web

import "embed"

// Files holds the static client assets.
//
//go:embed index.html app.js style.css favicon.svg
var Files embed.FS

var names = []string{"index.html", "app.js", "style.css", "favicon.svg"}

// Names lists the servable assets.
func Names() []string { return names }
