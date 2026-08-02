// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"os"
	"testing"

	"github.com/hanzoai/cloud/credz"
)

// TestMain resolves this test process's data-plane key the way the binary does.
//
// cek refuses to open a store with no master key — correctly: a build that can
// encrypt must never ship plaintext at rest. Every test in this package mounts the
// embedded IAM, so an unkeyed process fail-closes that mount to a 503 and the
// assertions downstream read an empty router: "0 typed operations, want at least 94",
// "the composed document carries no component schemas at all", "GET /v1/iam/users =
// 503, want 401". Those are the SAME sentence — no key — told nine different ways,
// which is how a missing key reads as a lost registry.
//
// credz.Boot is the ONE path that resolves it (env → broker → deterministic dev key)
// and the one build.go calls at startup, so booting it here is not test scaffolding
// around the key: it is the production boot, run first. A deployment that supplies a
// real key keeps it — Boot never overrides one — so this cannot mask a keyed CI run.
func TestMain(m *testing.M) {
	credz.Boot(os.TempDir())
	os.Exit(m.Run())
}
