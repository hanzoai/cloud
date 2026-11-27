package host

import (
	"testing"

	cloud "github.com/hanzoai/cloud/sdk"
)

// index installs a boot snapshot for one test and restores the previous one after,
// so these tests can share the package-level index without ordering coupling.
func index(t *testing.T, cfg *cloud.Config, plugins ...cloud.Plugin) {
	t.Helper()
	t.Cleanup(func() { cloud.Declare(nil, &cloud.Config{}) })
	cloud.Declare(plugins, cfg)
}
