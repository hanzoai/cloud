package risk

// TestMain makes this suite build-tag agnostic, the same harness apps/plugin,
// esign and platform use. On an encryption-capable (cgo) build cek REFUSES to
// open a store without a master key; on a pure-Go build a key is itself refused.
// So supply a throwaway dev key ONLY when the build can encrypt AND the
// environment did not already provide one.
//
// Risk needs it because every decision is DURABLE FIRST — the record lands in
// the tenant's own encrypted file before the analytics copy is emitted — so a
// suite that cannot open a store cannot exercise the decision path at all.

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
