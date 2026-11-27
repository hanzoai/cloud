package dataroom

import (
	"errors"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// replicas opens the index as n pods do: n handles, each with its own pool, on the
// one database. A SQLite file belongs to one process, so there the replicas share
// the file's one handle.
func replicas(t *testing.T, n int) []*linkIndex {
	t.Helper()
	dir := t.TempDir()
	var out []*linkIndex
	for range n {
		if len(out) > 0 && locus.DSN() == "" {
			out = append(out, out[0])
			continue
		}
		x, err := openLinkIndex(dir)
		if err != nil {
			t.Fatalf("openLinkIndex: %v", err)
		}
		t.Cleanup(func() { _ = x.Close() })
		out = append(out, x)
	}
	return out
}

// A visitor holding a link id is served by whichever replica the edge picks, so the
// replica that made the link and the one that opens it must read one index. Three
// replicas each made one link and every replica resolves all three.
//
// MUTATION: open the index as a file under the pod's own data directory (one per
// replica, as locus.System did with the DSN set) and a link made on one replica is
// unknown to the others.
func TestALinkIsKnownToEveryReplica(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pods := replicas(t, 3)
		for i, x := range pods {
			if err := x.put("link-"+string(rune('a'+i)), "org-"+string(rune('a'+i))); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
		for _, x := range pods {
			for i := range pods {
				id, want := "link-"+string(rune('a'+i)), "org-"+string(rune('a'+i))
				if org, ok, err := x.org(id); err != nil || !ok || org != want {
					t.Fatalf("%s resolves to %q, %v, %v; want %q", id, org, ok, err, want)
				}
			}
		}
		if _, ok, err := pods[0].org("link-z"); err != nil || ok {
			t.Fatalf("an unknown link resolved: %v %v", ok, err)
		}
	})
}

// A trust center's slug is unique across the deployment. Three replicas, each asked
// by a different org to publish the same slug at the same moment, give it to one and
// tell the other two it is taken, and the one that has it keeps it.
//
// MUTATION: check the slug and write it without the store's write lock and two
// replicas both pass the check, one fails the primary key as a 500 and not as taken.
func TestASlugGoesToOneOrgAcrossReplicas(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pods := replicas(t, 3)
		orgs := []string{"acme", "globex", "initech"}
		errs := make([]error, len(pods))
		var wg sync.WaitGroup
		for i, x := range pods {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = x.publish("trust", orgs[i])
			}()
		}
		wg.Wait()
		won, taken := -1, 0
		for i, err := range errs {
			switch {
			case err == nil:
				if won != -1 {
					t.Fatalf("%s and %s both published the slug", orgs[won], orgs[i])
				}
				won = i
			case errors.Is(err, errSlugTaken):
				taken++
			default:
				t.Fatalf("%s: %v", orgs[i], err)
			}
		}
		if won == -1 || taken != 2 {
			t.Fatalf("won %d, told taken %d; want one winner and two told it is taken", won, taken)
		}
		for _, x := range pods {
			if org, ok, err := x.resolve("trust"); err != nil || !ok || org != orgs[won] {
				t.Fatalf("the slug resolves to %q, %v, %v; want %q", org, ok, err, orgs[won])
			}
		}
	})
}
