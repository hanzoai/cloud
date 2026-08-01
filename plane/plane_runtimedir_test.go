package plane

import (
	"os"
	"testing"
)

// A caller resolves where to DIAL and a callee where to LISTEN. If they compute it
// differently they miss each other in a way nothing reports: plugins listened on
// /tmp/zip-commerce-*/commerce.sock while callers dialed /var/lib/cloud/run/commerce.sock,
// and a stale socket file at the shared path turned the miss into "connection refused" —
// which reads like the callee is DOWN rather than somewhere else. Money ops are
// fail-closed, so every AI completion answered 503 on a healthy fleet.
func TestBindRuntimeDirDerivesTheSharedPath(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", "")
	t.Setenv("CLOUD_DATA_DIR", "/var/lib/cloud")
	if got := BindRuntimeDir(); got != "/var/lib/cloud/run" {
		t.Fatalf("got %q, want /var/lib/cloud/run", got)
	}
	if got := os.Getenv("ZIP_RUNTIME_DIR"); got != "/var/lib/cloud/run" {
		t.Fatalf("must publish it for zip: %q", got)
	}
}

func TestBindRuntimeDirIsIdempotentAndHonoursAnOverride(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "/already/set")
	t.Setenv("CLOUD_DATA_DIR", "/var/lib/cloud")
	if got := BindRuntimeDir(); got != "/already/set" {
		t.Fatalf("an externally-set dir must win, got %q", got)
	}

	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", "/explicit/run")
	t.Setenv("CLOUD_DATA_DIR", "/var/lib/cloud")
	if got := BindRuntimeDir(); got != "/explicit/run" {
		t.Fatalf("CLOUD_RUN_DIR must beat CLOUD_DATA_DIR, got %q", got)
	}
	if again := BindRuntimeDir(); again != "/explicit/run" {
		t.Fatalf("idempotent: got %q", again)
	}
}
