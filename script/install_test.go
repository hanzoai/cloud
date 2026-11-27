package script

import (
	"os"
	"testing"

	"github.com/hanzoai/cloud/piece/catalog"
)

// TestMain installs the generated catalog's programs, as a composition root
// that serves connectors does at init.
func TestMain(m *testing.M) {
	Install(catalog.Program, catalog.Programs())
	os.Exit(m.Run())
}
