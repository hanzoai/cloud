package integrations

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "integrations.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreOrgScopeIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", ExternalID: "TA", AccountLabel: "Acme", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatalf("upsert acme: %v", err)
	}
	if err := s.Upsert(ctx, Connection{Org: "globex", Provider: "slack", ExternalID: "TG", AccountLabel: "Globex"}); err != nil {
		t.Fatalf("upsert globex: %v", err)
	}

	// Each org sees only its own row.
	if list, err := s.List(ctx, "acme"); err != nil || len(list) != 1 || list[0].ExternalID != "TA" {
		t.Fatalf("acme list must be exactly its own: %v %+v", err, list)
	}
	if c, found, err := s.Get(ctx, "globex", "slack"); err != nil || !found || c.AccountLabel != "Globex" {
		t.Fatalf("globex get: %v found=%v %+v", err, found, c)
	}
	// A provider acme never connected is not-found (no cross-row bleed).
	if _, found, err := s.Get(ctx, "acme", "github"); err != nil || found {
		t.Fatalf("acme github must be not-found, got found=%v err=%v", found, err)
	}
	// Scopes round-trip.
	if c, _, _ := s.Get(ctx, "acme", "slack"); len(c.Scopes) != 1 || c.Scopes[0] != "chat:write" {
		t.Fatalf("scopes round-trip failed: %+v", c.Scopes)
	}
}

func TestStoreUpsertPreservesConnectedAt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", AccountLabel: "v1"}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := s.Get(ctx, "acme", "slack")
	time.Sleep(1100 * time.Millisecond) // cross a unix-second boundary
	if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", AccountLabel: "v2"}); err != nil {
		t.Fatal(err)
	}
	second, _, _ := s.Get(ctx, "acme", "slack")
	if second.AccountLabel != "v2" {
		t.Fatalf("re-connect must update label, got %q", second.AccountLabel)
	}
	if second.ConnectedAt != first.ConnectedAt {
		t.Fatalf("connected_at must be preserved across re-connect: %d != %d", second.ConnectedAt, first.ConnectedAt)
	}
	if second.UpdatedAt < first.UpdatedAt {
		t.Fatalf("updated_at must advance: %d < %d", second.UpdatedAt, first.UpdatedAt)
	}
}

func TestStoreResolveOrgByExternalID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", ExternalID: "T123"})

	if org, ok, err := s.ResolveOrgByExternalID(ctx, "slack", "T123"); err != nil || !ok || org != "acme" {
		t.Fatalf("resolve T123: org=%q ok=%v err=%v", org, ok, err)
	}
	// Unknown external id → not found.
	if _, ok, _ := s.ResolveOrgByExternalID(ctx, "slack", "T999"); ok {
		t.Fatal("unknown external id must not resolve")
	}
	// Empty external id never matches (so scaffold '' rows don't collide).
	if _, ok, _ := s.ResolveOrgByExternalID(ctx, "slack", ""); ok {
		t.Fatal("empty external id must not resolve")
	}
	// Wrong provider, same id → not found (provider-scoped).
	if _, ok, _ := s.ResolveOrgByExternalID(ctx, "github", "T123"); ok {
		t.Fatal("external id is provider-scoped")
	}
}

func TestStoreNonceSingleUse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.PutNonce(ctx, "n1", "acme", "slack"); err != nil {
		t.Fatalf("put nonce: %v", err)
	}
	// First consume with the correct binding succeeds.
	if ok, err := s.ConsumeNonce(ctx, "n1", "acme", "slack"); err != nil || !ok {
		t.Fatalf("first consume must succeed: ok=%v err=%v", ok, err)
	}
	// Second consume (replay) deletes zero rows.
	if ok, _ := s.ConsumeNonce(ctx, "n1", "acme", "slack"); ok {
		t.Fatal("replay consume must return false (single-use)")
	}

	// A nonce consumed under the WRONG org/provider binding must not fire.
	_ = s.PutNonce(ctx, "n2", "acme", "slack")
	if ok, _ := s.ConsumeNonce(ctx, "n2", "globex", "slack"); ok {
		t.Fatal("consume with wrong org must return false")
	}
	if ok, _ := s.ConsumeNonce(ctx, "n2", "acme", "github"); ok {
		t.Fatal("consume with wrong provider must return false")
	}
	// The correct binding still works afterward (the failed attempts didn't burn it).
	if ok, _ := s.ConsumeNonce(ctx, "n2", "acme", "slack"); !ok {
		t.Fatal("correct binding must still consume n2")
	}
}

// TestStoreConsumeNonceConcurrentSingleWinner is the RACE proof for two (here N)
// concurrent OAuth callbacks presenting the SAME valid state: the single atomic
// DELETE ... WHERE nonce AND org AND provider (RowsAffected==1) is the gate, so
// EXACTLY ONE consumer wins and the rest see zero rows — no read-then-delete
// TOCTOU window, no double-exchange. Run under `-race`.
func TestStoreConsumeNonceConcurrentSingleWinner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.PutNonce(ctx, "hot", "acme", "slack"); err != nil {
		t.Fatalf("put nonce: %v", err)
	}
	const racers = 32
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines together to maximize contention
			ok, err := s.ConsumeNonce(ctx, "hot", "acme", "slack")
			if err != nil {
				t.Errorf("consume: %v", err)
				return
			}
			wins <- ok
		}()
	}
	close(start)
	wg.Wait()
	close(wins)
	won := 0
	for ok := range wins {
		if ok {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("exactly one concurrent consumer must win, got %d", won)
	}
}

func TestStoreGCNonces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_ = s.PutNonce(ctx, "old", "acme", "slack")
	// GC everything created before "now+1h" — reaps the just-created nonce.
	n, err := s.GCNonces(ctx, time.Now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if n != 1 {
		t.Fatalf("gc want 1 reaped, got %d", n)
	}
	// It is gone — a later consume finds nothing.
	if ok, _ := s.ConsumeNonce(ctx, "old", "acme", "slack"); ok {
		t.Fatal("gc'd nonce must not consume")
	}
}

func TestStoreDeleteIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, Connection{Org: "acme", Provider: "slack"})
	if gone, err := s.Delete(ctx, "acme", "slack"); err != nil || !gone {
		t.Fatalf("first delete: gone=%v err=%v", gone, err)
	}
	if _, found, _ := s.Get(ctx, "acme", "slack"); found {
		t.Fatal("row must be gone after delete")
	}
	if gone, err := s.Delete(ctx, "acme", "slack"); err != nil || gone {
		t.Fatalf("second delete must report gone=false: gone=%v err=%v", gone, err)
	}
}
