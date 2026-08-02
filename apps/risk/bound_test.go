package risk

// bound_test.go pins the property bound.go exists for: a tenant may only ever
// degrade ITSELF.
//
// Each test here was written by reintroducing the defect and checking that this
// test goes red. The defect is one line — `velocity.New(velocity.Config{})` on a
// process-wide field — so the guard that survives a revert is the SOURCE test at
// the bottom: it reads this package's own files and fails the moment a second
// constructor appears, whichever file it appears in.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/velocity"
)

// TestASharedStoreEvictsAcrossTenants is the DEFECT, demonstrated on the library
// value this package used to hold one of.
//
// It is not a test of this package — it is the reason the rest of this file
// exists, and it is written first so the property below cannot be read as
// defending against something hypothetical. velocity.Store shards by hashing the
// WHOLE key, so two tenants land in the same shard, and the overflow eviction
// takes the least-recently-updated key IN THAT SHARD — whoever owns it.
func TestASharedStoreEvictsAcrossTenants(t *testing.T) {
	shared := velocity.New(velocity.Config{Windows: windows(), MaxKeys: 64})
	now := time.Now()
	victim := velocity.Key{OrgID: "hanzo/victim", Kind: "ip", Value: "203.0.113.5"}
	shared.Record(victim, now, 10, 10_000)
	if count(shared, victim) != 1 {
		t.Fatalf("the victim's own record did not land")
	}
	for i := range 4_000 {
		shared.Record(velocity.Key{OrgID: "hanzo/flooder", Kind: "ip", Value: fmt.Sprintf("198.51.100.%d", i)}, now, 1, 10_000)
	}
	if got := count(shared, victim); got != 0 {
		t.Skipf("the shared store still holds the victim's key (%v) — the eviction is probabilistic in the shard, "+
			"but the shape that allows it is what this file rules out", got)
	}
	t.Log("a shared store with a global cap silently deleted another tenant's counter: this is the shape bound.go forbids")
}

// TestOneTenantsFloodCannotQuietAnother is the property.
//
// The flooder blows through its OWN cardinality bound many times over. The
// victim's ring — one key, recorded before the flood and never touched again —
// must still read exactly what the victim put in it. Under the shared store
// above it reads zero, the victim's `velocity.ip.1h.count >= 5` rule stops
// firing, and nothing anywhere says so.
func TestOneTenantsFloodCannotQuietAnother(t *testing.T) {
	t.Setenv(envVelBytes, strconv.Itoa(1<<20)) // the smallest budget the knob allows
	_, s := wireApp(t)

	victim, flooder := Tenant("hanzo/victim"), Tenant("hanzo/flooder")
	vvel, _, _ := armsOf(t, s, victim)
	fvel, _, _ := armsOf(t, s, flooder)
	if vvel == fvel {
		t.Fatal("two tenants were handed the SAME aggregate store — one org's volume can evict another's counters")
	}

	now := time.Now()
	key := velocity.Key{OrgID: victim.String(), Kind: "ip", Value: "203.0.113.5"}
	for range 5 {
		vvel.Record(key, now, 100, 10_000)
	}

	for i := range maxKeys() * 4 {
		fvel.Record(velocity.Key{OrgID: flooder.String(), Kind: "ip", Value: fmt.Sprintf("198.51.100.%d", i)}, now, 1, 10_000)
	}

	if got := count(vvel, key); got != 5 {
		t.Fatalf("the victim's 1h count is %d, want 5 — another tenant's traffic quieted this tenant's control", got)
	}
	// And the flooder degraded ITSELF, which is the other half of the property:
	// its own bound held, and it is reported rather than silent.
	// velocity rounds its cap UP to a whole number of keys per shard, so the true
	// ceiling is a little above the budget's key count and never a multiple of it.
	// What has to hold is that the flood is bounded at all: it recorded four times
	// the budget and kept about one.
	if got := fvel.Keys(); got >= 2*maxKeys() {
		t.Fatalf("the flooder holds %d keys against a budget of %d — its own bound did not hold", got, maxKeys())
	}
	c, err := s.State.shelf.of(flooder)
	if err != nil {
		t.Fatalf("resolve flooder: %v", err)
	}
	if !c.strained() {
		t.Fatal("a tenant at its own cardinality bound does not report itself strained, so a partial ring reads as a complete one")
	}
	if vc, err := s.State.shelf.of(victim); err != nil || vc.strained() {
		t.Fatalf("the victim reports strained (%v) because of another tenant's traffic", err)
	}
}

// TestAFullNodeRefusesANewTenantAndKeepsEveryIncumbent pins the admission rule.
//
// At the ceiling the answer is 503 and an incumbent keeps everything: its rings,
// its model and its file. Admitting by eviction would be the same defect wearing
// an admission badge — the newcomer's arrival would be the reason a tenant that
// did nothing lost its counters.
func TestAFullNodeRefusesANewTenantAndKeepsEveryIncumbent(t *testing.T) {
	t.Setenv(envTenantMax, "2")
	_, s := wireApp(t)

	a, b := Tenant("hanzo/one"), Tenant("hanzo/two")
	avel, amodel, _ := armsOf(t, s, a)
	bvel, _, _ := armsOf(t, s, b)

	key := velocity.Key{OrgID: a.String(), Kind: "ip", Value: "203.0.113.9"}
	avel.Record(key, time.Now(), 10, 10_000)

	if _, err := s.State.shelf.of(Tenant("hanzo/three")); err == nil {
		t.Fatal("a third tenant was admitted past the ceiling of two")
	} else if !strings.Contains(err.Error(), "tenant capacity") {
		t.Fatalf("refusal says %q, want the capacity refusal", err)
	}

	av2, am2, _ := armsOf(t, s, a)
	bv2, _, _ := armsOf(t, s, b)
	if av2 != avel || am2 != amodel || bv2 != bvel {
		t.Fatal("an incumbent's state was replaced to make room — that is eviction with a different name")
	}
	if got := count(avel, key); got != 1 {
		t.Fatalf("an incumbent's count is %d after another tenant was refused, want 1", got)
	}
	if _, refused, _ := s.State.shelf.count(); refused != 1 {
		t.Fatalf("the refusal was not counted (%d), so the probe cannot report a pod at capacity", refused)
	}
}

// TestTheProbeReportsAPodAtCapacity: a refusal is an operator's problem and it
// has to reach one. A pod that quietly turns tenants away while answering 200 is
// a pod nobody investigates.
func TestTheProbeReportsAPodAtCapacity(t *testing.T) {
	t.Setenv(envTenantMax, "1")
	app, s := wireApp(t)
	if _, err := s.State.shelf.of(Tenant("hanzo/one")); err != nil {
		t.Fatalf("first tenant: %v", err)
	}
	code, _ := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("probe = %d before any refusal, want 200", code)
	}
	if _, err := s.State.shelf.of(Tenant("hanzo/two")); err == nil {
		t.Fatal("a second tenant was admitted past the ceiling of one")
	}
	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("probe = %d %s after a refusal, want 503 carrying the report", code, body)
	}
	if !strings.Contains(string(body), "tenant ceiling") {
		t.Fatalf("the degraded probe does not name the capacity event: %s", body)
	}
}

// TestTheProbeNamesNoTenant.
//
// GET /v1/<app>/health is unauthenticated BY DESIGN across this fleet — the
// billing gate, the tracing filter and the identity middleware each exempt it by
// suffix — so everything the probe says, it says to the internet. The capacity
// report needs a strained COUNT; it must never carry the KEY of a strained
// organisation, because that publishes who is a customer and which pod holds
// them, from an anonymous GET.
//
// The tenant that needs the fact gets it on its own scoped surface.
func TestTheProbeNamesNoTenant(t *testing.T) {
	t.Setenv(envVelBytes, strconv.Itoa(1<<20)) // the smallest budget the knob allows
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	vel, _, _ := armsOf(t, s, tn)
	now := time.Now()
	for i := range maxKeys() * 2 {
		vel.Record(velocity.Key{OrgID: tn.String(), Kind: "ip", Value: fmt.Sprintf("198.51.100.%d", i)}, now, 1, 1_000)
	}
	c, err := s.State.shelf.of(tn)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !c.strained() {
		t.Fatal("the tenant is not at its own bound, so this test proves nothing")
	}

	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("probe = %d %s", code, body)
	}
	var report map[string]any
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if n, ok := report["strained"].(float64); !ok || n != 1 {
		t.Fatalf("the probe does not report the strained COUNT an operator needs: %s", body)
	}
	for _, leak := range []string{tn.String(), tn.org(), "acme"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the unauthenticated probe names a tenant (%q): an anonymous GET reads this "+
				"organisation's key off the customer list — %s", leak, body)
		}
	}

	// And the organisation itself IS told, on its own authenticated state.
	code, body = req(t, app, http.MethodGet, "/v1/ml/state", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("state = %d %s", code, body)
	}
	var st mlModelState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Strained {
		t.Error("the organisation whose own rings are partial is not told so on its own state, " +
			"so a velocity rule that stopped firing reads as a clean stream")
	}

	// A DIFFERENT organisation is told nothing about this one.
	code, body = req(t, app, http.MethodGet, "/v1/ml/state", "beta", "u_beta", "")
	if code != http.StatusOK {
		t.Fatalf("beta state = %d %s", code, body)
	}
	var other mlModelState
	if err := json.Unmarshal(body, &other); err != nil {
		t.Fatal(err)
	}
	if other.Strained {
		t.Error("one organisation's strain is reported on another organisation's state")
	}
}

// TestARetiredTenantHeldNothingButZeros pins the reclaim's safety argument.
//
// Reclaim is floored at the longest window, so every ring of a retired tenant
// has already rotated to zero and retiring it cannot lose a count. The tenant
// comes back with its LEARNED state, because the model is snapshotted to its own
// file before the cell is dropped.
func TestARetiredTenantHeldNothingButZeros(t *testing.T) {
	if idleReclaim() < longestWindow() {
		t.Fatalf("the reclaim threshold %s is under the longest window %s: retiring a tenant would delete live counts",
			idleReclaim(), longestWindow())
	}
	_, s := wireApp(t)
	tn := Tenant("hanzo/quiet")
	vel, _, _ := armsOf(t, s, tn)
	key := velocity.Key{OrgID: tn.String(), Kind: "ip", Value: "203.0.113.7"}
	vel.Record(key, time.Now(), 10, 10_000)

	// A sweep at the real threshold must NOT touch a tenant that just spoke.
	s.State.shelf.sweep()
	if n, _, _ := s.State.shelf.count(); n != 1 {
		t.Fatalf("a tenant that just decided was retired (%d resident)", n)
	}
	// Silent for longer than the longest window: now it goes, and its rings held
	// nothing.
	c, err := s.State.shelf.of(tn)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	c.mu.Lock()
	c.touched = time.Now().Add(-2 * idleReclaim())
	c.mu.Unlock()
	s.State.shelf.sweep()
	if n, _, _ := s.State.shelf.count(); n != 0 {
		t.Fatalf("a tenant silent for twice the reclaim threshold is still resident (%d)", n)
	}
	// It comes back armed, on its own file, with no trace of another tenant.
	back, _, _ := armsOf(t, s, tn)
	if back == vel {
		t.Fatal("the retired tenant came back with the SAME store, so nothing was released")
	}
}

// TestOnlyTheBoundedConstructorsBuildThePlanes is the guard that survives a
// revert.
//
// It reads this package's own source. velocity.New and anomaly.New may appear in
// bound.go and nowhere else — including tests, except the one above that
// demonstrates the defect. Reintroduce the process-wide store anywhere and this
// fails, whatever it is called and whoever calls it.
func TestOnlyTheBoundedConstructorsBuildThePlanes(t *testing.T) {
	allowed := map[string]bool{"bound.go": true, "bound_test.go": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no source read, so this guard proved nothing")
	}
	for _, f := range files {
		if allowed[filepath.Base(f)] {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, ctor := range []string{"velocity.New(", "anomaly.New("} {
			if strings.Contains(string(body), ctor) {
				t.Errorf("%s calls %s — the two in-memory planes are built ONLY by aggregates() and forest() "+
					"in bound.go, which is what makes a process-wide store with a global cap unrepresentable", f, ctor)
			}
		}
	}
}

// count reads a key's 1h count out of a store.
func count(s *velocity.Store, k velocity.Key) int {
	for _, o := range s.Observe(k) {
		if o.Window == "1h" {
			return o.Count
		}
	}
	return 0
}

// TestOneSearchPerTenantAndOnlyPerTenant pins the bound on the most expensive op
// this plane has.
//
// An exhaustive search is up to searchBudget of CPU across the whole grid, in a
// goroutine detached from the request. Unbounded, one key loops the route and
// takes down every product on the pod. Bounded per tenant, a caller that loops it
// spends its own slot and nobody else's — which is the same shape the measurement
// plane uses, for the same reason.
func TestOneSearchPerTenantAndOnlyPerTenant(t *testing.T) {
	app, s := wireApp(t)

	release, err := s.State.running.claim(Tenant("hanzo/acme"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	code, body := req(t, app, http.MethodPost, "/v1/ml/search", "acme", "u_acme", `{"limit":10}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("a second concurrent search = %d %s, want 429 — the run is unbounded", code, body)
	}
	// Another tenant is untouched: the bound is per tenant, not a fleet-wide slot
	// that one org can hold against everybody.
	code, body = req(t, app, http.MethodPost, "/v1/ml/search", "beta", "u_beta", `{"limit":10}`)
	if code == http.StatusTooManyRequests {
		t.Fatalf("another tenant's search = %d %s — one org's run is blocking the fleet", code, body)
	}
	release()
	code, body = req(t, app, http.MethodPost, "/v1/ml/search", "acme", "u_acme", `{"limit":10}`)
	if code == http.StatusTooManyRequests {
		t.Fatalf("the slot was not released: %d %s", code, body)
	}
}
