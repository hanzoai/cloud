// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"os"
	"testing"

	"github.com/hanzoai/cloud"
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
// cloud.BootMaster is the ONE path that resolves it (the environment, else a random
// dev master over an empty directory) and the one build.go calls at startup, so
// booting it here is not test scaffolding around the key: it is the production boot,
// run first. A deployment that supplies a real key keeps it — BootMaster never
// overrides one — so this cannot mask a keyed CI run.
func TestMain(m *testing.M) {
	// BootMaster alone is not enough on a codec-linked build: its last resort is
	// cek.EnsureDevKey, which DECLINES there by design (a build that can really
	// encrypt must be handed a real key, not invent one). So supply a throwaway
	// through the same path a deployment uses, and only when nothing else did —
	// a keyed CI run keeps its own key and this cannot mask it.
	if os.Getenv(masterKeyEnv) == "" {
		_ = os.Setenv(masterKeyEnv, devMasterKey)
	}
	cloud.BootMaster(os.TempDir())
	os.Exit(m.Run())
}

const (
	masterKeyEnv = "CLOUD_KMS_MASTER_KEY_REF"
	devMasterKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes, dev-only
)
