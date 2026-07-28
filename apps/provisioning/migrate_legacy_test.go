package provisioning

import (
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacyProvisionedDDL is provisioned_resources BEFORE the dedicated-instance
// dimensions (size, instance) existed.
const legacyProvisionedDDL = `
CREATE TABLE provisioned_resources (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  kind          TEXT NOT NULL,
  name          TEXT NOT NULL,
  physical_name TEXT NOT NULL,
  secret_ref    TEXT NOT NULL DEFAULT '',
  host          TEXT NOT NULL DEFAULT '',
  port          INTEGER NOT NULL DEFAULT 0,
  username      TEXT NOT NULL DEFAULT '',
  dbname        TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);`

// TestMigrateOverLegacyProvisioned locks that migrate() succeeds on a DB whose
// provisioned_resources table predates the size/instance columns.
func TestMigrateOverLegacyProvisioned(t *testing.T) {
	migratetest.Case{
		Name:      "provisioning",
		LegacyDDL: legacyProvisionedDDL,
		Open: func(path string) (io.Closer, error) {
			st, err := openStore(path)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
	}.Run(t)
}
