package experiments

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain injects a dev master key so the per-org SQLite registry opens under an
// encryption-capable build (the repo's shared OrgDB-test setup). 32 dev-only bytes,
// temp dirs only.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	}
	os.Exit(m.Run())
}
