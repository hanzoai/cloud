// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/o11y/pkg/community"
	o11yapp "github.com/hanzoai/o11y/pkg/query-service/app"
	signozrt "github.com/hanzoai/o11y/pkg/signoz"
)

// embeddedDSN is the enable signal AND the ClickHouse (Hanzo Datastore) target for
// the in-process runtime. It reads the SAME operator knob the standalone o11y pod
// sets — the flat, canonical O11Y_DATASTORE_DSN (which signoz config maps onto
// telemetrystore.datastore.dsn and gives precedence) — falling back to the
// structured O11Y_TELEMETRYSTORE_DATASTORE_DSN. Set (prod CR) ⇒ construct the
// runtime in-process; empty (local dev / tests, no ClickHouse) ⇒ buildEmbeddedHandler
// no-ops and /v1/o11y/* uses the reverse-proxy fallback. ONE knob, no separate flag.
func embeddedDSN() string {
	return firstNonEmpty(os.Getenv("O11Y_DATASTORE_DSN"), os.Getenv("O11Y_TELEMETRYSTORE_DATASTORE_DSN"))
}

// embeddedRuntime / embeddedServer pin the ONE in-process o11y runtime for the
// life of the process (a package ref the GC won't collect), mirroring cloud's
// embeddedTasks. It is the SAME runtime the standalone o11y cmd/community builds —
// telemetry stores (ClickHouse/datastore), sqlstore, querier, rule manager,
// dashboards, alerts — served through cloud's HTTP stack instead of a second
// Deployment.
var (
	embeddedRuntime *signozrt.SigNoz
	embeddedServer  *o11yapp.Server
)

// buildEmbeddedHandler constructs the o11y runtime IN-PROCESS and returns its
// public HTTP handler via the ONE shared builder — community.NewServer — the
// EXACT construction the standalone o11y cmd/community server runs (signoz.New
// with its provider factories → app.NewServer → server.PublicHandler). Because
// it is the same code, the embed cannot drift from the running pod: identity is
// resolved by the IdentN resolver's iamidentn provider (default-enabled) from the
// gateway-injected Hanzo IAM session headers (X-Org-Id/X-User-Id/X-User-Email),
// authorization by iamauthz (Hanzo IAM Casbin). This is the SAME auth model as the
// running o11y pod — gateway-header traffic authenticates (200), NOT the native-JWT
// 401 of the retired v1.3.x line. The telemetry backend (ClickHouse `datastore`
// StatefulSet, cluster `insights`) is untouched: the embedded runtime connects to
// it over ClickHouse-native :9000; only the query/dashboards/alerts control plane
// moves in-process.
//
// Returns (nil, nil) when the embed is disabled (no DSN) so the caller keeps the
// proxy fallback. Returns (nil, err) on a genuine construction failure — also
// fail-soft at the call site.
//
// Lifecycle: the registry background services (alertmanager, statsreporter,
// licensing, tokenizer, authz, user, ruler/alert-rule-manager, auditor,
// meterreporter) run via SigNoz.Start — non-blocking (per-service goroutines).
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
	// both under cloud's data root and create it eagerly so signoz.New's migrations
	// don't fail on a missing parent. The standalone pod used an emptyDir at
	// /var/lib/o11y — this is the in-process equivalent, owned by cloud.
	dataDir := filepath.Join(firstNonEmpty(deps.DataDir, "/var/lib/cloud"), "o11y")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	setenvDefault("O11Y_SQLSTORE_SQLITE_PATH", filepath.Join(dataDir, "o11y.db"))
	setenvDefault("O11Y_PROMETHEUS_ACTIVE__QUERY__TRACKER_PATH", dataDir)

	ctx := context.Background()

	// community.NewConfig + community.NewServer are the ONE construction shared with
	// the standalone binary — config from env (env:, applying the O11Y_DATASTORE_DSN
	// alias), the SigNoz provider set (iamidentn identity, iamauthz authz, ClickHouse
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
	return server.PublicHandler(), nil
}

// firstNonEmpty returns the first non-empty string, else "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// setenvDefault sets key=val only if key is currently unset, so an operator-pinned
// value always wins over the derived default.
func setenvDefault(key, val string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, val)
	}
}
