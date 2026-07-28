package plugin

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain makes this suite build-tag agnostic, the same harness the esign and
// platform suites use. On an encryption-capable (cgo) build cek REFUSES to open
// a store without a master key; on a pure-Go build a key is itself refused. So
// supply a throwaway dev key ONLY when the build can encrypt AND the environment
// did not already provide one.
//
// The control plane needs it because every mutation is recorded before it is
// made, and the audit chain is an encrypted store that fails closed.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
