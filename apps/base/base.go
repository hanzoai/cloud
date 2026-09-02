// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// Package base is managed Hanzo Base: a hosted backend for your app —
// collections, records, access rules and sign-in.
//
// It serves that engine per org at /v1/base, plus the platform's public
// waitlist at /v1/waitlist.
//
// It is the in-binary replacement for the standalone `ghcr.io/hanzoai/superbase`
// pod, whose whole job was `base.New()` + serve. cloud already links
// github.com/hanzoai/base, so it runs the SAME engine in-process across TWO
// orthogonal lanes:
//
//	LANE 1 — the viral waitlist (GTM launch surface). ONE platform Base app
//	carries the waitlist plugin; its /v1/waitlist/* routes are PUBLIC (a signup
//	surface has no principal to scope by) and platform-owned (one waitlist per
//	brand, not customer data). console reads it via WAITLIST_URL=…/v1/waitlist.
//
//	LANE 2 — managed Base hosting (what superbase/PocketHost provided). ONE Base
//	app PER ORG, opened lazily and pooled, each on its OWN SQLite under
//	{DataDir}/base/{orgSegment}/ — the same "prod = SQLite per tenant" model
//	(HIP-0302) the NewBase leaves (captable/sign/dataroom) use, so an org's
//	collections/records are PHYSICALLY isolated. Served AUTHENTICATED under
//	/v1/base/*, the org resolved from the VALIDATED cloud principal (never a
//	client header). This is the console Bases manager's backend.
//
// The two lanes are deliberately NOT one app: the waitlist is a public, single,
// brand-level instance; hosted Bases are private, per-org, and many.
//
// There was a THIRD prefix, /v1/collections, forwarding to a SEPARATE managed
// Base deployment for the sake of a cross-instance `tenants` registry — one row
// per Base instance, each on its own subdomain. It is gone, and the registry is
// why: it could only ever answer anonymously, because an authenticated request
// is scoped to the caller's org and opens that org's own Base, which has no
// `tenants` collection. So a registry of every org's Bases could not be read by
// anyone who had signed in, and it held zero rows for its whole life. What
// remained was two engines answering one question — an org's collections and
// their records — from two disks, where a record written through one was
// invisible through the other. LANE 2 below is the answer.
//
// MOUNT PREFIX. Base's REST router honours BASE_API_PREFIX (default /v1); this
// package pins it to /v1/base so the per-org engine serves its collections API
// natively at /v1/base/collections/… (self-generated URLs included) and never
// collides with cloud's other /v1 routes. The waitlist plugin binds a FIXED
// /v1/waitlist regardless of the prefix, so the two lanes never overlap.
//
// IAM-NATIVE, ONE AUTH SOURCE. Each per-org app validates bearer tokens against
// Hanzo IAM's JWKS ({IAMIssuer}/v1/iam/.well-known/jwks) as its EXCLUSIVE auth
// source (apis.StoreKey{JWKSURL,ExternalAuthOnly}) — the same IAM the cloud edge
// validates for org routing. The edge selects the org; Base authorises the
// record; both consume ONE IAM. No second auth path is introduced.
//
// FAIL-CLOSED + STAGED. The embed activates only when CLOUD_BASE_EMBED is
// truthy. Absent it, Mount is a no-op except the always-on GET /v1/base/health
// liveness route, so linking this subsystem into every cloud variant changes
// nothing until a single-writer deployment opts in. Activation is one CR env.
//
// ONE WRITER, DURABLE. Every embedded store is single-open + single-writer: the
// base deployment is single-replica, strategy Recreate, on the RWO cloud-api-data
// PVC, so each per-org SQLite is durable across restarts — the property the
// standalone base pod had with its own PVC.
package base

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"

	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/plugins/waitlist"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/sites"
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

// Placement answers where one Base keeps its data. A Base is embedded SQLite,
// so the empty answer — which is also what no placement at all gives — is the
// right one for every Base until a host says otherwise.
//
// cloud answers this because cloud is the host. Whether an org's Base belongs
// on a server, and which one, follows from what cloud provisioned for that org
// (apps/provisioning already mints exactly such a postgres:// DSN for its `sql`
// add-on) and from who runs that server. Base cannot know either, which is why
// it stopped reading the answer from the process environment: one Base per
// tenant means the answer is per tenant, and only the host holds it.
//
// Asked with the org for a tenant's Base, and with the empty org for the
// platform's own — the one Base here that belongs to no tenant. An empty org is
// already refused wherever a tenant is expected (pool.acquire), so the two can
// never be confused.
type Placement func(org string) (dataDSN, auxDSN string)

// placement is the host's answer, nil until a host gives one. Package-level
// like sites.SetBaseHostHandler, because it is one deployment-wide policy
// rather than a per-request choice.
var placement Placement

// SetPlacement binds the resolver consulted whenever a Base is opened. Call it
// before Mount. Nil — the default — leaves every Base embedded, which is what
// every Base is today.
func SetPlacement(p Placement) { placement = p }

// appConfig builds the config for one Base, asking the host where that Base
// keeps its data. It is the ONE place this package constructs a baseapp.Config,
// so no Base can be opened without the question being put.
func appConfig(dir, org string) baseapp.Config {
	cfg := baseapp.Config{DefaultDataDir: dir, HideStartBanner: true}
	if placement != nil {
		cfg.DataDSN, cfg.AuxDSN = placement(org)
	}
	return cfg
}

func embedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(embedEnv))) {
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
}

// mounted is the active subsystem (nil until Mount succeeds with the embed on).
var mounted *subsystem

// Mount wires the base subsystem onto app per HIP-0106: the always-on health
// route, then — behind CLOUD_BASE_EMBED — the public waitlist lane and the
// authenticated per-org hosting lane.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("base.Use:  nil app")
	}
	log := luxlog.Default().New("subsystem", "base")

	// A typed op is a route PLUS a registry entry, and the registry lives on the
	// App. A router that cannot reach it would serve the health route with no
	// schema, no prose, no MCP tool and no SDK method — so the mount FAILS rather
	// than quietly publishing a surface no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("base.Use:  router is not a zip app, so the typed ops have no registry")
	}

	// Native /v1/base/health — always answers (OwnsHealth), no auth, BEFORE the
	// embed gate AND before the /v1/base/* wildcard, so the liveness probe never
	// depends on CLOUD_BASE_EMBED and the wildcard never shadows it (same pattern
	// as clients/plan + clients/pricing).
	zip.Get(zapp, "/v1/base/health", health)

	// A typed op receives only a context, so the validated identity has to be
	// parked there — and it is parked by the bridge, ahead of the leaves. It is
	// installed here rather than relied on from Serve because this package's own
	// tests do not run Serve, which is how an op that works in production reads
	// no identity under test (or the reverse, which is worse).
	app.Use(cloud.Bridge())

	// Which Bases the caller can reach — ahead of the embed gate for the same
	// reason /health is, and at a literal path so it wins the address against the
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
	zip.Get(zapp, "/v1/base/bases", ops.list)
	zip.Get(zapp, "/v1/base/bases/:org", ops.read)

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
	if deps.DataDir == "" {
		return fmt.Errorf("base.Use:  empty DataDir")
	}

	// Pin Base's router prefix to /v1/base (its documented multi-app knob) unless
	// the operator already set one. Read once, applied to EVERY app built below.
	if strings.TrimRight(os.Getenv("BASE_API_PREFIX"), "/") == "" {
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

	root := filepath.Join(deps.DataDir, "base")

	// LANE 1 — platform waitlist app (public /v1/waitlist/*). Raw because it
	// relays the embedded Base engine's own bytes verbatim — the routes, shapes
	// and refusals are that program's, not this package's to declare.
	platformApp, h, err := newPlatformApp(filepath.Join(root, platformSeg))
	if err != nil {
		return fmt.Errorf("base.Use:  platform waitlist app: %w", err)
	}
	app.All("/v1/waitlist", h)
	app.All("/v1/waitlist/*", h)

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

	app.All("/v1/base/*", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return principal.Refused(c)
		}
		return p.serve(org, c)
	})

	mounted = &subsystem{pool: p, platform: platformApp}

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
		"prefix", os.Getenv("BASE_API_PREFIX"), "brand", deps.Brand, "env", deps.Env)
	return nil
}

// publicHostEnv gates host-as-project-ref Base routing (default OFF).
const publicHostEnv = "CLOUD_BASE_PUBLIC_HOST"

func publicHostEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(publicHostEnv))) {
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
	bapp := baseapp.NewWithConfig(appConfig(dir, ""))
	waitlist.MustRegister(bapp, waitlist.Config{Enabled: true})
	if err := bapp.Bootstrap(); err != nil {
		return nil, nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := bapp.RunAllMigrations(); err != nil {
		_ = bapp.ResetBootstrapState()
		return nil, nil, fmt.Errorf("migrations: %w", err)
	}
	h, err := handler(bapp)
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
func handler(app core.App) (zip.Handler, error) {
	router, err := apis.NewRouter(app)
	if err != nil {
		return nil, err
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
		return nil, triggerErr
	}
	if mux == nil {
		return nil, fmt.Errorf("nil handler after OnServe")
	}
	return zip.AdaptNetHTTP(mux), nil
}

// Shutdown releases the platform app + every pooled per-org app. Idempotent.
func Shutdown(context.Context) error {
	s := mounted
	if s == nil {
		return nil
	}
	mounted = nil
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
