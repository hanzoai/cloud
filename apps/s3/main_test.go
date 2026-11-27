package s3_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hanzoai/cloud/sdk"
)

// TestMain resolves this test process's data-plane key the way a new deployment
// does, before any test names an object store.
//
// newApp points S3_ADMIN_* at a fake store, and a process that keeps its stores
// in an object store refuses to mint a key (master.go): an empty disk beside one
// is a new pod of a deployment that has data. Each fake store sits on a temp
// directory, so this process IS a new deployment, and BootMaster mints its key
// once here — the first call wins for the life of the process — so every test
// runs holding one, as a booted cloud does.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "s3-master-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "s3 tests: data dir:", err)
		os.Exit(1)
	}
	for _, k := range []string{"S3_ADMIN_ACCESS_KEY", "S3_ADMIN_SECRET_KEY", "S3_ADMIN_ENDPOINT"} {
		os.Unsetenv(k)
	}
	cloud.BootMaster(dir)
	if err := cloud.MasterErr(); err != nil {
		fmt.Fprintln(os.Stderr, "s3 tests: data-plane key:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
