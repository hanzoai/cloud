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

	"github.com/hanzoai/o11y/pkg/alertmanager"
	"github.com/hanzoai/o11y/pkg/analytics"
	"github.com/hanzoai/o11y/pkg/auditor"
	"github.com/hanzoai/o11y/pkg/authn"
	"github.com/hanzoai/o11y/pkg/authz"
	"github.com/hanzoai/o11y/pkg/authz/iamauthz"
	"github.com/hanzoai/o11y/pkg/cache"
	o11yconfig "github.com/hanzoai/o11y/pkg/config"
	"github.com/hanzoai/o11y/pkg/config/envprovider"
	"github.com/hanzoai/o11y/pkg/config/fileprovider"
	"github.com/hanzoai/o11y/pkg/factory"
	"github.com/hanzoai/o11y/pkg/flagger"
	"github.com/hanzoai/o11y/pkg/gateway"
	"github.com/hanzoai/o11y/pkg/gateway/noopgateway"
	"github.com/hanzoai/o11y/pkg/global"
	"github.com/hanzoai/o11y/pkg/licensing"
	"github.com/hanzoai/o11y/pkg/licensing/nooplicensing"
	"github.com/hanzoai/o11y/pkg/meterreporter"
	"github.com/hanzoai/o11y/pkg/modules/cloudintegration"
	"github.com/hanzoai/o11y/pkg/modules/cloudintegration/implcloudintegration"
	"github.com/hanzoai/o11y/pkg/modules/dashboard"
	"github.com/hanzoai/o11y/pkg/modules/dashboard/impldashboard"
	"github.com/hanzoai/o11y/pkg/modules/metricreductionrule"
	"github.com/hanzoai/o11y/pkg/modules/metricreductionrule/implmetricreductionrule"
	"github.com/hanzoai/o11y/pkg/modules/organization"
	"github.com/hanzoai/o11y/pkg/modules/retention"
	"github.com/hanzoai/o11y/pkg/modules/rulestatehistory"
	"github.com/hanzoai/o11y/pkg/modules/serviceaccount"
	"github.com/hanzoai/o11y/pkg/modules/tag"
	"github.com/hanzoai/o11y/pkg/prometheus"
	o11yapp "github.com/hanzoai/o11y/pkg/query-service/app"
	"github.com/hanzoai/o11y/pkg/querier"
	"github.com/hanzoai/o11y/pkg/queryparser"
	"github.com/hanzoai/o11y/pkg/ruler"
	"github.com/hanzoai/o11y/pkg/ruler/signozruler"
	signozrt "github.com/hanzoai/o11y/pkg/signoz"
	"github.com/hanzoai/o11y/pkg/sqlschema"
	"github.com/hanzoai/o11y/pkg/sqlstore"
	"github.com/hanzoai/o11y/pkg/telemetrystore"
	"github.com/hanzoai/o11y/pkg/types/authtypes"
	"github.com/hanzoai/o11y/pkg/types/telemetrytypes"
	"github.com/hanzoai/o11y/pkg/zeus"
	"github.com/hanzoai/o11y/pkg/zeus/noopzeus"
)

// dsnEnv is both the enable signal AND the ClickHouse target for the embedded
// runtime. Set (prod CR) ⇒ construct the runtime in-process; unset (local dev /
// tests, no ClickHouse) ⇒ buildEmbeddedHandler no-ops and /v1/o11y/* uses the
// reverse-proxy fallback. ONE knob, no separate flag. This is the structured
// telemetrystore key the o11y config reader consumes (telemetrystore.datastore.dsn);
// the same StatefulSet `datastore` (cluster `insights`) backs both paths.
const dsnEnv = "O11Y_TELEMETRYSTORE_DATASTORE_DSN"

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
// public HTTP handler — the SAME construction the standalone o11y cmd/community
// server performs (signoz.New with its provider factories → app.NewServer →
// server.PublicHandler) — wired to the SAME ClickHouse datastore over the O11Y_*
// env the config reader consumes (env:). Identity is resolved by the IdentN
// resolver's iamidentn provider (default-enabled) from the gateway-injected Hanzo
// IAM session headers (X-Org-Id/X-User-Id/X-User-Email); authorization by iamauthz
// (Hanzo IAM Casbin). This is the SAME auth model as the running o11y:0.2.0 pod —
// gateway-header traffic authenticates (200), NOT the native-JWT 401 of the v1.3.x
// line. The telemetry backend (ClickHouse `datastore` StatefulSet, cluster
// `insights`) is untouched: the embedded runtime connects to it over ClickHouse-
// native :9000; only the query/dashboards/alerts control plane moves in-process.
//
// Returns (nil, nil) when the embed is disabled (no DSN) so the caller keeps the
// proxy fallback. Returns (nil, err) on a genuine construction failure — also
// fail-soft at the call site.
//
// Lifecycle: the registry background services (alertmanager, statsreporter,
// licensing, tokenizer, authz, user, ruler/alert-rule-manager, auditor,
// meterreporter) run via runtime.Start — non-blocking (per-service goroutines).
// Cloud owns its own HTTP listeners, so we never call server.Start (which would
// bind the standalone query/opamp ports). OpAMP collector management (a second
// websocket listener) is NOT started in-process — telemetry ingest continues on
// the existing collector→datastore path, independent of this control plane.
func buildEmbeddedHandler(deps cloud.Deps) (http.Handler, error) {
	if os.Getenv(dsnEnv) == "" {
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

	// Config is read purely from env (env:), exactly like cmd.NewSigNozConfig.
	cfg, err := signozrt.NewConfig(
		ctx,
		slog.Default(),
		o11yconfig.ResolverConfig{
			Uris: []string{"env:"},
			ProviderFactories: []o11yconfig.ProviderFactory{
				envprovider.NewFactory(),
				fileprovider.NewFactory(),
			},
		},
	)
	if err != nil {
		return nil, err
	}

	// Construct the runtime with the SAME provider factories the standalone
	// cmd/community server uses (noop zeus/licensing/gateway/auditor/meterreporter,
	// Hanzo IAM authz via iamauthz, ClickHouse telemetrystore, sqlite sqlstore,
	// IdentN → iamidentn gateway-header identity) — kept verbatim so standalone and
	// embedded build byte-identical runtimes with the SAME auth. One bootstrap, one way.
	runtime, err := signozrt.New(
		ctx,
		cfg,
		zeus.Config{},
		noopzeus.NewProviderFactory(),
		licensing.Config{},
		func(_ sqlstore.SQLStore, _ zeus.Zeus, _ organization.Getter, _ analytics.Analytics) factory.ProviderFactory[licensing.Licensing, licensing.Config] {
			return nooplicensing.NewFactory()
		},
		signozrt.NewEmailingProviderFactories(),
		signozrt.NewCacheProviderFactories(),
		signozrt.NewWebProviderFactories(cfg.Global),
		func(ss sqlstore.SQLStore) factory.NamedMap[factory.ProviderFactory[sqlschema.SQLSchema, sqlschema.Config]] {
			return signozrt.NewSQLSchemaProviderFactories(ss)
		},
		signozrt.NewSQLStoreProviderFactories(),
		signozrt.NewTelemetryStoreProviderFactories(),
		func(ctx context.Context, providerSettings factory.ProviderSettings, store authtypes.AuthNStore, licensing licensing.Licensing) (map[authtypes.AuthNProvider]authn.AuthN, error) {
			return signozrt.NewAuthNs(ctx, providerSettings, store, licensing, cfg.Global)
		},
		func(_ context.Context, sqlstore sqlstore.SQLStore, _ authz.Config, _ licensing.Licensing, _ []authz.OnBeforeRoleDelete) (factory.ProviderFactory[authz.AuthZ, authz.Config], error) {
			// Hanzo IAM is the sole authorization provider — every decision is
			// delegated to IAM's Casbin enforce endpoint. No OpenFGA, no fallback.
			return iamauthz.NewProviderFactory(sqlstore), nil
		},
		func(store sqlstore.SQLStore, settings factory.ProviderSettings, analytics analytics.Analytics, orgGetter organization.Getter, queryParser queryparser.QueryParser, _ querier.Querier, _ licensing.Licensing, tagModule tag.Module) dashboard.Module {
			return impldashboard.NewModule(impldashboard.NewStore(store), settings, analytics, orgGetter, queryParser, tagModule)
		},
		func(_ licensing.Licensing) factory.ProviderFactory[gateway.Gateway, gateway.Config] {
			return noopgateway.NewProviderFactory()
		},
		func(_ licensing.Licensing) factory.NamedMap[factory.ProviderFactory[auditor.Auditor, auditor.Config]] {
			return signozrt.NewAuditorProviderFactories()
		},
		func(_ context.Context, _ factory.ProviderSettings, _ flagger.Flagger, _ licensing.Licensing, _ telemetrystore.TelemetryStore, _ retention.Getter, _ organization.Getter, _ zeus.Zeus) (factory.NamedMap[factory.ProviderFactory[meterreporter.Reporter, meterreporter.Config]], string) {
			return signozrt.NewMeterReporterProviderFactories(), "noop"
		},
		func(ps factory.ProviderSettings, q querier.Querier, a analytics.Analytics) querier.Handler {
			return querier.NewHandler(ps, q, a)
		},
		func(_ sqlstore.SQLStore, _ dashboard.Module, _ global.Global, _ zeus.Zeus, _ gateway.Gateway, _ licensing.Licensing, _ serviceaccount.Module, _ cloudintegration.Config) (cloudintegration.Module, error) {
			return implcloudintegration.NewModule(), nil
		},
		func(_ sqlstore.SQLStore, _ telemetrystore.TelemetryStore, _ dashboard.Module, _ queryparser.QueryParser, _ licensing.Licensing, _ flagger.Flagger, _ telemetrytypes.MetadataStore, _ factory.ProviderSettings, _ int) metricreductionrule.Module {
			return implmetricreductionrule.NewModule()
		},
		func(c cache.Cache, am alertmanager.Alertmanager, ss sqlstore.SQLStore, ts telemetrystore.TelemetryStore, ms telemetrytypes.MetadataStore, p prometheus.Prometheus, og organization.Getter, rsh rulestatehistory.Module, q querier.Querier, qp queryparser.QueryParser) factory.NamedMap[factory.ProviderFactory[ruler.Ruler, ruler.Config]] {
			return factory.MustNewNamedMap(signozruler.NewFactory(c, am, ss, ts, ms, p, og, rsh, q, qp, nil, nil))
		},
	)
	if err != nil {
		return nil, err
	}

	server, err := o11yapp.NewServer(cfg, runtime)
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
