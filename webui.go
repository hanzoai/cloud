// SPDX-License-Identifier: Apache-2.0

package cloud

// Headless web surface (OSS core).
//
// The private Hanzo Cloud build embeds the @hanzo/gui console (via //go:embed
// webui/dist) and serves it as an SPA at the web root, with a deep-link fallback
// to index.html. The OSS binary ships NO console: it is API-only on :8000 and ops
// on :9090. mountConsole here registers the ONE terminal catch-all — after every
// /v1 route, the /zap plane, and the health contract — that answers any unmatched
// path with a JSON 404. There is no SPA fallback: a client that expects JSON never
// receives an HTML shell.

import (
	"net/http"

	"github.com/zap-proto/zip"
)

// mountConsole registers the API-only terminal handler at the web root. It is
// called LAST in Serve — after every real route — so Fiber's in-order matching
// gives all API routes precedence and only genuinely unmatched paths reach this
// JSON 404. The private build overrides this file to serve the embedded console.
func mountConsole(app *zip.App) error {
	app.All("/*", func(c *zip.Ctx) error {
		return c.JSON(http.StatusNotFound, map[string]any{
			"error": map[string]string{
				"code":    "not_found",
				"message": "no such route",
			},
		})
	})
	return nil
}
