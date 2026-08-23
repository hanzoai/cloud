// Package world is a live news feed filtered to what your project cares about.
//
// A per-org, per-project intelligence feed that normalizes GDELT +
// host-allowlisted RSS/Atom into one NewsItem stream, applies the project's
// keyword/region/source filter, and serves it over REST + SSE. It is the Go
// backend for the World monitor frontend (hanzoai/world), replacing that app's
// Vercel edge functions (api/gdelt-doc.js, api/rss-proxy.js) with an
// org-scoped, in-binary subsystem.
//
// Surface (/v1 only; org/project-scoped except where marked public):
//
//	GET /v1/world            front door: the product and its wires    -> {…} PUBLIC
//	GET /v1/world/news       merged, filtered, freshest-first feed -> {items:[…]}
//	GET /v1/world/pipeline   per-project pipeline config (read)    -> {…}
//	PUT /v1/world/pipeline   per-project pipeline config (write)   -> {…}
//	GET /v1/world/limits     a World plan's rate/alert/model gates -> {…} PUBLIC
//	GET /v1/world/stream     SSE live refresh (ZAP-native)         -> event: news
//
// Two more wires answer under this prefix and are NOT served by this binary:
// /v1/world/mcp (Model Context Protocol) and /v1/world/zap (ZAP over WebSocket)
// are carved off the cloud catch-all by the ingress and answered by world-gw. The
// generated document cannot declare them — openapi.Describe renders prose only
// for a route this router actually serves, which is what stops the document
// claiming an operation nothing answers — so GET /v1/world names them instead.
// That op is the only place in the product's own surface those addresses appear.
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request. The (org, project)
// tuple is principal.Org + principal.Project (the values SanitizeIdentity minted
// from the VALIDATED bearer, HIP-0026) — never a query param, body, or client
// header. Every store statement carries `WHERE org=? AND project=?`; the SSE bus
// filters on org and the stream loop drops other projects. A request with no
// validated principal is a 403.
//
// SECURITY. The RSS fetcher is an SSRF boundary: a feed URL's host must be in the
// ported rss-proxy.js allowlist (allowlist.go), enforced at BOTH the PUT write
// boundary and at fetch time, including on redirect targets (client CheckRedirect).
//
// The host routes by first matching prefix in manifest.Apps order, and world's row
// (/v1/world) precedes the ai row that answers the bare /v1 remainder.
package world

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/world openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	feedCacheTTL = 10 * time.Minute
	httpTimeout  = 15 * time.Second
	maxRedirects = 5
)

// service is the composition root for the world data plane. It owns the pipeline
// store, the SSE bus, the upstream TTL cache and HTTP client, and the RSS host
// allowlist (a field so tests can point it at a local server).
type service struct {
	store     *PipelineStore
	bus       *bus
	cache     *feedCache
	http      *http.Client
	rssAllow  map[string]struct{}
	gdeltBase string
	log       luxlog.Logger

	// Reserved composition clients, wired now so the root is complete:
	//   ai  — the news-summarization slice (groq/openrouter-summarize parity).
	//   kms — per-feed API-key custody for authenticated upstreams (never plaintext).
	ai  cloud.AIClient
	kms cloud.KMSClient
}

var mounted *service

// Mount registers the world surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("world.Mount: nil app")
	}
	log := luxlog.Default().New("subsystem", "world")
	if deps.DataDir == "" {
		return fmt.Errorf("world.Mount: empty DataDir")
	}
	store, err := openPipelineStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("world.Mount: open pipeline store: %w", err)
	}

	s := &service{
		store:     store,
		bus:       newBus(),
		cache:     newFeedCache(feedCacheTTL),
		rssAllow:  allowedRSSHosts,
		gdeltBase: gdeltDefaultBase,
		log:       log,
		ai:        deps.AI,
		kms:       deps.KMS,
	}
	// The HTTP client re-validates EVERY redirect hop against the same host
	// allowlist (plus the fixed GDELT host), so a feed cannot 302 the pod onto an
	// arbitrary internal host — closing the redirect-SSRF hole rss-proxy.js guards
	// manually.
	s.http = &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			host := strings.ToLower(req.URL.Hostname())
			if s.rssAllowed(host) || host == gdeltHost {
				return nil
			}
			return fmt.Errorf("redirect to disallowed host %q", host)
		},
	}
	mounted = s

	// UNIFIED PAYWALL (server-side enforcement). To gate this group behind the
	// caller's plan, prepend the middleware to the group:
	//   g := app.Group("/v1/world", entitlement.RequireProduct(deps.Commerce, "world"))
	// DEFERRED — DO NOT ENABLE YET: the "world" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so CheckEntitlement returns Active:false for
	// EVERY org and enforcing now would 402 all users. Flip on once the catalog
	// licenses "world" to a tier (and confirm /v1/world/news may 403 unvalidated —
	// RequireProduct refuses anonymous callers). See clients/entitlements.
	// A typed op receives only a context, so the facts its signature drops — the
	// validated org, and the request the PROJECT claim and the ?project
	// cross-check ride on — reach it by being parked there. cloud.Bridge parks
	// them, and the composer owns that install — the fused host at its root, a
	// plugin program in its constructor — so this package only reads them.

	// The ops are declared on the APP with absolute paths rather than on a group,
	// because one of them IS the prefix: GET /v1/world has no leaf, and a group
	// cannot express it — zip.Get(g, "") composes to "/v1/world/", a different
	// route. A group for four plus an app-level exception for one is two idioms;
	// one absolute address per op is one, and it is the form apps/pricing and
	// apps/plan already use for exactly this reason.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("world.Mount: router is not backed by a zip app — typed ops have no registry to declare into")
	}
	zip.Get(zapp, "/v1/world", s.index)
	zip.Get(zapp, "/v1/world/news", s.news)
	zip.Get(zapp, "/v1/world/pipeline", s.pipeline)
	zip.Put(zapp, "/v1/world/pipeline", s.setPipeline)
	zip.Get(zapp, "/v1/world/limits", s.limits)

	// GET /v1/world/stream stays an untyped handler, deliberately: it is Server-Sent
	// Events. A typed op returns ONE value that zip marshals and writes as the whole
	// response, and this route holds the connection open writing frame after frame
	// through c.SendStreamWriter (stream.go) until the client goes away. There is no
	// Out that can express a stream, so typing it would turn a live feed into a
	// single JSON object. Its document prose is declared instead by stream.go's
	// init (openapi.Describe), so the SDKs and the spec-derived CLI still carry it.
	zapp.Get("/v1/world/stream", s.stream)

	log.Info("world surface mounted", "brand", deps.Brand,
		"ai", s.ai != nil, "kms", s.kms != nil, "allowlisted_hosts", len(s.rssAllow))
	return nil
}

// Shutdown closes the SSE bus (unblocking every open stream) and the pipeline
// store. Idempotent — safe to call when nothing is mounted.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	if mounted.bus != nil {
		mounted.bus.close()
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}
