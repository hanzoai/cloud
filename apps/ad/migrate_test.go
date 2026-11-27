package ad

import (
	"context"
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// legacyCampaignsDDL is the ads_campaigns table BEFORE a campaign could name its
// ad account and the platform's own id for it, with a campaign already in it.
const legacyCampaignsDDL = `
CREATE TABLE ads_campaigns (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  name       TEXT NOT NULL,
  platform   TEXT NOT NULL DEFAULT 'meta',
  status     TEXT NOT NULL DEFAULT 'draft',
  objective  TEXT NOT NULL DEFAULT '',
  budget     INTEGER NOT NULL DEFAULT 0,
  spend      INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
INSERT INTO ads_campaigns (id, org, name, created_at, updated_at) VALUES ('adc_old', 'acme', 'Old', 1, 2);`

// TestMigrateOverLegacyCampaigns: a database that predates the account and
// external_id columns gains them on the next open, on either engine, the campaign
// written before them reads back with neither, one launched afterwards keeps both,
// and opening again changes nothing.
func TestMigrateOverLegacyCampaigns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		migratetest.Case{
			Name:      "ads",
			LegacyDDL: legacyCampaignsDDL,
			Open: func(dir string) (io.Closer, error) {
				st, err := openStore(dir)
				if err != nil {
					return nil, err
				}
				return st, nil
			},
			Probe: func(t *testing.T, c io.Closer) {
				st, ctx := c.(*Store), context.Background()
				old, err := st.GetCampaign(ctx, "acme", "adc_old")
				if err != nil || old.Name != "Old" || old.Account != "" || old.ExternalID != "" {
					t.Fatalf("the campaign written before the columns read back as %+v, %v", old, err)
				}
				got, err := st.MarkLaunched(ctx, "acme", "adc_old", "act_9", "ext_9", 3)
				if err != nil || got.Account != "act_9" || got.ExternalID != "ext_9" || got.Status != "active" {
					t.Fatalf("a launch after the migration = %+v, %v", got, err)
				}
			},
		}.Run(t)
	})
}
