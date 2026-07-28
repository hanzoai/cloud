package metering_test

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain makes the finance-in-process debit test (finance_inproc_test.go, which opens
// a real per-org finance store) build-tag agnostic. On an encryption-capable (cgo) build
// cek REFUSES to open a store without a master key; on a pure-Go build a key is itself
// refused. So supply a throwaway dev key ONLY when the build can encrypt AND the
// environment did not already provide one (CI may inject the real key) — then the finance
// open succeeds (encrypted on cgo, plaintext on pure-Go) without ever overriding a
// provided key. Mirrors the root package's TestMain; resolved once per process,
// order-independent.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
