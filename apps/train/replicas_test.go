package train

// Train runs on every replica. These tests stand a second replica beside the first: on
// hanzo-sql a second service with stores and a lock of its own over the same schemas,
// which is what another pod is; on SQLite the same service, because a file belongs to
// one process.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

// replicaOf is another replica's service beside r, over the same books and bucket.
func replicaOf(t *testing.T, r *rig) *rig {
	t.Helper()
	if !dialect.Of(r.store("acme").db).Postgres() {
		return r
	}
	other := mount(t, "")
	other.books, other.objs = r.books, r.objs
	other.st.afford, other.st.debit, other.st.objects = r.books.afford, r.books.debit, r.objs
	return other
}

// claimStatus is the platform executor's claim on r, answered with its status.
func claimStatus(r *rig) (int, error) {
	b, _ := json.Marshal(platformClaim("cuda"))
	req := httptest.NewRequest(http.MethodPost, "/v1/train/jobs/claim", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "u-hanzo")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// meeting makes the first two weighings wait for each other, so each replica has
// weighed the wallet before either has held a window: the moment two replicas could
// each see the other's hold as absent. A replica alone at the gate (the other held out
// by the lock) waits a moment and goes on.
func meeting(b *books) {
	var mu sync.Mutex
	var n int
	met := make(chan struct{})
	b.mu.Lock()
	b.meet = func() {
		mu.Lock()
		n++
		first := n <= 2
		if n == 2 {
			close(met)
		}
		mu.Unlock()
		if first {
			select {
			case <-met:
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	b.mu.Unlock()
}

// One wallet that pays in two orgs, claimed on at once by two replicas, holds one window
// and not two: each replica owns one org's store, and the wallet is the same.
func TestTwoReplicasHoldOneWalletsWindowOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		first := mount(t, "")
		pin(t)
		second := replicaOf(t, first)
		first.books.balance = gpu(windowSeconds) + 1
		for _, org := range []string{"acme", "beta"} {
			if code, m := first.asIn("POST", "/v1/train/jobs", org, "", "admin", true, hanzoJob(map[string]any{})); code != 200 {
				t.Fatalf("create in %s: %d %v", org, code, m)
			}
		}
		// Each replica sweeps the org it owns: the wallet is the SuperAdmin's, in both.
		if !first.st.stores.Owned(mustNS(t, "acme")) || !second.st.stores.Owned(mustNS(t, "beta")) {
			t.Fatal("the replicas do not own one org each")
		}

		meeting(first.books)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, r := range []*rig{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, err := claimStatus(r)
				if err != nil {
					t.Errorf("claim: %v", err)
				}
				codes[i] = code
			}()
		}
		wg.Wait()
		var held, claimed int64
		for _, org := range []string{"acme", "beta"} {
			n, err := first.store(org).outstanding(t.Context(), "org:admin")
			if err != nil {
				t.Fatal(err)
			}
			held += n
		}
		for _, code := range codes {
			if code == 200 {
				claimed++
			}
		}
		if held > first.books.balance || claimed != 1 {
			t.Fatalf("%d claims were answered (%v), holding %d µ$ of one wallet against a balance of %d", claimed, codes, held, first.books.balance)
		}
	})
}
