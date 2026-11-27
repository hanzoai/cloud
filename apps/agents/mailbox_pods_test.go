package agents

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// THREE PODS, ONE MACHINE. The activity that offers a run is on one pod, the
// machine's long poll lands on another, and its report on a third. Each pod has a
// mailbox of its own; they share only the KV.

func eachKV(t *testing.T, f func(t *testing.T, pod func(id string) *coord.Coord)) {
	t.Helper()
	t.Run("in process", func(t *testing.T) { f(t, coordtest.Memory(t)) })
	t.Run("kv", func(t *testing.T) {
		pod := coordtest.Memory(t)
		f(t, pod)
	})
}

func pods(pod func(string) *coord.Coord, n int) []*mailbox {
	out := make([]*mailbox, n)
	for i := range out {
		out[i] = newMailbox()
		out[i].attach(pod("cloud-" + string(rune('0'+i))))
	}
	return out
}

// A machine whose poll lands on another pod claims the run, and its report reaches
// the activity that awaits it, from any pod.
func TestAClaimAndAReportCrossPods(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		m := pods(pod, 3)
		defer func() {
			for _, x := range m {
				x.attach(nil)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		off := m[0].Offer(mkRun("acme", "tgt_1", "sess_1"))
		defer off.Close()
		got, ok := m[1].Claim(ctx, "acme", "tgt_1")
		if !ok || got.SessionID != "sess_1" || got.Branch != "agent/sess_1" {
			t.Fatalf("claim on another pod: ok=%v run=%+v", ok, got)
		}

		done := make(chan RoutedResult, 1)
		go func() {
			res, _ := off.Await(ctx)
			done <- res
		}()
		delivered, err := m[2].Report(ctx, "acme", "tgt_1", "sess_1", RoutedResult{OK: true, CommitSha: "abc"})
		if err != nil || !delivered {
			t.Fatalf("report from a third pod: delivered=%v err=%v", delivered, err)
		}
		select {
		case res := <-done:
			if !res.OK || res.CommitSha != "abc" {
				t.Fatalf("the awaiting activity got %+v", res)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the activity never heard the report")
		}
		// And once: the same report again finds nobody awaiting.
		if again, err := m[2].Report(ctx, "acme", "tgt_1", "sess_1", RoutedResult{OK: true}); err != nil || again {
			t.Fatalf("a second report: delivered=%v err=%v, want a clean no", again, err)
		}
	})
}

// A claim that is already waiting is woken by an offer another pod makes: pushed,
// not found by a timer. The wait is far longer than the test lets pass.
func TestAWaitingClaimIsWokenByAnotherPodsOffer(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		m := pods(pod, 2)
		defer func() {
			for _, x := range m {
				x.attach(nil)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		type claimed struct {
			run RoutedRun
			ok  bool
		}
		out := make(chan claimed, 1)
		go func() {
			run, ok := m[1].Claim(ctx, "acme", "tgt_1")
			out <- claimed{run, ok}
		}()
		time.Sleep(200 * time.Millisecond) // the claim is waiting
		start := time.Now()
		off := m[0].Offer(mkRun("acme", "tgt_1", "sess_1"))
		defer off.Close()
		select {
		case c := <-out:
			if !c.ok || c.run.SessionID != "sess_1" {
				t.Fatalf("woken with ok=%v run=%+v", c.ok, c.run)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("the claim took %v to hear of the offer", took)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a waiting claim never heard of an offer on another pod")
		}
	})
}

// However many pods claim at once, a run goes to one of them.
func TestARunIsClaimedOnceAcrossPods(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		m := pods(pod, 3)
		defer func() {
			for _, x := range m {
				x.attach(nil)
			}
		}()
		for round := range 10 {
			sess := "sess_" + string(rune('a'+round))
			off := m[round%3].Offer(mkRun("acme", "tgt_1", sess))
			var won atomic.Int32
			var wg sync.WaitGroup
			for i := range 6 {
				wg.Go(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
					defer cancel()
					if run, ok := m[i%3].Claim(ctx, "acme", "tgt_1"); ok && run.SessionID == sess {
						won.Add(1)
					}
				})
			}
			wg.Wait()
			off.Close()
			if won.Load() != 1 {
				t.Fatalf("round %d: %d claimers got one run", round, won.Load())
			}
		}
	})
}

// Runs offered on two pods for one machine are both claimed, once each, in order of
// asking.
func TestRunsOnTwoPodsAreBothClaimed(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		m := pods(pod, 3)
		defer func() {
			for _, x := range m {
				x.attach(nil)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a := m[0].Offer(mkRun("acme", "tgt_1", "sess_a"))
		b := m[1].Offer(mkRun("acme", "tgt_1", "sess_b"))
		defer a.Close()
		defer b.Close()
		seen := map[string]bool{}
		for range 2 {
			run, ok := m[2].Claim(ctx, "acme", "tgt_1")
			if !ok {
				t.Fatal("a run offered on another pod was never claimed")
			}
			if seen[run.SessionID] {
				t.Fatalf("%s claimed twice", run.SessionID)
			}
			seen[run.SessionID] = true
		}
		short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
		defer stop()
		if run, ok := m[2].Claim(short, "acme", "tgt_1"); ok {
			t.Fatalf("a third claim got %+v: nothing was left", run)
		}
	})
}

// The tenant and the machine are still the boundary when the pods are many: a claim
// for one cannot reach another's run on another pod, and nor can a report.
func TestTheBoundaryHoldsAcrossPods(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		m := pods(pod, 2)
		defer func() {
			for _, x := range m {
				x.attach(nil)
			}
		}()
		ctx := context.Background()
		foreignOrg := m[0].Offer(mkRun("orgB", "tgt_Y", "sess_foreign_org"))
		foreignMachine := m[0].Offer(mkRun("acme", "tgt_Y", "sess_foreign_machine"))
		mine := m[0].Offer(mkRun("acme", "tgt_X", "sess_mine"))
		defer foreignOrg.Close()
		defer foreignMachine.Close()
		defer mine.Close()

		got, ok := m[1].Claim(ctx, "acme", "tgt_X")
		if !ok || got.SessionID != "sess_mine" {
			t.Fatalf("claim leaked across a boundary: ok=%v run=%+v", ok, got)
		}
		short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
		defer stop()
		if run, ok := m[1].Claim(short, "acme", "tgt_X"); ok {
			t.Fatalf("a foreign run was claimable as acme/tgt_X: %+v", run)
		}
		if ok, _ := m[1].Report(ctx, "acme", "tgt_X", "sess_foreign_machine", RoutedResult{OK: true}); ok {
			t.Fatal("a report crossed the machine boundary")
		}
		if ok, _ := m[1].Report(ctx, "acme", "tgt_Y", "sess_foreign_org", RoutedResult{OK: true}); ok {
			t.Fatal("a report crossed the org boundary")
		}
	})
}

// A report for a run nobody awaits is a clean no, at once and not when a timer ends.
func TestAReportForNoRunIsAnImmediateNo(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		m := pods(pod, 3)
		defer func() {
			for _, x := range m {
				x.attach(nil)
			}
		}()
		start := time.Now()
		ok, err := m[0].Report(context.Background(), "acme", "tgt_1", "nope", RoutedResult{OK: true})
		if err != nil || ok {
			t.Fatalf("delivered=%v err=%v", ok, err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("took %v: it waited for a timer", took)
		}
	})
}
