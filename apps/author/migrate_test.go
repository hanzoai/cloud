package author

import (
	"context"
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// legacyPayoutsDDL is the author_payouts table BEFORE a payout recorded where its
// money landed (settlement), with a payout already in it.
const legacyPayoutsDDL = `
CREATE TABLE author_payouts (
  id           TEXT PRIMARY KEY,
  author_id    TEXT NOT NULL,
  amount_cents INTEGER NOT NULL,
  method       TEXT NOT NULL,
  reference    TEXT NOT NULL DEFAULT '',
  txn          TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
INSERT INTO author_payouts (id, author_id, amount_cents, method, created_at) VALUES ('pay_old', 'auth_old', 500, 'wire', 1);`

// TestMigrateOverLegacyPayouts: a database that predates the settlement column gains
// it on the next open, on either engine, and the payout written before it reads back
// as honestly unrecorded, never guessed.
func TestMigrateOverLegacyPayouts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		migratetest.Case{
			Name:      "authors",
			LegacyDDL: legacyPayoutsDDL,
			Open: func(dir string) (io.Closer, error) {
				st, err := openStore(dir)
				if err != nil {
					return nil, err
				}
				return st, nil
			},
			Probe: func(t *testing.T, c io.Closer) {
				st := c.(*Store)
				ps, err := st.ListPayouts(context.Background(), "auth_old", 10)
				if err != nil || len(ps) != 1 || ps[0].ID != "pay_old" || ps[0].Settlement != "" {
					t.Fatalf("the payout written before the column read back as %+v, %v", ps, err)
				}
			},
		}.Run(t)
	})
}
