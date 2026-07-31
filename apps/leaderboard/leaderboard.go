// Package leaderboard ranks AI usage: who leads inside an org, which orgs lead
// globally, and a GitHub-style per-day contribution graph for one subject — all
// opt-in for public listing.
//
// It is a DERIVED, read-only lens over the ONE usage ledger (hanzo.cloud_usage)
// through the datastore OLAP rollup — it adds no metering path and double-counts
// nothing.
//
// Surface (all /v1, NO /api/ prefix; org-scoped, fail-closed):
//
//	GET  /v1/usage/leaderboard   ranked top users (personal|org) or orgs (global)
//	GET  /v1/usage/activity      per-day series for a heatmap + timeline (authorized subject)
//	GET  /v1/usage/leaderboard/optin       the caller's opt-in + their org's opt-in
//	PUT  /v1/usage/leaderboard/optin        set the caller's OWN public-listing opt-in
//	PUT  /v1/usage/leaderboard/optin/org    set the ORG's public-board opt-in (org admin)
//	POST /v1/usage/rollup/backfill          seed the rollup from ledger history (SuperAdmin, once)
//
// It co-owns the /v1/usage/* prefix with apps/usage (the cost footprint at
// /v1/usage/summary) — a DISTINCT concern (who leads + your activity graph) at its
// own paths, registered as a separate subsystem so it stays isolated. Its auto
// health route is /v1/leaderboard/health (the spec name). apps/usage's own doc
// claims it "owns ALL usage"; two packages under one prefix is one prefix with no
// owner, and the name here (leaderboard) does not match the prefix it serves.
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Datastore seams. In production these are cloud's one warehouse connection;
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "leaderboard", build, routes)
}

// build opens the opt-in store under the shared data dir (mirrors clients/settings).
func build(b cloud.Base) (state, error) {
	if err := os.MkdirAll(b.DataDir, 0o755); err != nil {
		return state{}, err
	}
	store, err := openOptinStore(filepath.Join(b.DataDir, "leaderboard.db"))
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
// cloud.Bridge comes FIRST, ahead of every leaf: a typed op receives a
// context.Context and its decoded In and nothing else, so the validated org — and
// the request the admin predicates read their attested claims off — cross on the
// context. Installed through the subsystem's Router, it lands once per prefix this
// subsystem DECLARES (/v1/usage/activity, /v1/usage/leaderboard,
// /v1/usage/rollup/backfill) and never on the /v1/usage/* paths clients/usage owns.
// Serve installs the same middleware binary-wide; nesting is harmless (the inner one
// is the one the handler sees) and declaring it here is what makes the subsystem
// self-sufficient when a test or a non-Serve composition root mounts it on a bare app.
//
// The group is a bare path prefix — no middleware, so it gates nothing on the
// co-owned /v1/usage root — and exists only so each op's path is composed the one way
// the router composes it.
func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Use(cloud.Bridge())
	g := app.Group("/v1/usage")
	o := boardOps{s: s}
	zip.Get(g, "/leaderboard", o.leaderboard)
	zip.Get(g, "/activity", o.activity)
	zip.Get(g, "/leaderboard/optin", o.getOptin)
	zip.Put(g, "/leaderboard/optin", o.putUserOptin)
	zip.Put(g, "/leaderboard/optin/org", o.putOrgOptin)
	zip.Post(g, "/rollup/backfill", o.backfill)
}

// boardOps binds the service to leaderboard's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
type boardOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// ── identity helpers ──────────────────────────────────────────────────────────

// tenantOf resolves the caller's validated effective org, fail-closed — the ONE
// tenancy gate this subsystem has. It is the value principal.Org decided at the
// identity boundary, which cloud.Bridge parked on the context; it is never an In
// field, because an In field is caller-supplied and a tenant key read from one is a
// cross-tenant read the caller asserted for itself. `why` is the refusal the surface
// shows, so each op keeps the wording it has always sent.
func tenantOf(ctx context.Context, why string) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrUnauthorized(why)
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
