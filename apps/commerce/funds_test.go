// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/coord"
)

// Credits added, a plan opened and a cap set or dropped each tell the deployment ONCE, at
// the place the change is made, as a typed event naming the org: the deposit in the ledger,
// the plan recorded, the limit row written or removed. A cap on a plan names the plan,
// since every org on the plan reads it; no event names nobody. Nothing is asked of the balance, the plan or the cap to
// find out.
//
// MUTATION: drop the hook at the deposit, the call in the plan record or the call in
// writeLimit/dropLimit, and that change tells nobody.
func TestMoneyAPlanOrACapChangingTellsTheDeployment(t *testing.T) {
	fin := bootRecord(t)
	const org = "tell-acme"
	var mu sync.Mutex
	var heard []funds.Event
	stop := funds.Hear(coord.Default(), func(e funds.Event) { mu.Lock(); heard = append(heard, e); mu.Unlock() }, nil)
	defer stop()
	wait := func(n int) []funds.Event {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			got := slices.Clone(heard)
			mu.Unlock()
			if len(got) >= n || time.Now().After(deadline) {
				return got
			}
			time.Sleep(time.Millisecond)
		}
	}

	if _, err := fin.Deposit(t.Context(), money.DepositInput{Org: org, Subject: org, Amount: money.FromCents(500), Ref: "pay-tell"}); err != nil {
		t.Fatal(err)
	}
	if got := wait(1); !slices.Equal(got, []funds.Event{{Org: org, Kind: funds.Credit}}) {
		t.Fatalf("a deposit told %v, want a credit for %s", got, org)
	}
	if _, err := planeSubscriptionRecord(caller(org, true), paidFor(t, org)); err != nil {
		t.Fatal(err)
	}
	set := func(scope string, value int64) {
		t.Helper()
		if _, err := planeLimitSet(staff(), &client.LimitSet{Key: client.LimitSandboxRunning, Scope: scope, Value: value, Reason: "agency grew"}); err != nil {
			t.Fatal(err)
		}
	}
	set("org:"+org, 500)
	set("plan:go", 300)
	if _, err := planeLimitDrop(staff(), &client.LimitDrop{Key: client.LimitSandboxRunning, Scope: "org:" + org}); err != nil {
		t.Fatal(err)
	}
	want := []funds.Event{
		{Org: org, Kind: funds.Credit}, {Org: org, Kind: funds.Plan},
		{Org: org, Kind: funds.Cap}, {Plan: "go", Kind: funds.Cap}, {Org: org, Kind: funds.Cap},
	}
	if got := wait(len(want)); !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
}
