// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/plane"
)

// A plugin must listen on the SHARED runtime dir, because that is where every caller
// dials. zip.Addr("") derives the socket from ZIP_RUNTIME_DIR, and nothing set it this
// early — so each plugin bound a PRIVATE temp path (/tmp/zip-commerce-*/commerce.sock)
// while callers dialed /var/lib/cloud/run/commerce.sock. A stale socket file sat at the
// shared path, so the dial failed as "connection refused" (reads like the callee is
// down) rather than "no such file" (reads like it is absent).
//
// Every cross-process plane call was therefore unreachable. For the money ops that is
// fail-CLOSED: the AI balance gate could not verify a balance and EVERY completion
// answered 503 — chat, copilot, documents — on a pod whose ledger was healthy.
func TestPluginListensOnTheSharedRuntimeDir(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv(runDirEnv, "")
	t.Setenv("CLOUD_DATA_DIR", "/var/lib/cloud")
	plane.Unbind()

	// listenOn is what the serve path calls; it must bind before asking for the addr.
	_, _ = listenOn(&Config{})

	got := strings.TrimSpace(os.Getenv("ZIP_RUNTIME_DIR"))
	if got != "/var/lib/cloud/run" {
		t.Fatalf("listenOn must bind the shared runtime dir before computing the socket; "+
			"ZIP_RUNTIME_DIR = %q, want /var/lib/cloud/run (a temp path here means every "+
			"cross-process plane call is unreachable)", got)
	}
}

// An externally-set runtime dir always wins — the operator's choice is not ours to
// overwrite, and both halves (listener and dialer) must agree on whatever it says.
func TestExternalRuntimeDirIsHonoured(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "/custom/run")
	t.Setenv("CLOUD_DATA_DIR", "/var/lib/cloud")
	plane.Unbind()

	_, _ = listenOn(&Config{})

	if got := os.Getenv("ZIP_RUNTIME_DIR"); got != "/custom/run" {
		t.Fatalf("an externally-set ZIP_RUNTIME_DIR must win; got %q", got)
	}
}
