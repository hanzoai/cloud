package studio

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"testing"

	"github.com/hanzoai/orm/pgtest"

	"github.com/hanzoai/cloud/sdk"
	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// pg is a throwaway PostgreSQL for the suite, or nil with why when this machine
// has no server to run.
var (
	pg  *pgtest.Server
	why string
)

func TestMain(m *testing.M) {
	// The store's login password is derived from the data-plane master, so the
	// process holds one, as every process of a deployment does.
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	_ = os.Setenv(cloud.MasterEnv, base64.StdEncoding.EncodeToString(k))
	dir, _ := os.MkdirTemp("", "studio-master")
	cloud.BootMaster(dir)
	pg, why = pgtest.Start()
	code := m.Run()
	if pg != nil {
		pg.Stop()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
