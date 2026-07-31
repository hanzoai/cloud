package crawl

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// CrawlRequest is the /v1/crawl body. One URL per call: batching would make the
// response a partial-failure envelope that every caller then has to unpack, and no
// caller has asked for more than one.
type CrawlRequest struct {
	// URL is the page to fetch. Required. It is fetched from inside the cluster,
	// so it is checked against the address guard before any connection is made.
	URL string `json:"url"`
}

// CrawlView is the /v1/crawl body.
//
// Success is a field rather than an HTTP status because "the page could not be
// fetched" is a normal outcome of asking about a URL, not a fault of the request:
// the caller sent a well-formed ask and gets a well-formed answer saying the page
// was unreachable. Reserving non-2xx for auth and malformed input keeps a caller's
// error handling honest — a 200 means the surface worked, and Success says what it
// found.
type CrawlView struct {
	// Success is true when the page was fetched and extracted. False with a 200 is
	// a normal answer: the ask was well-formed and the page was unreachable.
	Success bool `json:"success"`
	// Data is the crawled page, present only on success.
	Data *Document `json:"data,omitempty"`
	// Error says why the fetch failed — refused host, unreachable, wrong content
	// type — verbatim, so a caller debugging a crawl can tell those apart.
	Error string `json:"error,omitempty"`
}

// Document is the crawled page.
type Document struct {
	// URL is the FINAL url after redirects, not the one asked for.
	URL string `json:"url"`
	// Title is the document title, best-effort from <title> or og:title.
	Title string `json:"title,omitempty"`
	// Markdown is the readable content. Empty is a legitimate result for a page
	// that carries none.
	Markdown string `json:"markdown"`
	// Metadata is what the page declared about itself (og:*, description, …).
	Metadata map[string]any `json:"metadata,omitempty"`
}

// serviceKey is the shared key a service caller presents. It is deliberately the
// SAME credential the web-search surface takes (WEBSEARCH_API_KEY), not a second
// one: both surfaces exist for the same caller — the chat server, reaching cloud
// service-to-service with no user principal — and a second key would be a second
// thing to mint, mount and rotate, with nothing distinguishing when to use which.
//
// The name still says WEBSEARCH because that is the key that is minted and mounted
// today; renaming a live credential is its own coordinated change, and doing it
// inside this one would put a rename in the path of a fix.
func serviceKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_API_KEY")) }

// Mount registers /v1/crawl.
//
// The gate mirrors /v1/websearch/search exactly, and it is not optional here. This
// surface fetches a URL the caller chooses, from inside the cluster — an open one
// is a proxy into the private network, and the address guard in crawl.go is the
// second line of that defence, not the first. A caller is admitted with EITHER a
// validated principal (a signed-in user, already authenticated and metered) OR the
// shared service key. Neither ⇒ refused. An unset key 503s rather than defaulting
// open, so a misconfigured deploy fails closed and loudly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("crawl.Mount: nil app")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("crawl.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "crawl")

	// Bind the corpus to the ONE object seam the binary already has. A deployment
	// with no object store keeps crawling and keeps nothing — see Bind.
	Bind(deps.VFS)

	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("crawl.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}

	g := app.Group("/v1/crawl")
	// The typed-op bridge FIRST, then the gate — fiber runs middleware in
	// registration order, so the op below sees an admitted caller and the request
	// its scope is read from. Serve installs a bridge app-wide too; nesting is
	// harmless, and this is what makes the surface testable on a bare app.
	g.Use(cloud.Bridge())
	// The gate is middleware rather than a wrapper inside the handler because a
	// typed op answers ONE shape and a refusal is not that shape: a caller turned
	// away here never reaches the crawl and gets the same body it always did.
	g.Use(admit)

	// One route, registered without the trailing slash: fiber is non-strict, so
	// /v1/crawl/ reaches it too, and a second registration would mint a second
	// operation (and a second MCP tool) for the same address.
	zip.Post(zapp, "/v1/crawl", handle)

	// No "archive" field: it used to log deps.VFS != nil, which is ALWAYS true —
	// deps.VFS is guaranteed non-nil by contract (R-7, so consumers never
	// nil-deref) and is a fail-closed STUB when no object store is configured. So
	// the field reported an archive whether or not one byte could ever be stored,
	// and read as reassurance exactly when the corpus was off. Whether the store
	// works is not knowable here without doing I/O on the boot path, which is its
	// own hazard; a field that cannot be false should not be printed at all.
	logger.Info("crawl surface mounted (native in-process fetch + extract; no external crawler)")
	return nil
}

// admit lets a caller through on EITHER a validated principal (a signed-in user,
// already authenticated and metered) OR the shared service key, presented as
// either X-API-Key or a Bearer. Both header spellings are accepted because the
// two clients that reach this surface already differ on that point and neither is
// wrong; requiring one would break a working caller to no benefit.
func admit(c *zip.Ctx) error {
	if principal.Validated(c) {
		return c.Next()
	}
	want := serviceKey()
	if want == "" {
		return c.JSON(http.StatusServiceUnavailable, CrawlView{Error: "crawl not configured"})
	}
	got := strings.TrimSpace(c.Header("X-API-Key"))
	if got == "" {
		got = strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	}
	// Constant-time: a byte-at-a-time comparison leaks the key's prefix to a
	// caller willing to time enough requests.
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return c.JSON(http.StatusUnauthorized, CrawlView{Error: "invalid api key"})
	}
	return c.Next()
}

// scope reads the caller's corpus scope from the VERIFIED principal.
//
// A service caller (the chat server, holding the shared key) has no user
// principal and therefore no org — its pages land in the shared "_" prefix that
// seg() produces for an empty segment. That is deliberate: a service-wide corpus
// is the honest home for pages fetched on nobody's behalf, and inventing an org
// for it would file them under a tenant that did not ask.
//
// It reads the REQUEST, never the body: the scope selects the corpus prefix, and
// a caller who could name it in a field could name another tenant's.
func scope(ctx context.Context) Scope {
	c, ok := cloud.Request(ctx)
	if !ok {
		return Scope{}
	}
	org, _ := principal.Org(c)
	return Scope{Org: org, Project: principal.Project(c)}
}

// handle fetches ONE url and returns it as markdown plus the metadata the page
// declared about itself, archiving the result under the caller's own corpus. A
// page that cannot be fetched is a 200 with success:false and the reason — the
// ask was well-formed, the page was not there — so a non-2xx from this route
// always means the request itself was refused.
//
// Example: {"url": "https://hanzo.ai/about"}
// Response: {"success": true, "data": {"url": "https://hanzo.ai/about", "title": "About Hanzo", "markdown": "# About Hanzo\n…"}}
func handle(ctx context.Context, in *CrawlRequest) (*CrawlView, error) {
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return nil, zip.ErrBadRequest("missing url")
	}

	page, err := Read(ctx, scope(ctx), url)
	if err != nil {
		// 200 with Success:false — see the note on CrawlView. The message is the
		// error verbatim: a caller debugging a failed crawl needs to know whether the
		// host was refused, unreachable, or served the wrong type.
		return &CrawlView{Error: err.Error()}, nil
	}
	return &CrawlView{
		Success: true,
		Data: &Document{
			URL:      page.URL,
			Title:    page.Title,
			Markdown: page.Markdown,
			Metadata: page.Metadata,
		},
	}, nil
}
