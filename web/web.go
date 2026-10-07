// Package web embeds the browser client and the local dashboard.
package web

import "embed"

// Files holds the streaming client assets.
//
//go:embed index.html app.js style.css favicon.svg logo.svg icon.png
var Files embed.FS

// Admin holds the local dashboard (served on loopback only).
//
//go:embed admin
var Admin embed.FS

var names = []string{"index.html", "app.js", "style.css", "favicon.svg", "logo.svg", "icon.png"}

// Names lists the servable streaming client assets.
func Names() []string { return names }
