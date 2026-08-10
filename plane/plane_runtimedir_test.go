package plane

import (
	"github.com/hanzoai/cloud/internal/planetest"
	"context"
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

// Reach must resolve WHERE sockets live before looking for one.
//
// It was the only entry point in this file that did not Bind, so it answered about
// whatever directory zip had defaulted to. That is not academic: Serve binds when it
// computes its listen addresses, which is AFTER every subsystem has mounted, so a
// mount-time caller asked about /tmp while the fleet's sockets live under the data
// dir — and got "not deployed here" for an app that is. The two sibling entry points
// (Peer, Ask) have always bound; this asserts all three resolve one path.
func TestReachResolvesTheSharedPathWithoutAPriorBind(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", planetest.Dir(t))
	t.Setenv("CLOUD_DATA_DIR", "")
	t.Setenv("ZIP_ADDR", "") // no router: "no socket, no router" is a fast, real answer
	Unbind()
	t.Cleanup(Unbind)

	// Nothing is listening anywhere, so the ANSWER is ErrNoPeer either way. What is
	// under test is the DIRECTORY it looked in.
	_ = Reach(context.Background(), "somepeer")

	if got := os.Getenv("ZIP_RUNTIME_DIR"); got != os.Getenv("CLOUD_RUN_DIR") {
		t.Fatalf("Reach looked in %q, want the cloud run dir %q — a mount-time caller would be told an app that IS deployed is not", got, os.Getenv("CLOUD_RUN_DIR"))
	}
}
