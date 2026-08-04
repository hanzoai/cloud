package reference

// reference_test.go holds the properties this plane is only worth having if it
// has: that one organisation's entries can never reach another's, that an
// organisation's own say beats the published baseline, that a stale or unloaded
// set says so instead of answering clean, and that the shared baseline can carry
// nothing a single organisation produced.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ── the catalog ──────────────────────────────────────────────────────────────

// TestCatalogStatesItsTerms holds the lawfulness rule at the shape: every source
// we fetch names the licence it is redistributed under, and every source we
// cannot lawfully hold is DECLARED as a seam with the reason rather than being
// quietly absent. An omission and a refusal look identical from outside, and
// only one of them is a decision.
func TestCatalogStatesItsTerms(t *testing.T) {
	seams := 0
	for _, s := range Catalog() {
		if s.Name == "" || s.What == "" || s.Match == "" {
			t.Errorf("%q is missing a name, a description or a matcher", s.Name)
		}
		if s.Name != strings.ToLower(s.Name) || strings.ContainsAny(s.Name, " _-/") {
			t.Errorf("%q is not one lower-case word", s.Name)
		}
		switch s.Kind {
		case KindSeam:
			seams++
			if s.Refusal == "" {
				t.Errorf("%q is a seam with no stated reason, which is an omission wearing a decision's clothes", s.Name)
			}
			if len(s.Sources) != 0 {
				t.Errorf("%q is a seam and yet names sources", s.Name)
			}
		default:
			if s.Refusal != "" {
				t.Errorf("%q states a licence refusal but is not a seam", s.Name)
			}
			if len(s.Sources) == 0 {
				t.Errorf("%q has no sources", s.Name)
			}
			for _, src := range s.Sources {
				if src.Terms == "" {
					t.Errorf("%s/%s states no terms; a source with no stated licence does not go in the catalog", s.Name, src.Name)
				}
				if src.Origin == "" {
					t.Errorf("%s/%s states no origin, so nobody can take the same bytes", s.Name, src.Name)
				}
			}
		}
		if s.MaxAge <= 0 {
			t.Errorf("%q has no freshness bound, so it can never be reported stale", s.Name)
		}
	}
	if seams == 0 {
		t.Error("the catalog declares no seams at all, which would mean every source we want is licensed to us")
	}
}

// TestEverySourceStatesABasisItsKindPermits is the licence gate with teeth.
//
// Terms alone could not be gated: it carried "CC0-1.0" (a licence) and
// "operator-published range list" (a description of where a file came from) in
// the same field, and the only assertion over it was that the string was not
// empty — so eight of fourteen sources stated a CATEGORY where a grant was
// implied and passed. An unlicensed source wearing a licence field is the mirror
// image of the seam argument this plane is built on.
//
// Basis is a closed vocabulary, so the position is now machine-checkable: what
// each kind of set may rest on is stated here once, and a source whose basis is
// unset — which is what re-introducing a free-text-only catalog entry looks like —
// is not in the vocabulary and fails.
func TestEverySourceStatesABasisItsKindPermits(t *testing.T) {
	// What each kind of set may rest on, and why.
	allowed := map[Kind]map[Grant]bool{
		// Downloaded from someone else, so it must rest on something that lets
		// their bytes reach a tenant.
		KindFetch: {GrantLicence: true, GrantRegistry: true, GrantOperator: true},
		// Computed here, so nothing of anyone else's is redistributed.
		KindLocal: {GrantOwn: true},
		// Held by the component that screens against it: nothing reaches a tenant.
		KindAttest: {GrantNone: true},
	}
	for _, s := range Catalog() {
		if s.Kind == KindSeam {
			continue
		}
		for _, src := range s.Sources {
			if !grants[src.Basis] {
				t.Errorf("%s/%s states basis %q, which is not one of the five; a basis outside the vocabulary is a licence claim nothing can check", s.Name, src.Name, src.Basis)
				continue
			}
			if !allowed[s.Kind][src.Basis] {
				t.Errorf("%s/%s is a %s source resting on %q, which that kind may not rest on", s.Name, src.Name, s.Kind, src.Basis)
			}
			if src.Basis.Redistributes() != (s.Kind == KindFetch) {
				t.Errorf("%s/%s: basis %q redistributes=%v but the set is kind %s", s.Name, src.Name, src.Basis, src.Basis.Redistributes(), s.Kind)
			}
			// "We hold a licence" is a claim, and a claim is only checkable if it
			// says WHICH. So a licence basis has to name one of the identifiers this
			// catalog actually redistributes under — which is what stops the
			// substitution the old field allowed, a category sentence sitting in the
			// place a grant belongs.
			if src.Basis == GrantLicence && !named(src.Terms) {
				t.Errorf("%s/%s rests on a licence and cites %q, which names no licence", s.Name, src.Name, src.Terms)
			}
		}
	}

	// And the position is on the WIRE, so the licence audit is one an operator can
	// run without reading this file.
	for _, s := range Catalog() {
		view := project(s, nil, time.Now())
		for _, src := range view.Sources {
			if src.Basis == "" {
				t.Errorf("%s/%s publishes no basis", s.Name, src.Source)
			}
		}
	}
}

// named reports whether a citation identifies a licence, from the closed list of
// the ones this catalog redistributes under. Adding a source under a new licence
// is adding it here — deliberately, because that is the moment somebody read it.
func named(terms string) bool {
	for _, id := range []string{"CC0-1.0", "MIT", "CC BY"} {
		if strings.Contains(terms, id) {
			return true
		}
	}
	return false
}

// TestFetchSourcesParseAndLocalSourcesProduce holds that every source has
// exactly one way to become entries. A source with neither is unreachable; a
// source with both has two answers to one question.
func TestFetchSourcesParseAndLocalSourcesProduce(t *testing.T) {
	for _, s := range Catalog() {
		for _, src := range s.Sources {
			has, makes := src.parse != nil, src.produce != nil
			switch s.Kind {
			case KindFetch:
				if !has || makes {
					t.Errorf("%s/%s: a fetched source parses and does not produce", s.Name, src.Name)
				}
			case KindLocal:
				if has || !makes {
					t.Errorf("%s/%s: a local source produces and does not parse", s.Name, src.Name)
				}
			case KindAttest:
				if has || makes {
					t.Errorf("%s/%s: an attested source neither parses nor produces — the holder does", s.Name, src.Name)
				}
			}
		}
	}
}

// ── the baseline boundary ────────────────────────────────────────────────────

// TestBaselineHasNowhereToPutATenant is the isolation argument as a test on the
// SHAPE. The two warehouse tables every organisation reads carry no column an
// organisation could be recorded in, so a cross-tenant write is not something
// the code declines to do — it is something the schema cannot express.
func TestBaselineHasNowhereToPutATenant(t *testing.T) {
	forbidden := []string{"org", "owner", "tenant", "scope", "project", "account", "brand", "user", "subject", "person", "distinct"}
	for _, ddl := range []string{createSource, createEntry} {
		for _, line := range strings.Split(ddl, "\n") {
			col, _, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok || col == "" {
				continue
			}
			for _, bad := range forbidden {
				if strings.EqualFold(col, bad) {
					t.Errorf("the shared baseline declares a %q column; the whole isolation argument is that it cannot hold one", col)
				}
			}
		}
	}
}

// TestNoWriteBindsATenant holds the other half: no statement in this file's
// plane writes a tenant, because none of them takes one. If a scope argument
// ever appears the count changes and this test says so.
func TestNoWriteBindsATenant(t *testing.T) {
	for name, stmt := range map[string]string{
		"mark":  markStatement,
		"read":  readStatement,
		"taken": takenStatement,
		"list":  currentStatement,
		"prune": pruneStatement,
	} {
		lower := strings.ToLower(stmt)
		for _, bad := range []string{" org ", "org =", "owner", "tenant", "scope ="} {
			if strings.Contains(lower, bad) {
				t.Errorf("the %s statement mentions %q; the baseline plane has no tenant", name, strings.TrimSpace(bad))
			}
		}
	}
}

// TestDeriveRefusesWhatOneOrgProduced is the mandated proof. A fleet in which a
// single organisation produced every observation yields an EMPTY baseline: the
// aggregate cannot carry anything one organisation could have made alone,
// whatever the volume.
func TestDeriveRefusesWhatOneOrgProduced(t *testing.T) {
	// One organisation, an enormous number of observations, one very busy browser.
	one := func(_ context.Context, q string, args ...any) ([]map[string]any, error) {
		if !strings.Contains(q, "uniqExact(org)") {
			t.Fatalf("the device aggregate must count distinct organisations; statement was %q", q)
		}
		return []map[string]any{
			{"id": "browser-seen-a-million-times", "orgs": uint64(1), "n": uint64(1_000_000)},
			{"id": "another-busy-browser", "orgs": uint64(Orgs - 1), "n": uint64(Rows * 10)},
			{"id": "wide-but-thin", "orgs": uint64(Orgs + 5), "n": uint64(Rows - 1)},
		}, nil
	}
	got, err := produceDevice(producer{ctx: context.Background(), now: time.Now(), query: one})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the baseline published %d entries from below the k-anonymity floor: %+v", len(got), got)
	}

	// And the floor is not a wall: a genuinely fleet-wide browser DOES publish.
	many := func(context.Context, string, ...any) ([]map[string]any, error) {
		return []map[string]any{{"id": "shared-everywhere", "orgs": uint64(Orgs), "n": uint64(Rows)}}, nil
	}
	got, err = produceDevice(producer{ctx: context.Background(), now: time.Now(), query: many})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 publishable entry, got %d", len(got))
	}
	if got[0].Key == "shared-everywhere" {
		t.Error("the baseline published the raw browser identifier; it must publish a digest")
	}
	if len(got[0].Key) != 64 {
		t.Errorf("the published key is not a sha256 digest: %q", got[0].Key)
	}
	if got[0].Value["orgs"] != "25-99" {
		t.Errorf("the count must be published as a band, got %q", got[0].Value["orgs"])
	}
}

// TestDeviceStatementIsConstantAndExcludesTheAnonymousLane holds the two
// properties of the one cross-organisation statement: nothing a caller sends can
// reach it, and the reserved anonymous tenant — where credential-less writes are
// filed — never contributes, so a stranger cannot push a key over the floor.
func TestDeviceStatementIsConstantAndExcludesTheAnonymousLane(t *testing.T) {
	var bound []any
	spy := func(_ context.Context, q string, args ...any) ([]map[string]any, error) {
		if q != deviceStatement {
			t.Error("the device statement is not the package constant")
		}
		bound = args
		return nil, nil
	}
	if _, err := produceDevice(producer{ctx: context.Background(), now: time.Now(), query: spy}); err != nil {
		t.Fatal(err)
	}
	if len(bound) != 5 {
		t.Fatalf("want 5 bound parameters, got %d", len(bound))
	}
	if bound[2] != publicTenant {
		t.Errorf("the anonymous lane is not excluded; third bind is %v", bound[2])
	}
	if bound[3] != Orgs || bound[4] != Rows {
		t.Errorf("the floor is not bound into the statement: %v, %v", bound[3], bound[4])
	}
	if !strings.Contains(deviceStatement, "org != ?") {
		t.Error("the anonymous lane must be excluded at the source, not filtered later")
	}
}

// TestTheDeviceAggregationStatesItsBudget.
//
// The LIMIT bounds the ROWS RETURNED and says nothing about the work: the inner
// GROUP BY visits every distinct browser identity the whole fleet saw in the
// window before a single row meets the floor. That is potentially hundreds of
// millions of groups against the ONE warehouse that analytics, insights, sentry,
// commerce and gateway usage all read from, run unattended roughly daily. A
// statement with no ceiling on memory and no ceiling on time can stall every
// other plane in the fleet to refresh one reference set.
//
// So the statement carries its own budget: spill rather than grow, stop rather
// than spill forever, give up rather than run past its window. Exceeding one
// fails THIS take, which is a case the plane already handles — the previous
// version stands and ages out visibly.
func TestTheDeviceAggregationStatesItsBudget(t *testing.T) {
	for _, want := range []string{
		"max_execution_time",
		"max_memory_usage",
		"max_bytes_before_external_group_by",
	} {
		if !strings.Contains(deviceStatement, want) {
			t.Errorf("the one cross-fleet aggregation states no %s; a LIMIT bounds the answer, not the work", want)
		}
	}
	// The budget is part of the statement CONSTANT, so nothing a caller sends can
	// raise it and no call site can forget it.
	if !strings.Contains(deviceStatement, "SETTINGS") || strings.Index(deviceStatement, "SETTINGS") < strings.Index(deviceStatement, "LIMIT") {
		t.Error("the budget must ride the statement itself, after the LIMIT")
	}
	if strings.Contains(deviceBudget, "?") {
		t.Error("a budget with a placeholder in it is a budget a caller can set")
	}
}

// TestDeviceRefusesWithoutTheEventPlane holds that a derived set with no
// warehouse produces an error rather than an empty set that would read as
// "no shared devices anywhere".
func TestDeviceRefusesWithoutTheEventPlane(t *testing.T) {
	if _, err := produceDevice(producer{ctx: context.Background(), now: time.Now()}); err == nil {
		t.Fatal("a device aggregate with no event plane must refuse, not answer empty")
	}
}

// ── freshness ────────────────────────────────────────────────────────────────

func setNamed(t *testing.T, name string) Set {
	t.Helper()
	s, ok := byName(name)
	if !ok {
		t.Fatalf("no set named %q", name)
	}
	return s
}

// TestUnloadedSetRefusesRatherThanAnsweringClean is the discipline this whole
// plane inherits: a list that was never loaded answers "not listed" for
// everything, and that is indistinguishable from a clean world.
func TestUnloadedSetRefusesRatherThanAnsweringClean(t *testing.T) {
	set := setNamed(t, "domain")
	s := build(set, nil, nil)
	if s.refusal == "" {
		t.Fatal("a set that never loaded must refuse")
	}
	a, err := answer(set, s, nil, "user@tempbox.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a.Hit {
		t.Error("an unloaded set cannot hit")
	}
	if a.Refusal == "" {
		t.Fatal("the answer must carry the refusal, or a caller reads a miss as clean")
	}
}

// TestEmptyFetchedSetIsAFailureAndEmptyLocalSetIsAFact holds the one rule that
// separates them, derived from the kind so the two can never disagree.
func TestEmptyFetchedSetIsAFailureAndEmptyLocalSetIsAFact(t *testing.T) {
	took := []version{{Source: "disposable", Version: "v", AsOf: time.Now(), Status: statusReady}}
	if got := build(setNamed(t, "domain"), took, nil); got.refusal == "" {
		t.Error("a downloaded list that parsed to nothing must refuse: no publisher's list is empty")
	}
	took = []version{{Source: "fleet", Version: "v", AsOf: time.Now(), Status: statusReady}}
	if got := build(setNamed(t, "device"), took, nil); got.refusal != "" {
		t.Errorf("a computed set with no members is a fact, not a failure: %q", got.refusal)
	}
}

// TestStalenessIsReportedAndTheSetStillAnswers: a stale set is not a broken one.
// Yesterday's list beats no list, so it answers — and every answer says how old
// it is, because a decision taken against a three-week-old list is a weaker
// decision and has to be able to know that.
func TestStalenessIsReportedAndTheSetStillAnswers(t *testing.T) {
	set := setNamed(t, "domain")
	now := time.Now().UTC()
	old := now.Add(-set.MaxAge - time.Hour)
	took := []version{{Source: "disposable", Version: "abc", AsOf: old, Fetched: old, Status: statusReady, Keys: 1, Landed: 1}}
	s := build(set, took, []Entry{{Key: "tempbox.example", Value: map[string]string{"class": "disposable"}}})

	if !s.stale(now) {
		t.Fatal("a set older than its bound is stale")
	}
	a, err := answer(set, s, nil, "user@tempbox.example", now)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Hit {
		t.Fatal("a stale set still answers")
	}
	if !a.Stale {
		t.Error("the answer must say the set was stale")
	}
	if a.Version == "" || a.AsOf == "" || a.Age == "" {
		t.Errorf("an answer must name the version it consulted and how old it is: %+v", a)
	}
	if !strings.Contains(a.Version, "abc") {
		t.Errorf("the version must name the contributing digest, got %q", a.Version)
	}

	// Fresh again once the publisher is current.
	fresh := []version{{Source: "disposable", Version: "abc", AsOf: now, Fetched: now, Status: statusReady, Keys: 1, Landed: 1}}
	if build(set, fresh, []Entry{{Key: "tempbox.example"}}).stale(now) {
		t.Error("a set current as of now is not stale")
	}
}

// TestASetIsAsFreshAsItsOldestPublisher: reporting the newest would let one
// daily-updating source hide three that stopped answering months ago.
func TestASetIsAsFreshAsItsOldestPublisher(t *testing.T) {
	set := setNamed(t, "net")
	now := time.Now().UTC()
	took := []version{
		{Source: "aws", Version: "1", AsOf: now, Status: statusReady},
		{Source: "tor", Version: "2", AsOf: now.Add(-set.MaxAge - time.Hour), Status: statusReady},
	}
	s := build(set, took, []Entry{{Key: "10.0.0.0/8"}})
	if !s.stale(now) {
		t.Fatal("one stale publisher makes the set stale, however current the others are")
	}
}

// TestSeamRefusesWithItsLicenceReason: an unlicensed set names the licence we do
// not hold. It does not answer, and it does not pretend to be missing.
func TestSeamRefusesWithItsLicenceReason(t *testing.T) {
	for _, name := range []string{"pep", "issuer", "reputation"} {
		set := setNamed(t, name)
		if set.Kind != KindSeam {
			t.Fatalf("%q should be a seam", name)
		}
		s := build(set, nil, nil)
		a, err := answer(set, s, nil, "anything", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if a.Hit || a.Refusal == "" {
			t.Errorf("%q must refuse, got %+v", name, a)
		}
		if !strings.Contains(strings.ToLower(a.Refusal), "licen") && !strings.Contains(strings.ToLower(a.Refusal), "held") {
			t.Errorf("%q's refusal should name the licence problem, got %q", name, a.Refusal)
		}
	}
}

// TestAttestSetReportsFreshnessAndNotMembership: the designations stay with the
// component that screens against them, so this plane answers how fresh they are
// and refuses to answer whether a party is on them.
func TestAttestSetReportsFreshnessAndNotMembership(t *testing.T) {
	set := setNamed(t, "sanction")
	now := time.Now().UTC()
	took := []version{{Source: "OFAC", Version: "deadbeef", AsOf: now, Status: statusReady, Keys: 12000, Landed: 12000}}
	s := build(set, took, nil)
	if s.version == "" || !strings.Contains(s.version, "OFAC@deadbeef") {
		t.Errorf("an attested set still names its version, got %q", s.version)
	}
	if s.stale(now) {
		t.Error("a receipt taken now is not stale")
	}
	a, _ := answer(set, s, nil, "some party", now)
	if a.Hit || a.Refusal == "" {
		t.Errorf("membership is held elsewhere and must refuse here, got %+v", a)
	}
}

// ── matching ─────────────────────────────────────────────────────────────────

func TestCandidatesAreBoundedAndMostSpecificFirst(t *testing.T) {
	cases := []struct {
		set   string
		key   string
		first string
		max   int
	}{
		{"domain", "user@mail.tempbox.example", "mail.tempbox.example", 8},
		{"net", "3.5.140.1", "3.5.140.1/32", 40},
		{"net", "2600:3c00::1", "2600:3c00::1/128", 140},
		{"bin", "4111111111111111", "41111111", 8},
		{"asn", "13335", "13335", 1},
	}
	for _, c := range cases {
		got := candidates(setNamed(t, c.set), c.key)
		if len(got) == 0 {
			t.Fatalf("%s/%s produced no candidates", c.set, c.key)
		}
		if got[0] != c.first {
			t.Errorf("%s/%s: most specific candidate is %q, want %q", c.set, c.key, got[0], c.first)
		}
		if len(got) > c.max {
			t.Errorf("%s/%s produced %d candidates, which is not a bounded lookup", c.set, c.key, len(got))
		}
	}
}

// TestASuffixWalkIsLinearInTheKey is the allocation half of the fan-out
// ship-blocker, stated on the function rather than on the wire.
//
// candidates claimed to be "bounded", and it was — in COUNT. It split the host
// into labels and re-joined every tail, so an L-label host allocated a fresh copy
// of each of its L suffixes: O(L x len(key)) BYTES. One 8 KB dotted key
// materialised 16.8 MB, and [maxKeys] of them 1.7 GB in a single request. A Go
// string is immutable, so a suffix is the same bytes with a different header:
// walking the dot offsets is the identical answer in O(L) headers over one
// backing array.
//
// The door refuses a key this long. This measures at a size the door would never
// admit precisely so the SHAPE is pinned and not just the bound — the two are
// independent, and either one alone is one edit away from the outage.
func TestASuffixWalkIsLinearInTheKey(t *testing.T) {
	set := setNamed(t, "domain")
	key := strings.Repeat("a.", 4096) + "example"

	// The answer is unchanged: the same suffixes, most specific first, as the
	// split-and-join it replaces.
	got := candidates(set, "user@mail.tempbox.example")
	want := []string{"mail.tempbox.example", "tempbox.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if one := candidates(set, "example"); !reflect.DeepEqual(one, []string{"example"}) {
		t.Fatalf("a single-label host = %v, want the host itself", one)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	sink := candidates(set, key)
	runtime.ReadMemStats(&after)
	if len(sink) != 4096 {
		t.Fatalf("a %d-label host produced %d candidates", strings.Count(key, ".")+1, len(sink))
	}
	// The headers are 16 bytes each; the bytes themselves are shared. Linear is
	// ~64 KB, quadratic is ~16 MB, so a 1 MiB ceiling separates them by a factor
	// of sixteen and is nowhere near either.
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("one %d-byte key allocated %d bytes of suffixes; a suffix is a slice, not a copy", len(key), grew)
	}
}

func TestDomainMatchWalksUpLabels(t *testing.T) {
	set := setNamed(t, "domain")
	s := loaded(set, []Entry{{Key: "tempbox.example", Value: map[string]string{"class": "disposable"}}})
	for _, key := range []string{"tempbox.example", "user@tempbox.example", "a.b.tempbox.example", "USER@Mail.TempBox.Example"} {
		a, _ := answer(set, s, nil, key, time.Now())
		if !a.Hit {
			t.Errorf("%q should match the disposable apex", key)
		}
		if a.Matched != "tempbox.example" {
			t.Errorf("%q matched %q; the answer must name the enclosing member", key, a.Matched)
		}
	}
	if a, _ := answer(set, s, nil, "user@notdisposable.example", time.Now()); a.Hit {
		t.Error("an unrelated domain must not match")
	}
}

func TestNetMatchTakesTheLongestPrefix(t *testing.T) {
	set := setNamed(t, "net")
	s := loaded(set, []Entry{
		{Key: "10.0.0.0/8", Value: map[string]string{"class": "hosting", "operator": "wide"}},
		{Key: "10.1.2.0/24", Value: map[string]string{"class": "tor", "operator": "narrow"}},
	})
	a, _ := answer(set, s, nil, "10.1.2.3", time.Now())
	if a.Matched != "10.1.2.0/24" || a.Value["operator"] != "narrow" {
		t.Errorf("longest prefix must win, got %+v", a)
	}
	a, _ = answer(set, s, nil, "10.9.9.9", time.Now())
	if a.Matched != "10.0.0.0/8" {
		t.Errorf("the enclosing block should answer, got %+v", a)
	}
	if a, _ := answer(set, s, nil, "203.0.113.1", time.Now()); a.Hit {
		t.Error("an address in neither block must not match")
	}
}

func TestBINMatchIsStructural(t *testing.T) {
	set := setNamed(t, "bin")
	entries, err := produceBIN(producer{})
	if err != nil {
		t.Fatal(err)
	}
	s := loaded(set, entries)
	for pan, want := range map[string]string{
		"4111111111111111": "visa",
		"5500000000000004": "mastercard",
		"2221000000000009": "mastercard",
		"378282246310005":  "amex",
		"6011111111111117": "discover",
		"3530111333300000": "jcb",
		"6221260000000000": "unionpay",
	} {
		a, _ := answer(set, s, nil, pan, time.Now())
		if !a.Hit || a.Value["scheme"] != want {
			t.Errorf("%s should be %s, got %+v", pan, want, a)
		}
	}
	// The issuer behind the prefix is the seam, and the bin set does not pretend
	// to know it.
	a, _ := answer(set, s, nil, "4111111111111111", time.Now())
	for _, forbidden := range []string{"issuer", "bank", "country", "funding"} {
		if _, ok := a.Value[forbidden]; ok {
			t.Errorf("the structural table must not claim to know %q", forbidden)
		}
	}
}

func TestPatternMatchAndItsFastReject(t *testing.T) {
	set := setNamed(t, "crawler")
	s := loaded(set, []Entry{
		{Key: `Googlebot\/`, Value: map[string]string{"class": "crawler"}},
		{Key: `bingbot`, Value: map[string]string{"class": "crawler"}},
	})
	if s.any == nil {
		t.Fatal("the fast-reject alternation was not built")
	}
	a, _ := answer(set, s, nil, "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", time.Now())
	if !a.Hit || a.Matched != `Googlebot\/` {
		t.Errorf("a declared crawler should match, got %+v", a)
	}
	if a, _ := answer(set, s, nil, "Mozilla/5.0 (Macintosh) Safari/605.1", time.Now()); a.Hit {
		t.Error("an ordinary browser must not match")
	}
}

func TestRangeMatchCoversDelegatedBlocks(t *testing.T) {
	set := setNamed(t, "asn")
	s := loaded(set, []Entry{
		{Key: "1-1876", Value: map[string]string{"registry": "arin", "status": "delegated"}},
		{Key: "1877-1901", Value: map[string]string{"registry": "ripe", "status": "delegated"}},
	})
	a, _ := answer(set, s, nil, "1880", time.Now())
	if !a.Hit || a.Value["registry"] != "ripe" {
		t.Errorf("1880 falls in the RIPE block, got %+v", a)
	}
	if a, _ := answer(set, s, nil, "AS100", time.Now()); !a.Hit {
		t.Error("an AS-prefixed number should read the same as a bare one")
	}
	if a, _ := answer(set, s, nil, "4200000000", time.Now()); a.Hit {
		t.Error("a number in no delegated block must not match")
	}
}

// loaded builds a snapshot as though one publisher had just supplied entries.
func loaded(set Set, entries []Entry) *snap {
	now := time.Now().UTC()
	src := "test"
	if len(set.Sources) > 0 {
		src = set.Sources[0].Name
	}
	return build(set, []version{{
		Set: set.Name, Source: src, Version: digest(entries),
		AsOf: now, Fetched: now, Status: statusReady,
		Keys: uint64(len(entries)), Landed: uint64(len(entries)),
	}}, entries)
}

// ── the digest, ingest idempotence and resumability ──────────────────────────

// TestDigestIsOfTheSetAndNotOfTheBytes: a publisher who reorders their file has
// not changed the set, and must not mint a version.
func TestDigestIsOfTheSetAndNotOfTheBytes(t *testing.T) {
	a := []Entry{{Key: "b", Value: map[string]string{"x": "1", "y": "2"}}, {Key: "a"}}
	b := []Entry{{Key: "a"}, {Key: "b", Value: map[string]string{"y": "2", "x": "1"}}}
	if digest(a) != digest(b) {
		t.Fatal("reordering the file, or a value map, changed the version")
	}
	c := []Entry{{Key: "a"}, {Key: "b", Value: map[string]string{"x": "1", "y": "3"}}}
	if digest(a) == digest(c) {
		t.Fatal("a changed value did not change the version")
	}
	if digest(nil) == digest(a) {
		t.Fatal("an empty set and a full one share a version")
	}
}

// TestPullRetriesTransportAndNotAParse holds the two halves of the retry policy:
// a blip costs an attempt, and a schema disagreement costs one attempt because
// it will fail identically on the next.
func TestPullRetriesTransportAndNotAParse(t *testing.T) {
	tries := 0
	flaky := func(context.Context, string) ([]byte, error) {
		tries++
		if tries < 3 {
			return nil, errors.New("connection reset")
		}
		return []byte("one.example\ntwo.example\n"), nil
	}
	src := Source{Name: "x", Origin: "u", parse: parseLines("x", map[string]string{"class": "disposable"})}
	got, err := pull(context.Background(), flaky, src, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("a transport blip must be retried: %v", err)
	}
	if len(got) != 2 || tries != 3 {
		t.Fatalf("got %d entries after %d attempts", len(got), tries)
	}

	parses := 0
	bad := Source{Name: "y", Origin: "u", parse: func([]byte) ([]Entry, error) {
		parses++
		return nil, errors.New("schema changed")
	}}
	if _, err := pull(context.Background(), func(context.Context, string) ([]byte, error) { return []byte("x"), nil }, bad,
		func(context.Context, time.Duration) error { t.Fatal("a parse failure must not be retried"); return nil }); err == nil {
		t.Fatal("a parse failure must be reported")
	}
	if parses != 1 {
		t.Fatalf("the parse ran %d times; it must run once", parses)
	}
}

// TestEmptyDownloadIsRefused: the dangerous failure is the one that parses. A
// truncated or emptied list yields correct members and fewer of them, and
// nothing anywhere reports a problem.
func TestEmptyDownloadIsRefused(t *testing.T) {
	src := Source{Name: "x", Origin: "u", parse: parseLines("x", nil)}
	if _, err := pull(context.Background(), func(context.Context, string) ([]byte, error) { return []byte("# only a comment\n"), nil },
		src, pause); err == nil {
		t.Fatal("a published list that parsed to nothing must be refused, not accepted as an empty world")
	}
}

// TestChunkingCoversEveryEntryFromAnyCursor is resumability as arithmetic: from
// any cursor, the chunks that follow cover exactly the remaining entries in
// order, so a run that died at chunk k continues rather than restarting — and
// because the version is the content, a resumed run derives the identical list.
func TestChunkingCoversEveryEntryFromAnyCursor(t *testing.T) {
	entries := make([]Entry, 0, 12345)
	for i := range 12345 {
		entries = append(entries, Entry{Key: fmt.Sprintf("k%06d", i)})
	}
	sorted := order(entries)
	if digest(sorted) != digest(entries) {
		t.Fatal("the digest must not depend on the order it was handed")
	}
	for _, from := range []int{0, 1, chunk, chunk + 1, 12344} {
		var seen []string
		for i := from; i < len(sorted); i += chunk {
			end := min(i+chunk, len(sorted))
			for _, e := range sorted[i:end] {
				seen = append(seen, e.Key)
			}
		}
		if len(seen) != len(sorted)-from {
			t.Fatalf("resuming at %d covered %d entries, want %d", from, len(seen), len(sorted)-from)
		}
		if seen[0] != sorted[from].Key {
			t.Fatalf("resuming at %d started at %q, want %q", from, seen[0], sorted[from].Key)
		}
	}
}

// TestReadyIsHalfLandedProof: a version whose tail has not landed must never
// answer, because a set missing its tail answers "not listed" for everything in
// it.
func TestReadyIsHalfLandedProof(t *testing.T) {
	if (version{Status: statusReady, Keys: 100, Landed: 40}).ready() {
		t.Error("a half-landed version is not ready")
	}
	if (version{Status: statusIngest, Keys: 100, Landed: 100}).ready() {
		t.Error("an in-flight version is not ready")
	}
	if !(version{Status: statusReady, Keys: 100, Landed: 100}).ready() {
		t.Error("a fully landed version is ready")
	}
}

// ── the parsers ──────────────────────────────────────────────────────────────

func TestParsersReadTheirPublishers(t *testing.T) {
	cases := []struct {
		name  string
		parse func([]byte) ([]Entry, error)
		body  string
		key   string
		want  map[string]string
	}{
		{"aws", parseAWS, `{"prefixes":[{"ip_prefix":"3.5.140.0/22","region":"ap-northeast-2","service":"AMAZON"},{"ip_prefix":"1.2.3.0/24","region":"x","service":"EC2"}],"ipv6_prefixes":[]}`,
			"3.5.140.0/22", map[string]string{"class": "hosting", "operator": "aws", "region": "ap-northeast-2"}},
		{"gcp", parseGCP, `{"prefixes":[{"ipv4Prefix":"34.1.208.0/20","scope":"africa-south1"}]}`,
			"34.1.208.0/20", map[string]string{"class": "hosting", "operator": "gcp", "region": "africa-south1"}},
		{"oracle", parseOracle, `{"regions":[{"region":"mx-monterrey-1","cidrs":[{"cidr":"40.233.0.0/19"}]}]}`,
			"40.233.0.0/19", map[string]string{"class": "hosting", "operator": "oracle", "region": "mx-monterrey-1"}},
		{"fastly", parseFastly, `{"addresses":["23.235.32.0/20"],"ipv6_addresses":["2a04:4e40::/32"]}`,
			"23.235.32.0/20", map[string]string{"class": "hosting", "operator": "fastly"}},
		{"tor", parseTor, "171.25.193.25\n80.67.167.81\n",
			"171.25.193.25/32", map[string]string{"class": "tor", "operator": "tor"}},
		{"linode", parseLinode, "# a comment\n2600:3c00::/32,US,US-TX,Richardson,\n",
			"2600:3c00::/32", map[string]string{"class": "hosting", "operator": "linode", "region": "US"}},
		{"digitalocean", parseDigitalOcean, "5.101.96.0/21,NL,NL-NH,Amsterdam,1098 XH\n",
			"5.101.96.0/21", map[string]string{"class": "hosting", "operator": "digitalocean", "region": "NL"}},
		{"cloudflare", parseCIDRs("cloudflare", "hosting", "cloudflare"), "173.245.48.0/20\n2400:cb00::/32\n",
			"173.245.48.0/20", map[string]string{"class": "hosting", "operator": "cloudflare"}},
	}
	for _, c := range cases {
		got, err := c.parse([]byte(c.body))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		found := false
		for _, e := range got {
			if e.Key == c.key {
				found = true
				if !reflect.DeepEqual(e.Value, c.want) {
					t.Errorf("%s: %s value = %v, want %v", c.name, c.key, e.Value, c.want)
				}
			}
		}
		if !found {
			t.Errorf("%s: %q not among %d entries", c.name, c.key, len(got))
		}
	}
}

// TestSpecialRegistryStripsFootnotes: the registry carries footnote markers and
// quoted names, and a parser that choked on either would drop the blocks no
// public host may legitimately use.
func TestSpecialRegistryStripsFootnotes(t *testing.T) {
	body := "Address Block,Name,RFC\n" +
		`10.0.0.0/8,Private-Use,[RFC1918]` + "\n" +
		`"192.0.0.0/29[2]","IPv4 Service Continuity Prefix","[RFC7335]"` + "\n" +
		`0.0.0.0/8,"""This network""","[RFC791]"` + "\n"
	got, err := parseSpecial([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]Entry{}
	for _, e := range got {
		keys[e.Key] = e
	}
	for _, want := range []string{"10.0.0.0/8", "192.0.0.0/29", "0.0.0.0/8"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("%q missing from %v", want, keys)
		}
	}
	if keys["10.0.0.0/8"].Value["class"] != "reserved" {
		t.Error("a special-purpose block is reserved, not hosting")
	}
}

func TestASNRegistryReadsRangesAndRegistries(t *testing.T) {
	body := "Number,Description,WHOIS\n0,Reserved,\n1-1876,Assigned by ARIN,whois.arin.net\n1877-1901,Assigned by RIPE NCC,whois.ripe.net\n"
	got, err := parseASN([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	by := map[string]Entry{}
	for _, e := range got {
		by[e.Key] = e
	}
	if by["1-1876"].Value["registry"] != "arin" || by["1-1876"].Value["status"] != "delegated" {
		t.Errorf("ARIN row read as %v", by["1-1876"].Value)
	}
	if by["1877-1901"].Value["registry"] != "ripe" {
		t.Errorf("RIPE row read as %v", by["1877-1901"].Value)
	}
	if by["0-0"].Value["status"] != "reserved" {
		t.Errorf("a reserved number must not read as delegated: %v", by["0-0"].Value)
	}
}

func TestCrawlerPatternsSurviveAsPatterns(t *testing.T) {
	got, err := parseCrawlers([]byte(`[{"pattern":"Googlebot\\/","url":"http://www.google.com/bot.html"},{"pattern":""}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 usable pattern, got %d", len(got))
	}
	if got[0].Key != `Googlebot\/` {
		t.Errorf("the publisher's regular expression must survive verbatim, got %q", got[0].Key)
	}
}

// ── the statements ───────────────────────────────────────────────────────────

// TestEveryValueBindsAndNothingIsInterpolated is the injection argument as a
// test. A publisher supplies the keys and the values; a hostile one supplying a
// key made of SQL must reach the statement as a parameter and never as text.
func TestEveryValueBindsAndNothingIsInterpolated(t *testing.T) {
	hostile := "'); DROP TABLE hanzo.reference_entry; --"
	at := time.Unix(1, 0).UTC()
	stmt, args := insert("domain", "disposable", "v1", []Entry{
		{Key: hostile, Value: map[string]string{hostile: hostile}},
		{Key: "ok.example"},
	}, at)

	if strings.Contains(stmt, "DROP") || strings.Contains(stmt, hostile) {
		t.Fatalf("a supplied value reached the statement text: %s", stmt)
	}
	if got := strings.Count(stmt, "?"); got != 18 {
		t.Fatalf("want 18 placeholders for 2 rows of 9 columns, got %d", got)
	}
	if len(args) != 18 {
		t.Fatalf("want 18 bound arguments, got %d", len(args))
	}
	if args[3] != hostile {
		t.Errorf("the key must arrive as a bound argument, got %v", args[3])
	}
	if stmt, _ := insert("domain", "disposable", "v1", nil, at); stmt != "" {
		t.Error("an empty chunk must produce no statement")
	}
}

// TestPruneStatementSparesTwoVersions is about the STATEMENT, and only the
// statement: that it takes two versions to spare, and that it touches the
// membership and never the manifest.
//
// It does NOT prove the plane keeps two versions, and it never did. The
// statement was always correct; the call site passed the current version for
// both placeholders, so `version != ? AND version != ?` spared one — and this
// test passed the whole time, which is what a test written against a constant
// instead of against behaviour buys. What the plane actually does is
// TestPruneSparesTheVersionADecisionMayStillCite, over a warehouse.
func TestPruneStatementSparesTwoVersions(t *testing.T) {
	if n := strings.Count(pruneStatement, "?"); n != 4 {
		t.Fatalf("prune binds %d parameters; it takes a set, a source and the two versions to keep", n)
	}
	if strings.Count(pruneStatement, "version != ?") != 2 {
		t.Error("prune must spare two versions, not one")
	}
	if !strings.HasPrefix(pruneStatement, "ALTER TABLE "+entryTable) {
		t.Errorf("prune must only ever touch the membership table: %s", pruneStatement)
	}
	if strings.Contains(pruneStatement, sourceTable) {
		t.Error("prune must never touch the manifest — provenance is a record and is not pruned")
	}
}

// TestTheManifestIsNeverPruned holds the split the design turns on: bulk
// membership is a cache and may be dropped, provenance is a record and may not.
func TestTheManifestIsNeverPruned(t *testing.T) {
	if strings.Contains(createSource, "TTL") {
		t.Error("the version manifest must carry no TTL: a decision names a version, and that name has to keep resolving")
	}
	if strings.Contains(createEntry, "TTL") {
		t.Error("the membership table's lifetime is decided by the ingest that supersedes it, not by a fleet-wide clock")
	}
}

// TestAPublisherCannotRedirectThePlaneInwards.
//
// Every Origin in the catalog is an HTTPS constant, so the one thing about the
// address this process cannot state in code is where a REDIRECT goes. The fetch
// runs inside the cluster, where "wherever the publisher says" reaches the pod
// network and the instance metadata address, and a hop to http:// hands the
// baseline every tenant's decisions read to anyone on the path.
//
// The hop rule is pinned twice on purpose: on the predicate, and through `wire`,
// because a rule the client never installs is a rule that holds in a test and
// nowhere else.
func TestAPublisherCannotRedirectThePlaneInwards(t *testing.T) {
	from := httptest.NewRequest(http.MethodGet, "https://publisher.example/list", nil)
	to := func(u string) *http.Request { return httptest.NewRequest(http.MethodGet, u, nil) }

	for _, c := range []struct {
		what    string
		req     *http.Request
		via     []*http.Request
		refused bool
	}{
		{"a public https hop", to("https://cdn.example/list"), []*http.Request{from}, false},
		{"a downgrade to http", to("http://publisher.example/list"), []*http.Request{from}, true},
		{"the instance metadata address", to("https://169.254.169.254/latest/meta-data/"), []*http.Request{from}, true},
		{"a private address", to("https://10.0.1.7/list"), []*http.Request{from}, true},
		{"loopback", to("https://127.0.0.1:8080/list"), []*http.Request{from}, true},
		{"an ipv6 literal loopback", to("https://[::1]/list"), []*http.Request{from}, true},
		{"a chain that will not end", to("https://cdn.example/list"), []*http.Request{from, from, from, from, from}, true},
	} {
		if got := hop(c.req, c.via) != nil; got != c.refused {
			t.Errorf("%s: refused = %v, want %v", c.what, got, c.refused)
		}
	}

	// And the rule is the CLIENT's, not just the function's: a publisher that
	// redirects off TLS is refused by the downloader this plane actually uses.
	landed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tempbox.example\n"))
	}))
	defer landed.Close()
	sends := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, landed.URL, http.StatusFound)
	}))
	defer sends.Close()

	body, err := wire(context.Background(), sends.URL)
	if err == nil {
		t.Fatalf("the downloader followed a redirect off TLS and took %d bytes", len(body))
	}
}

// TestTheVolumeOneOrgMayOccupyIsStated.
//
// The per-tenant bound is what keeps this plane out of the defect class the
// other risk tracks kept landing in — one shared store with a fleet-wide cap,
// where a tenant degrades its neighbours. It is only a tenancy property once
// somebody can say what it comes to: every org's SQLite file sits on the ONE
// volume this deployment mounts.
//
// The bound is in BYTES ([ownBudget]) and the per-set count is its QUOTIENT, so
// this checks the division rather than a figure someone chose. What a row costs
// is measured against a real store in
// [TestOneOrgsOverridesCostWhatTheyArePublishedToCost]; this holds the arithmetic
// that turns that cost into a count.
func TestTheVolumeOneOrgMayOccupyIsStated(t *testing.T) {
	if got := ownVolume(); got > ownBudget {
		t.Errorf("one org may occupy %d bytes and the budget is %d; the count is supposed to BE the quotient", got, int64(ownBudget))
	}
	// The count IS the division, not a number beside it: one more entry per set
	// must not still fit.
	n := maxOverrides()
	if over := int64(n+1) * int64(len(Catalog())) * rowBytes; over <= ownBudget {
		t.Errorf("%d entries per set is not the budget divided by the row cost — one more per set (%d bytes) still fits inside %d",
			n, over, int64(ownBudget))
	}
	// And it has to leave a usable plane behind: a per-set bound under one legal
	// write would advertise a call the door refuses.
	if n < maxKeys {
		t.Errorf("one org may hold %d entries per set and one resolve names %d keys; the plane is too small to use", n, maxKeys)
	}
	// stated is the WIRE width and rowBytes is the STORAGE cost. Conflating them is
	// what understated the ceiling by 1.69x, so they are required to differ in the
	// direction a store actually adds bytes.
	if rowBytes < stated {
		t.Errorf("rowBytes (%d) is under the bounded terms a row carries (%d); a store never stores less than it was given", rowBytes, stated)
	}
	t.Logf("budget %d MiB = %d entries x %d sets x %d bytes", int64(ownBudget)>>20, n, len(Catalog()), rowBytes)
}
