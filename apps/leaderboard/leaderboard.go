// Package leaderboard mounts the Hanzo Cloud GAMIFIED usage analytics surface: AI
// usage leaderboards (top users / orgs) + a GitHub-style per-day contribution graph,
// over the datastore OLAP rollup (#43). It is a DERIVED, read-only lens over the ONE
// usage ledger (hanzo.cloud_usage) — it adds no metering path and double-counts
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
// It co-owns the /v1/usage/* prefix with clients/usage (the cost footprint at
// /v1/usage/summary) — a DISTINCT concern (who leads + your activity graph) at its
// own paths, registered as a separate subsystem so it stays isolated. Its auto
// health route is /v1/leaderboard/health (the spec name).
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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
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
	if app == nil {
		return fmt.Errorf("leaderboard.Mount: nil app")
	}
	// Every route is a TYPED op, which lives on the *zip.App's registry — the one value
	// OpenAPI, MCP and the CLI are projected from. A Router not backed by one must fail
	// the mount rather than serve routes no projection knows.
	if cloud.ZipApp(app) == nil {
		return fmt.Errorf("leaderboard.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
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

// routes registers the surface. Every route is a TYPED op: zip.<Verb> registers the
// route AND the registry entry OpenAPI / MCP / the CLI are projected from, and it
// takes the ABSOLUTE path because the registry keys on it.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one installed
	// after these leaves would never run — and every op below reads the validated
	// principal (and sets Cache-Control) off the request it parks.
	app.Group("/v1/usage").Use(cloud.Bridge())

	zip.Get(z, "/v1/usage/leaderboard", o.leaderboard)
	zip.Get(z, "/v1/usage/activity", o.activity)
	zip.Get(z, "/v1/usage/leaderboard/optin", o.getOptin)
	zip.Put(z, "/v1/usage/leaderboard/optin", o.putUserOptin)
	zip.Put(z, "/v1/usage/leaderboard/optin/org", o.putOrgOptin)
	zip.Post(z, "/v1/usage/rollup/backfill", o.backfill)
}

// ops binds the service to the leaderboard's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.leaderboard), which is also
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// request recovers the request a typed op is serving — the one place the validated
// principal (org, user name, admin tier) and the response headers live. Absent off
// the HTTP path, where a tenant-scoped read has no identity and must refuse.
func (o ops) request(ctx context.Context) (*zip.Ctx, bool) { return cloud.Request(ctx) }

// ── identity helpers ──────────────────────────────────────────────────────────

// tenant resolves the caller's validated effective org (fail-closed). ONE gate.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

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
