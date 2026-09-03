// Package leaderboard is the ranking of who uses AI most, in your org and globally.
//
// It ranks who leads inside an org, which orgs lead globally, and draws a
// GitHub-style per-day contribution graph for one subject — all opt-in for
// public listing.
//
// It is a DERIVED, read-only lens over the ONE usage ledger (hanzo.cloud_usage)
// through the datastore OLAP rollup — it adds no metering path and double-counts
// nothing.
//
// Surface (all /v1, NO /api/ prefix; org-scoped, fail-closed):
//
//	GET  /v1/leaderboard              ranked top users (personal|org) or orgs (global)
//	GET  /v1/leaderboard/activity     per-day series for a heatmap + timeline (authorized subject)
//	GET  /v1/leaderboard/optin        the caller's opt-in + their org's opt-in
//	PUT  /v1/leaderboard/optin        set the caller's OWN public-listing opt-in
//	PUT  /v1/leaderboard/optin/org    set the ORG's public-board opt-in (org admin)
//	POST /v1/admin/leaderboard/rollup seed the rollup from ledger history (SuperAdmin, once)
//
// It answers under its OWN name. It used to sit inside apps/usage's /v1/usage/*
// prefix — a distinct concern (who leads + your activity graph) at paths another
// subsystem's name was on, which apps/usage's own doc read as "owns ALL usage".
// Two packages under one prefix is one prefix with no owner, and the two ARE two:
// this one keeps a store (the opt-in preferences, store.go) and usage keeps none,
// so the boundary was already there and only the address disagreed. Now the
// address agrees, and usage keeps what it actually owns (/v1/usage/summary,
// /v1/usage/analytics). Its auto health route is /v1/leaderboard/health, which
// the name now matches. The SuperAdmin backfill is the operator's view of this
// capability and lands where those live, /v1/admin/leaderboard/rollup.
//
// TENANT ISOLATION (the bar). The org is the VALIDATED IAM owner claim (principal.Org
// — the trusted X-Org-Id the identity middleware minted from the verified bearer,
// HIP-0026; NEVER a client header) AND a validated principal is required. Every
// datastore read binds the org POSITIONALLY (never interpolated). A user board only
// ever contains the caller's own org's rows; an org board carries org-level aggregates
// only; cross-org detail is structurally impossible. Fail closed: no principal → 401;
// datastore not connected → honest-empty (available:false), never fabricated ranks.
//
// PRIVACY (opt-in). Public listing is OPT-IN and PRIVATE by default: a user sees their
// OWN rank always, but is shown to others only after opting in with a chosen handle;
// an org appears on the cross-org global board only after an org admin opts it in.
// See view.go for the naming/anonymization policy.
package leaderboard

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Datastore clients. In production these are cloud's one warehouse connection;
// tests substitute fakes to exercise the full read+assemble path (and to assert the
// exact org-bound SQL the handlers build) without a live warehouse.
var (
	queryDatastore   = datastore.Query
	execDatastore    = datastore.Exec
	datastoreEnabled = datastore.Ready
	ensureUsageTable = datastore.EnsureCloudUsage
	nowFn            = time.Now
)

// state is the leaderboard subsystem's own data: the opt-in preference store.
type state struct {
	store *optinStore
}

// mountedStore holds the opt-in store for the shutdown hook (mirrors the
// package-global pattern of clients/settings + clients/marketing, whose generic
// Mount also owns a SQLite store closed on SIGTERM).
var mountedStore *optinStore

// Mount wires the leaderboard surface onto app per HIP-0106 — one line over the
// generic subsystem entrypoint.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "leaderboard", build, routes)
}

// build opens the opt-in store under the shared data dir (mirrors clients/settings).
func build(b cloud.Base) (state, error) {
	store, err := openOptinStore(b.DataDir)
	if err != nil {
		return state{}, err
	}
	mountedStore = store
	b.Log.Info("usage leaderboard surface", "prefix", "/v1/usage", "rollup", rollupTable)
	return state{store: store}, nil
}

// Shutdown closes the opt-in store on SIGTERM (registered as the subsystem's
// Shutdown hook). Idempotent.
func Shutdown(_ context.Context) error {
	if mountedStore == nil {
		return nil
	}
	err := mountedStore.Close()
	mountedStore = nil
	return err
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list and the generated SDK — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the surface.
//
// A typed op receives a context.Context and its decoded In and nothing else, so
// the validated org — and the request the admin predicates read their attested
// claims off — cross on the context, parked there by cloud.Bridge. The composer
// owns that install, once at its root.
//
// The group is /v1 with a non-empty leaf on every op, not /v1/leaderboard: the
// board IS the prefix, and zip.Get(g, "") composes to "/v1/leaderboard/", a
// different route — and op.Path is the identity every projection keys on. One
// group also spans both of this surface's roots, the public one and the
// operator's, so this is one idiom rather than a group plus an exception. It is a
// bare path prefix carrying no middleware, so it gates nothing.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1")
	o := boardOps{s: s}
	zip.Get(g, "/leaderboard", o.leaderboard)
	zip.Get(g, "/leaderboard/activity", o.activity)
	zip.Get(g, "/leaderboard/optin", o.getOptin)
	zip.Put(g, "/leaderboard/optin", o.putUserOptin)
	zip.Put(g, "/leaderboard/optin/org", o.putOrgOptin)
	zip.Post(g, "/admin/leaderboard/rollup", o.backfill)
}

// boardOps binds the service to leaderboard's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
type boardOps struct{ s *cloud.Service[state] }

// ── identity helpers ──────────────────────────────────────────────────────────

// tenantOf resolves the caller's validated effective org, fail-closed — the ONE
// tenancy gate this subsystem has. It is the value principal.Org decided at the
// identity boundary, which cloud.Bridge parked on the context; it is never an In
// field, because an In field is caller-supplied and a tenant key read from one is a
// cross-tenant read the caller asserted for itself. `why` is the refusal the surface
// shows, so each op keeps the wording it has always sent.
func tenantOf(ctx context.Context, why string) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	return org, nil
}

// requestOf is the request a typed op is serving. This subsystem needs it for the
// three facts that live in headers rather than in the tenant key — the caller's
// username, org-admin-ness and platform-admin-ness — and for the one response header
// it sets. Absent off the HTTP path (a CLI LocalInvoke), where the honest answer is
// that there is no request; every reader below then fails closed.
func requestOf(ctx context.Context) (*zip.Ctx, bool) { return cloud.Request(ctx) }

// superOf reports whether the caller is a platform SuperAdmin. False off the HTTP
// path: no request, no attested claim, no elevation.
func superOf(ctx context.Context) bool {
	c, ok := requestOf(ctx)
	return ok && principal.IsSuperAdmin(c)
}

// adminOf reports whether the caller may see their org's members named — an org
// admin or a SuperAdmin. False off the HTTP path, for the same reason as superOf.
func adminOf(ctx context.Context) bool {
	c, ok := requestOf(ctx)
	return ok && (principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c))
}

// noStore marks a response uncacheable. Per-tenant analytics must never be held by a
// browser or an intermediary. A no-op off the HTTP path, where there is no response
// to head.
func noStore(ctx context.Context) {
	if c, ok := requestOf(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// selfLedgerID is the caller's user_id AS RECORDED in the ledger for `org`:
// "<org>/<name>", matching the write path (organization + "/" + name). Empty when
// the validated username header is absent — self-marking + personal rank then
// degrade honestly rather than mis-attributing a row. `org` is the board's org.
func selfLedgerID(c *zip.Ctx, org string) string {
	name := strings.TrimSpace(c.Header("X-User-Name"))
	if name == "" {
		return ""
	}
	return org + "/" + name
}

// selfIDOf is selfLedgerID for a typed op. Empty off the HTTP path — the same honest
// degrade an absent username header already produces.
func selfIDOf(ctx context.Context, org string) string {
	c, ok := requestOf(ctx)
	if !ok {
		return ""
	}
	return selfLedgerID(c, org)
}
