// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// UseGrants installs doc as the grant document the next Use loads — the file
// production mounts at /etc/cloud/kms/grants.yaml — for the rest of t. An
// empty doc is no document mounted at all.
func UseGrants(t testing.TB, doc string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "grants.yaml")
	if doc != "" {
		if err := os.WriteFile(f, []byte(doc), 0o600); err != nil {
			t.Fatalf("write grants: %v", err)
		}
	}
	prev := GrantsFile
	GrantsFile = f
	t.Cleanup(func() { GrantsFile = prev })
}

// HeaderBoundary stands in for SanitizeIdentity in the header harnesses, which
// inject the identity headers the boundary would mint and never run it. The
// boundary also mints the principal a typed op reads, so this mints the one
// those headers describe: the org, and the user as its holder. It is test
// scaffolding in _test.go and exists in no binary.
var HeaderBoundary zip.Handler = func(c *zip.Ctx) error {
	if u := c.Header("X-User-Id"); u != "" {
		principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: u, Subject: u, Holder: u})
	} else {
		principal.Mint(c, principal.Principal{})
	}
	return c.Next()
}
