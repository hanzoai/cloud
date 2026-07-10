// SPA handler for the hosted checkout. Serves the embedded Vite bundle at
// "/". Follows the admin/embed.go pattern:
//   - path with file extension that exists in the embed → serve with
//     long-cache immutable headers (hashed filenames from Vite)
//   - anything else → serve index.html (client-side router takes over),
//     no-cache so a deploy rolls out without stale page fragments
package checkout

import (
	"io/fs"
	"net/http"
	"strings"
)

// SPAHandler returns an http.Handler that serves the embedded checkout
// SPA. prefix is typically "" (mounted at root) or "/pay" if the SPA
// needs to live at a subpath. Unknown extension-less paths fall through
// to index.html so TanStack Router can render them.
//
// The handler is Host-agnostic — tenant branding is fetched at runtime by
// the SPA via GET /checkout/v1/tenant. This keeps the embed identical
// across all tenants and the binary itself reproducible.
func SPAHandler(prefix string) http.Handler {
	root := UISub()
	file := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, prefix)
		if path == "" {
			path = "/"
		}
		path = strings.TrimPrefix(path, "/")

		// Asset request: has a file extension and no further slash after
		// the extension. Hashed filenames from Vite → immutable cache.
		if i := strings.LastIndexByte(path, '.'); i >= 0 && !strings.Contains(path[i:], "/") {
			if _, err := fs.Stat(root, path); err == nil {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + path
				file.ServeHTTP(w, r2)
				return
			}
		}

		// SPA fallback — always the freshest index.html so deploys
		// invalidate immediately without users holding a stale shell.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Defense-in-depth browser hardening for the checkout surface:
		//   - framing: clickjacking the Square payment form would be
		//     catastrophic, so deny all.
		//   - MIME: lock so a crafted upload can't be interpreted as
		//     script.
		//   - referrer: strict origin cross-origin means the backend
		//     never sees the full URL including ?return= in logs.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		idx, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "checkout SPA not built", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(idx)
	})
}
