package taxonomy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The first boot seeds the catalogue and no later one does, however many replicas
// boot at once: of 20 racing seeds, exactly one writes, and what it wrote is there
// once.
func TestRacingSeedsWriteTheCatalogueOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		cats := []Category{{Owner: hanzo, ID: "ai", Label: "AI", Order: 1}}
		taxa := []Taxon{{Owner: hanzo, ID: "chat", Name: "Chat", Category: "ai", Published: true}}
		var wg sync.WaitGroup
		var mu sync.Mutex
		wrote := 0
		for range 20 {
			wg.Go(func() {
				ok, err := s.Seed(ctx, cats, taxa)
				if err != nil {
					t.Errorf("seed: %v", err)
					return
				}
				if ok {
					mu.Lock()
					wrote++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if wrote != 1 {
			t.Fatalf("%d seeds wrote, want exactly one", wrote)
		}
		got, err := s.Taxa(ctx, "", "")
		if err != nil || len(got) != 1 || !got[0].Published {
			t.Fatalf("taxa after the race: %+v %v, want the one published", got, err)
		}
	})
}

// Text PostgreSQL would refuse is refused at the store on both engines, and writes
// nothing.
func TestAnEntryThatIsNotTextIsRefusedOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			for what, err := range map[string]error{
				"PutCategory": s.PutCategory(ctx, Category{Owner: "acme", ID: "c", Label: bad}),
				"PutTaxon":    s.PutTaxon(ctx, Taxon{Owner: "acme", ID: "t", Name: bad, Category: "c"}),
			} {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
			if _, err := s.Seed(ctx, []Category{{Owner: hanzo, ID: "c", Label: bad}}, nil); !errors.Is(err, dialect.ErrText) {
				t.Errorf("Seed with %s text: %v, want ErrText", name, err)
			}
		}
		if got, err := s.Categories(ctx, "acme"); err != nil || len(got) != 0 {
			t.Fatalf("a refused write left a row: %+v %v", got, err)
		}
	})
}
