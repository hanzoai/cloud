// Copyright © 2026 Hanzo AI. MIT License.
//
// Package admin is the legacy mount for /admin/*. It now delegates to
// commerce/ui (the canonical Hanzo GUI v7 SPA embed) so there is one
// and only one bundled SPA. Once admin.commerce.hanzo.ai DNS flips to
// /_/commerce/ui/ this package can be deleted wholesale.
package admin

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/ui"
)

// Handler returns an http.Handler that serves the embedded admin SPA
// at the given prefix (typically "/admin"). It strips the prefix and
// forwards to the commerce/ui handler — same bundle, same SPA.
func Handler(prefix string) http.Handler {
	inner := ui.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, prefix)
		if path == "" {
			path = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = path
		inner.ServeHTTP(w, r2)
	})
}
