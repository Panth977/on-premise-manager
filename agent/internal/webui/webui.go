// Package webui embeds the agent GUI build (agent/gui → npm run build writes dist/). T3 keeps a placeholder
// dist/index.html so the agent builds without the GUI.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the GUI files rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
