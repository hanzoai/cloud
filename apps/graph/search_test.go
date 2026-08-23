package graph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// searched runs a search against a store seeded with facts, returning the values
// that matched. It goes through the same filter every other read does, which is
// the point of the term.
func searched(t *testing.T, s *store, q string, f filter) []string {
	t.Helper()
	f.Match = match(q)
	facts, err := s.read(context.Background(), f)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	out := make([]string, 0, len(facts))
	for _, fact := range facts {
		out = append(out, fact.Value)
	}
	return out
}

func seedForSearch(t *testing.T) *store {
	t.Helper()
	s := newStore(t)
	facts := []Fact{
		mk(t, "acme/svc/api", "owner", "acme/team/core", true, "registry", t0),
		mk(t, "acme/svc/api", "runtime", "go1.25", false, "scanner", t0),
		mk(t, "acme/svc/worker", "owner", "acme/team/platform", true, "registry", t0),
		mk(t, "acme/svc/worker", "runtime", "rust", false, "scanner", t0),
	}
	if _, err := s.record(context.Background(), facts); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s
}

// TestSearchFindsByTextWhereReadFindsByKey is the capability. Nothing in the
// plane could answer "where is this mentioned" before: a reader had to know an
// entity key already, which is exactly what somebody searching does not have.
func TestSearchFindsByTextWhereReadFindsByKey(t *testing.T) {
	s := seedForSearch(t)

	got := searched(t, s, "platform", filter{})
	if len(got) != 1 || got[0] != "acme/team/platform" {
		t.Fatalf("search for platform = %v, want the one assertion naming it", got)
	}

	// The term matches wherever the index reads: the value above, and the SOURCE
	// here, which no read filter can narrow by at all.
	if got := searched(t, s, "scanner", filter{}); len(got) != 2 {
		t.Errorf("search by source = %v, want both scanner assertions", got)
	}
}

// TestSearchIsAPrefix pins the behavior a search box needs: a caller typing part
// of a word finds the word.
func TestSearchIsAPrefix(t *testing.T) {
	s := seedForSearch(t)

	if got := searched(t, s, "regist", filter{}); len(got) != 2 {
		t.Errorf("prefix search = %v, want the two registry assertions", got)
	}
}

// TestEveryWordNarrows proves more words mean fewer answers, not more.
func TestEveryWordNarrows(t *testing.T) {
	s := seedForSearch(t)

	one := searched(t, s, "registry", filter{})
	two := searched(t, s, "registry core", filter{})
	if len(one) != 2 {
		t.Fatalf("registry = %v, want 2", one)
	}
	if len(two) != 1 || two[0] != "acme/team/core" {
		t.Errorf("registry core = %v, want only the assertion carrying both", two)
	}
}

// TestSearchComposesWithEveryOtherNarrowing is why Match is a term of the filter
// rather than a read of its own: a caller narrows by relation and by instant in
// the same request, and gets them applied to what it searched for.
func TestSearchComposesWithEveryOtherNarrowing(t *testing.T) {
	s := seedForSearch(t)

	if got := searched(t, s, "acme", filter{}); len(got) != 4 {
		t.Fatalf("unnarrowed search = %v, want every assertion", got)
	}
	if got := searched(t, s, "acme", filter{Relation: "owner"}); len(got) != 2 {
		t.Errorf("search narrowed to owner = %v, want the two owner assertions", got)
	}
	if got := searched(t, s, "acme", filter{Entity: "acme/svc/api"}); len(got) != 2 {
		t.Errorf("search narrowed to one entity = %v, want its two assertions", got)
	}
	if got := searched(t, s, "acme", filter{AsOf: t0.Add(-time.Hour)}); len(got) != 0 {
		t.Errorf("search before anything was knowable = %v, want nothing", got)
	}
	if got := searched(t, s, "acme", filter{Limit: 1}); len(got) != 1 {
		t.Errorf("search under a ceiling = %v, want one", got)
	}
}

// TestAssertionsIndexOnArrival guards the trigger. An assertion written after the
// index exists has to be findable without anybody rebuilding anything.
func TestAssertionsIndexOnArrival(t *testing.T) {
	s := seedForSearch(t)

	if got := searched(t, s, "kubernetes", filter{}); len(got) != 0 {
		t.Fatalf("nothing has said kubernetes yet, got %v", got)
	}
	if _, err := s.record(context.Background(), []Fact{
		mk(t, "acme/svc/api", "runsOn", "kubernetes", false, "deploy", t0),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := searched(t, s, "kubernetes", filter{}); len(got) != 1 {
		t.Errorf("a new assertion was not indexed: %v", got)
	}
}

// TestTheIndexIsBuiltFromRowsAlreadyThere is the migration. A file written before
// the index existed must search as what it holds, not as though it were empty —
// an index that answers nothing is worse than one that errors, because a caller
// believes it.
func TestTheIndexIsBuiltFromRowsAlreadyThere(t *testing.T) {
	s := seedForSearch(t)

	// Drop the index and its triggers, as a file predating this migration has.
	for _, stmt := range []string{
		`DROP TRIGGER assertion_ai`,
		`DROP TRIGGER assertion_ad`,
		`DROP TABLE assertion_fts`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := reindex(s.db); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if got := searched(t, s, "registry", filter{}); len(got) != 2 {
		t.Errorf("the rebuild missed rows already in the file: %v", got)
	}
}

// TestReindexLeavesABuiltIndexAlone pins that the build runs once. Opening a file
// must not re-scan every assertion in it.
func TestReindexLeavesABuiltIndexAlone(t *testing.T) {
	s := seedForSearch(t)

	for i := 0; i < 3; i++ {
		if err := reindex(s.db); err != nil {
			t.Fatalf("reindex %d: %v", i, err)
		}
	}
	if got := searched(t, s, "registry", filter{}); len(got) != 2 {
		t.Errorf("re-opening changed what the index holds: %v", got)
	}
}

// TestAnAssertionsPunctuationIsTextAndNotSyntax is the one that would otherwise
// be a 500. FTS5 reads `-`, `*`, `:`, `^` and `"` as operators, and entity keys
// and search boxes are full of them.
func TestAnAssertionsPunctuationIsTextAndNotSyntax(t *testing.T) {
	s := seedForSearch(t)

	for _, q := range []string{
		`acme/svc/api`, `-core`, `"`, `a"b`, `*`, `^`, `foo:bar`, `AND`, `NOT registry`, `((`,
	} {
		f := filter{Match: match(q)}
		if f.Match == "" {
			continue
		}
		if _, err := s.read(context.Background(), f); err != nil {
			t.Errorf("searching %q was read as syntax: %v", q, err)
		}
	}
}

// TestASlashedKeySearchesAsItself proves the punctuation above is not merely
// tolerated but useful: the way people name entities here is findable.
func TestASlashedKeySearchesAsItself(t *testing.T) {
	s := seedForSearch(t)

	if got := searched(t, s, "acme/svc/worker", filter{}); len(got) != 2 {
		t.Errorf("searching an entity key = %v, want its two assertions", got)
	}
}

// TestNothingToSearchForIsNotAMatchForEverything guards the empty query. A filter
// with no term matches every row, so a blank search must be refused rather than
// quietly becoming a full read.
func TestNothingToSearchForIsNotAMatchForEverything(t *testing.T) {
	for _, q := range []string{"", "   ", "\t\n"} {
		if got := match(q); got != "" {
			t.Errorf("match(%q) = %q, want empty so the op can refuse it", q, got)
		}
	}
	if got := match(`"" ""`); !strings.Contains(got, `*`) {
		t.Errorf("match of quote-only input = %q; it is still a term, not nothing", got)
	}
}

// searchDoor is GET /v1/graph/search as a caller of one project sees it.
func searchDoor(t *testing.T, app *zip.App, project, q string) ([]string, int) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://cloud/v1/graph/search?q="+url.QueryEscape(q), nil)
	req.Header.Set(zip.HeaderOrg, "acme")
	req.Header.Set(zip.HeaderUser, "acme/z@acme.test")
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("search door: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode
	}
	var out struct {
		Assertions []struct{ Value string } `json:"assertions"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("search door answered something that is not the read shape: %v: %s", err, b)
	}
	got := make([]string, 0, len(out.Assertions))
	for _, a := range out.Assertions {
		got = append(got, a.Value)
	}
	return got, resp.StatusCode
}

// TestTheSearchDoorAnswers carries the term out to the wire.
func TestTheSearchDoorAnswers(t *testing.T) {
	app := mountGraph(t)
	assertFact(t, app, "acme/svc/api", "owner", "acme/team/core", true)
	assertFact(t, app, "acme/svc/worker", "owner", "acme/team/platform", true)

	got, code := searchDoor(t, app, "", "platform")
	if code != http.StatusOK {
		t.Fatalf("search door status %d", code)
	}
	if len(got) != 1 || got[0] != "acme/team/platform" {
		t.Errorf("the door found %v, want the one assertion naming platform", got)
	}
}

// TestABlankSearchIsRefused pins that the door does not quietly become a full
// read when there is nothing to search for.
func TestABlankSearchIsRefused(t *testing.T) {
	app := mountGraph(t)
	assertFact(t, app, "acme/svc/api", "owner", "acme/team/core", true)

	if _, code := searchDoor(t, app, "", "   "); code != http.StatusBadRequest {
		t.Errorf("a blank search answered %d; it must be refused, not answered with everything", code)
	}
}

// TestSearchCannotCrossAGraphDatabase is the tenancy property, restated for the
// door that reaches rows by text rather than by key. The index lives in the same
// file as the assertions, so this holds by construction — and this is the test
// that says a change which moved it out would be wrong.
func TestSearchCannotCrossAGraphDatabase(t *testing.T) {
	app := mountGraph(t)
	inProject(t, app, "alpha", "acme/svc/api", "owner", "acme/team/core")
	inProject(t, app, "beta", "acme/svc/api", "owner", "acme/team/platform")

	if got, _ := searchDoor(t, app, "alpha", "team"); len(got) != 1 || got[0] != "acme/team/core" {
		t.Errorf("alpha searched into another database: %v", got)
	}
	if got, _ := searchDoor(t, app, "beta", "core"); len(got) != 0 {
		t.Errorf("beta found alpha's assertion by text: %v", got)
	}
}

// TestSearchIsOnTheGraphQLDoorToo keeps the two doors from drifting: one schema,
// one op, both addresses.
func TestSearchIsOnTheGraphQLDoorToo(t *testing.T) {
	app := mountGraph(t)
	assertFact(t, app, "acme/svc/api", "owner", "acme/team/core", true)
	assertFact(t, app, "acme/svc/worker", "runtime", "rust", false)

	env := ask(t, app, `{ search(q: "rust") { entity relation value source } }`)
	hits, _ := env["data"].(map[string]any)["search"].([]any)
	if len(hits) != 1 {
		t.Fatalf("graphql search found %d, want 1", len(hits))
	}
	if v := hits[0].(map[string]any)["value"]; v != "rust" {
		t.Errorf("graphql search returned %v", v)
	}
}
