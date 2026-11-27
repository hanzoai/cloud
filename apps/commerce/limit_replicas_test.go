// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The bounds staff set are written on every replica. These tests stand a second replica
// beside the first: another Keyed beside the same database, which is what another pod
// is; on SQLite there is one process and the Keyed is the whole lock.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	commercelimit "github.com/hanzoai/commerce/models/limit"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// rowsOf is how many rows the store holds under one slug: a limit is one row.
func rowsOf(t *testing.T, slug string) int {
	t.Helper()
	var rows []*commercelimit.Limit
	if _, err := commercelimit.Query(commercelimit.AuthorityDB(context.Background())).Filter("Slug=", slug).GetAll(&rows); err != nil {
		t.Fatalf("read %s: %v", slug, err)
	}
	return len(rows)
}

// A limit set by many staff together on two replicas is one row, and a drop leaves none
// behind: each set looks for the row by its slug before it creates it, so without the
// row's lock across replicas every set that looked before the first created makes a row
// of its own, and a drop removes only one.
func TestALimitSetOnTwoReplicasIsOneRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bootRecord(t)
		const scope = "org:limit-replicas"
		slug := scope + "/" + client.LimitSandboxRunning
		l, ok := client.LimitOf(client.LimitSandboxRunning)
		if !ok {
			t.Fatal("the registry holds no sandbox limit")
		}
		replicas := []*lock.Keyed{{}, {}}
		if locus.DSN() == "" {
			replicas[1] = replicas[0] // a file belongs to one process: one Keyed
		}
		ctx := staff()

		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := writeLimit(ctx, replicas[i%2], l, scope, int64(200+i), "agency grew"); err != nil {
					t.Errorf("set %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		if n := rowsOf(t, slug); n != 1 {
			t.Fatalf("%d staff setting one limit together made %d rows, want 1", 16, n)
		}

		if _, err := dropLimit(ctx, replicas[1], l, scope); err != nil {
			t.Fatal(err)
		}
		if n := rowsOf(t, slug); n != 0 {
			t.Fatalf("a drop left %d row(s) of the limit behind", n)
		}
		var he *zip.HTTPError
		if _, err := dropLimit(ctx, replicas[0], l, scope); err == nil || !asHTTP(err, &he) || he.Status != http.StatusNotFound {
			t.Fatalf("a limit dropped twice answered %v, want 404", err)
		}
	})
}

func asHTTP(err error, out **zip.HTTPError) bool {
	he, ok := err.(*zip.HTTPError)
	if ok {
		*out = he
	}
	return ok
}
