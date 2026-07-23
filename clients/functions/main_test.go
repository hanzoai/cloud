package functions

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain makes the functions suite build-tag agnostic (mirrors the sibling
// clients/automations harness). On an encryption-capable (cgo) build cek REFUSES
// to open a store without a master key; on a pure-Go build a key is itself
// refused. So supply a throwaway dev key ONLY when the build can encrypt AND the
// environment did not already provide one (CI may inject the real key) — then
// every store opens (encrypted on cgo, plaintext on pure-Go) without ever
// overriding a provided key. Resolved once per process, order-independent.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
