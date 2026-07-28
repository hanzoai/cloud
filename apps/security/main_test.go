package security

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestMain makes this package's store-backed tests runnable on either build, the
// same way clients/agents and the root package already do it.
//
// Every test here mounts security, and cek REFUSES to open a store without a
// master key on an encryption-capable build — which, since hanzoai/sqlite v0.3,
// includes the pure-Go build. Without this the WHOLE package fails at Mount, so
// a change to the detection engine could not be proved safe against the suite
// that exercises it.
//
// Supply a throwaway dev key ONLY when the build can encrypt AND the environment
// did not already provide one, so a real injected key is never overridden.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}
