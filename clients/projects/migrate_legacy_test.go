package projects

import (
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacyProjectsDDL is the projects table BEFORE the cache_control/last_purge_at
// columns existed.
const legacyProjectsDDL = `
CREATE TABLE projects (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  slug            TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  repo_url        TEXT NOT NULL DEFAULT '',
  repo_branch     TEXT NOT NULL DEFAULT '',
  repo_provider   TEXT NOT NULL DEFAULT '',
  framework       TEXT NOT NULL DEFAULT 'static',
  status          TEXT NOT NULL DEFAULT 'draft',
  live_url        TEXT NOT NULL DEFAULT '',
  bucket          TEXT NOT NULL DEFAULT '',
  current_deploy  TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);`

// TestMigrateOverLegacyProjects locks that migrate() succeeds on a DB whose
// projects table predates the cache_control/last_purge_at columns.
func TestMigrateOverLegacyProjects(t *testing.T) {
	migratetest.Case{
		Name:      "projects",
		LegacyDDL: legacyProjectsDDL,
		Open: func(path string) (io.Closer, error) {
			st, err := openStore(path)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
	}.Run(t)
}
