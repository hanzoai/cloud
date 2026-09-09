package platform

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacyPlatformAppsDDL is the platform_apps table BEFORE the /v1/platform/run autoscaling
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
		Open: func(dir string) (io.Closer, error) {
			st, err := openStore(dir)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
	}.Run(t)
}

// legacyPlatformBuildsDDL is the platform_builds table BEFORE a build could name
// the architectures it publishes.
const legacyPlatformBuildsDDL = `
CREATE TABLE platform_builds (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  application_id TEXT NOT NULL,
  deployment_id  TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,
  image          TEXT NOT NULL DEFAULT '',
  job_name       TEXT NOT NULL DEFAULT '',
  logs_ref       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);`

// TestMigrateOverLegacyPlatformBuilds locks that migrate() succeeds on a DB whose
// platform_builds table predates the platforms column, and that a row written
// before it reads back as the single default-architecture build it was.
//
// The column is read by every SELECT the reconciler makes, so a migration that
// did not run would not degrade — the store would refuse to open and the whole
// subsystem would stay down.
func TestMigrateOverLegacyPlatformBuilds(t *testing.T) {
	migratetest.Case{
		Name:      "platform",
		LegacyDDL: legacyPlatformBuildsDDL,
		Open: func(dir string) (io.Closer, error) {
			st, err := openStore(dir)
			if err != nil {
				return nil, err
			}
			ctx := context.Background()
			// The case opens the store twice to prove migrate is idempotent, so the
			// row survives into the second pass and inserting it again collides.
			// Either way it is there to be read.
			_ = st.InsertBuild(ctx, Build{ID: "bld_legacy", Org: "hanzo", Status: "queued",
				Image: "ghcr.io/hanzoai/x:v1", JobName: "pf-runner-legacy", CreatedAt: 1, UpdatedAt: 1})
			b, err := st.GetBuild(ctx, "hanzo", "bld_legacy")
			if err != nil {
				st.Close()
				return nil, fmt.Errorf("read back: %w", err)
			}
			if len(b.Platforms) != 0 {
				st.Close()
				return nil, fmt.Errorf("a build that named no platform read back as %v", b.Platforms)
			}
			return st, nil
		},
	}.Run(t)
}
