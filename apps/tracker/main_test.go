package tracker

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain gives the tracker suite the same key harness as the sibling clients/sync
// and clients/git suites. cek refuses to open a store without a master key — there is
// no plaintext-at-rest mode on any build — so a suite that opens per-org stores needs
// one before the first Open. Supply a throwaway dev key ONLY when the environment did
// not already provide one, so CI's real key is never overridden. Resolved once per
// process, order-independent.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
