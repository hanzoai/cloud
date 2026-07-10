// Package world mounts the Hanzo Cloud "World" news data plane: a per-org,
// per-project intelligence feed that normalizes GDELT + host-allowlisted RSS/Atom
// into one NewsItem stream, applies the project's keyword/region/source filter,
// and serves it over REST + SSE. It is the Go backend for the World monitor
// frontend (hanzoai/world), replacing that app's Vercel edge functions
// (api/gdelt-doc.js, api/rss-proxy.js) with an org-scoped, in-binary subsystem.
//
// Surface (all org/project-scoped; /v1 only):
//
//	GET /v1/world/news       merged, filtered, freshest-first feed -> {items:[…]}
//	GET /v1/world/pipeline   per-project pipeline config (read)    -> {…}
//	PUT /v1/world/pipeline   per-project pipeline config (write)   -> {…}
//	GET /v1/world/stream     SSE live refresh (ZAP-native)         -> event: news
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
// Order 142 binds /v1/world/* ahead of the AI /v1/* catch-all (150).
package world

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

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

	// Reserved composition seams, wired now so the root is complete:
	//   ai  — the news-summarization slice (groq/openrouter-summarize parity).
	//   kms — per-feed API-key custody for authenticated upstreams (never plaintext).
	ai  cloud.AIClient
	kms cloud.KMSClient
}

var mounted *service

// Mount registers the world surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("world.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("world.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "world")
	if deps.DataDir == "" {
		return fmt.Errorf("world.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("world.Mount: data dir: %w", err)
	}
	store, err := openPipelineStore(filepath.Join(deps.DataDir, "world.db"))
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

	app.Get("/v1/world/news", s.getNews)
	app.Get("/v1/world/pipeline", s.getPipeline)
	app.Put("/v1/world/pipeline", s.putPipeline)
	app.Get("/v1/world/stream", s.stream)
	app.Get("/v1/world/limits", s.getLimits)

	log.Info("world surface mounted", "brand", deps.Brand,
		"ai", s.ai != nil, "kms", s.kms != nil, "allowlisted_hosts", len(s.rssAllow))
	return nil
}

func init() {
	cloud.RegisterWithShutdown("world", 142, cloud.Typed(Mount), func(context.Context) error {
		return Shutdown()
	})
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
