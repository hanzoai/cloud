package tel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// the store seals its personal-data columns under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir(), testMaster)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A number is held by one org at a time: a second org's attempt to record the same
// number fails and leaves the first's row alone, and a row records when it was made.
func TestANumberIsHeldByOneOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if err := s.PutNumber(ctx, Number{Org: "acme", ID: "n1", E164: "+14155550100", Country: "US", Monthly: 150, Currency: "usd"}); err != nil {
			t.Fatalf("first org: %v", err)
		}
		if err := s.PutNumber(ctx, Number{Org: "globex", ID: "n2", E164: "+14155550100"}); !dialect.Unique(err) {
			t.Fatalf("a second org recording the same number: %v, want a unique violation", err)
		}
		got, err := s.NumberByE164(ctx, "acme", " +14155550100 ")
		if err != nil || got.ID != "n1" || got.Monthly != 150 {
			t.Fatalf("the first org's number: %+v %v", got, err)
		}
		if other, err := s.NumberByE164(ctx, "globex", "+14155550100"); err != nil || other.ID != "" {
			t.Fatalf("another org sees the number: %+v %v", other, err)
		}
		var bought int64
		if err := s.db.QueryRow(`SELECT bought FROM tel_numbers WHERE org = $1 AND id = $2`, "acme", "n1").Scan(&bought); err != nil || bought < 1_700_000_000 {
			t.Fatalf("bought = %d (%v), want the time the row was made", bought, err)
		}
		// Recording it again keeps when it was bought.
		if err := s.PutNumber(ctx, Number{Org: "acme", ID: "n1", E164: "+14155550100", Monthly: 200}); err != nil {
			t.Fatalf("update: %v", err)
		}
		var again int64
		if err := s.db.QueryRow(`SELECT bought FROM tel_numbers WHERE org = $1 AND id = $2`, "acme", "n1").Scan(&again); err != nil || again != bought {
			t.Fatalf("bought moved from %d to %d (%v) on an update", bought, again, err)
		}
	})
}

// Text PostgreSQL would refuse is refused at the store on both engines, writes
// nothing, and a read of it finds nothing.
func TestTelRefusesTextPostgresRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			for what, err := range map[string]error{
				"PutNumber":  s.PutNumber(ctx, Number{Org: "acme", ID: "n", E164: bad}),
				"PutCall":    s.PutCall(ctx, Call{Org: "acme", ID: "c", From: bad}),
				"PutMessage": s.PutMessage(ctx, SMS{Org: "acme", ID: "m", Text: bad}),
			} {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
			if n, err := s.NumberByE164(ctx, "acme", bad); err != nil || n.ID != "" {
				t.Errorf("a read of %s text: %+v %v, want nothing", name, n, err)
			}
		}
		if n, c, m, err := s.Counts(ctx, "acme"); err != nil || n+c+m != 0 {
			t.Fatalf("a refused write left rows: %d %d %d %v", n, c, m, err)
		}
	})
}
