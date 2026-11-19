// Package web embeds static assets served by the Live Demo endpoint (/demo).
package web

import "embed"

// DemoAssets holds embedded static HTML assets for the /demo route.
//
//go:embed demo.html
var DemoAssets embed.FS
