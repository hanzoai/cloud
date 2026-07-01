//go:build cloud

// Package console serves the Hanzo console2 single-page app FROM INSIDE the
// unified cloud binary — the "one binary" endgame (Hanzo V8: Open Edition).
//
// The whole frontend ships as static assets go:embed'd into `dist/` (produced by
// `console2`'s static export). This subsystem mounts them at "/" with SPA
// fallback: a request for a real embedded file serves that file; anything else
// serves index.html so the client-side router takes over. It registers LAST
// (order 990) so every /v1/* API route and every other subsystem wins first —
// the SPA is the catch-all of last resort.
//
// Result: ONE Go binary is the entire cloud — TLS/edge + gateway + every backend
// subsystem + the console UI. One load balancer, N stateless replicas, per-tenant
// SQLite → SeaweedFS/S3. Any dev runs the binary and has the whole cloud; any node
// can join. No separate ingress/gateway/console2/cloud-api pods to operate.
package console

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
)

// dist holds the built console2 SPA. The committed placeholder keeps the build
// green until `console2`'s static export is wired into CI to overwrite it; the
// real bundle is dropped here at image-build time.
//
//go:embed dist
var dist embed.FS

// order 990 — after every subsystem and every /v1/* route; the SPA is the
// last-resort catch-all for browser (non-API) paths only.
const order = 990

func init() {
	cloud.Register("console", order, func(app any, deps cloud.Deps) error {
		zapp, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("console.Mount: expected *zip.App, got %T", app)
		}
		return Mount(zapp, deps)
	})
}

// Mount serves the embedded SPA at "/" with SPA fallback.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("console.Mount: nil zip.App")
	}
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return fmt.Errorf("console.Mount: embed sub: %w", err)
	}
	app.Mount("/", spaHandler(sub))
	if deps.Logger != nil {
		deps.Logger.New("subsystem", "console").
			Info("console SPA mounted (embedded)", "brand", deps.Brand, "path", "/")
	}
	return nil
}

// spaHandler serves static files from fsys, falling back to index.html for any
// path that is not an embedded file (client-side routing). API namespaces are
// never reached here — they are matched by earlier-order routes — but we still
// refuse to serve index.html for /v1, /zap and /_ so a stray miss is an honest
// 404 rather than an HTML body that breaks JSON clients.
func spaHandler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	index, _ := fs.ReadFile(fsys, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w, r, index)
			return
		}
		// Never HTML-fallback API/edge namespaces — honest 404 for JSON clients.
		if isAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if f, err := fsys.Open(p); err == nil {
			_ = f.Close()
			files.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r, index) // SPA route → client router
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	if index == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

func isAPIPath(p string) bool {
	for _, pre := range []string{"/v1/", "/zap", "/_/", "/healthz", "/readyz"} {
		if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}
