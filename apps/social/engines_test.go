package social

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// engines_test.go holds what the port to hanzo-sql changed that no behaviour test
// above reaches by itself.

// Many callers racing to publish one post: exactly one wins the claim. A file's one
// writer made that true by being one; on hanzo-sql it is the UPDATE's own guard.
func TestOneCallerWinsTheClaimOfAPost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if _, err := s.CreatePost(ctx, socialPost{ID: "p", Org: "o", Content: "c", Channel: "x", Status: "scheduled", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		var won atomic.Int32
		var wg sync.WaitGroup
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, ok, err := s.ClaimForPublish(ctx, "o", "p", 5); err != nil {
					t.Error(err)
				} else if ok {
					won.Add(1)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d callers claimed one post, want 1", won.Load())
		}
	})
}

// A consent that names a platform id twice, at once, makes one account: the second
// lands on the first's row. An account recorded by hand under that id keeps the id it
// was given.
func TestAConsentRacingItselfMakesOneAccount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if _, err := s.CreateAccount(ctx, socialAccount{ID: "acct_hand", Org: "o", Provider: "linkedin", Handle: "@old", Status: "disconnected", ExternalID: "by-hand", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.UpsertTarget(ctx, "o", "linkedin", "fresh", "Ada", 5); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		adopted, err := s.UpsertTarget(ctx, "o", "linkedin", "by-hand", "@new", 9)
		if err != nil || adopted.ID != "acct_hand" || adopted.Handle != "@new" || adopted.Status != "connected" || adopted.CreatedAt != 1 || adopted.UpdatedAt != 9 {
			t.Fatalf("the hand-recorded account on consent = %+v, %v; want its own id, the new handle, connected, created at 1", adopted, err)
		}
		all, err := s.ListAccounts(ctx, "o", "linkedin", 10)
		if err != nil || len(all) != 2 {
			t.Fatalf("%d accounts after eight consents to one platform id and one by hand, %v; want 2", len(all), err)
		}
	})
}

// Text an engine cannot keep is refused where it would be stored.
func TestTextAnEngineCannotKeepIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if _, err := s.CreatePost(ctx, socialPost{ID: "p", Org: "o", Content: "a\x00b", Channel: "x", Status: "draft", CreatedAt: 1, UpdatedAt: 1}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("a post holding a NUL = %v, want ErrText", err)
		}
		if _, err := s.UpsertTarget(ctx, "o", "x", "id", "bad\xff", 1); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("a handle that is not UTF-8 = %v, want ErrText", err)
		}
	})
}
