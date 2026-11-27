package leaderboard

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
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
	return cloud.Use(app, deps, "leaderboard", func(b cloud.Base) (state, error) { return build(b, deps.MasterKey) }, routes)
}

// build opens the opt-in store under the shared data dir (mirrors clients/settings).
func build(b cloud.Base, master []byte) (state, error) {
	store, err := openOptinStore(b.DataDir, master)
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
	stopHub()
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
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := boardOps{s: s}
	exposeBetaPlace(o)
	exposeBountyTotals(o)
	g := app.Group("/v1/leaderboard")
	g.Get("", o.leaderboard)
	g.Get("/beta", o.betaBoard)
	g.Get("/bounty", o.bountyBoard)
	g.Get("/standing", o.standing)
	g.Get("/pulse", o.pulse)
	g.Get("/galaxy", o.galaxy)
	g.Raw(http.MethodGet, "/feed", cloud.Handle(s, feed))
	g.Get("/activity", o.activity)
	g.Get("/optin", o.getOptin)
	g.Put("/optin", o.putUserOptin)
	g.Put("/optin/org", o.putOrgOptin)
	admin := app.Group("/v1/admin/leaderboard")
	admin.Post("/rollup", o.backfill)
	admin.Post("/bounty", o.awardBounty)
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

// adminOf reports whether the caller may see their org's members named — an org
// admin or a SuperAdmin. False off the HTTP path, because there is no attested caller there.
func adminOf(ctx context.Context) bool {
	c, ok := requestOf(ctx)
	return ok && (principal.Super(c) || principal.IsOrgAdmin(c))
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
