// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/account"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// openLinksOn opens the plan-link store in a directory of its own, on the engine of
// the leg sqltest.Engines is running.
func openLinksOn(t *testing.T) *linkStore {
	t.Helper()
	s, err := openLinks(nil, t.TempDir())
	if err != nil {
		t.Fatalf("openLinks: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// bootLinks does for a test that boots the embed itself what Use does for a process: opens
// the plan-link store the tier and plan reads ask, and puts back what it found.
func bootLinks(t *testing.T) {
	t.Helper()
	old := planLinks.Swap(openLinksOn(t))
	t.Cleanup(func() { planLinks.Store(old) })
}

func linkOf(org string, from account.Account) planLink {
	return planLink{Org: org, From: from, By: "hanzo/james", At: time.Unix(1790000000, 0).UTC()}
}

// A link is read back whole from either side, and the two directions answer alike.
func TestALinkIsReadFromBothEnds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openLinksOn(t)
		ctx := context.Background()
		from := account.Person(account.SignupOrg, "james")
		if err := s.Put(ctx, linkOf("Carbon", from)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		byOrg, ok, err := s.ByOrg(ctx, "carbon")
		if err != nil || !ok {
			t.Fatalf("ByOrg: %v, %v", ok, err)
		}
		bySource, ok, err := s.BySource(ctx, from)
		if err != nil || !ok {
			t.Fatalf("BySource: %v, %v", ok, err)
		}
		if byOrg != bySource {
			t.Fatalf("the two ends disagree: %+v and %+v", byOrg, bySource)
		}
		if byOrg.Org != "carbon" || byOrg.From != from || byOrg.By != "hanzo/james" || !byOrg.At.Equal(time.Unix(1790000000, 0)) {
			t.Fatalf("read back %+v", byOrg)
		}
		if _, ok, _ := s.ByOrg(ctx, "other"); ok {
			t.Fatal("an org that borrows nothing read as linked")
		}
	})
}

// An org borrows one plan, and a plan is lent to one org: the second link of either kind
// is refused for the unique it breaks, and the first is left as it was.
func TestOneLinkPerOrgAndOnePerPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openLinksOn(t)
		ctx := context.Background()
		james, ada := account.Person(account.SignupOrg, "james"), account.Org("acme")
		if err := s.Put(ctx, linkOf("carbon", james)); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, linkOf("carbon", ada)); !errors.Is(err, errOrgLinked) {
			t.Fatalf("a second plan for one org: %v, want errOrgLinked", err)
		}
		if err := s.Put(ctx, linkOf("other", james)); !errors.Is(err, errSourceLinked) {
			t.Fatalf("one plan for a second org: %v, want errSourceLinked", err)
		}
		got, _, _ := s.ByOrg(ctx, "carbon")
		if got.From != james {
			t.Fatalf("a refused link moved the first: %+v", got)
		}
		if _, ok, _ := s.ByOrg(ctx, "other"); ok {
			t.Fatal("a refused link left a row")
		}
	})
}

// A person's wallet and an org's account of the same text are two accounts.
func TestAPersonAndAnOrgAreNotTheSameSource(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openLinksOn(t)
		ctx := context.Background()
		if err := s.Put(ctx, linkOf("a", account.Person("acme", "ada"))); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, linkOf("b", account.Org("acme"))); err != nil {
			t.Fatalf("the org account was taken for the person's wallet: %v", err)
		}
	})
}

// Racing links for one plan land one after the other on the unique: exactly one wins.
func TestRacingLinksOfOnePlanLandOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openLinksOn(t)
		ctx := context.Background()
		from := account.Org("acme")
		var won atomic.Int32
		var wg sync.WaitGroup
		for _, org := range []string{"a", "b", "c", "d", "e", "f"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				switch err := s.Put(ctx, linkOf(org, from)); {
				case err == nil:
					won.Add(1)
				case !errors.Is(err, errSourceLinked):
					t.Errorf("link %s: %v", org, err)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d links of one plan landed, want 1", won.Load())
		}
	})
}

// An unlink ends the link it was read as, says whether it was there, and frees both
// uniques; a link another act replaced since is not the one read and is left.
func TestAnUnlinkFreesTheOrgAndThePlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openLinksOn(t)
		ctx := context.Background()
		from, other := account.Person(account.SignupOrg, "james"), account.Org("acme")
		first := linkOf("carbon", from)
		if ok, err := s.Drop(ctx, first); err != nil || ok {
			t.Fatalf("dropping what is not linked: %v, %v", ok, err)
		}
		if err := s.Put(ctx, first); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.Drop(ctx, linkOf("carbon", other)); err != nil || ok {
			t.Fatalf("dropping a link that was never this one: %v, %v", ok, err)
		}
		if _, ok, _ := s.ByOrg(ctx, "carbon"); !ok {
			t.Fatal("a drop of another plan's link removed this one")
		}
		if ok, err := s.Drop(ctx, linkOf("CARBON", from)); err != nil || !ok {
			t.Fatalf("Drop: %v, %v", ok, err)
		}
		if err := s.Put(ctx, linkOf("other", from)); err != nil {
			t.Fatalf("the plan was not freed: %v", err)
		}
		if ok, _ := s.Drop(ctx, linkOf("other", from)); !ok {
			t.Fatal("the relinked plan did not drop")
		}
		if ok, _ := s.Drop(ctx, linkOf("other", from)); ok {
			t.Fatal("a second drop found a row")
		}
	})
}

// Booting commerce opens the store the plan reads ask: a process that serves the tier op
// and never opened it would answer every org with no plan of its own an error.
func TestUseOpensThePlanLinkStore(t *testing.T) {
	bootRecord(t)
	s, err := links()
	if err != nil {
		t.Fatalf("after Use the plan-link store is not open: %v", err)
	}
	from := account.Org("boot-source")
	if err := s.Put(context.Background(), linkOf("boot-target", from)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok, err := s.BySource(context.Background(), from); err != nil || !ok || got.Org != "boot-target" {
		t.Fatalf("BySource = %+v, %v, %v; want boot-target", got, ok, err)
	}
}
