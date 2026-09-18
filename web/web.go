// Package web embeds the dashboard so the whole product ships as a single
// binary. One file to scp to the Pi, nothing to install on it.
package web

import "embed"

//go:embed all:static
var FS embed.FS
