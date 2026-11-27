// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/sdk/env"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	// Every hosted Base offers the CRM: an org admin installs it with
	// POST /v1/base/modules/crm, a sibling with base_import.
	_ "github.com/hanzoai/base/modules/crm"
	"github.com/hanzoai/base/plugins/waitlist"
	"github.com/zap-proto/zip"
)

// embedEnv gates activation. Truthy (1/true/yes) embeds the base app; anything
// else (incl. unset) leaves the subsystem a health-only no-op.
const embedEnv = "CLOUD_BASE_EMBED"

// apiPrefix is the ONE mount prefix for the per-org Base engine. Base reads it
// from BASE_API_PREFIX at router build, so setting it here makes every per-org
// app serve its collections/records API natively under /v1/base/*.
const apiPrefix = "/v1/base"

// platformSeg is the platform waitlist app's data subdir. The leading '_' is
// OUTSIDE the base32 [a-z2-7] TenantSegment alphabet, so it can never collide
// with any org's per-org directory.
const platformSeg = "_platform"

func embedEnabled() bool {
	switch strings.ToLower(env.Or(embedEnv, "")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// subsystem is the mounted state, retained package-globally so Shutdown can
// release the platform app + every pooled per-org app.
type subsystem struct {
	pool     *pool
	platform *baseapp.Base
	stopBus  context.CancelFunc
}

// mounted is the active subsystem (nil until Mount succeeds with the embed on).
var mounted *subsystem

// Mount wires the base subsystem onto app per HIP-0106: the always-on health
// route, then — behind CLOUD_BASE_EMBED — the public waitlist lane and the
// authenticated per-org hosting lane.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("base: nil app")
	}
	log := luxlog.Default().New("subsystem", "base")

	hosting := app.Group("/v1/base")
	// Native /v1/base/health — always answers (OwnsHealth), no auth, BEFORE the
	// embed gate AND before the /v1/base/* wildcard, so the liveness probe never
	// depends on CLOUD_BASE_EMBED and the wildcard never shadows it (same pattern
	// as clients/plan + clients/pricing).
	hosting.Get("/health", health)

	// A typed op receives only a context, so the validated identity has to be
	// parked there — and it is parked by the bridge, ahead of the leaves. It is
	// installed here rather than relied on from Serve because this package's own
	// tests do not run Serve, which is how an op that works in production reads
	// no identity under test (or the reverse, which is worse).
	//
	// It goes on AFTER the health leaf, so the probe stays unauthenticated, and
	// the waitlist lane installs its own for the prefix it holds.
	hosting.Use(cloud.Bridge())

	// Which Bases the caller can reach — ahead of the embed gate for the same
	// reason /health is, and at a literal leaf so it wins the address against the
	// /v1/base/* wildcard below (most-specific-first). Behind that wildcard this
	// address reaches ONE org's engine, which has no route for it and answers
	// not-found; the space read that as an account with no Bases in it.
	//
	// Registering it here also keeps it in the DOCUMENT: describe projects the
	// live router, so a route behind a feature gate is absent from the SDKs, the
	// CLI and the tool list — which is why the hosting lane below publishes as
	// nothing at all. The ops read the mounted subsystem at request time, so with
	// no embed they answer honestly rather than needing the gate to exist.
	ops := baseOps{}
	hosting.Get("/bases", ops.list)
	hosting.Get("/bases/:org", ops.read)

	// The door other apps have into an org's Base, published whatever the embed
	// says for the same reason: each op answers 503 rather than being absent.
	exposePlane()

	// The org's Base as typed GraphQL, on zip's engine, at a literal leaf so it
	// wins the address against the wildcard below. Published whatever the embed
	// says, like the ops above; graphGate answers a caller with no org, or a
	// deployment with no Bases, before zip would call either a schema failure.
	hosting.Group("/graphql", graphGate).MountGraphFor("", orgSchema, orgResolve)

	// There is no second endpoint. /v1/collections used to reverse-proxy this same
	// concept to a separate Base deployment, and its stated reason for existing was
	// the cross-instance `tenants` registry that deployment held — one row per Base,
	// each on its own subdomain. That registry cannot work: an authenticated request
	// is scoped to the caller's org and opens THAT org's Base, which has no `tenants`
	// collection, so the registry answered only anonymously and only ever held zero
	// rows. What it left behind was two engines writing the same concept to two
	// disks, where a record created through one was invisible through the other.
	//
	// An org HAS a Base, and it is the one below.

	if !embedEnabled() {
		log.Info("base embed disabled; /v1/base + /v1/waitlist off (set CLOUD_BASE_EMBED=1 to enable)")
		return nil
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("base.Use:  empty DataDir")
	}

	// More than one writer needs the rows where every pod reaches them: a per-pod
	// file is a per-pod answer, and an org would write a record through one pod and
	// not read it back through the next.
	if deps.Peers && locus.DSN() == "" {
		return fmt.Errorf("base.Use:  this deployment runs more than one writer and %s names no hanzo-sql: each pod would serve every org from its own file and they would never converge", locus.Env)
	}

	// Pin Base's router prefix to /v1/base (its documented multi-app knob) unless
	// the operator already set one. Read once, applied to EVERY app built below.
	if strings.TrimRight(env.Or("BASE_API_PREFIX", ""), "/") == "" {
		if err := os.Setenv("BASE_API_PREFIX", apiPrefix); err != nil {
			return fmt.Errorf("base.Use:  set BASE_API_PREFIX: %w", err)
		}
	}

	// The cloud binary owns the ZAP transport; embedded Base apps must NOT each
	// start their own node (they'd all contend for the same :9999 bind). Disable
	// it for every app built below unless the operator explicitly set the knob.
	if _, set := os.LookupEnv("ZAP_DISABLED"); !set {
		if err := os.Setenv("ZAP_DISABLED", "true"); err != nil {
			return fmt.Errorf("base.Use:  set ZAP_DISABLED: %w", err)
		}
	}

	root := filepath.Join(cloud.DataDir(), "base")

	// LANE 1 — platform waitlist app (public /v1/waitlist/*). Raw because it
	// relays the embedded Base engine's own bytes verbatim — the routes, shapes
	// and refusals are that program's, not this package's to declare.
	platformApp, h, err := newPlatformApp(filepath.Join(root, platformSeg))
	if err != nil {
		return fmt.Errorf("base.Use:  platform waitlist app: %w", err)
	}
	// The second prefix this app holds, opened here rather than beside the hosting
	// group: with the embed off there are no routes beneath it, and a group that
	// declares middleware over nothing refuses to compose.
	waitlist := app.Group("/v1/waitlist")
	waitlist.Use(cloud.Bridge())
	waitlist.Raw(zip.MethodAll, "", h)
	waitlist.Raw(zip.MethodAll, "/*", h)

	// LANE 2 — per-org Base hosting (authenticated /v1/base/*). Raw for the same
	// reason as the waitlist lane: each request is answered by the org's own Base
	// verbatim, so there is no shape here for a typed op to state.
	// ONE registration, because the org's Base now answers everything it serves
	// under one root. Base's table wire moved beneath the mount prefix, so it is
	// /v1/base/rest/{collection} and arrives here with the collections API rather
	// than needing its own route at /rest/v1 outside it. Both are the same engine
	// over the same rows — the table wire is a rendering of the SAME read, running
	// the same recordsList with the same list rule and rate limit, differing only
	// in what it writes back (a bare array with the count in Content-Range).
	//
	// So the org still comes from the validated principal, for both, because there
	// is one handler and one prefix.
	p := newPool(root, deps, log)
	busCtx, stopBus := context.WithCancel(context.Background())
	p.changes = newChanges(busCtx, log, capture)
	// The "sweep" policy moves the Bases still files in the background.
	go p.sweep(busCtx)

	hosting.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			// A realtime stream is opened by EventSource, which sends no
			// header, so it carries a grant instead of a credential. The grant
			// was minted by the org's own Base, and names it.
			if org, ok = streamOrg(c); !ok {
				return principal.Refused(c)
			}
		}
		return p.serve(org, c)
	})

	mounted = &subsystem{pool: p, platform: platformApp, stopBus: stopBus}

	// Host-as-project-ref (HIP-0014, gated by CLOUD_BASE_PUBLIC_HOST, default OFF):
	// let a published site host serve /v1/base, /v1/realtime and /_/ scoped to the
	// org its SUBDOMAIN resolves to — so an anon page can reach its own Base, authz
	// by Base's collection rules. The org comes from the resolved site, never the
	// caller. Absent the flag, site hosts serve only static files (unchanged).
	if publicHostEnabled() {
		sites.SetBaseHostHandler(p.serve)
		log.Info("base public-host routing enabled", "flag", publicHostEnv)
	}

	log.Info("base app embedded",
		"waitlist", "/v1/waitlist/*", "hosting", "/v1/base/*",
		"prefix", env.Or("BASE_API_PREFIX", ""), "brand", cloud.Brand(), "env", cloud.Env())
	return nil
}

// streamOrg answers the org a realtime stream request is for, read off the
// grant in its query, when the request is the stream's own GET. A grant nobody
// minted, or one already spent or expired, names no org.
func streamOrg(c *zip.Ctx) (string, bool) {
	if c.Method() != http.MethodGet || strings.TrimRight(c.Path(), "/") != apiPrefix+"/realtime" {
		return "", false
	}
	token := c.Query("token")
	if token == "" {
		return "", false
	}
	return apis.StreamGrantOrg(token)
}

// publicHostEnv gates host-as-project-ref Base routing (default OFF).
const publicHostEnv = "CLOUD_BASE_PUBLIC_HOST"

func publicHostEnabled() bool {
	switch strings.ToLower(env.Or(publicHostEnv, "")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// newPlatformApp builds the single platform Base app carrying the waitlist
// plugin and returns its handler (the FULL base mux; only /v1/waitlist/* is
// mounted from it). Every waitlist knob resolves from the environment at boot
// (see waitlist Config.resolve): TURNSTILE_SECRET_KEY, WAITLIST_ADMIN_SECRET,
// WAITLIST_AWARD_SECRET, WAITLIST_DEFAULT_SLUGS, WAITLIST_ACCESS_CAPACITY,
// WAITLIST_OPEN — secrets injected from KMS, never in code.
func newPlatformApp(dir string) (*baseapp.Base, zip.Handler, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	cfg, err := configure(context.Background(), luxlog.Default().New("subsystem", "base"), "", dir)
	if err != nil {
		return nil, nil, fmt.Errorf("place: %w", err)
	}
	bapp := baseapp.NewWithConfig(cfg)
	waitlist.MustRegister(bapp, waitlist.Config{Enabled: true})
	if err := bapp.Bootstrap(); err != nil {
		return nil, nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := bapp.RunAllMigrations(); err != nil {
		_ = bapp.ResetBootstrapState()
		return nil, nil, fmt.Errorf("migrations: %w", err)
	}
	h, _, err := handler(bapp)
	if err != nil {
		_ = bapp.ResetBootstrapState()
		return nil, nil, fmt.Errorf("build handler: %w", err)
	}
	return bapp, h, nil
}

// handler compiles one Base into the handler that answers for it, and it is the
// ONE place in this package where a Base meets zip.
//
// It reproduces apis.Serve's construction without binding a listener: build the
// base router, fire the OnServe hook so plugins register their routes on it, then
// compile the mux. The SAME code path apis.Serve runs inside its OnServe trigger,
// just without the net.Listen.
//
// A Base routes with its own router (github.com/hanzoai/base/tools/router),
// compiled to an *http.ServeMux whose handlers are unexported and whose chain
// carries base's own middleware — its rate limit, its auth token, its security
// headers, its JSON refusals. zip.Static takes an fs.FS, zip.Proxy takes an
// address to dial, and App.Use takes a *zip.App; none of those is a thing a Base
// has, so what is left is to adopt the handler. Adopting it HERE means it happens
// once when a Base opens, so no request builds an adapter and both lanes serve
// the same value.
//
// It answers the mux too: the org's graph asks its fields of the Base through
// it, in process, the same routes a request here reaches.
func handler(app core.App) (zip.Handler, http.Handler, error) {
	router, err := apis.NewRouter(app)
	if err != nil {
		return nil, nil, err
	}

	var (
		once sync.Once
		mux  http.Handler
	)
	serveEvent := new(core.ServeEvent)
	serveEvent.App = app
	serveEvent.Router = router
	serveEvent.Server = &http.Server{}

	triggerErr := app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		built, err := e.Router.BuildMux()
		if err != nil {
			return err
		}
		once.Do(func() { mux = built })
		return nil
	})
	if triggerErr != nil {
		return nil, nil, triggerErr
	}
	if mux == nil {
		return nil, nil, fmt.Errorf("nil handler after OnServe")
	}
	return zip.AdaptNetHTTP(mux), mux, nil
}

// Shutdown releases the platform app + every pooled per-org app. Idempotent.
func Shutdown(context.Context) error {
	s := mounted
	if s == nil {
		return nil
	}
	mounted = nil
	if s.stopBus != nil {
		s.stopBus()
	}
	var firstErr error
	if s.pool != nil {
		if err := s.pool.closeAll(); err != nil {
			firstErr = err
		}
	}
	if s.platform != nil {
		if err := s.platform.ResetBootstrapState(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// baseHealth is the base subsystem's own liveness answer.
type baseHealth struct {
	// Service is "base" — which subsystem answered.
	Service string `json:"service"`
	// Status is "ok" when the subsystem is serving.
	Status string `json:"status"`
}

// BaseHealth reports that the base subsystem is serving.
//
// It is deliberately INDEPENDENT of whether this deployment actually embeds the
// Base engine: the route answers before the CLOUD_BASE_EMBED gate and before the
// /v1/base/* wildcard, so a liveness probe measures the process rather than an
// optional feature, and the wildcard can never shadow it. It reads no tenant, so a
// prober that sends no principal is answered rather than refused.
func health(context.Context, *cloud.Unit) (*baseHealth, error) {
	return &baseHealth{Service: "base", Status: "ok"}, nil
}
