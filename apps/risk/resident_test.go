package risk

// resident_test.go covers the residency: admission under a bound, reclaim driven
// only by a tenant's OWN idleness, and the reload that makes a disarmed model
// impossible to mistake for a warming one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// TestAFullNodeRefusesRatherThanEvicts pins the admission rule.
//
// When a pod is out of room there are two things it can do: turn the newcomer
// away, or take an incumbent's state. The second is the defect — silent, and
// aimed at whoever happens to be quietest — so this asserts the first, and
// asserts the incumbent is untouched afterwards.
func TestAFullNodeRefusesRatherThanEvicts(t *testing.T) {
	t.Setenv(envTenantMax, "1")
	_, s := wireApp(t)

	a := Tenant("hanzo/acme")
	first, err := s.State.res.of(a, s.Log)
	if err != nil {
		t.Fatalf("the first tenant was refused: %v", err)
	}
	vel, model, _ := first.arms()
	record(vel, a, observation{at: time.Now(), kind: "account", subject: "keep-me", amount: 1})

	if _, err := s.State.res.of(Tenant("hanzo/beta"), s.Log); err == nil {
		t.Fatal("a second tenant was admitted past the armed ceiling — something was evicted to make room")
	}

	// The incumbent still has everything.
	vel2, model2, _ := first.arms()
	if vel2 != vel || model2 != model {
		t.Fatal("the incumbent's planes were replaced by the refused admission")
	}
	if vel2.Keys() == 0 {
		t.Fatal("the incumbent's aggregates were dropped to make room for a tenant that was refused anyway")
	}

	// And the refusal is LOUD: the probe goes degraded and names it.
	if _, refused, _ := s.State.res.count(); refused == 0 {
		t.Fatal("the refusal was not counted, so nothing pages an operator about a node that is out of room")
	}
}

// TestTheCeilingIsAWorkingSetAndNotAHighWaterMark.
//
// THE DEFECT: reclaim dropped a tenant's aggregates but KEPT its cell, and the
// admission bound counted cells. So the count only ever went up: once tenantMax
// distinct tenants had passed through, the next one was refused for the life of
// the process — with the pod completely idle and nothing to reclaim. A ceiling
// that a tenant can raise by leaving is not a ceiling, it is a fuse.
func TestTheCeilingIsAWorkingSetAndNotAHighWaterMark(t *testing.T) {
	t.Setenv(envTenantMax, "2")
	_, s := wireApp(t)

	for _, name := range []Tenant{"hanzo/one", "hanzo/two"} {
		if _, err := s.State.res.of(name, s.Log); err != nil {
			t.Fatalf("%s was refused: %v", name, err)
		}
	}
	if n, _, _ := s.State.res.count(); n != 2 {
		t.Fatalf("the node holds %d tenants, want 2", n)
	}
	// Both go silent. Retirement is driven by their OWN idleness.
	for _, name := range []Tenant{"hanzo/one", "hanzo/two"} {
		r := resOf(t, s, name)
		r.mu.Lock()
		r.touched = time.Now().Add(-24 * time.Hour)
		r.mu.Unlock()
	}
	s.State.res.sweep(s.Log)
	if n, _, _ := s.State.res.count(); n != 0 {
		t.Fatalf("after both tenants went silent the node still holds %d cells — the map only grows, so the ceiling is a fuse", n)
	}

	// And the room really is usable: two NEW tenants are admitted.
	for _, name := range []Tenant{"hanzo/three", "hanzo/four"} {
		if _, err := s.State.res.of(name, s.Log); err != nil {
			t.Fatalf("%s was refused after the node emptied: %v", name, err)
		}
	}
	if _, refused, _ := s.State.res.count(); refused != 0 {
		t.Fatalf("%d admissions were refused by a node with room", refused)
	}
}

// TestRetirementLosesNothingDurable. Retiring closes a tenant's file, so the
// test that matters is not that memory came back but that the tenant did: its
// rules, its decisions and its learned state must all still be there on the
// request that brings it home.
func TestRetirementLosesNothingDurable(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")

	code, body := req(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"name":"keep me","stage":"signup","action":"review","weight":0.5,"enabled":true,`+
			`"all":[{"field":"subject.kind","op":"eq","value":"account"}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("rule = %d %s", code, body)
	}
	feed(t, s, tn, 60)
	before := resOf(t, s, tn)
	_, model, _ := before.arms()
	learned := model.State(tn.String()).Learned
	if learned == 0 {
		t.Fatal("the tenant learned nothing, so a reload proves nothing")
	}

	before.mu.Lock()
	before.touched = time.Now().Add(-24 * time.Hour)
	before.mu.Unlock()
	s.State.res.sweep(s.Log)
	if n, _, _ := s.State.res.count(); n != 0 {
		t.Fatalf("the silent tenant was not retired (%d cells)", n)
	}

	// It comes home on its next request, through the ordinary door.
	code, body = req(t, app, http.MethodGet, "/v1/risk/rules", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("a retired tenant's rules read %d %s — retiring closed a file it could not reopen", code, body)
	}
	if !strings.Contains(string(body), "keep me") {
		t.Fatalf("the rule did not survive the retirement: %s", body)
	}
	_, back, _ := resOf(t, s, tn).arms()
	if got := back.State(tn.String()).Learned; got != learned {
		t.Fatalf("the retired tenant came back having learned %d of %d — retirement is losing state, which is eviction with a nicer name", got, learned)
	}
}

// TestConcurrentAdmissionDoesNotStall is the regression for a lock order that
// deadlocked.
//
// THE DEFECT: arming took the residency lock and then a cell's lock, and called
// the sweep — which locks EVERY cell — from inside both. The first tenant to arm
// while the node was at its ceiling took its own cell's lock twice and the whole
// process stopped deciding, for everyone, with no error and no log.
//
// Concurrency is the only way to catch it and `go test -race -timeout` is the
// assertion: a deadlock here does not fail, it hangs.
func TestConcurrentAdmissionDoesNotStall(t *testing.T) {
	t.Setenv(envTenantMax, "8")
	_, s := wireApp(t)

	const workers, each = 16, 12
	var wg sync.WaitGroup
	var admitted, refused atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Deliberately more distinct tenants than the ceiling, so the
				// admission path runs its sweep-then-refuse arm under contention.
				tn := Tenant(fmt.Sprintf("hanzo/t%d", (w*each+i)%12))
				if _, err := s.State.res.of(tn, s.Log); err != nil {
					refused.Add(1)
					continue
				}
				admitted.Add(1)
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent admission did not finish — the residency deadlocked, which stops every tenant's decisions at once")
	}
	if admitted.Load() == 0 {
		t.Fatal("nothing was admitted at all")
	}
	// Whatever the split, the node never holds more than it promised.
	if n, _, _ := s.State.res.count(); n > tenantMax() {
		t.Fatalf("the node holds %d tenants against a ceiling of %d", n, tenantMax())
	}
	t.Logf("admitted %d, refused %d, resident %d", admitted.Load(), refused.Load(), func() int { n, _, _ := s.State.res.count(); return n }())
}

// TestReclaimIsDrivenOnlyByATenantsOwnIdleness pins the difference between
// reclaim and eviction. The trigger must be the reclaimed tenant's own silence —
// never another tenant's arrival, and never memory pressure, because both make
// one tenant's traffic the reason another tenant's control weakened.
func TestReclaimIsDrivenOnlyByATenantsOwnIdleness(t *testing.T) {
	_, s := wireApp(t)
	a, b := Tenant("hanzo/acme"), Tenant("hanzo/beta")

	ra, rb := resOf(t, s, a), resOf(t, s, b)
	avel, _, _ := ra.arms()
	bvel, _, _ := rb.arms()
	record(avel, a, observation{at: time.Now(), kind: "account", subject: "a1", amount: 1})
	record(bvel, b, observation{at: time.Now(), kind: "account", subject: "b1", amount: 1})

	// A is made to look silent; B was touched just now.
	ra.mu.Lock()
	ra.touched = time.Now().Add(-24 * time.Hour)
	ra.mu.Unlock()

	s.State.res.mu.Lock()
	s.State.res.retireLocked(time.Now(), time.Hour, s.Log)
	s.State.res.mu.Unlock()

	if v, m, _ := ra.arms(); v != nil || m != nil {
		t.Fatal("the silent tenant was not reclaimed")
	}
	if v, _, _ := rb.arms(); v == nil {
		t.Fatal("a tenant that was active a moment ago had its aggregates reclaimed — the trigger is not its own idleness")
	}
	if v, _, _ := rb.arms(); v.Keys() == 0 {
		t.Fatal("the active tenant's counters are gone")
	}
}

// TestAReclaimedTenantComesBackWithWhatItLearned is the regression for the
// silent-disarm defect.
//
// THE DEFECT: the process latched "this tenant has been restored" in a map that
// OUTLIVED the model. When the shared store's LRU dropped a tenant's model, the
// latch still said restored, so it was never reloaded — the tenant scored nothing
// for the rest of the process's life while reporting only "warming".
//
// THE FIX IS STRUCTURAL: the latch IS the model. A cell with no model has no
// latch, so the only way to come back is through the path that reloads.
func TestAReclaimedTenantComesBackWithWhatItLearned(t *testing.T) {
	_, s := wireApp(t)
	tn := Tenant("hanzo/acme")

	feed(t, s, tn, 60)
	r := resOf(t, s, tn)
	_, model, _ := r.arms()
	learned := model.State(tn.String()).Learned
	if learned == 0 {
		t.Fatal("the tenant learned nothing, so this test cannot tell a reload from a fresh start")
	}
	if err := keep(r.db, tn, model); err != nil {
		t.Fatalf("keep: %v", err)
	}

	// Reclaim it, exactly as the idle sweep would.
	r.mu.Lock()
	r.touched = time.Now().Add(-24 * time.Hour)
	r.mu.Unlock()
	s.State.res.mu.Lock()
	s.State.res.retireLocked(time.Now(), time.Hour, s.Log)
	s.State.res.mu.Unlock()
	if v, _, _ := r.arms(); v != nil {
		t.Fatal("reclaim did not disarm")
	}

	// It comes back with what it knew.
	back := resOf(t, s, tn)
	_, model2, _ := back.arms()
	if model2 == nil {
		t.Fatal("the tenant did not re-arm")
	}
	if got := model2.State(tn.String()).Learned; got != learned {
		t.Fatalf("the model came back having learned %d of %d — a reclaimed tenant is silently back to warming", got, learned)
	}
	if back.grade(RefusalWarming) == RefusalDisarmed {
		t.Fatal("a correctly reloaded tenant is being reported as disarmed")
	}
}

// TestLostLearnedStateIsCalledDisarmedAndNotWarming pins the loud half.
//
// Warming is a control coming up. Disarmed is a control that is OFF. They are the
// same bytes on the wire unless they are different words, and reporting the second
// as the first is exactly how a control stays off with nobody looking.
func TestLostLearnedStateIsCalledDisarmedAndNotWarming(t *testing.T) {
	dir := t.TempDir()
	deps := cloud.Deps{Logger: luxlog.New("risktest"), DataDir: dir, Brand: "hanzo"}

	// First process: learn something and pin it.
	one, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	tn := Tenant("hanzo/acme")
	feed(t, one, tn, 60)
	r := resOf(t, one, tn)
	_, model, _ := r.arms()
	snap, ok := model.Snapshot(tn.String())
	if !ok {
		t.Fatal("the tenant learned nothing, so there is no state to lose")
	}
	_, _ = one.State.res.close(one.Log)
	one.State.runs.stop()

	// Damage the pinned state the way a shape change or a bad write would: the
	// row is there and says a great deal was learned, and the engine will refuse
	// it. The tenant HAD a model and the next process cannot have it.
	//
	// AFTER the shutdown, deliberately: shutdown's whole job is to snapshot every
	// resident tenant, so damaging the file first would simply be overwritten by
	// the good state on the way down — and the test would then be asserting that
	// a healthy model is disarmed, which it is not.
	snap.Digest = "not-this-shape"
	body, err := encodeSnapshot(snap)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	db, err := cloud.OrgDB(dir, cloud.MustOrgNamespace(tn.org(), ""), "risk")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := putModel(db, snapshotKey, body); err != nil {
		t.Fatalf("putModel: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second process over the same directory — the rollout.
	two, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _, _ = two.State.res.close(two.Log); two.State.runs.stop() })
	back := resOf(t, two, tn)
	if got := back.grade(RefusalWarming); got != RefusalDisarmed {
		t.Fatalf("a model whose learned state was refused reports %q — a control that is OFF is reporting itself as one that is coming up", got)
	}
	if got := back.grade(RefusalUnidentified); got != RefusalUnidentified {
		t.Fatalf("grading rewrote an unrelated refusal to %q", got)
	}
}

// TestARolloutDoesNotSilentlyResetEveryTenant pins the durability contract. cloud
// deploys strategy Recreate at one replica, so every deploy drops the process; a
// model that comes back with nothing learned declines to score for its whole warm
// period, and reads as clean to anything that does not check the refusal.
func TestARolloutDoesNotSilentlyResetEveryTenant(t *testing.T) {
	dir := t.TempDir()
	deps := cloud.Deps{Logger: luxlog.New("risktest"), DataDir: dir, Brand: "hanzo"}
	tn := Tenant("hanzo/acme")

	one, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	feed(t, one, tn, 60)
	_, model, _ := resOf(t, one, tn).arms()
	learned := model.State(tn.String()).Learned
	if learned == 0 {
		t.Fatal("nothing was learned, so the restart proves nothing")
	}
	// Shutdown is the snapshot: nothing else runs on a rollout.
	kept, failed := one.State.res.close(one.Log)
	one.State.runs.stop()
	if kept != 1 || failed != 0 {
		t.Fatalf("shutdown kept %d and lost %d models", kept, failed)
	}

	two, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _, _ = two.State.res.close(two.Log); two.State.runs.stop() })
	_, back, since := resOf(t, two, tn).arms()
	if got := back.State(tn.String()).Learned; got != learned {
		t.Fatalf("after a restart the model has learned %d of %d", got, learned)
	}
	// The AGGREGATES do not survive — they are not durable — and the decision
	// says so rather than presenting a ten-minute ring as a thirty-day count.
	if since.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("the aggregates claim to have been running since %s across a restart", since)
	}
}

// TestTheProbeGoesDegradedWhenTheNodeIsFull pins that capacity is an ALARM. A
// counter nobody reads is how a pod quietly stops protecting new tenants.
func TestTheProbeGoesDegradedWhenTheNodeIsFull(t *testing.T) {
	t.Setenv(envTenantMax, "1")
	app, s := wireApp(t)

	code, _ := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("a healthy node probes %d, want 200", code)
	}
	if _, err := s.State.res.of(Tenant("hanzo/acme"), s.Log); err != nil {
		t.Fatalf("first tenant: %v", err)
	}
	if _, err := s.State.res.of(Tenant("hanzo/beta"), s.Log); err == nil {
		t.Fatal("the second tenant was admitted")
	}

	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a node that has refused a tenant probes %d, want 503 — nothing pages an operator", code)
	}
	var report map[string]any
	_ = json.Unmarshal(body, &report)
	if report["status"] != "degraded" {
		t.Fatalf("probe status = %v, want degraded", report["status"])
	}
	if report["refused"] == nil || report["tenant_max"] == nil {
		t.Fatalf("the probe does not carry the capacity facts: %s", body)
	}
}
