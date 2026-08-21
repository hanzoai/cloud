package dataset

// spec_test.go walks the door. [normalize] is the ONLY constructor of a spec, so
// everything it refuses is unreachable from the rest of the package — which is
// what lets plane.go compose statements without re-checking anything.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var day = 24 * time.Hour

func window(from, to time.Time) riskDatasetSpec {
	return riskDatasetSpec{Name: "d", From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)}
}

// TestNormalizeRefusesEveryUnboundedOrUnreadableSpec. Each case below is either a
// way to spend the warehouse or a way to build a dataset that does not mean what
// it says.
func TestNormalizeRefusesEveryUnboundedOrUnreadableSpec(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ok := window(now.Add(-30*day), now.Add(-1*day))

	mutate := func(f func(*riskDatasetSpec)) riskDatasetSpec {
		in := ok
		f(&in)
		return in
	}
	for _, tc := range []struct {
		name string
		in   riskDatasetSpec
		says string
	}{
		{"no name", mutate(func(s *riskDatasetSpec) { s.Name = "" }), "dataset name"},
		{"a name with syntax in it", mutate(func(s *riskDatasetSpec) { s.Name = "a;b" }), "dataset name"},
		{"an unknown subject kind", mutate(func(s *riskDatasetSpec) { s.Kind = "device" }), "subject kind"},
		{"an unpublished dim", mutate(func(s *riskDatasetSpec) { s.Dims = []string{"events", "secrets"} }), "not a published dim"},
		{"an unparseable window", mutate(func(s *riskDatasetSpec) { s.From = "yesterday" }), "RFC 3339"},
		{"an inverted window", mutate(func(s *riskDatasetSpec) { s.From, s.To = s.To, s.From }), "empty or inverted"},
		{"an empty window", mutate(func(s *riskDatasetSpec) { s.To = s.From }), "empty or inverted"},
		{"a window past the source's retention", window(now.Add(-401*day), now), "retention of the source"},
		{"a negative horizon", mutate(func(s *riskDatasetSpec) { s.Horizon = -1 }), "between 0 and"},
		{"a horizon past a year", mutate(func(s *riskDatasetSpec) { s.Horizon = 366 }), "between 0 and"},
		{"a window younger than its horizon", mutate(func(s *riskDatasetSpec) { s.Horizon = 60 }), "maturity horizon"},
		{"one cut", mutate(func(s *riskDatasetSpec) { s.Cuts = []string{now.Format(time.RFC3339)} }), "exactly two"},
		{"three cuts", mutate(func(s *riskDatasetSpec) {
			s.Cuts = []string{now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339)}
		}), "exactly two"},
		{"cuts out of order", mutate(func(s *riskDatasetSpec) {
			s.Cuts = []string{now.Add(-5 * day).Format(time.RFC3339), now.Add(-20 * day).Format(time.RFC3339)}
		}), "strictly increase"},
		{"a cut outside the window", mutate(func(s *riskDatasetSpec) {
			s.Cuts = []string{now.Add(-20 * day).Format(time.RFC3339), now.Add(5 * day).Format(time.RFC3339)}
		}), "inside the window"},
		{"an oversized seed", mutate(func(s *riskDatasetSpec) { s.Seed = strings.Repeat("s", maxSeed+1) }), "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalize(tc.in, now)
			if err == nil {
				t.Fatalf("admitted %+v", tc.in)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("the refusal does not name the reason (%q): %v", tc.says, err)
			}
		})
	}
}

// TestNormalizeAdmitsAndBounds: the defaults are stated, not implied.
func TestNormalizeAdmitsAndBounds(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	from, to := now.Add(-100*day), now.Add(-1*day)
	s, err := normalize(window(from, to), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Dims) != len(dims) {
		t.Fatalf("an empty dim list took %d dims, want the whole published surface", len(s.Dims))
	}
	if s.Rows != maxRows {
		t.Fatalf("an unstated cap is %d, want the plane's bound %d", s.Rows, maxRows)
	}
	if s.Seed != "d" {
		t.Fatalf("an unstated seed is %q, want it derived from the name", s.Seed)
	}
	if !s.Cuts[0].After(from) || !s.Cuts[1].After(s.Cuts[0]) || !to.After(s.Cuts[1]) {
		t.Fatalf("the derived cuts are not inside the window: %v", s.Cuts)
	}

	// A cap above the bound takes the bound. A tenant cannot raise it.
	over := window(from, to)
	over.Rows = maxRows * 10
	s, err = normalize(over, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rows != maxRows {
		t.Fatalf("a caller raised the row cap to %d", s.Rows)
	}
}

// TestDimsTakeThePlanesOrderNotTheCallers. Two requests naming the same dims must
// produce identical point vectors, or the digest would depend on how a request
// was typed.
func TestDimsTakeThePlanesOrderNotTheCallers(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	in := window(now.Add(-30*day), now.Add(-1*day))

	in.Dims = []string{"ips", "events", "spend"}
	a, err := normalize(in, now)
	if err != nil {
		t.Fatal(err)
	}
	in.Dims = []string{"spend", "ips", "events", "events"}
	b, err := normalize(in, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.Dims, b.Dims) {
		t.Fatalf("dim order follows the caller: %v vs %v", a.Dims, b.Dims)
	}
	if want := []string{"events", "spend", "ips"}; !slices.Equal(a.Dims, want) {
		t.Fatalf("dims = %v, want the plane's published order %v", a.Dims, want)
	}
	if a.canon() != b.canon() {
		t.Fatal("two spellings of one spec have two canonical forms")
	}
	// And the columns they resolve to are package constants, in the same order.
	if want := []string{"events", "spend_nano", "ips"}; !slices.Equal(a.columns(), want) {
		t.Fatalf("columns = %v, want %v", a.columns(), want)
	}
}

// TestTheStoredSpecRoundTrips. The register holds the canonical JSON and every
// later answer — lineage, export, the description — reads it back. A spec that
// did not round-trip would make an old version unexplainable.
func TestTheStoredSpecRoundTrips(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	in := window(now.Add(-90*day), now.Add(-2*day))
	in.Kind = kindAccount
	in.Dims = []string{"spend", "calls"}
	in.Horizon = 30
	in.Seed = "abc"
	in.Rows = 1234

	s, err := normalize(in, now)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decode(s.canon())
	if err != nil {
		t.Fatal(err)
	}
	again, err := back.spec()
	if err != nil {
		t.Fatal(err)
	}
	if again.canon() != s.canon() {
		t.Fatalf("a stored spec does not round-trip:\n  %s\n  %s", s.canon(), again.canon())
	}
	if !again.From.Equal(s.From) || !again.To.Equal(s.To) || again.Horizon != s.Horizon {
		t.Fatalf("the window or the horizon moved: %+v vs %+v", again, s)
	}
	if back.Source != sourceTable {
		t.Fatalf("the stored spec does not name its source: %q", back.Source)
	}
}

// TestAnUnreadableStoredSpecIsRefusedRatherThanHalfParsed. A manifest row whose
// spec cannot be read is a row whose dataset cannot be explained, and answering
// with an invented window would put a guess on an audit reply.
func TestAnUnreadableStoredSpecIsRefusedRatherThanHalfParsed(t *testing.T) {
	if _, err := decode("not json"); err == nil {
		t.Error("garbage decoded as a spec")
	}
	for _, bad := range []record{
		{Name: "d", From: "nope", To: "2026-01-01T00:00:00Z", Cuts: []string{"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"}},
		{Name: "d", From: "2026-01-01T00:00:00Z", To: "nope", Cuts: []string{"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"}},
		{Name: "d", From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:00Z", Cuts: []string{"only-one"}},
		{Name: "d", From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:00Z", Cuts: []string{"2026-01-01T00:00:00Z", "nope"}},
		{Name: "d", From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:00Z",
			Cuts: []string{"2026-01-05T00:00:00Z", "2026-01-06T00:00:00Z"}, Dims: []string{"withdrawn"}},
	} {
		if _, err := bad.spec(); err == nil {
			t.Errorf("a broken stored spec parsed: %+v", bad)
		}
	}
}

// TestTheCanonicalFormIsStable. The digest hashes these bytes, so a change in
// field order or in how an instant renders would silently invalidate every
// digest already recorded.
func TestTheCanonicalFormIsStable(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	in := window(now.Add(-10*day), now.Add(-1*day))
	in.Kind = kindPerson
	in.Dims = []string{"events"}
	in.Seed = "abc"
	in.Rows = 100
	in.Cuts = []string{now.Add(-5 * day).Format(time.RFC3339), now.Add(-3 * day).Format(time.RFC3339)}
	s, err := normalize(in, now)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"name":"d","kind":"person","dims":["events"],"from":"2026-05-22T00:00:00Z",` +
		`"to":"2026-05-31T00:00:00Z","horizon":0,"cuts":["2026-05-27T00:00:00Z","2026-05-29T00:00:00Z"],` +
		`"seed":"abc","rows":100,"source":"hanzo.risk_feature"}`
	if s.canon() != want {
		t.Fatalf("the canonical form moved:\n got %s\nwant %s", s.canon(), want)
	}
	// Sub-second precision is truncated on the way in, because the store's DateTime
	// has one-second resolution: a spec that kept it would render one instant and
	// bind another, and the digest would not reproduce.
	fine := in
	fine.From = now.Add(-10*day + 500*time.Millisecond).Format(time.RFC3339Nano)
	f, err := normalize(fine, now)
	if err != nil {
		t.Fatal(err)
	}
	var a, b record
	_ = json.Unmarshal([]byte(s.canon()), &a)
	_ = json.Unmarshal([]byte(f.canon()), &b)
	if a.From != b.From {
		t.Fatalf("sub-second precision survived normalisation: %q vs %q", a.From, b.From)
	}
}

// TestEveryPublishedDimResolvesToAConstantColumn. The allowlist is the ONLY way a
// name becomes a column, so a dim with no column would compose an empty
// identifier into a statement.
func TestEveryPublishedDimResolvesToAConstantColumn(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range dims {
		if d.Name == "" || d.Column == "" || d.Unit == "" {
			t.Errorf("dim %+v is incomplete; a coordinate with no unit is a number nobody can read back", d)
		}
		if seen[d.Column] {
			t.Errorf("two dims read column %q, so one point would carry it twice", d.Column)
		}
		seen[d.Column] = true
		if dimBy[d.Name].Column != d.Column {
			t.Errorf("dim %q is not reachable through the allowlist", d.Name)
		}
	}
	if len(dimBy) != len(dims) {
		t.Fatalf("the allowlist holds %d entries for %d dims", len(dimBy), len(dims))
	}
}
