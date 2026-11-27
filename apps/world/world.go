package world

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
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
	ai  infer.Client
	kms secret.Store
}

var mounted *service

// Mount registers the world surface on app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("world: nil app")
	}
	log := luxlog.Default().New("subsystem", "world")
	if cloud.DataDir() == "" {
		return fmt.Errorf("world.Use:  empty DataDir")
	}
	store, err := openPipelineStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("world.Use:  open pipeline store: %w", err)
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
		Transport: feeds,
		Timeout:   httpTimeout,
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

	// UNIFIED PAYWALL (server-side enforcement). To hold this surface to the
	// caller's plan, ask entitlement in each operation's PREAMBLE — the one place
	// every seam passes through. A group's middleware reaches the REST route only.
	// DEFERRED — DO NOT ENABLE YET: the "world" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so CheckEntitlement returns Active:false for
	// EVERY org and enforcing now would 402 all users. Flip on once the catalog
	// licenses "world" to a tier, and confirm /v1/world/news may 403 an unvalidated
	// caller. See apps/entitlement.
	// A typed op receives only a context, so the facts its signature drops — the
	// validated org, and the request the PROJECT claim and the ?project
	// cross-check ride on — reach it by being parked there. cloud.Bridge parks
	// them, and the composer owns that install — the fused host at its root, a
	// plugin program in its constructor — so this package only reads them.

	// The index IS the prefix: an empty leaf declares /v1/world, not /v1/world/.
	g := app.Group("/v1/world")
	g.Get("", s.index)
	g.Get("/news", s.news)
	g.Get("/pipeline", s.pipeline)
	g.Put("/pipeline", s.setPipeline)
	g.Get("/limits", s.limits)

	// GET /v1/world/stream stays an untyped handler, deliberately: it is Server-Sent
	// Events. A typed op returns ONE value that zip marshals and writes as the whole
	// response, and this route holds the connection open writing frame after frame
	// through c.SendStreamWriter (stream.go) until the client goes away. There is no
	// Out that can express a stream, so typing it would turn a live feed into a
	// single JSON object. Its document prose is declared instead by stream.go's
	// init (openapi.Describe), so the SDKs and the spec-derived CLI still carry it.
	g.Raw(http.MethodGet, "/stream", s.stream)

	log.Info("world surface mounted", "brand", cloud.Brand(),
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
