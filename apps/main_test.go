// Copyright © 2026 Hanzo AI. MIT License.

package apps

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain makes this package's store-backed tests (the finance-ledger money proofs in
// starter_test.go) build-tag agnostic, exactly as the root package's TestMain does. On
// an encryption-capable (cgo) build cek REFUSES to open a store without a master key;
// on a pure-Go build a key is itself refused. So supply a throwaway dev key ONLY when
// the build can encrypt AND the environment did not already provide one (CI may inject
// the real key) — never overriding a provided key. Resolved once per process,
// order-independent.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
