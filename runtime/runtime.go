// Package runtime wires the OSS Hanzo Cloud plugin runtime drivers and host
// hooks so external plugin binaries (such as hanzo-inc/cloud's thin private
// production plugins) can blank-import one public package without duplicating
// internal/driver or internal/host.
package runtime

import (
	"context"

	"github.com/hanzoai/ha"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	_ "github.com/hanzoai/cloud/internal/driver"
	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/internal/pin"
	"github.com/hanzoai/cloud/internal/sqlcheck"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

// PostgresDialTimeoutEnv is the env var controlling postgres seam dial timeout.
const PostgresDialTimeoutEnv = postgres.DialTimeoutEnv

// Members returns the live HA peer membership for deps, delegating to the
// canonical host coordinator binding.
func Members(deps cloud.Deps) []ha.Member {
	return host.Members(deps)
}

// SetPluginsLock overrides the embedded plugins.lock content used by the host
// and /v1/admin/plugins when resolving digest-pinned plugin artifacts.
func SetPluginsLock(raw string) error {
	return pin.SetCurrent(raw)
}

// SpecConfig constructs an isolated single-plugin test Config and cleanup func.
func SpecConfig() (*cloud.Config, func(), error) {
	return host.SpecConfig()
}

// SpecDeps returns the host Deps binding for a single-plugin test harness.
func SpecDeps(cfg *cloud.Config) cloud.Deps {
	return host.SpecDeps(cfg)
}

// ObservePostgres installs a query observer on the registered postgres seam.
func ObservePostgres(fn func(query string)) func() {
	return postgres.Observe(fn)
}

// SQLProblems checks whether query conforms to the shared SQLite/Postgres dialect.
func SQLProblems(query string, strict bool) []string {
	return sqlcheck.Problems(query, strict)
}

// DescribeRequested reports whether argv asks this binary to describe itself (`<bin> describe <dir>`).
func DescribeRequested() (string, bool) {
	return host.DescribeRequested()
}

// Describe writes app's OpenAPI projection into dir.
func Describe(dir string, app *zip.App) error {
	return host.Describe(dir, app)
}

// UseMetrics mounts hanzoai/o11y/metrics onto app.
func UseMetrics(app cloud.Router, deps cloud.Deps) error {
	return host.UseMetrics(app, deps)
}

// InstallTelemetry installs this process's OTel tracer and meter providers.
func InstallTelemetry(ctx context.Context, log luxlog.Logger, serviceName string) func(context.Context) {
	return host.InstallTelemetry(ctx, log, serviceName)
}

// Recorder constructs the audit Recorder from cfg.
func Recorder(cfg *cloud.Config, logger luxlog.Logger, proc string, holds bool) (*audit.Recorder, error) {
	return host.Recorder(cfg, logger, proc, holds)
}

// AbuseMiddleware returns the edge lifecycle-defense middleware for a hand-written plugin host.
func AbuseMiddleware(cfg *cloud.Config, deps cloud.Deps) zip.Handler {
	return host.AbuseGate(deps, host.EdgePolicy(cfg), host.Traffic())
}
