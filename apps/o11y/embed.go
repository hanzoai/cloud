// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	planeops "github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/o11y/pkg/community"
	"github.com/hanzoai/o11y/pkg/modules/sentry/implsentry"
	o11yrt "github.com/hanzoai/o11y/pkg/o11y"
	o11yapp "github.com/hanzoai/o11y/pkg/query-service/app"
)

// embeddedDSN is the enable signal AND the datastore (Hanzo Datastore) target for
// the in-process runtime. It reads the SAME operator knob the standalone o11y pod
// sets — the flat, canonical O11Y_DATASTORE_DSN (which o11y config maps onto
// telemetrystore.datastore.dsn and gives precedence) — falling back to the
// structured O11Y_TELEMETRYSTORE_DATASTORE_DSN. Set (prod CR) ⇒ construct the
// runtime in-process; empty (local dev / tests, no datastore) ⇒ buildEmbeddedHandler
// no-ops and /v1/o11y/* uses the reverse-proxy fallback. ONE knob, no separate flag.
func embeddedDSN() string {
	return cmp.Or(os.Getenv("O11Y_DATASTORE_DSN"), os.Getenv("O11Y_TELEMETRYSTORE_DATASTORE_DSN"))
}

// embeddedRuntime / embeddedServer pin the ONE in-process o11y runtime for the
// life of the process (a package ref the GC won't collect), mirroring cloud's
// embeddedTasks. It is the SAME runtime the standalone o11y cmd/community builds —
// telemetry stores (datastore/datastore), sqlstore, querier, rule manager,
// dashboards, alerts — served through cloud's HTTP stack instead of a second
// Deployment.
var (
	embeddedRuntime *o11yrt.O11y
	embeddedServer  *o11yapp.Server
)

// buildEmbeddedHandler constructs the o11y runtime IN-PROCESS and returns its
// public HTTP handler via the ONE shared builder — community.NewServer — the
// EXACT construction the standalone o11y cmd/community server runs (o11y.New
// with its provider factories → app.NewServer → server.PublicHandler). Because
// it is the same code, the embed cannot drift from the running pod: identity is
// resolved by the IdentN resolver's iamidentn provider (default-enabled) from the
// gateway-injected Hanzo IAM session headers (X-Org-Id/X-User-Id/X-User-Email).
// Authorization is edge-trusted (O11Y_AUTHZ_PROVIDER=local, set in
// applyEmbedEnvDefaults): the gateway already validated the Hanzo IAM session and
// the sharder gates cross-org, so the org-scoped user is authorized locally rather
// than round-tripping back out to an external IAM Casbin enforcer (iamauthz) the
// one-binary carries no credentials for — same enforced policy, tuples in-process
// (o11y/pkg/authz/localauthz). Gateway-header traffic authenticates (200), NOT the
// native-JWT 401 of the retired v1.3.x line. The telemetry backend (datastore `datastore`
// StatefulSet, cluster `insights`) is untouched: the embedded runtime connects to
// it over datastore-native :9000; only the query/dashboards/alerts control plane
// moves in-process.
//
// Returns (nil, nil) when the embed is disabled (no DSN) so the caller keeps the
// proxy fallback. Returns (nil, err) on a genuine construction failure — also
// fail-soft at the call site.
//
// Lifecycle: the registry background services (alertmanager, statsreporter,
// licensing, tokenizer, authz, user, ruler/alert-rule-manager, auditor,
// meterreporter) run via the o11y registry Start — non-blocking (per-service goroutines).
// Cloud owns its own HTTP listeners, so we never call server.Start (which would
// bind the standalone query/opamp ports). OpAMP collector management (a second
// websocket listener) is NOT started in-process — telemetry ingest continues on
// the existing collector→datastore path, independent of this control plane.
func buildEmbeddedHandler(deps cloud.Deps) (http.Handler, error) {
	if embeddedDSN() == "" {
		return nil, nil // disabled — proxy fallback
	}

	// o11y's sqlstore (sqlite, control-plane metadata) and Prometheus active-query
	// tracker need a writable dir. Cloud's container is distroless (no /tmp), so pin
	// both under cloud's data root and create it eagerly so o11y.New's migrations
	// don't fail on a missing parent. The standalone pod used an emptyDir at
	// /var/lib/o11y — this is the in-process equivalent, owned by cloud.
	dataDir := filepath.Join(cmp.Or(deps.DataDir, "/var/lib/cloud"), "o11y")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	applyEmbedEnvDefaults(dataDir)

	ctx := context.Background()

	// community.NewConfig + community.NewServer are the ONE construction shared with
	// the standalone binary — config from env (env:, applying the O11Y_DATASTORE_DSN
	// alias), the o11y provider set (iamidentn identity, iamauthz authz, datastore
	// telemetrystore, sqlite sqlstore), and the app server. One bootstrap, one way.
	cfg, err := community.NewConfig(ctx, slog.Default(), nil)
	if err != nil {
		return nil, err
	}

	server, runtime, err := community.NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Start background services (registry: alertmanager, licensing, statsreporter,
	// tokenizer, authz, user, ruler/alert-rule-manager, auditor, meterreporter) —
	// non-blocking (per-service goroutines). ctx is Background: the runtime lives for
	// the process and is reclaimed on exit, mirroring cloud's embeddedTasks. We never
	// call server.Start (binds the standalone listeners) — cloud owns its own HTTP.
	runtime.Start(ctx)

	embeddedRuntime = runtime
	embeddedServer = server

	// Keyed error ingest (pk- → org) for the embedded sentry runtime goes through the
	// ONE binary-wide key resolver — the same seam /v1/event's out-of-band resolution
	// uses — so a publishable key attributes errors to the SAME org it attributes
	// events to. This is what lights sentry.hanzo.ai up with the key that already
	// feeds analytics + insights: one key, one endpoint, no per-project DSN secret.
	implsentry.SetIngestKeyResolver(ingestKeyOrg)

	return server.PublicHandler(), nil
}

// projectKeys and iamKeys are the two key spaces, as seams. They are variables so
// the ORDER between them is a property a test can hold without standing up a plane
// peer or an IAM — the order IS the fix, so it is the thing worth pinning.
var (
	projectKeys = projectKeyOrg
	iamKeys     = func(ctx context.Context, key string) (string, bool) {
		org := cloud.ResolvePublishableKeyOrg(ctx, key)
		return org, org != ""
	}
)

// ingestKeyOrg resolves a publishable ingest key to the org that owns it, asking the
// app that MINTS the key before the one that mints the other kind.
//
// Absence, not preference: a key found in the projects space is not "preferred" over
// an IAM answer, because IAM never has one to give. The order is what makes both
// spaces reachable through one door.
func ingestKeyOrg(ctx context.Context, key string) (string, bool) {
	if org, ok := projectKeys(ctx, key); ok {
		return org, true
	}
	return iamKeys(ctx, key)
}

// projectKeyOrg asks the app that MINTS publishable keys which org owns one.
//
// ONE PREFIX, TWO KEY SPACES. `pk-` is minted in two places and only one of them is
// IAM: apps/projects generates its own from crypto/rand and keeps it on the project
// row, and every key a customer has actually been handed is one of those. Asking IAM
// about a projects key is not a lookup that misses, it is a question about a
// different set — so this door refused every key that works everywhere else, and the
// error endpoint accepted nothing at all while /v1/event accepted the same key.
//
// Projects first, IAM second, because the fallback is a genuine org-scoped key and
// IAM does own those. It is the same op analytics resolves this key through
// (apps/analytics/plane.go), so one key now means one org at both doors.
//
// The resolver it feeds answers a bool and carries no error, so a projects outage
// reads here as "not this space" and falls through to IAM, which will not know the
// key either — the caller then refuses. That is the behaviour this path already had
// for these keys, so an outage cannot make attribution WRONG, only absent.
func projectKeyOrg(ctx context.Context, key string) (string, bool) {
	// Org-less by construction: the KEY is the tenant key and the answer names the
	// org, so passing one would let a caller file errors under someone else's.
	out, err := cloud.Ask[planeops.KeyIn, planeops.Attribution](
		cloud.For(ctx, ""), "projects", planeops.ProjectsResolveKey, &planeops.KeyIn{Key: key})
	if err != nil || out == nil || !out.Found {
		return "", false
	}
	return out.Org, out.Org != ""
}

// applyEmbedEnvDefaults sets the env the embedded runtime needs but the operator
// does not have to know about — all overridable (setenvDefault), all safe for
// cloud's distroless single-process container:
//
//   - the sqlite control-plane store and Prometheus active-query tracker are
//     pinned under cloud's writable data root (the distroless image has no /tmp);
//   - the runtime's own OTel self-metrics Prometheus pull reader is turned OFF.
//     The o11y runtime default binds 0.0.0.0:9090, which collides with cloud's health
//     listener (CLOUD_HEALTH_LISTEN=:9090) and crash-loops the whole process — the
//     one listener the standalone pod never contended for. Cloud owns process-level
//     observability (it exports its own OTel telemetry), so activating the embed can
//     never take cloud down on a :9090 clash. An operator can still re-enable it on
//     a FREE port by setting the vars explicitly.
func applyEmbedEnvDefaults(dataDir string) {
	setenvDefault("O11Y_SQLSTORE_SQLITE_PATH", filepath.Join(dataDir, "o11y.db"))
	setenvDefault("O11Y_PROMETHEUS_ACTIVE__QUERY__TRACKER_PATH", dataDir)
	setenvDefault("O11Y_INSTRUMENTATION_METRICS_ENABLED", "false")
	// Edge-trusted authorization: the gateway already validated the Hanzo IAM
	// session and injected trusted identity headers, and o11y's sharder gates
	// cross-org — so authorize the org-scoped user locally instead of round-tripping
	// back out to an external IAM Casbin enforcer (iamauthz) that the one-binary
	// carries no credentials for and that adds a synchronous failure mode to every
	// telemetry read. Same enforced policy, tuples kept in-process. See
	// o11y/pkg/authz/localauthz. Operator-overridable like every other embed default.
	setenvDefault("O11Y_AUTHZ_PROVIDER", "local")
}

// setenvDefault sets key=val only if key is currently unset, so an operator-pinned
// value always wins over the derived default.
func setenvDefault(key, val string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, val)
	}
}
