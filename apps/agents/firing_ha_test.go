package agents

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/twopod"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// billedReplica is replica with the run fee metered to the shared fake commerce.
func billedReplica(t *testing.T, p *twopod.Pod, ai *countingAI, bill *cloud.Meter, seed ...Agent) *scheduler {
	t.Helper()
	sc := replica(t, p, ai, seed...)
	sc.s.State.bill = bill
	return sc
}

// movingOrg is an org the election hands to pod-b the moment pod-b joins pod-a.
func movingOrg(t *testing.T) string {
	t.Helper()
	both := []tenure.Member{{ID: "pod-a", Addr: "pod-a"}, {ID: "pod-b", Addr: "pod-b"}}
	for i := range 64 {
		o := "acme" + strconv.Itoa(i)
		if m, ok := tenure.Owner(o, both); ok && m.ID == "pod-b" {
			return o
		}
	}
	t.Fatal("no org moves to pod-b; the election changed")
	return ""
}

// ONE DUE MINUTE IS ONE RUN AND ONE CHARGE, WHICHEVER PODS TICK IT.
//
// The hazard is a handover, not the steady state. pod-a owned the org; pod-b joins
// and the election moves the org to it. pod-a goes on believing it owns the org
// until a ship of its own is refused, and its ticker is not aligned to pod-b's, so
// both tick the same minute. Then pod-b restarts inside that minute and ticks it
// again. Every one of those ticks asked the in-memory schedule and nothing that
// survives a pod, so each pod ran the agent and each charged its fee: the customer
// paid twice for one minute.
//
// A firing is now claimed in the org's own store and the claim is shipped, fenced,
// before the agent runs: the deposed pod's ship is refused, and a pod that ticks a
// minute already claimed finds the claim on the copy it hydrated. The fee is keyed
// on the firing, so the ledger takes it once even from two runs.
func TestADueMinuteIsOneRunAndOneChargeAcrossAHandover(t *testing.T) {
	ctx := context.Background()
	o := movingOrg(t)
	bs := &billServer{available: 100000}
	m, err := meter.New(meter.Config{BaseURL: bs.start(t)})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	bill := cloud.NewMeter(cloud.Deps{Metering: m}, meterKind)
	ai := &countingAI{}

	f := twopod.New(t, "pod-a")
	a := billedReplica(t, f.Pod("pod-a"), ai, bill, longRunning(o, "cron", "*/5 * * * *"))
	ns, err := principal.OrgNamespace(o, "")
	if err != nil {
		t.Fatal(err)
	}
	if acked, err := a.s.State.stores.Sync(ns); err != nil || !acked {
		t.Fatalf("pod-a could not ship %s: acked=%v err=%v", o, acked, err)
	}

	f.Start("pod-b")
	b := billedReplica(t, f.Pod("pod-b"), ai, bill)
	if _, err := b.s.State.storeFor(o); err != nil {
		t.Fatalf("pod-b could not open %s: %v", o, err)
	}
	if !b.s.State.owns(o) {
		t.Fatalf("pod-b, the elected owner of %s, does not hold it", o)
	}

	due := at(t, "2026-07-01 12:35")
	a.tick(ctx, due) // the deposed owner, still believing
	b.tick(ctx, due) // the new owner
	waitFor(func() bool { return ai.count() >= 2 })
	a.wg.Wait()
	b.wg.Wait()

	// pod-b restarts on a fresh volume and ticks the same minute again.
	f.Stop("pod-b")
	f.Start("pod-b")
	b2 := billedReplica(t, f.Pod("pod-b"), ai, bill)
	if _, err := b2.s.State.storeFor(o); err != nil {
		t.Fatalf("restarted pod-b could not open %s: %v", o, err)
	}
	b2.tick(ctx, due)
	waitFor(func() bool { return ai.count() >= 2 })
	b2.wg.Wait()

	if n := ai.count(); n != 1 {
		t.Fatalf("one due minute ran the agent %d times across a handover and a restart, want 1", n)
	}
	if !waitForDebit(func() bool { return bs.debits() >= 1 }) {
		t.Fatal("the one run was never charged")
	}
	time.Sleep(50 * time.Millisecond)
	if n := bs.debits(); n != 1 {
		t.Fatalf("one due minute charged %d fees, want 1", n)
	}
	_, body := bs.lastDebit()
	var u struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(body, &u)
	if want := "agent_" + firingRun(longRunning(o, "cron", "*/5 * * * *"), due); u.Ref != want {
		t.Fatalf("the fee is keyed %q, want %q: the firing, so a second run of it charges nothing", u.Ref, want)
	}

	// The next due minute runs, on the owner: the claim stops a repeat, not the agent.
	b2.tick(ctx, at(t, "2026-07-01 12:40"))
	if !waitFor(func() bool { return ai.count() == 2 }) {
		t.Fatalf("the next due minute did not run on the new owner: %d runs", ai.count())
	}
	b2.wg.Wait()
}
