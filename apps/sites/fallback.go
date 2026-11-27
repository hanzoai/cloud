package sites

// fallback.go — the deep links a site declares.
//
// A static export holds one file per page it knew at build time. A single-page
// app's routes are not all known then — /chat/<id>, /dev/runs/<id> — so a reload
// or a shared link on one is a 404 here, while the same route navigated to in
// the app renders. `_fallback.json` at the bundle's root declares which path
// prefixes are routes of that kind, and which page answers them:
//
//	{"/chat": "chat.html", "/dev": "dev.html"}
//
// A page-shaped path (the root, or no extension) that no file answers is
// answered 200 by the page of the LONGEST declared prefix it sits under, bounded
// by segment — /chat answers /chat and /chat/abc, never /chatter — and the app
// reads its own location to render the route. A path under no prefix stays the
// 404 it was. A path with an extension is never answered by a page, so a missing
// chunk is still a 404 its loader can see rather than HTML parsed as script. "/"
// is a prefix like any other; it declares the whole site one app.
//
// OPT-IN, AND IT FAILS TO NOTHING. A bundle without the file is served exactly as
// before. A file that is not a table of prefixes to .html pages — malformed, too
// large, too many rows, two prefixes that clean to one — declares nothing, and
// every path is what it was without it. Prefix and page are cleaned by the same
// resolveKey a request path is, and the page is fetched under the site's own
// prefix like every other key (objectKey), so a table can name nothing but the
// site's own files.

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"strings"
)

const (
	// fallbackFile is the table's key at the bundle's root.
	fallbackFile = "_fallback.json"
	// maxFallbackBytes and maxFallbackRoutes bound what one miss reads and parses.
	maxFallbackBytes  = 64 << 10
	maxFallbackRoutes = 256
)

// routes is a site's declared table: a cleaned prefix ("" for the root) to the
// cleaned key of the page that answers it.
type routes map[string]string

// page is the page declared for rel: the longest prefix rel sits under, by whole
// segment. A nil table declares nothing.
func (r routes) page(rel string) (string, bool) {
	if len(r) == 0 {
		return "", false
	}
	for p := rel; ; {
		if page, ok := r[p]; ok {
			return page, true
		}
		if p == "" {
			return "", false
		}
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			p = p[:i]
		} else {
			p = ""
		}
	}
}

// parseRoutes reads a table, or nil for anything that is not one.
func parseRoutes(b []byte) routes {
	var raw map[string]string
	if json.Unmarshal(b, &raw) != nil || len(raw) == 0 || len(raw) > maxFallbackRoutes {
		return nil
	}
	out := make(routes, len(raw))
	for prefix, page := range raw {
		p, key := resolveKey(prefix), resolveKey(page)
		if _, dup := out[p]; dup {
			return nil
		}
		if ext := path.Ext(key); ext != ".html" && ext != ".htm" {
			return nil
		}
		out[p] = key
	}
	return out
}

// declared reads the site's table. A site without one declares nothing; an error
// is the store failing to answer, which the caller reports as such.
func (s *Server) declared(ctx context.Context, site Site) (routes, error) {
	body, info, found, err := s.open(ctx, site.Bucket, objectKey(site.Prefix, fallbackFile))
	if err != nil || !found {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	if info.Size > maxFallbackBytes {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(body, maxFallbackBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFallbackBytes {
		return nil, nil
	}
	return parseRoutes(b), nil
}
