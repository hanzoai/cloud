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
	o11yrt "github.com/hanzoai/o11y"
	"github.com/hanzoai/o11y/pkg/analytics"
	"github.com/hanzoai/o11y/pkg/authn"
	"github.com/hanzoai/o11y/pkg/authz"
	"github.com/hanzoai/o11y/pkg/authz/iamauthz"
	o11yconfig "github.com/hanzoai/o11y/pkg/config"
	"github.com/hanzoai/o11y/pkg/config/envprovider"
	"github.com/hanzoai/o11y/pkg/config/fileprovider"
	"github.com/hanzoai/o11y/pkg/factory"
	"github.com/hanzoai/o11y/pkg/gateway"
	"github.com/hanzoai/o11y/pkg/gateway/noopgateway"
	"github.com/hanzoai/o11y/pkg/licensing"
	"github.com/hanzoai/o11y/pkg/licensing/nooplicensing"
	"github.com/hanzoai/o11y/pkg/modules/dashboard"
	"github.com/hanzoai/o11y/pkg/modules/dashboard/impldashboard"
	"github.com/hanzoai/o11y/pkg/modules/organization"
	"github.com/hanzoai/o11y/pkg/querier"
	o11yapp "github.com/hanzoai/o11y/pkg/query-service/app"
	"github.com/hanzoai/o11y/pkg/queryparser"
	"github.com/hanzoai/o11y/pkg/sqlschema"
	"github.com/hanzoai/o11y/pkg/sqlstore"
	"github.com/hanzoai/o11y/pkg/types/authtypes"
	"github.com/hanzoai/o11y/pkg/zeus"
	"github.com/hanzoai/o11y/pkg/zeus/noopzeus"
)

// dsnEnv is both the enable signal AND the ClickHouse target for the embedded
// runtime. Set (prod CR) ⇒ construct the runtime in-process; unset (local dev /
// tests, no ClickHouse) ⇒ buildEmbeddedHandler no-ops and /v1/o11y/* uses the
// reverse-proxy fallback. ONE knob, no separate flag. This is the structured
// telemetrystore key the o11y config reader consumes on the v1.3.x line (the flat
// O11Y_DATASTORE_DSN alias is a later-mainline addition; cloud pins v1.3.13).
const dsnEnv = "O11Y_TELEMETRYSTORE_DATASTORE_DSN"

// embeddedRuntime / embeddedServer pin the ONE in-process o11y runtime for the
// life of the process (a package ref the GC won't collect), mirroring cloud's
// embeddedTasks. It is the SAME runtime the standalone o11y cmd/server builds —
// telemetry stores (ClickHouse/datastore), sqlstore, querier, rule manager,
// dashboards, alerts — served through cloud's HTTP stack instead of a second
// Deployment.
var (
	embeddedRuntime *o11yrt.HanzoO11y
	embeddedServer  *o11yapp.Server
)

// buildEmbeddedHandler constructs the o11y runtime IN-PROCESS and returns its
// public HTTP handler — exactly what the standalone o11y cmd/server builds
// (o11yrt.New with its provider factories → app.NewServer → server.PublicHandler)
// — wired to the SAME ClickHouse datastore over the O11Y_* env the config reader
// consumes (env:). The telemetry backend (ClickHouse `datastore` StatefulSet,
// cluster `insights`) is untouched: the embedded runtime connects to it over
// ClickHouse-native :9000; only the query/dashboards/alerts control plane moves
// in-process.
//
// Returns (nil, nil) when the embed is disabled (no DSN) so the caller keeps the
// proxy fallback. Returns (nil, err) on a genuine construction failure — also
// fail-soft at the call site.
//
// Deferred vs embedded: the alert rule manager (evaluation) runs via the server's
// StartBackground; the registry background services (alertmanager, statsreporter,
// licensing, tokenizer, authz, user) run via the runtime's own Start. OpAMP
// collector management (a second websocket listener) is NOT started in-process —
// telemetry ingest continues on the existing collector→datastore path, independent
// of this control plane.
func buildEmbeddedHandler(deps cloud.Deps) (http.Handler, error) {
	if os.Getenv(dsnEnv) == "" {
		return nil, nil // disabled — proxy fallback
	}

	// o11y's sqlstore (sqlite, control-plane metadata) and Prometheus active-query
	// tracker need a writable dir. Cloud's container is distroless (no /tmp), so pin
	// both under cloud's data root and create it eagerly so o11y.New's migrations
	// don't fail on a missing parent. The standalone pod used an emptyDir at
	// /var/lib/o11y — this is the in-process equivalent, owned by cloud.
	dataDir := filepath.Join(firstNonEmpty(deps.DataDir, "/var/lib/cloud"), "o11y")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	setenvDefault("O11Y_SQLSTORE_SQLITE_PATH", filepath.Join(dataDir, "o11y.db"))
	setenvDefault("O11Y_PROMETHEUS_ACTIVE__QUERY__TRACKER_PATH", dataDir)

	ctx := context.Background()

	// Config is read purely from env (env:), exactly like cmd.NewHanzoO11yConfig.
	config, err := o11yrt.NewConfig(
		ctx,
		slog.Default(),
		o11yconfig.ResolverConfig{
			Uris: []string{"env:"},
			ProviderFactories: []o11yconfig.ProviderFactory{
				envprovider.NewFactory(),
				fileprovider.NewFactory(),
			},
		},
		o11yrt.DeprecatedFlags{},
	)
	if err != nil {
		return nil, err
	}

	// Construct the runtime with the SAME provider factories the standalone
	// cmd/server uses (noop zeus/licensing/gateway, Hanzo IAM authz, ClickHouse
	// telemetrystore, sqlite sqlstore) — kept verbatim so standalone and embedded
	// build byte-identical runtimes. One bootstrap, one way.
	runtime, err := o11yrt.New(
		ctx,
		config,
		zeus.Config{},
		noopzeus.NewProviderFactory(),
		licensing.Config{},
		func(_ sqlstore.SQLStore, _ zeus.Zeus, _ organization.Getter, _ analytics.Analytics) factory.ProviderFactory[licensing.Licensing, licensing.Config] {
			return nooplicensing.NewFactory()
		},
		o11yrt.NewEmailingProviderFactories(),
		o11yrt.NewCacheProviderFactories(),
		o11yrt.NewWebProviderFactories(),
		func(sqlstore sqlstore.SQLStore) factory.NamedMap[factory.ProviderFactory[sqlschema.SQLSchema, sqlschema.Config]] {
			return o11yrt.NewSQLSchemaProviderFactories(sqlstore)
		},
		o11yrt.NewSQLStoreProviderFactories(),
		o11yrt.NewTelemetryStoreProviderFactories(),
		func(ctx context.Context, providerSettings factory.ProviderSettings, store authtypes.AuthNStore, licensing licensing.Licensing) (map[authtypes.AuthNProvider]authn.AuthN, error) {
			return o11yrt.NewAuthNs(ctx, providerSettings, store, licensing)
		},
		func(_ context.Context, sqlstore sqlstore.SQLStore, _ licensing.Licensing, _ dashboard.Module) factory.ProviderFactory[authz.AuthZ, authz.Config] {
			return iamauthz.NewProviderFactory(sqlstore)
		},
		func(store sqlstore.SQLStore, settings factory.ProviderSettings, analytics analytics.Analytics, orgGetter organization.Getter, queryParser queryparser.QueryParser, _ querier.Querier, _ licensing.Licensing) dashboard.Module {
			return impldashboard.NewModule(impldashboard.NewStore(store), settings, analytics, orgGetter, queryParser)
		},
		func(_ licensing.Licensing) factory.ProviderFactory[gateway.Gateway, gateway.Config] {
			return noopgateway.NewProviderFactory()
		},
		func(ps factory.ProviderSettings, q querier.Querier, a analytics.Analytics) querier.Handler {
			return querier.NewHandler(ps, q, a)
		},
	)
	if err != nil {
		return nil, err
	}

	server, err := o11yapp.NewServer(config, runtime)
	if err != nil {
		return nil, err
	}

	// Start background evaluation — non-blocking (goroutines). The registry services
	// (alertmanager, statsreporter, licensing, tokenizer, authz, user) via the
	// runtime's Registry; the alert rule manager via the server. Cloud owns its own
	// listeners, so we never call server.Start (which would bind the standalone
	// query/pprof/opamp ports). ctx is Background: the runtime lives for the process
	// and is reclaimed on exit, mirroring cloud's embeddedTasks.
	runtime.Start(ctx)
	if err := server.StartBackground(ctx); err != nil {
		return nil, err
	}

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
