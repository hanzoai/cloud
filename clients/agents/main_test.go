package agents

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain makes this package's store-backed tests runnable on either build, the
// same way the root package and clients/research already do it.
//
// Every test here opens an agents.db, and cek REFUSES to open a store without a
// master key on an encryption-capable build — which, since hanzoai/sqlite v0.3,
// includes the pure-Go build. Without this the WHOLE package fails to run, which
// is exactly how the brand guard could go missing unnoticed.
//
// Supply a throwaway dev key ONLY when the build can encrypt AND the environment
// did not already provide one, so a real injected key is never overridden.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
