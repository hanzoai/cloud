package o11y

import (
	"os"
	"path/filepath"
	"testing"
)

// The embedded o11y runtime's OTel self-metrics reader binds 0.0.0.0:9090 by
// default — the SAME port as cloud's health listener (CLOUD_HEALTH_LISTEN=:9090).
// Activating the embed must never take cloud down on that clash, so the embed
// defaults the reader OFF before construction. This is the guard that keeps the
// whole console (api.hanzo.ai) from crash-looping when o11y goes in-process.
func TestApplyEmbedEnvDefaultsDisablesMetricsReader(t *testing.T) {
	os.Unsetenv("O11Y_INSTRUMENTATION_METRICS_ENABLED")
	os.Unsetenv("O11Y_SQLSTORE_SQLITE_PATH")
	os.Unsetenv("O11Y_PROMETHEUS_ACTIVE__QUERY__TRACKER_PATH")
	dir := t.TempDir()

	applyEmbedEnvDefaults(dir)

	if got := os.Getenv("O11Y_INSTRUMENTATION_METRICS_ENABLED"); got != "false" {
		t.Fatalf("O11Y_INSTRUMENTATION_METRICS_ENABLED = %q, want \"false\" — embed would bind :9090 and crash-loop cloud", got)
	}
	if got := os.Getenv("O11Y_SQLSTORE_SQLITE_PATH"); got != filepath.Join(dir, "o11y.db") {
		t.Fatalf("sqlite path = %q, want under the data dir", got)
	}
	if got := os.Getenv("O11Y_PROMETHEUS_ACTIVE__QUERY__TRACKER_PATH"); got != dir {
		t.Fatalf("tracker path = %q, want %q", got, dir)
	}
}

// An operator-pinned value must win (setenvDefault semantics) — e.g. re-enabling
// the reader on a free port.
func TestApplyEmbedEnvDefaultsRespectsOperatorOverride(t *testing.T) {
	t.Setenv("O11Y_INSTRUMENTATION_METRICS_ENABLED", "true")
	applyEmbedEnvDefaults(t.TempDir())
	if got := os.Getenv("O11Y_INSTRUMENTATION_METRICS_ENABLED"); got != "true" {
		t.Fatalf("operator override lost: got %q, want \"true\"", got)
	}
}
