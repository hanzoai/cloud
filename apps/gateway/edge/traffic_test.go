package edge

// The sensor's own contract, tested where it lives: counts roll, holds expire,
// memory is bounded, and a tenant cannot be reached from outside itself.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func sig(org, cred, ip, path string) Signal {
	return Signal{Org: org, Cred: cred, IP: ip, Path: path, Agency: "agent"}
}

func TestTraffic_CountsPerCredential(t *testing.T) {
	tr := NewTraffic()
	var p Pattern
	for i := 0; i < 7; i++ {
		p = tr.Observe(sig("acme", "fp1", "203.0.113.1", "/v1/models"), t0)
	}
	if p.Requests != 7 {
		t.Fatalf("requests = %d, want 7", p.Requests)
	}
	// A SECOND credential in the same org and from the same address has its own
	// count — this is the fact neither existing limiter can express.
	if q := tr.Observe(sig("acme", "fp2", "203.0.113.1", "/v1/models"), t0); q.Requests != 1 {
		t.Fatalf("a second credential's count = %d, want 1", q.Requests)
	}
}

func TestTraffic_CountsPathSpreadAndPeerSpread(t *testing.T) {
	tr := NewTraffic()
	var p Pattern
	for i := 0; i < 12; i++ {
		p = tr.Observe(sig("acme", "fp1", "203.0.113.1", fmt.Sprintf("/v1/thing/%d", i)), t0)
	}
	if p.Paths < 8 {
		// The spread word saturates and can collide, so the assertion is a floor,
		// not equality — an approximate count must not be tested as an exact one.
		t.Fatalf("path spread = %d after 12 distinct paths, want at least 8", p.Paths)
	}

	tr2 := NewTraffic()
	var q Pattern
	for i := 0; i < 10; i++ {
		q = tr2.Observe(sig("acme", fmt.Sprintf("fp%d", i), "198.51.100.9", "/v1/models"), t0)
	}
	if q.Peers < 6 {
		t.Fatalf("peer spread = %d after 10 credentials from one address, want at least 6", q.Peers)
	}
}

// An anonymous stream from one address is a flood, not stuffing: with no
// credential presented there is no peer to count, and saying otherwise would let
// one anonymous client manufacture a stuffing signature by itself.
func TestTraffic_AnonymousTrafficHasNoPeers(t *testing.T) {
	tr := NewTraffic()
	var p Pattern
	for i := 0; i < 20; i++ {
		p = tr.Observe(sig("", "", "198.51.100.9", "/v1/models"), t0)
	}
	if p.Peers != 0 {
		t.Fatalf("peers = %d for credential-less traffic, want 0", p.Peers)
	}
	if p.Requests != 20 {
		t.Fatalf("requests = %d, want 20 — anonymous traffic is still counted", p.Requests)
	}
}

func TestTraffic_CountsRollOutOfTheWindow(t *testing.T) {
	tr := NewTraffic()
	for i := 0; i < 5; i++ {
		tr.Observe(sig("acme", "fp1", "203.0.113.1", "/v1/models"), t0)
	}
	later := t0.Add(2 * window)
	if p := tr.Observe(sig("acme", "fp1", "203.0.113.1", "/v1/models"), later); p.Requests != 1 {
		t.Fatalf("requests = %d two windows later, want 1 — the ring did not roll", p.Requests)
	}
}

func TestTraffic_HoldsExpireAndAreCapped(t *testing.T) {
	tr := NewTraffic()
	s := sig("acme", "fp1", "203.0.113.1", "/v1/models")

	if _, ok := tr.Held(s, t0); ok {
		t.Fatal("nothing is held before anything is held")
	}
	tr.Hold(s, Hold{Action: "block", Reason: "peers", Decision: "d-1"}, 30*time.Second, t0)

	h, ok := tr.Held(s, t0.Add(10*time.Second))
	if !ok || h.Action != "block" || h.Decision != "d-1" {
		t.Fatalf("held verdict = %+v ok=%v", h, ok)
	}
	if _, ok := tr.Held(s, t0.Add(31*time.Second)); ok {
		t.Fatal("a hold must lapse so a false positive clears itself")
	}

	// A caller asking for a week gets the cap. Enforcement without a fresh
	// judgement is bounded, always.
	tr.Hold(s, Hold{Action: "block"}, 7*24*time.Hour, t0)
	if _, ok := tr.Held(s, t0.Add(holdCap+time.Second)); ok {
		t.Fatalf("a hold longer than %s must be clamped", holdCap)
	}

	tr.Hold(s, Hold{Action: "block"}, time.Minute, t0)
	tr.Release(s)
	if _, ok := tr.Held(s, t0.Add(time.Second)); ok {
		t.Fatal("Release must drop a held verdict — the operator's undo")
	}
}

// A hold is keyed on (org, credential). One org's hold must be unreachable from
// another's traffic even when the credential string and the address are identical.
func TestTraffic_HoldsDoNotCrossTenants(t *testing.T) {
	tr := NewTraffic()
	a := sig("acme", "fp1", "203.0.113.1", "/v1/models")
	b := sig("globex", "fp1", "203.0.113.1", "/v1/models")
	tr.Hold(a, Hold{Action: "block"}, time.Minute, t0)
	if _, ok := tr.Held(b, t0); ok {
		t.Fatal("acme's hold reached globex — the tenant boundary is not in the key")
	}
}

func TestTraffic_ViewIsScopedToOneTenant(t *testing.T) {
	tr := NewTraffic()
	tr.Observe(sig("acme", "fpA", "203.0.113.1", "/v1/models"), t0)
	tr.Observe(sig("acme", "fpA", "203.0.113.1", "/v1/models"), t0)
	tr.Observe(sig("globex", "fpB", "203.0.113.2", "/v1/models"), t0)

	v := tr.View("acme", ModeShadow, t0)
	if v.Org != "acme" || v.Requests != 2 {
		t.Fatalf("acme view = %+v, want org=acme requests=2", v)
	}
	if len(v.Callers) != 1 || v.Callers[0].Cred != "fpA" {
		t.Fatalf("acme must see exactly its own caller: %+v", v.Callers)
	}
	blob := fmt.Sprintf("%+v", v)
	if strings.Contains(blob, "fpB") || strings.Contains(blob, "globex") {
		t.Fatalf("another tenant appeared in acme's view: %s", blob)
	}

	// An org that never called sees an empty, well-formed view — never another
	// org's rows, and never an error that would confirm the other org exists.
	if e := tr.View("stranger", ModeShadow, t0); e.Requests != 0 || len(e.Callers) != 0 {
		t.Fatalf("an unknown org must see nothing, got %+v", e)
	}
}

// The org prefix must be matched as a WHOLE segment. Without the separator,
// "acme" would be a prefix of "acme-corp" and one tenant would read another's
// rows — the classic prefix-matching tenancy bug.
func TestTraffic_ViewDoesNotLeakToAPrefixNeighbour(t *testing.T) {
	tr := NewTraffic()
	tr.Observe(sig("acme-corp", "fpX", "203.0.113.3", "/v1/models"), t0)
	if v := tr.View("acme", ModeShadow, t0); len(v.Callers) != 0 {
		t.Fatalf("acme read acme-corp's rows: %+v", v.Callers)
	}
}

func TestTraffic_ViewIsBounded(t *testing.T) {
	tr := NewTraffic()
	for i := 0; i < maxViewCallers*3; i++ {
		for n := 0; n <= i; n++ { // give each caller a distinct request count
			tr.Observe(sig("acme", fmt.Sprintf("fp%04d", i), "203.0.113.1", "/v1/models"), t0)
		}
	}
	v := tr.View("acme", ModeShadow, t0)
	if len(v.Callers) != maxViewCallers {
		t.Fatalf("view returned %d callers, want the %d busiest", len(v.Callers), maxViewCallers)
	}
	for i := 1; i < len(v.Callers); i++ {
		if v.Callers[i-1].Requests < v.Callers[i].Requests {
			t.Fatal("the view must be ordered busiest first")
		}
	}
}

// The table must not grow without bound under an adversary minting credentials.
func TestTraffic_TableIsBounded(t *testing.T) {
	tr := NewTraffic()
	now := t0
	for i := 0; i < maxKeys+5_000; i++ {
		if i%1000 == 0 {
			now = now.Add(2 * window) // let idle keys age out
		}
		tr.Observe(sig("acme", fmt.Sprintf("fp%06d", i), "203.0.113.1", "/v1/models"), now)
	}
	tr.mu.Lock()
	n := len(tr.kv)
	tr.mu.Unlock()
	if n >= maxKeys {
		t.Fatalf("the sensor holds %d keys, at or past the %d cap", n, maxKeys)
	}
}

func TestTraffic_FailCountsOnlyAfterTheCallerIsKnown(t *testing.T) {
	tr := NewTraffic()
	s := sig("acme", "fp1", "203.0.113.1", "/v1/models")
	tr.Fail(s, t0) // no Observe yet: nothing to attribute it to.
	if p := tr.Observe(s, t0); p.Failures != 0 {
		t.Fatalf("failures = %d before the caller was seen, want 0", p.Failures)
	}
	tr.Fail(s, t0)
	tr.Fail(s, t0)
	if p := tr.Observe(s, t0); p.Failures != 2 {
		t.Fatalf("failures = %d, want 2", p.Failures)
	}
}

// Every method takes the one lock; -race is the whole assertion.
func TestTraffic_IsRaceFree(t *testing.T) {
	tr := NewTraffic()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			s := sig(fmt.Sprintf("org%d", g%3), fmt.Sprintf("fp%d", g), "203.0.113.1", "/v1/models")
			for i := 0; i < 200; i++ {
				tr.Observe(s, time.Now())
				tr.Fail(s, time.Now())
				tr.Hold(s, Hold{Action: "block"}, time.Second, time.Now())
				tr.Held(s, time.Now())
				tr.Deny(s.Org, time.Now())
				tr.View(s.Org, ModeLive, time.Now())
				tr.Release(s)
			}
		}(g)
	}
	wg.Wait()
}

func TestPolicy_ModeDefaultsToShadowAndIsValidated(t *testing.T) {
	if err := (Policy{Mode: "enforce"}).Validate(); err == nil {
		t.Fatal("an unknown mode must be refused rather than silently ignored")
	}
	for _, m := range []string{"", ModeShadow, ModeLive} {
		if err := (Policy{Mode: m}).Validate(); err != nil {
			t.Fatalf("mode %q must be accepted: %v", m, err)
		}
	}

	s, err := New(t.TempDir(), "admin", Policy{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.Mode("acme"); got != ModeShadow {
		t.Fatalf("an unconfigured org must be %q, got %q", ModeShadow, got)
	}
	if _, err := s.Put(t.Context(), "acme", Policy{Mode: ModeLive}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := s.Mode("acme"); got != ModeLive {
		t.Fatalf("acme = %q after arming, want %q", got, ModeLive)
	}
	// Arming one tenant must not arm another.
	if got := s.Mode("globex"); got != ModeShadow {
		t.Fatalf("globex = %q, want %q — arming crossed the tenant boundary", got, ModeShadow)
	}
	// A nil store is shadow, not live: an unwired deployment never enforces.
	var nilStore *Store
	if got := nilStore.Mode("acme"); got != ModeShadow {
		t.Fatalf("a nil store must resolve to %q, got %q", ModeShadow, got)
	}
}

// A screen is the billable unit of the risk product. It must be visible to the
// org from the first request — before anyone decides what one costs — so it is
// counted here as well as metered, and the two are independent.
func TestTraffic_CountsScreensPerTenant(t *testing.T) {
	tr := NewTraffic()
	tr.Observe(sig("acme", "fp1", "203.0.113.1", "/v1/models"), t0)
	tr.Screen("acme", t0)
	tr.Screen("acme", t0)
	tr.Observe(sig("globex", "fp2", "203.0.113.2", "/v1/models"), t0)

	if v := tr.View("acme", ModeLive, t0); v.Screens != 2 {
		t.Fatalf("acme screens = %d, want 2", v.Screens)
	}
	if v := tr.View("globex", ModeLive, t0); v.Screens != 0 {
		t.Fatalf("globex screens = %d, want 0 — screens crossed the tenant boundary", v.Screens)
	}
	// A screen is not a request: counting it in both would make an org's own
	// report say it sent more traffic than it did.
	if v := tr.View("acme", ModeLive, t0); v.Requests != 1 {
		t.Fatalf("acme requests = %d, want 1", v.Requests)
	}
}

// The anonymous lane has no org, and it is the lane a bad bot calls from. It must
// resolve to the PLATFORM row — otherwise the one lane the gate exists for could
// never be armed, because there would be no org to arm.
func TestPolicy_TheAnonymousLaneResolvesToThePlatformRow(t *testing.T) {
	s, err := New(t.TempDir(), "admin", Policy{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.Mode(""); got != ModeShadow {
		t.Fatalf("anonymous starts %q, want %q", got, ModeShadow)
	}
	// Arming a TENANT must not arm the anonymous lane.
	if _, err := s.Put(t.Context(), "acme", Policy{Mode: ModeLive}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := s.Mode(""); got != ModeShadow {
		t.Fatalf("arming acme armed the anonymous lane too: %q", got)
	}
	// Arming the reserved admin org — which IS the platform row — arms it.
	if _, err := s.PutPlatform(t.Context(), Policy{Mode: ModeLive}); err != nil {
		t.Fatalf("PutPlatform: %v", err)
	}
	if got := s.Mode(""); got != ModeLive {
		t.Fatalf("anonymous = %q after arming the platform row, want %q", got, ModeLive)
	}
	// And the platform row is a DEFAULT for a tenant, not an override of one: an
	// org that has said nothing inherits it.
	if got := s.Mode("globex"); got != ModeLive {
		t.Fatalf("globex = %q, want the inherited %q", got, ModeLive)
	}
}
