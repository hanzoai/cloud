// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/hanzoai/cloud"
)

// testMaster is this package's throwaway data-plane key: 32 bytes, dev only.
var testMaster = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}()

// TestMain resolves this test process's data-plane key the way the binary does.
//
// cek refuses to open a store with no master, so a store opened before one is
// installed fails — and most tests here open one. TestMain is the only point
// guaranteed to run first, which is why the key is installed here rather than in
// each test body.
//
// A deployment that supplies its own key keeps it: BootMaster never overrides
// one, so a keyed CI run cannot be masked by this.
func TestMain(m *testing.M) {
	if os.Getenv(cloud.MasterEnv) == "" {
		_ = os.Setenv(cloud.MasterEnv, base64.StdEncoding.EncodeToString(testMaster))
	}
	cloud.BootMaster(os.TempDir())
	os.Exit(m.Run())
}
