// SPDX-License-Identifier: Apache-2.0

package cloud

// Web console surface (OSS core).
//
// The dev edition ships a console of its own: a handful of static files under
// console/, embedded in the binary, with no build step and no request to any
// other origin — so the local binary is usable from a browser the moment it
// starts, offline, with nothing installed.
//
// serveFS is the ONE serving path and it takes an fs.FS, so where the bytes come
// from and how they are served stay separate: an edition that embeds a different
// console changes only the provider it passes in.
//
// Ordering is what makes a catch-all safe. Serve registers this LAST — after
// every /v1 route, the /zap plane, and the health contract — so Fiber's in-order
// matching gives every real route precedence and only unmatched paths arrive
// here. Of those, a path under an API plane is a real miss and still answers the
// JSON 404 it always did: a client that expects JSON never receives an HTML
// shell. The rest is the browser, so an asset that exists is served with its own
// content type and everything else falls back to index.html, which is what makes
// a client-side route deep-linkable.

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/hanzoai/cloud/console"
	"github.com/zap-proto/zip"
)

// apiPlanes are the planes this process serves to machines rather than to a
// browser. Reaching the catch-all under one of them means nothing matched, which
// is an API miss and must be answered in the API's own language.
var apiPlanes = []string{"/v1", "/zap"}

// mountConsole registers the embedded console at the web root. It is called LAST
// in Serve — after every real route — so all API routes take precedence over it.
func mountConsole(app *zip.App) error {
	return serveFS(app, console.FS())
}

// serveFS registers the terminal catch-all that serves fsys as a single-page app.
//
// index.html is read once, at mount: it answers every deep link, so a build that
// lost it is broken everywhere and that belongs in the boot error rather than in
// a 404 an hour later.
func serveFS(app *zip.App, fsys fs.FS) error {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return fmt.Errorf("console index: %w", err)
	}
	app.All("/*", func(c *zip.Ctx) error {
		p := c.Path()
		if isAPIPath(p) {
			return c.JSON(http.StatusNotFound, map[string]any{
				"error": map[string]string{
					"code":    "not_found",
					"message": "no such route",
				},
			})
		}
		// Clean folds any ".." away before the lookup; fs.ReadFile would refuse
		// such a name anyway, so an escape attempt just lands on the shell.
		if b, err := fs.ReadFile(fsys, strings.TrimPrefix(path.Clean(p), "/")); err == nil {
			c.SetHeader("Content-Type", contentType(p))
			return c.Bytes(http.StatusOK, b)
		}
		c.SetHeader("Content-Type", "text/html; charset=utf-8")
		return c.Bytes(http.StatusOK, index)
	})
	return nil
}

// isAPIPath reports whether p addresses an API plane rather than the console.
func isAPIPath(p string) bool {
	for _, plane := range apiPlanes {
		if p == plane || strings.HasPrefix(p, plane+"/") {
			return true
		}
	}
	return false
}

// contentType answers from the extension. A browser drops a stylesheet or a
// script served as the wrong type, so guessing is not optional here; an unknown
// extension gets the neutral type, which browsers download rather than execute.
func contentType(p string) string {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	return "application/octet-stream"
}
