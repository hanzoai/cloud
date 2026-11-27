package ingress

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"golang.org/x/crypto/acme/autocert"
)

var acmeMaster = bytes.Repeat([]byte{0x3c}, 32)

// edges opens the ingress store as n pods do: n stores, each with its own pool, on the
// one database, and the autocert cache of each. A SQLite file belongs to one process,
// so there the replicas share the file's one handle.
func edges(t *testing.T, n int) ([]*Store, []autocert.Cache) {
	t.Helper()
	dir := t.TempDir()
	var stores []*Store
	var caches []autocert.Cache
	for range n {
		s := (*Store)(nil)
		if len(stores) > 0 && locus.DSN() == "" {
			s = stores[0]
		} else {
			var err error
			if s, err = openStore(dir); err != nil {
				t.Fatalf("openStore: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
		}
		c, err := s.acme(acmeMaster)
		if err != nil {
			t.Fatalf("acme: %v", err)
		}
		stores, caches = append(stores, s), append(caches, c)
	}
	return stores, caches
}

// What one replica of the edge obtained, every replica serves. The account key, a
// host's certificate and key, and a challenge token written on one replica are read
// on the others; a delete is seen by all; and a name nobody wrote is a miss. The
// value in the database is sealed, so the private key is not in it.
//
// MUTATION: cache in a directory of the pod's own (autocert.DirCache, as the edge did)
// and the second replica finds nothing the first wrote.
func TestTheCertificatesAreEveryReplicas(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		stores, caches := edges(t, 3)
		ctx := context.Background()
		const key = "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIFakeKeyForTheTestOnly\n-----END EC PRIVATE KEY-----\n"

		if _, err := caches[1].Get(ctx, "example.com"); !errors.Is(err, autocert.ErrCacheMiss) {
			t.Fatalf("a name nobody wrote: %v, want a miss", err)
		}
		if err := caches[0].Put(ctx, "example.com", []byte(key)); err != nil {
			t.Fatalf("put: %v", err)
		}
		for i, c := range caches {
			got, err := c.Get(ctx, "example.com")
			if err != nil || string(got) != key {
				t.Fatalf("replica %d reads %q, %v; want what replica 0 wrote", i, got, err)
			}
		}

		var raw string
		if err := stores[0].db.QueryRow(`SELECT data FROM ingress_acme WHERE name=$1`, "example.com").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(raw, "seal:") || strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "FakeKey") {
			t.Fatalf("the private key is stored as %q, which is not sealed", raw)
		}

		// A replica replaces what another wrote.
		if err := caches[2].Put(ctx, "example.com", []byte("renewed")); err != nil {
			t.Fatalf("put: %v", err)
		}
		if got, err := caches[1].Get(ctx, "example.com"); err != nil || string(got) != "renewed" {
			t.Fatalf("after a renewal on another replica: %q, %v", got, err)
		}

		// A delete on one is a delete on all, and deleting nothing is not an error.
		if err := caches[1].Delete(ctx, "example.com"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := caches[0].Get(ctx, "example.com"); !errors.Is(err, autocert.ErrCacheMiss) {
			t.Fatalf("after a delete on another replica: %v, want a miss", err)
		}
		if err := caches[0].Delete(ctx, "example.com"); err != nil {
			t.Fatalf("delete of nothing: %v", err)
		}
	})
}

// A sealed value is bound to its name: one carried to another name by a writer that
// can UPDATE the table does not open, so a certificate cannot be swapped for another
// host's by anyone who is not the store.
func TestACachedValueDoesNotOpenUnderAnotherName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		stores, caches := edges(t, 1)
		ctx := context.Background()
		if err := caches[0].Put(ctx, "a.example", []byte("a's key")); err != nil {
			t.Fatal(err)
		}
		if err := caches[0].Put(ctx, "b.example", []byte("b's key")); err != nil {
			t.Fatal(err)
		}
		if _, err := stores[0].db.Exec(`UPDATE ingress_acme SET data=(SELECT data FROM ingress_acme WHERE name=$1) WHERE name=$2`, "a.example", "b.example"); err != nil {
			t.Fatal(err)
		}
		if got, err := caches[0].Get(ctx, "b.example"); err == nil {
			t.Fatalf("b.example opened %q from a.example's value", got)
		}
		if got, err := caches[0].Get(ctx, "a.example"); err != nil || string(got) != "a's key" {
			t.Fatalf("a.example: %q, %v", got, err)
		}
	})
}

// Replicas that renew the same certificate at the same moment each write it, and the
// cache holds one of them whole; and a process with no data-plane master caches
// nothing in the clear, it refuses.
func TestReplicasMayRenewTogetherAndAKeylessProcessRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		stores, caches := edges(t, 3)
		ctx := context.Background()
		var wg sync.WaitGroup
		for i, c := range caches {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.Put(ctx, "example.com", []byte(strings.Repeat(string(rune('a'+i)), 64))); err != nil {
					t.Errorf("put: %v", err)
				}
			}()
		}
		wg.Wait()
		got, err := caches[0].Get(ctx, "example.com")
		if err != nil || len(got) != 64 || strings.Trim(string(got), string(got[:1])) != "" {
			t.Fatalf("the cache holds %q, %v; want one replica's value whole", got, err)
		}

		if _, err := stores[0].acme(nil); err == nil {
			t.Fatal("a process with no master got a cache to write private keys into")
		}
		if _, err := stores[0].acme([]byte("short")); err == nil {
			t.Fatal("a short master got a cache")
		}
	})
}
