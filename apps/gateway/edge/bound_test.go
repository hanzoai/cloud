package edge

// The bound's own contract. Four properties, and each one is a defect that came
// back a layer down after the last one was fixed:
//
//	IDENTITY   — a caller is what the SERVER attested, never a string the caller
//	             picked. Rotating an unvalidated credential must not produce a new
//	             caller, must not leave a hold, and must not open a table entry.
//	ADMISSION  — nothing is ever removed to make room for something else. A flood
//	             is refused; it does not evict, and it cannot release a verdict.
//	BYTES      — the published per-entry ceilings are ceilings. Measured, not
//	             asserted.
//	LOUDNESS   — a ceiling that binds says so, in a graded state an operator can
//	             read, including for the lane that has no tenant.

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// REGRESSION — enforcement was keyed on the raw Authorization value, validated or
// not, which is a string the ATTACKER PICKS. A caller held under a block verdict
// walked out of it five times out of five by changing that header and nothing
// else, so presenting garbage was strictly better for an attacker than presenting
// nothing at all.
func TestTraffic_AnUnvalidatedCredentialIsNotAnIdentity(t *testing.T) {
	tr := NewTraffic()
	const addr = "203.0.113.200"

	first := forged("", "junk-000000", addr, "/v1/models")
	tr.Observe(first, t0)
	tr.Hold(first, Hold{Action: "block", Reason: "peers", Decision: "d-1"}, time.Minute, t0)

	for i := 1; i <= 5; i++ {
		next := forged("", fmt.Sprintf("junk-%06d", i), addr, "/v1/models")
		if _, ok := tr.Held(next, t0.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("attempt %d: the hold was evaded by presenting a different credential", i)
		}
	}

	// One address is one caller however many credentials it invents, so the
	// counts accumulate against it instead of resetting per request...
	var p Pattern
	for i := range 40 {
		p = tr.Observe(forged("", fmt.Sprintf("junk-%06d", i), addr, "/v1/models"), t0)
	}
	if p.Requests < 40 {
		t.Fatalf("requests = %d after 41 requests from one address, want at least 40", p.Requests)
	}
	// ...and it costs the sensor ONE key, not one per request.
	tr.mu.Lock()
	n := len(tr.tenants[""].callers.m)
	tr.mu.Unlock()
	if n != 1 {
		t.Fatalf("one address opened %d caller keys; a caller may not choose its own key", n)
	}

	// The credentials it presented are still counted — as SPREAD, which is the
	// stuffing signature the sensor exists to see.
	if p.Peers < 8 {
		t.Fatalf("peers = %d after 40 distinct credentials from one address, want at least 8", p.Peers)
	}

	// A VALIDATED credential is still an identity of its own: this is not "keys
	// stop mattering", it is "only a key we issued names a caller".
	a := sig("acme", "fpA", addr, "/v1/models")
	b := sig("acme", "fpB", addr, "/v1/models")
	tr.Hold(a, Hold{Action: "block"}, time.Minute, t0)
	if _, ok := tr.Held(b, t0); ok {
		t.Fatal("a hold on one validated credential reached another")
	}
}

// REGRESSION — the shared lane erased its own callers. org=="" is ONE scope for
// the whole internet, and a flood of forged credentials from one address used to
// reclaim every tracked caller in it: 200 of 200 lost their accumulated state,
// which made every pattern the sensor watches for resettable on demand.
func TestTraffic_AFloodCannotEraseAnotherCaller(t *testing.T) {
	tr := NewTraffic()

	// 200 anonymous callers, each with a history worth erasing.
	for i := range 200 {
		s := forged("", "", fmt.Sprintf("198.51.%d.%d", i/256, i%256), "/v1/models")
		for range 10 {
			tr.Observe(s, t0)
		}
	}
	// A hundred thousand requests from one address, each inventing a credential.
	for i := range 100_000 {
		tr.Observe(forged("", fmt.Sprintf("forged-%06d", i), "203.0.113.9", "/v1/chat"), t0)
	}

	lost := 0
	for i := range 200 {
		s := forged("", "", fmt.Sprintf("198.51.%d.%d", i/256, i%256), "/v1/models")
		if p := tr.Observe(s, t0); p.Requests < 11 {
			lost++
		}
	}
	if lost != 0 {
		t.Fatalf("%d of 200 tracked callers lost their state to another caller's flood", lost)
	}
}

// REGRESSION — the ceiling could be overrun. reclaim skipped keys under a live
// verdict and then admitted anyway, so 25,000 held callers lived in a table that
// published a ceiling far below that, and the report said nothing.
//
// The rule now has no second pass: what is live stays, what does not fit is
// refused, and the refusal is on the scope's own report.
func TestTraffic_TheCeilingRefusesRatherThanOverrunning(t *testing.T) {
	tr := NewTraffic()
	held := make([]Signal, 0, maxCallers+5_000)
	for i := range maxCallers + 5_000 {
		s := sig("acme", fmt.Sprintf("fp%06d", i), "203.0.113.1", "/v1/models")
		tr.Observe(s, t0)
		tr.Hold(s, Hold{Action: "block", Reason: "peers", Decision: "d"}, time.Minute, t0)
		held = append(held, s)
	}

	tr.mu.Lock()
	n := len(tr.tenants["acme"].callers.m)
	tr.mu.Unlock()
	if n > maxCallers {
		t.Fatalf("the table holds %d keys against a %d ceiling", n, maxCallers)
	}

	// Not one admitted verdict was released to make room for a later one.
	live := 0
	for _, s := range held {
		if _, ok := tr.Held(s, t0.Add(time.Second)); ok {
			live++
		}
	}
	if live != n {
		t.Fatalf("%d verdicts are held but the table holds %d keys: a hold was dropped, not refused", live, n)
	}

	v := tr.View("acme", ModeLive, t0)
	if v.Strain != StrainRefuse || v.Refused == 0 {
		t.Fatalf("a ceiling that refused %d callers must say so: strain=%q refused=%d",
			maxCallers+5_000-n, v.Strain, v.Refused)
	}
	if v.Tracked != n || v.Ceiling != maxCallers {
		t.Fatalf("the report must state occupancy against the ceiling: tracked=%d ceiling=%d (holding %d)",
			v.Tracked, v.Ceiling, n)
	}
}

// A caller the ceiling turned away is UNMEASURED, and says so on the observation
// itself — otherwise its zero counts read as "a caller making its first request",
// which is the one pattern that screens every single time.
func TestTraffic_ARefusedCallerIsToldItIsUnmeasured(t *testing.T) {
	tr := NewTraffic()
	for i := range maxCallers {
		tr.Observe(sig("acme", fmt.Sprintf("fp%06d", i), "203.0.113.1", "/v1/models"), t0)
	}
	p := tr.Observe(sig("acme", "fpOverflow", "203.0.113.1", "/v1/models"), t0)
	if p.Strain != StrainRefuse {
		t.Fatalf("strain = %q for a caller the ceiling refused, want %q", p.Strain, StrainRefuse)
	}
	if p.Requests != 0 {
		t.Fatalf("an unmeasured caller must report no counts, got %+v", p)
	}
	// The request is still counted in the scope's totals: a scope's own report
	// must never understate its traffic because the sensor ran out of room.
	if v := tr.View("acme", ModeLive, t0); v.Requests != maxCallers+1 {
		t.Fatalf("scope requests = %d, want %d", v.Requests, maxCallers+1)
	}
}

// The grade is announced ONCE per rise, not once per refused request: a flood
// produces one refusal per request, and a log line per request is an outage of
// its own.
func TestTraffic_StrainRisesOnce(t *testing.T) {
	tr := NewTraffic()
	rises := map[string]int{}
	for i := range maxCallers + 100 {
		if r := tr.Observe(sig("acme", fmt.Sprintf("fp%06d", i), "203.0.113.1", "/v1/models"), t0).Rise; r != "" {
			rises[r]++
		}
	}
	if rises[StrainFull] != 1 || rises[StrainRefuse] != 1 {
		t.Fatalf("each grade must be announced exactly once, got %v", rises)
	}
}

// The lane with no tenant is the one a bad bot calls from, and its saturation was
// readable by nobody: the report could only be asked for by NAME, and this scope
// has no name. It is the empty scope, and the empty scope is a value.
func TestTraffic_TheAnonymousLaneIsReadable(t *testing.T) {
	tr := NewTraffic()
	for i := range maxCallers + 50 {
		tr.Observe(forged("", "x", fmt.Sprintf("198.51.%d.%d", i/256, i%256), "/v1/models"), t0)
	}
	v := tr.View("", ModeLive, t0)
	if v.Strain != StrainRefuse || v.Refused == 0 || v.Tracked == 0 {
		t.Fatalf("the anonymous lane's own saturation must be readable: %+v", v)
	}
	if v.Requests == 0 || v.Lanes[AgencyUnknown]+v.Lanes[AgencyBot] == 0 {
		t.Fatalf("the anonymous lane's own traffic must be readable: %+v", v)
	}
}

// The lane split is DERIVED from the counts, so it can only be computed inside
// the observation that produced them. It used to be passed IN — before those
// counts existed — so every request ever counted landed in "unknown" and the one
// number this report exists for was a constant.
func TestTraffic_TheLaneSplitNamesTheLane(t *testing.T) {
	tr := NewTraffic()
	tr.Observe(Signal{Org: "acme", Cred: "fpA", Presented: "fpA", IP: "203.0.113.1", Path: "/v1/models", Class: CredSecret}, t0)
	tr.Observe(Signal{Org: "acme", Cred: "fpB", Presented: "fpB", IP: "203.0.113.2", Path: "/v1/models", Class: CredSession}, t0)

	v := tr.View("acme", ModeShadow, t0)
	if v.Lanes[AgencyAgent] != 1 || v.Lanes[AgencyHuman] != 1 {
		t.Fatalf("lanes = %v, want one agent and one human", v.Lanes)
	}
	if v.Lanes[AgencyUnknown] != 0 {
		t.Fatalf("attributable traffic landed in %q: %v", AgencyUnknown, v.Lanes)
	}

	// And an unattributable caller showing an abuse shape is the bot lane — the
	// pattern that produced it is the one this observation just counted.
	tr2 := NewTraffic()
	var last Pattern
	for i := range 40 {
		last = tr2.Observe(forged("", fmt.Sprintf("junk-%03d", i), "203.0.113.9", "/v1/models"), t0)
	}
	if last.Lane != AgencyBot {
		t.Fatalf("lane = %q for a caller presenting 40 credentials from one address, want %q", last.Lane, AgencyBot)
	}
}

// An unanswered screen is not a quiet day. The count is split so a scorer that
// has stopped answering is a number on the org's own report rather than an
// inference from traffic that all looks allowed.
func TestTraffic_UnansweredScreensAreCountedApart(t *testing.T) {
	tr := NewTraffic()
	tr.Observe(sig("acme", "fp1", "203.0.113.1", "/v1/models"), t0)
	tr.Screen("acme", "", t0)
	tr.Screen("acme", "scorer-stuck", t0)
	tr.Screen("acme", "scorer-timeout", t0)

	v := tr.View("acme", ModeLive, t0)
	if v.Screens != 3 || v.Unscored != 2 {
		t.Fatalf("screens=%d unscored=%d, want 3 and 2", v.Screens, v.Unscored)
	}
}

// A held verdict carries strings from the SCORER, which is an input like any
// other. Clamped on the way in, so one entry's size is a published fact and
// count × size is a real byte bound.
func TestTraffic_HeldStringsAreClampedAtTheDoor(t *testing.T) {
	tr := NewTraffic()
	s := sig("acme", "fp1", "203.0.113.1", "/v1/models")
	tr.Hold(s, Hold{
		Action:   strings.Repeat("a", 4096),
		Reason:   strings.Repeat("b", 1<<20),
		Decision: strings.Repeat("c", 1<<20),
	}, time.Minute, t0)

	h, ok := tr.Held(s, t0)
	if !ok {
		t.Fatal("the hold was not stored")
	}
	if len(h.Action) > maxActionLen || len(h.Reason) > maxCauseLen || len(h.Decision) > maxDecisionLen {
		t.Fatalf("a held verdict kept %d/%d/%d bytes past its clamps", len(h.Action), len(h.Reason), len(h.Decision))
	}
	// And the key itself, which an attacker supplies as an address.
	long := forged("", "", strings.Repeat("9", 4096), "/v1/models")
	tr.Observe(long, t0)
	tr.mu.Lock()
	for k := range tr.tenants[""].callers.m {
		if len(k) > maxKeyLen {
			t.Errorf("caller key is %d bytes, past the %d clamp", len(k), maxKeyLen)
		}
	}
	tr.mu.Unlock()
}

// THE BOUND IS IN BYTES. A cap on the NUMBER of keys is not a bound when the
// values behind them can be any size, so the ceiling this file publishes is
// measured here against a table filled with worst-case entries. If the published
// number understates what an entry really costs, this fails.
func TestTraffic_FootprintIsBoundedInBytes(t *testing.T) {
	// Worst case per entry: the longest key the clamps admit, a live verdict with
	// every string at its clamp, and a spread word in use.
	action, cause, decision := strings.Repeat("a", maxActionLen), strings.Repeat("b", maxCauseLen), strings.Repeat("c", maxDecisionLen)

	measure := func(fill func(tr *Traffic)) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		tr := NewTraffic()
		fill(tr)
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(tr)
		return after.HeapAlloc - before.HeapAlloc
	}

	const n = maxCallers
	callers := measure(func(tr *Traffic) {
		for i := range n {
			s := sig("acme", fmt.Sprintf("%012d", i), "203.0.113.1", "/v1/models")
			tr.Observe(s, t0)
			tr.Hold(s, Hold{Action: action, Reason: cause, Decision: decision}, time.Minute, t0)
		}
	})
	if per := callers / n; per > callerBytes {
		t.Fatalf("one caller entry measures %d bytes against a published %d", per, callerBytes)
	}

	hosts := measure(func(tr *Traffic) {
		for i := range n {
			tr.Observe(Signal{Org: "acme", Cred: "fp", Presented: "fp", Class: CredSecret,
				IP: fmt.Sprintf("2001:db8:%x:%x::%x", i/256, i%256, i), Path: "/v1/models"}, t0)
		}
	})
	// The host fill also creates one caller entry; subtract its published cost so
	// what is compared is the address table alone.
	if per := (hosts - callerBytes) / n; per > hostBytes {
		t.Fatalf("one address entry measures %d bytes against a published %d", per, hostBytes)
	}

	scopes := measure(func(tr *Traffic) {
		for i := range n {
			tr.Observe(sig(fmt.Sprintf("org%09d", i), "fp", "203.0.113.1", "/v1/models"), t0)
		}
	})
	if per := (scopes - callerBytes) / n; per > tenantBytes {
		t.Fatalf("one scope measures %d bytes against a published %d", per, tenantBytes)
	}
}

// The budget is the process ceiling, and it is charged and REFUNDED by exactly
// the admissions and reclaims that use it — a leak either way turns a bound into
// a slow refusal of everything or into no bound at all.
func TestTraffic_TheBudgetIsTheProcessCeiling(t *testing.T) {
	tr := NewTraffic()
	tr.budget.max = tenantBytes + 4*callerBytes // room for one scope and four callers

	// Callers with no credential, so every byte charged is a caller entry and the
	// arithmetic under test is not sharing the budget with the address table.
	for i := range 10 {
		tr.Observe(forged("acme", "", fmt.Sprintf("203.0.113.%d", i), "/v1/models"), t0)
	}
	tr.mu.Lock()
	n, used := len(tr.tenants["acme"].callers.m), tr.budget.used
	tr.mu.Unlock()
	if n != 4 {
		t.Fatalf("the budget admitted %d callers, want 4", n)
	}
	if used > tr.budget.max {
		t.Fatalf("the budget is over its own ceiling: %d > %d", used, tr.budget.max)
	}

	// A second scope cannot be admitted, and cannot displace the first.
	tr.Observe(forged("globex", "", "198.51.100.1", "/v1/models"), t0)
	tr.mu.Lock()
	_, stranger := tr.tenants["globex"]
	_, incumbent := tr.tenants["acme"]
	tr.mu.Unlock()
	if stranger {
		t.Fatal("a scope was admitted past the budget")
	}
	if !incumbent {
		t.Fatal("an admission displaced a scope that was already there")
	}

	// Once the first scope goes idle, its whole charge comes back.
	tr.mu.Lock()
	tr.sweepLocked(t0.Add(tenantIdle + time.Minute))
	used = tr.budget.used
	tr.mu.Unlock()
	if used != 0 {
		t.Fatalf("%d bytes stayed charged after every scope was reclaimed", used)
	}
}

// The reclaim policy has ONE rule and no exception: a key that is still in use is
// never removed. This is the property that makes every bound above safe, so it is
// asserted directly on the table rather than inferred from behaviour above it.
func TestTable_ReclaimTakesOnlyDeadKeys(t *testing.T) {
	b := budget{max: MaxBytes}
	tab := newTable[*caller](4, callerBytes, &b)

	fresh, _ := tab.admit("fresh", t0, func() *caller { return &caller{} })
	fresh.seen = t0
	holder, _ := tab.admit("holder", t0, func() *caller { return &caller{} })
	holder.seen = t0.Add(-time.Hour) // long idle...
	holder.hold = Hold{Action: "block", Until: t0.Add(time.Hour)}
	dead, _ := tab.admit("dead", t0, func() *caller { return &caller{} })
	dead.seen = t0.Add(-time.Hour)

	tab.swept = time.Time{}
	tab.reclaim(t0)

	if _, ok := tab.get("fresh"); !ok {
		t.Error("a caller seen inside the window was reclaimed")
	}
	if _, ok := tab.get("holder"); !ok {
		t.Error("a caller under a live verdict was reclaimed")
	}
	if _, ok := tab.get("dead"); ok {
		t.Error("a caller that is neither recent nor held was kept")
	}
	if b.used != 2*callerBytes {
		t.Errorf("budget = %d after reclaiming one of three entries, want %d", b.used, 2*callerBytes)
	}
}

// A request with NO identity — no credential the boundary validated and no
// client address — has nothing to be counted against. Keying it under the empty
// address would file the entire internet in one row, make it look like the worst
// credential-stuffing run ever recorded, and let one verdict be held against
// everybody at once.
//
// This is not hypothetical. A TCP load balancer that terminates the connection
// without PROXY protocol in front of it leaves every internet request with no
// client address at all, which is exactly the shape here.
func TestTraffic_ARequestWithNoIdentityIsNotACaller(t *testing.T) {
	tr := NewTraffic()
	blind := Signal{Org: "", Presented: "junk", IP: "", Path: "/v1/models", Class: CredAnonymous}

	var p Pattern
	for range 50 {
		p = tr.Observe(blind, t0)
	}
	if p.Strain != StrainBlind {
		t.Fatalf("strain = %q for a request with no identity, want %q", p.Strain, StrainBlind)
	}
	if p.Requests != 0 {
		t.Fatalf("a request with no identity accumulated %d requests against something", p.Requests)
	}
	tr.mu.Lock()
	n := len(tr.tenants[""].callers.m)
	tr.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d caller rows were opened for traffic with no identity", n)
	}

	// Nothing can be held against it, so no verdict can be enforced on everyone.
	tr.Hold(blind, Hold{Action: "block"}, time.Minute, t0)
	if _, ok := tr.Held(blind, t0); ok {
		t.Fatal("a verdict was held against every unidentifiable caller at once")
	}

	// And the volume is still visible, named as what it is.
	v := tr.View("", ModeLive, t0)
	if v.Requests != 50 || v.Blind != 50 {
		t.Fatalf("blind traffic must be counted as traffic: requests=%d blind=%d, want 50 and 50", v.Requests, v.Blind)
	}
	if v.Strain != StrainBlind {
		t.Fatalf("view strain = %q, want %q — a sensor that cannot see must say so", v.Strain, StrainBlind)
	}

	// A caller that HAS an address is unaffected: this is "no identity", not "no
	// credential".
	if q := tr.Observe(forged("", "junk", "203.0.113.4", "/v1/models"), t0); q.Strain != "" || q.Requests != 1 {
		t.Fatalf("an addressed caller was swept into the blind state: %+v", q)
	}
}

// The report is an expensive read on the same lock every request needs to be
// observed under, so the SCAN happens under the lock and the SORT does not.
// These two say what that costs: a full-table report, and the observation path
// it must not stall.
func BenchmarkViewAtTheCeiling(b *testing.B) {
	tr := NewTraffic()
	for i := range maxCallers {
		tr.Observe(sig("acme", fmt.Sprintf("fp%06d", i), "203.0.113.1", "/v1/models"), t0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.View("acme", ModeLive, t0)
	}
}

func BenchmarkObserveAtTheCeiling(b *testing.B) {
	tr := NewTraffic()
	for i := range maxCallers {
		tr.Observe(sig("acme", fmt.Sprintf("fp%06d", i), "203.0.113.1", "/v1/models"), t0)
	}
	s := sig("acme", "fp000001", "203.0.113.1", "/v1/models")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.Observe(s, t0)
	}
}
