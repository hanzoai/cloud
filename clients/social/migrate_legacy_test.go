package social

import (
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacySocialDDL is the accounts + posts tables BEFORE publishing landed —
// social_accounts without token; social_posts without account_id/external_id/error.
const legacySocialDDL = `
CREATE TABLE social_accounts (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  provider    TEXT NOT NULL DEFAULT 'x',
  handle      TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'connected',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE TABLE social_posts (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  content      TEXT NOT NULL DEFAULT '',
  channel      TEXT NOT NULL DEFAULT 'x',
  status       TEXT NOT NULL DEFAULT 'draft',
  schedule_at  INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);`

// TestMigrateOverLegacySocialTables locks that the org/provider/status/schedule_at
// indexes stay valid on a DB whose social tables predate the publish-state columns.
func TestMigrateOverLegacySocialTables(t *testing.T) {
	migratetest.Case{
		Name:      "social",
		LegacyDDL: legacySocialDDL,
		Open: func(path string) (io.Closer, error) {
			st, err := openStore(path)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
	}.Run(t)
}
