package reference

// refresh_test.go holds the four properties of a REFRESH — which version prune
// spares, what a take that changed size is allowed to do, what a receipt with
// nothing in it becomes, and when the first take happens — against a warehouse
// (warehouse_test.go) rather than against the text of a statement.
//
// The distinction is the point. Every one of these was a call site that
// disagreed with a statement constant that was itself correct, so a test reading
// only the constant passed while the plane did the opposite.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// plant builds the service the way Mount does, minus the routes and minus the
// background loop, with a downloader the test controls. It is how a refresh is
// driven without a network and without an HTTP surface.
func plant(t *testing.T, get download) *cloud.Service[state] {
	t.Helper()
	deps := cloud.Deps{DataDir: t.TempDir(), Brand: "hanzo"}
	base := cloud.NewBase(deps, subsystem)
	own := cloud.NewOrgStore[*overrides](base, subsystem, openOverrides)
	s := &cloud.Service[state]{
		Base: base,
		State: state{
			plane: newPlane(),
			own:   own,
			get:   get,
			sync:  own.Sync,
			now:   func() time.Time { return time.Now().UTC() },
			work:  &work{stop: make(chan struct{})},
		},
	}
	t.Cleanup(func() { _ = own.CloseAll() })
	return s
}

// serves is a downloader that answers every URL with the same bytes.
func serves(body *string) download {
	return func(context.Context, string) ([]byte, error) { return []byte(*body), nil }
}

// list renders n disposable domains, which is what the `domain` set's one
// publisher serves and what parseLines reads.
func list(n int) string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("tempbox%03d.example", i))
	}
	return strings.Join(out, "\n")
}

// domainSet is the catalog's `domain` set: one publisher, one parser, fetched.
func domainSet(t *testing.T) Set {
	t.Helper()
	set, ok := byName("domain")
	if !ok {
		t.Fatal("no domain set")
	}
	return set
}

// TestPruneSparesTheVersionADecisionMayStillCite.
//
// The whole plane is sold on one promise: a decision records the version it
// consulted and an auditor resolves that string back to what the version
// contained. A refresh happens at some instant; a decision taken a moment before
// it names the version that was current a moment before it. So a prune that
// spares only the NEW version deletes the rows behind every citation in that
// window, and the promise is void for exactly the decisions most likely to be
// disputed.
//
// The call site passed the current version for BOTH of the statement's two
// placeholders, so `version != ? AND version != ?` spared one version, not two —
// while the statement constant, and the only test over it, said two.
func TestPruneSparesTheVersionADecisionMayStillCite(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := list(10)
	s := plant(t, serves(&body))
	set := domainSet(t)
	ctx := context.Background()

	took := func() string {
		t.Helper()
		got, err := take(ctx, s, set, nil, false)
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		if got[0].Refusal != "" {
			t.Fatalf("take refused: %s", got[0].Refusal)
		}
		return got[0].Version
	}

	v1 := took()
	body = list(11)
	v2 := took()
	body = list(12)
	v3 := took()
	if v1 == v2 || v2 == v3 {
		t.Fatalf("three different lists must be three versions: %s %s %s", v1, v2, v3)
	}

	if n := len(w.rows("domain", "disposable", v3)); n != 12 {
		t.Errorf("the current version holds %d rows, want 12", n)
	}
	if n := len(w.rows("domain", "disposable", v2)); n != 11 {
		t.Errorf("the PREVIOUS version holds %d rows, want 11 — a decision taken a moment before the refresh cites it, and an auditor asking what it contained deserves an answer", n)
	}
	if n := len(w.rows("domain", "disposable", v1)); n != 0 {
		t.Errorf("the version before last still holds %d rows; two is the whole history the membership keeps", n)
	}

	// The manifest is a record and is never pruned: every version still resolves
	// to a publisher, a licence and a date.
	for _, v := range []string{v1, v2, v3} {
		if _, ok := w.held("domain", "disposable", v); !ok {
			t.Errorf("version %s lost its manifest row", v)
		}
	}

	// And the two versions named in the last prune are DIFFERENT ones.
	prunes := w.prunes()
	if len(prunes) == 0 {
		t.Fatal("no prune ran")
	}
	last := prunes[len(prunes)-1]
	if last[2] == last[3] {
		t.Errorf("prune spared %q twice; two placeholders bound to one string spare one version", last[2])
	}
	if last[2] != v3 || last[3] != v2 {
		t.Errorf("prune spared (%q, %q), want the current and the one it replaced (%q, %q)", last[2], last[3], v3, v2)
	}
}

// TestSweepOldSparesTheOneItReplaced states the same property on the pure
// function, so the pair a take carries — current, and the one it superseded — is
// pinned independently of the warehouse.
func TestSweepOldSparesTheOneItReplaced(t *testing.T) {
	var got [][4]string
	drop := func(_ context.Context, set, source, keep, alsoKeep string) error {
		got = append(got, [4]string{set, source, keep, alsoKeep})
		return nil
	}
	was := map[string]held{"disposable": {Version: "v1", Keys: 10}}
	now := []version{{Set: "domain", Source: "disposable", Version: "v2", Keys: 11}}
	if err := sweepOld(context.Background(), drop, "domain", was, now); err != nil {
		t.Fatalf("sweepOld: %v", err)
	}
	if len(got) != 1 || got[0] != [4]string{"domain", "disposable", "v2", "v1"} {
		t.Fatalf("sweepOld dropped %v, want one call sparing v2 and v1", got)
	}

	// A source taken for the FIRST time has no previous version, and an empty
	// second value spares nothing extra — which is correct, there is nothing extra.
	got = nil
	if err := sweepOld(context.Background(), drop, "domain", map[string]held{}, now); err != nil {
		t.Fatalf("sweepOld: %v", err)
	}
	if len(got) != 1 || got[0][3] != "" {
		t.Fatalf("a first take spared %v; there is no previous version to spare", got)
	}
}

// TestASourceThatSwungIsRefusedAndThePreviousVersionStands.
//
// The empty take was already an error and the truncated download already an
// error. Between them sat the dangerous case: a publisher serving a VALID,
// parseable list at a fraction or a multiple of its previous size, landing
// silently as the new baseline every organisation's decisions read. A list that
// shrank answers "not listed" for everything it lost and is indistinguishable
// from a clean world — the exact failure this plane exists to make visible.
func TestASourceThatSwungIsRefusedAndThePreviousVersionStands(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := list(100)
	s := plant(t, serves(&body))
	set := domainSet(t)
	ctx := context.Background()

	first, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("first take: %v", err)
	}
	full := first[0].Version
	if first[0].Keys != 100 {
		t.Fatalf("first take landed %d", first[0].Keys)
	}

	// A tenth of the list. Parses cleanly, and is refused.
	body = list(10)
	shrunk, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("shrunk take: %v", err)
	}
	if shrunk[0].Refusal == "" {
		t.Fatalf("a list at a tenth of its size landed silently: %+v", shrunk[0])
	}
	if got := s.State.plane.get("domain"); got == nil || len(got.byKey) != 100 {
		t.Fatalf("the refused take changed the live snapshot: %v", got)
	}
	if _, ok := w.held("domain", "disposable", full); !ok {
		t.Error("the previous version must stand")
	}

	// Ten times the list is refused for the same reason, in the other direction.
	body = list(1000)
	grown, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("grown take: %v", err)
	}
	if grown[0].Refusal == "" {
		t.Fatalf("a list at ten times its size landed silently: %+v", grown[0])
	}

	// Ordinary movement is not a swing: published lists do move.
	body = list(150)
	moved, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("moved take: %v", err)
	}
	if moved[0].Refusal != "" || moved[0].Keys != 150 {
		t.Fatalf("ordinary growth must land: %+v", moved[0])
	}

	// And the operator has a lever: force says the change is real.
	body = list(10)
	forced, err := take(ctx, s, set, nil, true)
	if err != nil {
		t.Fatalf("forced take: %v", err)
	}
	if forced[0].Refusal != "" || forced[0].Keys != 10 {
		t.Fatalf("force must accept a real change: %+v", forced[0])
	}
}

// TestAPoisonedDisposableListDoesNotLand.
//
// The size gate catches a list that arrives at a fraction or a multiple of
// itself. It cannot catch ONE added row, and one added row is the whole attack on
// the one source in this catalog with no pin, no signature and no digest: the
// disposable list is fetched from a public repository, and "gmail.com is
// disposable" refuses a large share of every tenant's legitimate signups at once.
// A publisher naming a mailbox provider is wrong about something we can check, so
// the take is refused whole and the previous version stands.
func TestAPoisonedDisposableListDoesNotLand(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := list(50)
	s := plant(t, serves(&body))
	set := domainSet(t)
	ctx := context.Background()

	first, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("first take: %v", err)
	}
	good := first[0].Version

	// One row added, everything else identical.
	body = list(50) + "\nGmail.com\n"
	poisoned, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("poisoned take: %v", err)
	}
	if poisoned[0].Refusal == "" {
		t.Fatalf("a list naming a mailbox provider landed: %+v", poisoned[0])
	}
	if got := s.State.plane.get("domain"); got == nil || len(got.byKey) != 50 {
		t.Fatalf("the poisoned take reached the live snapshot: %v", got)
	}
	if _, ok := w.held("domain", "disposable", good); !ok {
		t.Error("the previous version must stand")
	}
	// Even forced: force accepts a size change somebody vouched for, not a list
	// that is wrong about a fact.
	if forced, err := take(ctx, s, set, nil, true); err != nil || forced[0].Refusal == "" {
		t.Errorf("force must not land a poisoned list: %+v %v", forced, err)
	}
}

func TestSwungMeasuresBothDirections(t *testing.T) {
	for _, c := range []struct {
		was, now uint64
		want     bool
	}{
		{0, 5000, false},  // a first take has nothing to compare against
		{100, 100, false}, // unchanged
		{100, 399, false}, // inside the bound
		{100, 401, true},  // past it, growing
		{100, 26, false},  // inside the bound
		{100, 24, true},   // past it, shrinking
		{100, 0, true},    // gone
	} {
		if got := swung(c.was, c.now); got != c.want {
			t.Errorf("swung(%d, %d) = %v, want %v", c.was, c.now, got, c.want)
		}
	}
}

// TestAReceiptWithNothingInItIsNotAFreshList.
//
// A set of kind attest holds no membership here: the screening engine holds it
// and this plane records the engine's load receipt. That makes the receipt the
// ONLY evidence there is, and it was written through verbatim — a loader posting
// {"source":"OFAC","version":"","keys":0} made the sanction set report a current,
// non-stale version composed as "OFAC@", so the compliance freshness signal said
// the designation lists were current at the moment the loader served nothing.
// The field's own doc comment names this failure; the code did not check for it.
func TestAReceiptWithNothingInItIsNotAFreshList(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := ""
	s := plant(t, serves(&body))
	set, ok := byName("sanction")
	if !ok {
		t.Fatal("no sanction set")
	}
	ctx := context.Background()

	// A load that designated nobody, and one that names no version.
	got, err := take(ctx, s, set, []ReferenceReceipt{
		{Source: "OFAC", Version: "2026-08-01", Keys: 0},
		{Source: "UN", Version: "", Keys: 12000},
	}, false)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	for _, r := range got {
		if r.Refusal == "" {
			t.Errorf("%s: a receipt with no designations or no version was recorded as a successful load: %+v", r.Source, r)
		}
	}
	if v, ok := w.held("sanction", "OFAC", "2026-08-01"); !ok || v.Status == statusReady {
		t.Errorf("a zero-designation load is durable as a refusal, not as a ready version: %+v", v)
	}
	// The set therefore still has no version, and says so rather than reporting a
	// current one composed of nothing.
	view := project(set, s.State.plane.get("sanction"), s.State.now())
	if view.Version != "" {
		t.Errorf("the set reports version %q from receipts that carried nothing", view.Version)
	}
	if view.Refusal == "" {
		t.Error("a set with no ready version must refuse")
	}

	// A real receipt is recorded, ready, and names its freshness.
	if _, err := take(ctx, s, set, []ReferenceReceipt{{Source: "OFAC", Version: "sha-abc", Keys: 12000}}, false); err != nil {
		t.Fatalf("take: %v", err)
	}
	v, ok := w.held("sanction", "OFAC", "sha-abc")
	if !ok || v.Status != statusReady || v.Keys != 12000 {
		t.Fatalf("a real load must be ready: %+v", v)
	}
}

func TestUnattestedNamesWhyAReceiptIsNotEvidence(t *testing.T) {
	for _, c := range []struct {
		name string
		r    ReferenceReceipt
		want bool
	}{
		{"whole", ReferenceReceipt{Version: "v", Keys: 1}, false},
		{"no designations", ReferenceReceipt{Version: "v", Keys: 0}, true},
		{"negative", ReferenceReceipt{Version: "v", Keys: -1}, true},
		{"no version", ReferenceReceipt{Version: " ", Keys: 1}, true},
		{"the loader said so", ReferenceReceipt{Version: "v", Keys: 1, Refusal: "connection reset"}, true},
	} {
		if got := unattested(c.r) != ""; got != c.want {
			t.Errorf("%s: unattested = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestColdStartTakesRatherThanRefusingForTheWholeBeat.
//
// Into an EMPTY warehouse — a first deploy, a wipe, a migration — the first
// hydrate legitimately finds nothing and succeeds. With the take only on the
// beat, every set then answered "this set has never loaded" for a whole [beat]
// while the only operator lever was one hand-made refresh call per set. A control
// that is not there for six hours after a deploy is not a control.
func TestColdStartTakesRatherThanRefusingForTheWholeBeat(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := list(20)
	s := plant(t, serves(&body))

	// Nothing has ever loaded, and the plane says so.
	if got := s.State.plane.get("domain"); got != nil {
		t.Fatalf("a fresh plane already holds %v", got)
	}

	s.State.work.done.Add(1)
	go tend(s)
	t.Cleanup(func() { close(s.State.work.stop); s.State.work.done.Wait() })

	// Well inside `settle`, let alone `beat`.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := s.State.plane.get("domain")
		if got != nil && len(got.byKey) == 20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the plane did not take a set at cold start; it refuses until the %s beat: %v", beat, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if v, _ := project(domainSet(t), s.State.plane.get("domain"), s.State.now()), 0; v.Refusal != "" {
		t.Errorf("a set taken at cold start still refuses: %s", v.Refusal)
	}
}

// TestASweepTakesOnlyWhatIsDue: the cold start and the beat run the SAME pass, so
// a set that is current is not re-taken by either.
func TestASweepTakesOnlyWhatIsDue(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := list(20)
	s := plant(t, serves(&body))

	sweep(s)
	first := s.State.plane.get("domain")
	if first == nil || len(first.byKey) != 20 {
		t.Fatalf("the first sweep did not take the set: %v", first)
	}
	wrote := w.wrote

	// Nothing has aged, so the second sweep writes no entry rows.
	sweep(s)
	if w.wrote != wrote {
		t.Errorf("a sweep re-landed %d rows for a set that is still current", w.wrote-wrote)
	}
}

// TestASourceCannotLandWhatNoLookupCouldReach.
//
// [maxKey] is refused at the wire in both directions — a key looked up, a key
// written as an override — and a MEMBER arriving from a publisher is the same
// value from the third side. A member longer than the bound is one no lookup can
// ever match, so landing it costs the warehouse, every hydrate and the snapshot
// every request reads, and buys nothing.
//
// Refused WHOLE, like the mailbox-provider gate: a list quietly missing the rows
// we dropped answers "not listed" for them and reads exactly like a clean world.
func TestASourceCannotLandWhatNoLookupCouldReach(t *testing.T) {
	w := newWarehouse()
	w.use(t)
	body := list(50)
	s := plant(t, serves(&body))
	set := domainSet(t)
	ctx := context.Background()

	first, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("first take: %v", err)
	}
	good := first[0].Version

	// One member past the door, everything else ordinary.
	body = list(50) + "\n" + strings.Repeat("a.", maxKey) + "example\n"
	huge, err := take(ctx, s, set, nil, false)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if huge[0].Refusal == "" {
		t.Fatalf("a member no lookup could reach landed in the baseline every org reads: %+v", huge[0])
	}
	if got := s.State.plane.get("domain"); got == nil || len(got.byKey) != 50 {
		t.Fatalf("the refused take reached the live snapshot: %v", got)
	}
	if _, ok := w.held("domain", "disposable", good); !ok {
		t.Error("the previous version must stand")
	}
	// Force is the lever for a size somebody vouched for, not for a member the
	// lookup door would refuse anyway.
	if forced, err := take(ctx, s, set, nil, true); err != nil || forced[0].Refusal == "" {
		t.Errorf("force must not land an unreachable member: %+v %v", forced, err)
	}

	// A member AT the bound is a member, and lands.
	at := strings.Repeat("b", maxKey-len(".example")) + ".example"
	if len(at) != maxKey {
		t.Fatalf("the sample member is %d bytes, not the bound", len(at))
	}
	body = list(50) + "\n" + at + "\n"
	ok2, err := take(ctx, s, set, nil, false)
	if err != nil || ok2[0].Refusal != "" || ok2[0].Keys != 51 {
		t.Fatalf("a member at the bound must land: %+v %v", ok2[0], err)
	}
}

// TestASourceCannotLandMoreMembersThanTheDoorAdmits.
//
// [swing] bounds how far a take may move from the version it REPLACES, and a
// first take has no previous version to be measured against — which, after a
// cold start into an empty warehouse, is every take. So the publisher's end of
// the resolve amplifier was open: one source, one refresh, however many members
// [maxBody] admits, all of them into the snapshot this one-replica deployment
// keeps in memory for every request.
func TestASourceCannotLandMoreMembersThanTheDoorAdmits(t *testing.T) {
	s := plant(t, serves(new(string)))

	// One string, shared by every element: this test is about the COUNT, and
	// materialising a million distinct keys would only measure the test.
	flood := make([]Entry, maxMembers+1)
	for i := range flood {
		flood[i] = Entry{Key: "member.example"}
	}
	src := Source{Name: "flood", Origin: "local", Basis: GrantOwn, Terms: "a test",
		produce: func(producer) ([]Entry, error) { return flood, nil }}

	if _, err := gather(context.Background(), s, src, s.State.now()); err == nil {
		t.Fatalf("a source landed %d members with nothing to measure it against", len(flood))
	}
	// And the bound admits what a publisher plausibly serves: the largest list in
	// this catalog is a ninth of it.
	fine := Source{Name: "fine", Origin: "local", Basis: GrantOwn, Terms: "a test",
		produce: func(producer) ([]Entry, error) { return flood[:maxMembers], nil }}
	if _, err := gather(context.Background(), s, fine, s.State.now()); err != nil {
		t.Errorf("a source at the bound must be admitted: %v", err)
	}
}
