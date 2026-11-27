package environment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/namespace"
)

func testStore(t *testing.T, open sqltest.Open) *Store {
	t.Helper()
	s, err := openStore(open(namespace.MustOrg("acme"), "environment"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	return s
}

// A codebase's row is read, changed and written back as one transaction, so two
// writes at once each keep their own edit: 24 racing writers of 24 different scripts
// all land, none lost to another's write-back.
func TestRacingChangesAreNeitherLostNorTorn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := testStore(t, open)
		ctx := context.Background()
		const n = 24
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				name := string(rune('a' + i))
				if _, err := s.change(ctx, "site", int64(100+i), func(r *row) { r.secrets = append(r.secrets, name) }); err != nil {
					t.Errorf("change %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		got, ok, err := s.get(ctx, "site")
		if err != nil || !ok || len(got.secrets) != n {
			t.Fatalf("the row holds %d of %d racing names (%v %v)", len(got.secrets), n, ok, err)
		}
	})
}

// Text PostgreSQL would refuse is refused at the store on both engines, writes
// nothing, and a read of it finds nothing.
func TestAScriptThatIsNotTextIsRefusedOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := testStore(t, open)
		ctx := context.Background()
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			_, err := s.change(ctx, "site", 1, func(r *row) { r.install = bad })
			if !errors.Is(err, dialect.ErrText) {
				t.Errorf("change with %s text: %v, want ErrText", name, err)
			}
			if err != nil && strings.Contains(err.Error(), bad) {
				t.Errorf("change with %s text: the error carries the value", name)
			}
			if _, ok, err := s.get(ctx, bad); err != nil || ok {
				t.Errorf("a read of %s text: %v %v, want nothing", name, ok, err)
			}
		}
		if list, err := s.list(ctx); err != nil || len(list) != 0 {
			t.Fatalf("a refused write left a row: %+v %v", list, err)
		}
	})
}
