package agents

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/twopod"
	luxlog "github.com/luxfi/log"
)

// replica is one pod's scheduler over its own volume and its own durability
// against the shared object store. Peers says what a fleet is — more than one
// writer — which is the whole reason the ownership gate has anything to decide.
func replica(t *testing.T, p *twopod.Pod, ai *countingAI, seed ...Agent) *scheduler {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", p.Dir)
	b := cloud.NewBase(cloud.Deps{Durable: p.Durable, Peers: true}, "agents")
	b.Log = luxlog.NewNoOpLogger()
	stores := cloud.NewOrgStore(b, "agents", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })

	s := &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{stores: stores, ai: ai},
	}
	for _, a := range seed {
		if err := storeOf(t, &s.State, a.Org).Create(context.Background(), a); err != nil {
			t.Fatalf("seed %s/%s: %v", a.Org, a.Name, err)
		}
	}
	return newScheduler(s, luxlog.NewNoOpLogger())
}

// TestOneDueAgentIsOneRunOnTwoPods is why allLongRunning asks who owns the org.
//
// Both replicas hold the org's store, and both run the scheduler — which is what
// the deployment does, since every pod mounts every subsystem and every sweep is
// started by Mount. Running an agent is an OUTBOUND act, so it may happen once.
//
// MUTATION: delete the `if !st.stores.Owned(ns) { continue }` line in
// allLongRunning and this fails with two runs for one scheduled agent — the
// customer's model called twice, and billed twice, for one tick.
func TestOneDueAgentIsOneRunOnTwoPods(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"
	ai := &countingAI{}

	seed := longRunning(org, "cron", "*/5 * * * *")
	owner := replica(t, f.Owner(org), ai, seed)
	other := replica(t, f.Other(org), ai, seed)

	// A minute the cron matches, on both pods, as the fleet ticks it.
	when := at(t, "2026-07-01 12:35")
	owner.tick(context.Background(), when)
	other.tick(context.Background(), when)

	if !waitFor(func() bool { return ai.count() >= 1 }) {
		t.Fatal("the owning replica did not run the due agent at all")
	}
	if n := ai.count(); n != 1 {
		t.Fatalf("two pods ticked one due agent and produced %d runs, want 1", n)
	}
}
