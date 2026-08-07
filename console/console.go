// SPDX-License-Identifier: Apache-2.0

// Package console carries the dev edition's web console as bytes in the binary.
//
// It holds the assets and nothing else: no routing, no handlers, no knowledge of
// the server that serves it. That is the whole point of the split — cloud's
// serveFS takes an fs.FS, this package is one way to supply one, and neither has
// to know how the other works.
//
// The console is plain HTML, CSS and JavaScript that a browser runs as written.
// There is no build step and no bundler, so the source in this directory is
// exactly what ships, and it asks nothing of the network beyond its own origin —
// a developer with no internet still gets the whole console.
package console

import (
	"embed"
	"io/fs"
)

//go:embed index.html console.css console.js
var files embed.FS

// FS is the console's asset tree, rooted where index.html lives. Naming each
// file in the embed directive rather than globbing the directory keeps this
// package's Go source out of the served tree and makes the shipped set readable
// at a glance.
func FS() fs.FS { return files }
