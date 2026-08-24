package billing

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMountRefusesWithoutTheSharedKey: this app VERIFIES a token the account process
// minted. With no CONSOLE_CSRF_KEY the two processes resolve different keys and every
// console write here is refused for the life of the pod — a silent, permanent 403
// across the money path. So the mount fails instead, which in a plugin child is exit 1
// and in the host is /v1/billing absent behind a 503.
func TestMountRefusesWithoutTheSharedKey(t *testing.T) {
	luxlog.SetDefault(luxlog.New("test"))
	t.Setenv(account.KeyEnv, "")

	err := Mount(zip.New(zip.Config{Logger: luxlog.New("test")}), cloud.Deps{Brand: "hanzo"})
	if err == nil {
		t.Fatal("billing mounted with no shared anti-forgery key; every console write it serves would 403")
	}
	if !strings.Contains(err.Error(), account.KeyEnv) {
		t.Errorf("the refusal does not name the value to set: %v", err)
	}

	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err := Mount(zip.New(zip.Config{Logger: luxlog.New("test")}), cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("billing refused to mount with the key provisioned: %v", err)
	}
}
