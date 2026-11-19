// Package web holds the dashboard, compiled into the binary so it works from
// any working directory.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the dashboard files rooted at the static directory.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // only possible if the embed directive above is wrong
	}
	return sub
}
