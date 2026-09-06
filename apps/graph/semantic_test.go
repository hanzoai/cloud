package graph

// The two operations that read a DOCUMENT, over HTTP, through the same door every
// other test here uses (door_test.go): a request carrying an org and a validated
// principal, and nothing this package does not resolve from them.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// the document these tests read. Two sections, one heading each, one stated
// relation that names an entity and one that does not, and a line of prose
// carrying a colon — which states nothing and must stay out of the store.
const doc = `# acme/svc/api
Notes: the API tier, which we run three of.
owner:: [[acme/team/core]]
tier:: 1

# acme/team/core
- lead:: [[acme/person/z]]
`

const readAt = "2026-01-01T00:00:00Z"

// source is the primitive: one POST as the caller of a project, in the shape both
// of these operations take.
func source(t *testing.T, app *zip.App, path, project, text string) (int, []byte) {
	t.Helper()
	return call(t, app, http.MethodPost, path, project, map[string]any{
		"source": "wiki/api", "at": readAt, "text": text,
	})
}

// extracted is what the source states, as the extract operation reports it.
func extracted(t *testing.T, app *zip.App, text string) []graphTriple {
	t.Helper()
	code, b := source(t, app, "/v1/graph/extract", "", text)
	if code >= 300 {
		t.Fatalf("extract: status %d: %s", code, b)
	}
	var out graphExtractOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("extract answered something that is not the read shape: %v: %s", err, b)
	}
	return out.Triples
}

// filed is what the ingest operation recorded, read back through /v1/graph — the
// only way to read an assertion, so a test cannot prove a write the plane's own
// readers cannot see.
func filed(t *testing.T, app *zip.App, project, text string) []wireFact {
	t.Helper()
	code, b := source(t, app, "/v1/graph/ingest", project, text)
	if code >= 300 {
		t.Fatalf("ingest: status %d: %s", code, b)
	}
	return answered(t, app, project, "/v1/graph")
}

// TestASourceBecomesAssertions is the whole feature: a document goes in, and what
// it states comes back out of the assertion plane the app already keeps.
func TestASourceBecomesAssertions(t *testing.T) {
	got := map[string]wireFact{}
	for _, f := range filed(t, mountGraph(t), "", doc) {
		got[f.Entity+" "+f.Relation] = f
	}
	if len(got) != 3 {
		t.Fatalf("recorded %d assertions, want the 3 the document states: %v", len(got), got)
	}
	for _, want := range []struct {
		key, value string
		names      bool
	}{
		{"acme/svc/api owner", "acme/team/core", true},
		{"acme/svc/api tier", "1", false},
		{"acme/team/core lead", "acme/person/z", true},
	} {
		f, ok := got[want.key]
		if !ok {
			t.Errorf("%q was stated and not recorded", want.key)
			continue
		}
		if f.Value != want.value || f.Names != want.names {
			t.Errorf("%q recorded value=%q names=%v, want %q / %v", want.key, f.Value, f.Names, want.value, want.names)
		}
	}
}

// TestProseStatesNothing is the negative half, and it is the one that matters:
// this plane has no delete, so a reader that turned an English sentence into an
// assertion would leave it there forever.
func TestProseStatesNothing(t *testing.T) {
	app := mountGraph(t)
	for _, f := range filed(t, app, "", doc) {
		if f.Relation == "Notes" {
			t.Fatalf("a prose line with a colon was recorded as a relation: %+v", f)
		}
	}
	// And a document made ENTIRELY of such lines is refused rather than answered
	// with a 200 that filed nothing.
	code, b := source(t, app, "/v1/graph/ingest", "", "# thing\nNotes: we run three of these.\n")
	if code != http.StatusBadRequest {
		t.Errorf("a source stating no relations answered %d: %s", code, b)
	}
}

// TestAnEdgeIsDeclaredNotGuessed pins the notation to the author. Both values here
// are plain strings by the time they reach the store; only the one the document
// wrapped in a wikilink is an edge, and a walk reads only the edges.
func TestAnEdgeIsDeclaredNotGuessed(t *testing.T) {
	app := mountGraph(t)
	filed(t, app, "", "# a\nedge:: [[b]]\nplain:: b\n")

	code, b := call(t, app, http.MethodPost, "/v1/graph/neighbors", "", map[string]any{
		"seeds": []string{"a"}, "depth": 1,
	})
	if code >= 300 {
		t.Fatalf("neighbors: status %d: %s", code, b)
	}
	var out struct {
		Entities []string `json:"entities"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("neighbors answered %v: %s", err, b)
	}
	// a and b: the declared edge is walked, and the property that happens to hold
	// the same text is not a second way to reach it.
	if len(out.Entities) != 2 {
		t.Errorf("walked to %v; only the declared edge is an edge", out.Entities)
	}
}

// TestEvidencePointsAtTheSectionThatStatedIt is what makes an extracted assertion
// answerable later: the claim names the passage it came from, not just the file.
func TestEvidencePointsAtTheSectionThatStatedIt(t *testing.T) {
	want := map[string]string{
		"acme/svc/api":   "wiki/api#0",
		"acme/team/core": "wiki/api#1",
	}
	for _, f := range filed(t, mountGraph(t), "", doc) {
		if got := f.Evidence; got != want[f.Entity] {
			t.Errorf("%s %s carries evidence %q, want %q", f.Entity, f.Relation, got, want[f.Entity])
		}
		if f.Source != "wiki/api" {
			t.Errorf("%s %s names source %q, want the source that was read", f.Entity, f.Relation, f.Source)
		}
	}
}

// TestRereadingOneSourceAppendsNothing is the property an importer that runs on a
// schedule depends on. The assertion's content address covers `at`, which is why
// this surface takes one rather than reading a clock: a clock would make every
// re-read a fresh set of rows in a store that never overwrites.
func TestRereadingOneSourceAppendsNothing(t *testing.T) {
	app := mountGraph(t)
	first := filed(t, app, "", doc)

	code, b := source(t, app, "/v1/graph/ingest", "", doc)
	if code >= 300 {
		t.Fatalf("re-read: status %d: %s", code, b)
	}
	var out graphAssertOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("ingest answered %v: %s", err, b)
	}
	if out.Recorded != 0 || out.Duplicate != len(first) {
		t.Errorf("re-reading one source recorded=%d duplicate=%d, want 0 and %d", out.Recorded, out.Duplicate, len(first))
	}
	if got := answered(t, app, "", "/v1/graph"); len(got) != len(first) {
		t.Errorf("the plane holds %d assertions after two reads of one source, want %d", len(got), len(first))
	}
}

// TestExtractRecordsNothing keeps the two operations honestly different: the one
// that answers what a document WOULD file must not have filed it.
func TestExtractRecordsNothing(t *testing.T) {
	app := mountGraph(t)
	if got := extracted(t, app, doc); len(got) != 3 {
		t.Fatalf("extract found %d relations, want 3: %+v", len(got), got)
	}
	if got := answered(t, app, "", "/v1/graph"); len(got) != 0 {
		t.Errorf("extract recorded %d assertions; it records none", len(got))
	}
}

// TestTheSubjectIsTheNearestHeading covers the two ways a statement gets a
// subject, including the request's own for anything stated above the first
// heading.
func TestTheSubjectIsTheNearestHeading(t *testing.T) {
	code, b := call(t, mountGraph(t), http.MethodPost, "/v1/graph/extract", "", map[string]any{
		"source": "wiki/api", "at": readAt, "subject": "acme/svc/api",
		"text": "tier:: 1\n\n# acme/team/core\nlead:: [[acme/person/z]]\n",
	})
	if code >= 300 {
		t.Fatalf("extract: status %d: %s", code, b)
	}
	var out graphExtractOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("extract answered %v: %s", err, b)
	}
	if len(out.Triples) != 2 {
		t.Fatalf("found %d relations, want 2: %+v", len(out.Triples), out.Triples)
	}
	if out.Triples[0].Subject != "acme/svc/api" || out.Triples[0].Section != 0 {
		t.Errorf("the statement above the first heading is about %q in section %d, want the request's subject in section 0",
			out.Triples[0].Subject, out.Triples[0].Section)
	}
	if out.Triples[1].Subject != "acme/team/core" || out.Triples[1].Section != 1 {
		t.Errorf("the statement under a heading is about %q in section %d, want the heading in section 1",
			out.Triples[1].Subject, out.Triples[1].Section)
	}
}

// TestAHashTagIsNotAHeading is the one place the syntax could quietly move a
// subject: `#done` is a tag an author writes in a line of notes, and reading it
// as a heading would file everything below it about a thing called "done".
func TestAHashTagIsNotAHeading(t *testing.T) {
	got := extracted(t, mountGraph(t), "# acme/svc/api\n#done\ntier:: 1\n")
	if len(got) != 1 || got[0].Subject != "acme/svc/api" {
		t.Errorf("read %+v; a tag names no subject", got)
	}
}

// TestReadingASourceNeedsAPrincipal closes the read half. The write half is
// refused by the assert operation it calls, which every other test in this
// package already holds to that; this one proves the operation that never reaches
// the store asks too.
func TestReadingASourceNeedsAPrincipal(t *testing.T) {
	app := mountGraph(t)
	req, err := http.NewRequest(http.MethodPost, "http://cloud/v1/graph/extract", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := app.Test(req, zip.TestConfig{FailOnTimeout: true})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		t.Errorf("a caller with no principal read a source: status %d", resp.StatusCode)
	}
}

// TestAWrongRequestFieldIsOneRefusal keeps a mistake about the WHOLE request from
// arriving as a 200 carrying one refusal per relation the document states. A
// caller who left out the source, or dated the read in something that is not RFC
// 3339, made one mistake.
func TestAWrongRequestFieldIsOneRefusal(t *testing.T) {
	app := mountGraph(t)
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"no source", "/v1/graph/extract", map[string]any{"at": readAt, "text": doc}},
		{"no source", "/v1/graph/ingest", map[string]any{"at": readAt, "text": doc}},
		{"at is not RFC 3339", "/v1/graph/ingest", map[string]any{"source": "wiki/api", "at": "yesterday", "text": doc}},
		{"no text", "/v1/graph/extract", map[string]any{"source": "wiki/api", "at": readAt}},
	} {
		code, b := call(t, app, http.MethodPost, tc.path, "", tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s at %s answered %d, want 400: %s", tc.name, tc.path, code, b)
		}
	}
}
