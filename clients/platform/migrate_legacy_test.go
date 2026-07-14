package platform

import (
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacyPlatformAppsDDL is the platform_apps table BEFORE the /v1/run autoscaling
// bounds (min_scale/max_scale) existed.
const legacyPlatformAppsDDL = `
CREATE TABLE platform_apps (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  project_id     TEXT NOT NULL,
  slug           TEXT NOT NULL,
  name           TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  environment    TEXT NOT NULL DEFAULT 'production',
  source         TEXT NOT NULL,
  repo_url       TEXT NOT NULL DEFAULT '',
  repo_branch    TEXT NOT NULL DEFAULT '',
  repo_provider  TEXT NOT NULL DEFAULT '',
  image_repo     TEXT NOT NULL DEFAULT '',
  image_tag      TEXT NOT NULL DEFAULT '',
  build_type     TEXT NOT NULL DEFAULT '',
  dockerfile     TEXT NOT NULL DEFAULT '',
  port           INTEGER NOT NULL DEFAULT 8080,
  replicas       INTEGER NOT NULL DEFAULT 1,
  env_json       TEXT NOT NULL DEFAULT '[]',
  domains_json   TEXT NOT NULL DEFAULT '[]',
  status         TEXT NOT NULL DEFAULT 'draft',
  namespace      TEXT NOT NULL DEFAULT '',
  current_deploy TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);`

// TestMigrateOverLegacyPlatformApps locks that migrate() succeeds on a DB whose
// platform_apps table predates the min_scale/max_scale autoscaling columns.
func TestMigrateOverLegacyPlatformApps(t *testing.T) {
	migratetest.Case{
		Name:      "platform",
		LegacyDDL: legacyPlatformAppsDDL,
		Open: func(path string) (io.Closer, error) {
			st, err := openStore(path)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
	}.Run(t)
}
