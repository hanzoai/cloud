package connectors

import (
	"encoding/base64"
	"os"
	"testing"
)

// The KMS data plane refuses to open unencrypted, so a suite that seals a token
// has to supply a master key — the same one-line TestMain clients/base and
// clients/tasks use. Without it every custody assertion fails as "secret not
// found", which reads like a store bug and is really a refusal to write
// plaintext.
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}
