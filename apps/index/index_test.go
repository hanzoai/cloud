package index

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// newStore opens a throwaway store on a temp dir. It exercises the real
// openStore path (pragmas + migrate), not an in-memory shortcut, so a migration
// that only works on :memory: cannot pass here.
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func doc(kv ...string) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// titles decodes the `title` of every hit, so assertions read as the documents
// a user would see rather than as raw JSON.
func titles(t *testing.T, hits []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		var d map[string]any
		if err := json.Unmarshal(h, &d); err != nil {
			t.Fatalf("decode hit: %v", err)
		}
		out = append(out, stringify(d["title"]))
	}
	return out
}

// TestTenantIsolation is the security-critical property: two orgs may hold an
// index of the SAME name, and neither can see the other's documents through
// search, a document read, or a listing. A standalone Meilisearch cannot do
// this — one master key sees every index — so this is the whole reason the
// index moved in-binary.
func TestTenantIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()

		for _, org := range []string{"acme", "globex"} {
			if _, err := s.EnsureIndex(ctx, org, "messages", "id"); err != nil {
				t.Fatalf("%s EnsureIndex: %v", org, err)
			}
		}
		if err := s.Upsert(ctx, "acme", "messages", "id",
			[]map[string]any{doc("id", "1", "title", "acme secret roadmap")}); err != nil {
			t.Fatalf("acme upsert: %v", err)
		}
		if err := s.Upsert(ctx, "globex", "messages", "id",
			[]map[string]any{doc("id", "2", "title", "globex secret roadmap")}); err != nil {
			t.Fatalf("globex upsert: %v", err)
		}

		// A query that matches BOTH orgs' documents returns only the caller's.
		hits, err := s.Search(ctx, "acme", "messages", "secret roadmap", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		got := titles(t, hits)
		if len(got) != 1 || got[0] != "acme secret roadmap" {
			t.Errorf("cross-tenant leak: acme searching %q got %v, want only its own", "secret roadmap", got)
		}

		// A direct read of the other org's primary key must not resolve.
		if _, err := s.Document(ctx, "acme", "messages", "2"); err == nil {
			t.Error("cross-tenant leak: acme read globex's document by primary key")
		}

		// Listing the index reports only the caller's documents and count.
		docs, total, err := s.Documents(ctx, "globex", "messages", 100, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 || len(docs) != 1 || titles(t, docs)[0] != "globex secret roadmap" {
			t.Errorf("cross-tenant leak: globex list got total=%d %v", total, titles(t, docs))
		}

		// The same primary key in both orgs stays two distinct documents.
		if err := s.Upsert(ctx, "globex", "messages", "id",
			[]map[string]any{doc("id", "1", "title", "globex one")}); err != nil {
			t.Fatalf("globex upsert dup pk: %v", err)
		}
		acme, err := s.Document(ctx, "acme", "messages", "1")
		if err != nil {
			t.Fatalf("acme read own: %v", err)
		}
		if got := titles(t, []json.RawMessage{acme})[0]; got != "acme secret roadmap" {
			t.Errorf("shared primary key clobbered across orgs: acme id=1 is now %q", got)
		}
	})
}

// TestIndexIsolation proves two indexes in the SAME org stay separate — the
// property that lets chat keep `convos` and `messages` side by side.
func TestIndexIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		for _, uid := range []string{"convos", "messages"} {
			if _, err := s.EnsureIndex(ctx, "acme", uid, "id"); err != nil {
				t.Fatalf("EnsureIndex %s: %v", uid, err)
			}
		}
		mustUpsert(t, s, "acme", "convos", []map[string]any{doc("id", "a", "title", "planning session")})
		mustUpsert(t, s, "acme", "messages", []map[string]any{doc("id", "b", "title", "planning session")})

		hits, err := s.Search(ctx, "acme", "convos", "planning", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		if len(hits) != 1 {
			t.Errorf("index bleed: convos search returned %d hits, want 1", len(hits))
		}
	})
}

// TestSearchAndUserFilter covers the exact shape chat drives: prefix matching on
// a free-text query, narrowed to one end user.
func TestSearchAndUserFilter(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		mustUpsert(t, s, "acme", "convos", []map[string]any{
			doc("id", "1", "title", "Kubernetes migration plan", "user", "u1"),
			doc("id", "2", "title", "Kubernetes cost review", "user", "u2"),
			doc("id", "3", "title", "Dinner reservations", "user", "u1"),
		})

		// Prefix matching: "kube" finds both Kubernetes documents.
		hits, err := s.Search(ctx, "acme", "convos", "kube", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		if len(hits) != 2 {
			t.Errorf("prefix search %q returned %d hits, want 2: %v", "kube", len(hits), titles(t, hits))
		}

		// The user filter narrows to one end user WITHIN the org.
		hits, err = s.Search(ctx, "acme", "convos", "kube", ParseUserFilter(`user = "u1"`), 10, 0)
		if err != nil {
			t.Fatalf("filtered index: %v", err)
		}
		if got := titles(t, hits); len(got) != 1 || got[0] != "Kubernetes migration plan" {
			t.Errorf("user filter got %v, want only u1's Kubernetes document", got)
		}

		// An empty query with a user filter lists that user's documents.
		hits, err = s.Search(ctx, "acme", "convos", "", ParseUserFilter(`user = "u1"`), 10, 0)
		if err != nil {
			t.Fatalf("placeholder index: %v", err)
		}
		if len(hits) != 2 {
			t.Errorf("placeholder search for u1 returned %d hits, want 2: %v", len(hits), titles(t, hits))
		}
	})
}

// TestSearchInputIsInert proves a query full of query-language and SQL
// metacharacters is treated as text, not as syntax. Tokenizing to letters,
// digits and underscore is what makes that true — the terms reaching SQL are
// bound parameters that cannot carry an operator.
func TestSearchInputIsInert(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		mustUpsert(t, s, "acme", "convos", []map[string]any{doc("id", "1", "title", "quarterly report")})

		for _, q := range []string{
			`"`, `report" OR "`, `NEAR(a b)`, `*`, `^report`, `a AND (b`, `--`, `';DROP TABLE docs;--`,
		} {
			if _, err := s.Search(ctx, "acme", "convos", q, nil, 10, 0); err != nil {
				t.Errorf("query %q errored: %v", q, err)
			}
		}
		// The table survived every one of those.
		if _, total, err := s.Documents(ctx, "acme", "convos", 10, 0); err != nil || total != 1 {
			t.Errorf("store damaged by hostile queries: total=%d err=%v", total, err)
		}
	})
}

// TestUpsertReplacesAndReindexes proves a re-added document replaces its body
// AND its indexed terms, so a stale term can never keep matching. Deleting the
// document's terms before writing the new ones is what makes this true; an
// UPSERT on the row store alone would leave the old text searchable.
func TestUpsertReplacesAndReindexes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		mustUpsert(t, s, "acme", "convos", []map[string]any{doc("id", "1", "title", "aardvark")})
		mustUpsert(t, s, "acme", "convos", []map[string]any{doc("id", "1", "title", "buffalo")})

		if hits, err := s.Search(ctx, "acme", "convos", "aardvark", nil, 10, 0); err != nil {
			t.Fatalf("index: %v", err)
		} else if len(hits) != 0 {
			t.Errorf("stale text still matches after replace: %v", titles(t, hits))
		}
		hits, err := s.Search(ctx, "acme", "convos", "buffalo", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		if len(hits) != 1 {
			t.Fatalf("replaced document not found by its new text: %d hits", len(hits))
		}
		// One document, not two: the upsert replaced rather than appended.
		if _, total, err := s.Documents(ctx, "acme", "convos", 10, 0); err != nil || total != 1 {
			t.Errorf("upsert appended instead of replacing: total=%d err=%v", total, err)
		}
	})
}

// TestDeleteRemovesFromBothStores proves a deleted document is gone from the
// row store AND the FTS index — a delete that missed the index would keep
// returning hits whose bodies no longer exist.
func TestDeleteRemovesFromBothStores(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		mustUpsert(t, s, "acme", "convos", []map[string]any{
			doc("id", "1", "title", "keep me"),
			doc("id", "2", "title", "delete me"),
		})
		if err := s.Delete(ctx, "acme", "convos", []string{"2"}); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if hits, err := s.Search(ctx, "acme", "convos", "delete", nil, 10, 0); err != nil {
			t.Fatalf("index: %v", err)
		} else if len(hits) != 0 {
			t.Errorf("deleted document still searchable: %v", titles(t, hits))
		}
		if _, err := s.Document(ctx, "acme", "convos", "2"); err == nil {
			t.Error("deleted document still readable by primary key")
		}
		if _, total, err := s.Documents(ctx, "acme", "convos", 10, 0); err != nil || total != 1 {
			t.Errorf("after delete total=%d err=%v, want 1", total, err)
		}
	})
}

// TestEnsureIndexIsIdempotent proves a second create keeps the ORIGINAL primary
// key. Adopting a new one would orphan every document already keyed by the old.
func TestEnsureIndexIsIdempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		if _, err := s.EnsureIndex(ctx, "acme", "convos", "conversationId"); err != nil {
			t.Fatalf("EnsureIndex: %v", err)
		}
		idx, err := s.EnsureIndex(ctx, "acme", "convos", "somethingElse")
		if err != nil {
			t.Fatalf("EnsureIndex again: %v", err)
		}
		if idx.PrimaryKey != "conversationId" {
			t.Errorf("primary key changed to %q on re-create; documents would be orphaned", idx.PrimaryKey)
		}
		// An absent primary key defaults to "id", as Meilisearch does.
		other, err := s.EnsureIndex(ctx, "acme", "messages", "")
		if err != nil {
			t.Fatalf("EnsureIndex default: %v", err)
		}
		if other.PrimaryKey != "id" {
			t.Errorf("default primary key = %q, want id", other.PrimaryKey)
		}
	})
}

// TestMissingIndexIsReported proves an unknown uid reports errNoIndex, which the
// handler renders as Meilisearch's index_not_found — the code the JS client
// keys index auto-creation off.
func TestMissingIndexIsReported(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		if _, err := s.Index(context.Background(), "acme", "nope"); err == nil {
			t.Fatal("unknown index reported as present")
		} else if err != errNoIndex {
			t.Errorf("unknown index error = %v, want errNoIndex", err)
		}
	})
}

// TestNonStringPrimaryKey proves an integer key round-trips as "42" and not in
// scientific notation — JSON numbers decode to float64, so a naive %v would key
// a document as "4.2e+01" and make it unreadable by its own id.
func TestNonStringPrimaryKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		if err := s.Upsert(ctx, "acme", "convos", "id",
			[]map[string]any{{"id": float64(42), "title": "answer"}}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if _, err := s.Document(ctx, "acme", "convos", "42"); err != nil {
			t.Errorf("integer primary key not readable as %q: %v", "42", err)
		}
	})
}

// TestDocumentsWithoutPrimaryKeyAreSkipped proves an unkeyable document is
// dropped rather than failing the whole batch, matching Meilisearch.
func TestDocumentsWithoutPrimaryKeyAreSkipped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		if err := s.Upsert(ctx, "acme", "convos", "id", []map[string]any{
			doc("title", "no key here"),
			doc("id", "1", "title", "keyed"),
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		_, total, err := s.Documents(ctx, "acme", "convos", 10, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 {
			t.Errorf("total=%d, want 1 (the unkeyed document should be skipped, not stored)", total)
		}
	})
}

// TestSearchableTextCoversAllStringFields proves a document is findable by a
// field that is neither title nor text, which is what makes this index useful
// to subsystems other than chat.
func TestSearchableTextCoversAllStringFields(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "notes")
		mustUpsert(t, s, "acme", "notes", []map[string]any{
			{"id": "1", "title": "meeting", "summary": "discussed peregrine falcons"},
		})
		hits, err := s.Search(ctx, "acme", "notes", "peregrine", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		if len(hits) != 1 {
			t.Errorf("document not findable by a non-title field: %d hits", len(hits))
		}
	})
}

// TestFoldingIsCaseAndAccentInsensitive proves the tokenizer folds case and
// combining marks on BOTH sides, so a user typing "cafe" finds "Café" and vice
// versa. This is the job FTS5's `remove_diacritics 2` tokenizer used to do.
//
// Folding strips combining marks; it does not transliterate. `æ` and `ø` are
// distinct letters rather than accented forms of `ae` and `o`, so they fold to
// themselves — the same behaviour FTS5 has.
func TestFoldingIsCaseAndAccentInsensitive(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "notes")
		mustUpsert(t, s, "acme", "notes", []map[string]any{doc("id", "1", "title", "Café Ærø RÉSUMÉ")})

		for _, q := range []string{"cafe", "CAFE", "Café", "café", "resume", "RÉSUMÉ", "ærø", "ÆRØ"} {
			hits, err := s.Search(ctx, "acme", "notes", q, nil, 10, 0)
			if err != nil {
				t.Fatalf("search %q: %v", q, err)
			}
			if len(hits) != 1 {
				t.Errorf("query %q found %d documents, want 1", q, len(hits))
			}
		}
		// Transliteration is NOT claimed: "aero" is a different word from "ærø".
		if hits, err := s.Search(ctx, "acme", "notes", "aero", nil, 10, 0); err != nil {
			t.Fatalf("index: %v", err)
		} else if len(hits) != 0 {
			t.Errorf("query %q matched %d documents; folding must not transliterate", "aero", len(hits))
		}
	})
}

// TestRankingPrefersMoreMatchedTerms proves a document matching both query
// terms outranks one matching a single term. Query terms are OR-joined, so
// without this the more relevant document could land on page two.
func TestRankingPrefersMoreMatchedTerms(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "notes")
		mustUpsert(t, s, "acme", "notes", []map[string]any{
			// Primary-key order alone would put "a-partial" first; ranking must not.
			doc("id", "a-partial", "title", "kubernetes only"),
			doc("id", "b-both", "title", "kubernetes migration"),
		})
		hits, err := s.Search(ctx, "acme", "notes", "kubernetes migration", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		got := titles(t, hits)
		if len(got) != 2 {
			t.Fatalf("got %d hits, want 2: %v", len(got), got)
		}
		if got[0] != "kubernetes migration" {
			t.Errorf("ranking put %q first; the document matching BOTH terms should lead: %v", got[0], got)
		}
	})
}

// TestRankingCountsTheQuerysWordsNotTheDocumentsTerms is a log: every line holds
// many terms that begin with a query word's prefix ("09" in each timestamp), and
// one line names what was asked for. The line that holds more of the QUESTION's
// words leads, however many of its own terms the busy lines matched.
func TestRankingCountsTheQuerysWordsNotTheDocumentsTerms(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "logs")
		mustUpsert(t, s, "acme", "logs", []map[string]any{
			doc("id", "a-busy", "title", "2026-09-29T09:01:09Z 09a 09b 09c 09d 09e 09f 09g 09h GET /v1/models"),
			doc("id", "b-named", "title", "on 2026-09-29 the Sao Paulo edge dropped requests"),
		})
		hits, err := s.Search(ctx, "acme", "logs", "what happened at the Sao Paulo edge on 2026-09-29", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		if got := titles(t, hits); len(got) != 2 || got[0] != "on 2026-09-29 the Sao Paulo edge dropped requests" {
			t.Fatalf("ranking put %q first; the line holding the question's words should lead", got)
		}
	})
}

// TestSearchReadsEachWordsRangeAlone holds the query plan: every query word is
// a range seek on the primary key, and nothing reads the org's whole index. As
// one WHERE with the ranges OR-ed together SQLite scanned every term the org
// held, so a search through a workspace of large files took seconds. It reads
// SQLite's plan, which is SQLite's alone, so it runs on SQLite only; the
// statement it explains is the one both engines run.
func TestSearchReadsEachWordsRangeAlone(t *testing.T) {
	s := newStore(t)
	mustEnsure(t, s, "acme", "logs")
	query, args := s.searchSQL("acme", "logs", prefixRanges("frankfurt stale tls certificates"), []string{""}, 10, 0)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	// SQLite writes a bound value in a plan as a question mark.
	seek, byKey := regexp.MustCompile(`term>\? AND term<\?`), regexp.MustCompile(`pk=\?`)
	seeks := 0
	for _, step := range plan {
		if strings.Contains(step, "terms") && !seek.MatchString(step) {
			t.Fatalf("a step reads terms without a word's range: %q\nplan: %q", step, plan)
		}
		if (strings.Contains(step, " d ") || strings.HasSuffix(step, " d")) && !byKey.MatchString(step) {
			t.Fatalf("a step walks documents rather than fetching each match by key: %q\nplan: %q", step, plan)
		}
		if seek.MatchString(step) {
			seeks++
		}
	}
	if seeks != 4 {
		t.Fatalf("%d range seeks for 4 words; plan: %q", seeks, plan)
	}
}

// TestAWordInMostOfTheIndexIsNotRead is a log whose name sits in every passage's
// path: asking about one line names the log too, and that word is dropped from
// the search, so the line's own words decide and no query reads the whole file.
func TestAWordInMostOfTheIndexIsNotRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "logs")
		docs := make([]map[string]any, 0, commonAt+2)
		for i := 0; i <= commonAt; i++ {
			docs = append(docs, doc("id", fmt.Sprintf("p%06d", i), "title", "edge log"))
		}
		docs = append(docs, doc("id", "z-named", "title", "edge log lagos shed requests"))
		mustUpsert(t, s, "acme", "logs", docs)
		kept, err := s.rare(ctx, "acme", "logs", prefixRanges("lagos edge shed"))
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 2 || kept[0].lo != "lagos" || kept[1].lo != "shed" {
			t.Fatalf("kept %+v, want lagos and shed without edge", kept)
		}
		hits, err := s.Search(ctx, "acme", "logs", "lagos edge shed", nil, 3, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := titles(t, hits); len(got) != 1 || got[0] != "edge log lagos shed requests" {
			t.Fatalf("searched %v", got)
		}
		// A query of nothing but the common word still answers.
		if hits, _ := s.Search(ctx, "acme", "logs", "edge", nil, 3, 0); len(hits) != 3 {
			t.Fatalf("a query of only a common word answered %d", len(hits))
		}
	})
}

// TestLongTermsStayFindable proves a term past maxTerm is truncated rather than
// dropped, so an oversized word is still findable by its prefix.
func TestLongTermsStayFindable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "notes")
		long := "supercalifragilistic" + strings.Repeat("x", 200)
		mustUpsert(t, s, "acme", "notes", []map[string]any{doc("id", "1", "title", long)})

		hits, err := s.Search(ctx, "acme", "notes", "supercalifragilistic", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		if len(hits) != 1 {
			t.Errorf("long term not findable by its prefix: %d hits", len(hits))
		}
	})
}

// TestPrefixRangeStopsAtThePrefix proves a prefix query does not spill into
// neighbouring terms: searching "cat" must not return "dog", even though the
// range scan's upper bound is computed rather than exact.
func TestPrefixRangeStopsAtThePrefix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "notes")
		mustUpsert(t, s, "acme", "notes", []map[string]any{
			doc("id", "1", "title", "cat"),
			doc("id", "2", "title", "catalog"),
			doc("id", "3", "title", "cbt"),
			doc("id", "4", "title", "dog"),
			doc("id", "5", "title", "bat"),
		})
		hits, err := s.Search(ctx, "acme", "notes", "cat", nil, 10, 0)
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		got := titles(t, hits)
		if len(got) != 2 || !slices.Contains(got, "cat") || !slices.Contains(got, "catalog") {
			t.Errorf("prefix %q matched %v, want exactly [cat catalog]", "cat", got)
		}
	})
}

// TestIndexesListsTheOrgsSurface proves an org can enumerate what it holds, with
// document counts, and sees only its own. Without this an index whose uid nobody
// remembers is unreachable.
func TestIndexesListsTheOrgsSurface(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		mustEnsure(t, s, "acme", "messages")
		mustEnsure(t, s, "globex", "secrets")
		mustUpsert(t, s, "acme", "convos", []map[string]any{
			doc("id", "1", "title", "one"), doc("id", "2", "title", "two"),
		})

		idxs, counts, err := s.Indexes(ctx, "acme")
		if err != nil {
			t.Fatalf("Indexes: %v", err)
		}
		got := map[string]int{}
		for n, i := range idxs {
			got[i.UID] = counts[n]
		}
		if len(got) != 2 || got["convos"] != 2 || got["messages"] != 0 {
			t.Errorf("acme surface = %v, want convos:2 messages:0", got)
		}
		if _, leaked := got["secrets"]; leaked {
			t.Error("cross-tenant leak: acme's listing includes globex's index")
		}

		// An org with nothing gets an empty listing, never another org's.
		if idxs, _, err := s.Indexes(ctx, "nobody"); err != nil || len(idxs) != 0 {
			t.Errorf("empty org listing = %v (err %v), want none", idxs, err)
		}
	})
}

// TestDropIndexRemovesEverything proves deleting an index leaves no registry
// row, no documents and no terms — a partial drop would leave a uid that lists
// but cannot be read, or documents that match a search into nothing.
func TestDropIndexRemovesEverything(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		mustEnsure(t, s, "acme", "keep")
		mustUpsert(t, s, "acme", "convos", []map[string]any{doc("id", "1", "title", "aardvark")})
		mustUpsert(t, s, "acme", "keep", []map[string]any{doc("id", "1", "title", "aardvark")})

		if err := s.DropIndex(ctx, "acme", "convos"); err != nil {
			t.Fatalf("DropIndex: %v", err)
		}
		if _, err := s.Index(ctx, "acme", "convos"); err != errNoIndex {
			t.Errorf("dropped index still in the registry: %v", err)
		}
		if _, total, err := s.Documents(ctx, "acme", "convos", 10, 0); err != nil || total != 0 {
			t.Errorf("dropped index still holds %d documents (err %v)", total, err)
		}
		// The sibling index is untouched — a drop is scoped to its own uid.
		if hits, err := s.Search(ctx, "acme", "keep", "aardvark", nil, 10, 0); err != nil || len(hits) != 1 {
			t.Errorf("dropping convos damaged the keep index: %d hits, err %v", len(hits), err)
		}
	})
}

// TestEmptyIndexUIDIsNeverStored proves an index can never be created under an
// empty uid. A blank uid is not addressable through the API — its documents
// become invisible and its registry row cannot be dropped by name.
func TestEmptyIndexUIDIsNeverStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		if _, err := s.EnsureIndex(ctx, "acme", "", "id"); err == nil {
			t.Error("EnsureIndex accepted an empty uid; documents written there are unreachable")
		}
		idxs, _, err := s.Indexes(ctx, "acme")
		if err != nil {
			t.Fatalf("Indexes: %v", err)
		}
		for _, i := range idxs {
			if i.UID == "" {
				t.Error("an index with an empty uid exists in the registry")
			}
		}
	})
}

// TestPersistenceAcrossReopen proves indexes and documents survive a restart —
// the property a pod restart depends on.
func TestPersistenceAcrossReopen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		ctx := context.Background()

		first, err := openStore(dir)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		if _, err := first.EnsureIndex(ctx, "acme", "convos", "conversationId"); err != nil {
			t.Fatalf("EnsureIndex: %v", err)
		}
		if err := first.Upsert(ctx, "acme", "convos", "conversationId",
			[]map[string]any{doc("conversationId", "c1", "title", "durable topic")}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		second, err := openStore(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer func() { _ = second.Close() }()

		idx, err := second.Index(ctx, "acme", "convos")
		if err != nil {
			t.Fatalf("index lost across restart: %v", err)
		}
		if idx.PrimaryKey != "conversationId" {
			t.Errorf("primary key after restart = %q, want conversationId", idx.PrimaryKey)
		}
		hits, err := second.Search(ctx, "acme", "convos", "durable", nil, 10, 0)
		if err != nil {
			t.Fatalf("search after restart: %v", err)
		}
		if len(hits) != 1 {
			t.Errorf("documents lost across restart: %d hits", len(hits))
		}
	})
}

// TestParseUserFilter covers the filter grammar chat and hand-rolled callers
// send. An expression naming no user must yield nil — read as "no filter" — and
// never as "match nothing", which would silently empty every result set.
func TestParseUserFilter(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want []string
	}{
		{"double quotes", `user = "u1"`, []string{"u1"}},
		{"single quotes", `user = 'u1'`, []string{"u1"}},
		{"no spaces", `user="u1"`, []string{"u1"}},
		{"case insensitive", `USER = "u1"`, []string{"u1"}},
		{"IN list", `user IN ["u1", "u2"]`, []string{"u1", "u2"}},
		{"array form", []any{`user = "u1"`, `user = "u2"`}, []string{"u1", "u2"}},
		{"unrelated filter", `status = "open"`, nil},
		{"empty", ``, nil},
		{"nil", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseUserFilter(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseUserFilter(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ParseUserFilter(%v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestSearchPaging proves limit and offset walk the result set without
// repeating or dropping a document.
func TestSearchPaging(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "convos")
		docs := make([]map[string]any, 0, 5)
		for _, id := range []string{"1", "2", "3", "4", "5"} {
			docs = append(docs, doc("id", id, "title", "page item "+id))
		}
		mustUpsert(t, s, "acme", "convos", docs)

		seen := map[string]bool{}
		for offset := 0; offset < 5; offset += 2 {
			hits, err := s.Search(ctx, "acme", "convos", "page", nil, 2, offset)
			if err != nil {
				t.Fatalf("search offset %d: %v", offset, err)
			}
			for _, ttl := range titles(t, hits) {
				if seen[ttl] {
					t.Errorf("document %q returned on more than one page", ttl)
				}
				seen[ttl] = true
			}
		}
		if len(seen) != 5 {
			t.Errorf("paging saw %d of 5 documents", len(seen))
		}
		for _, id := range []string{"1", "2", "3", "4", "5"} {
			if !slices.Contains(keys(seen), "page item "+id) {
				t.Errorf("paging dropped document %q", id)
			}
		}
	})
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustEnsure(t *testing.T, s *Store, org, uid string) {
	t.Helper()
	if _, err := s.EnsureIndex(context.Background(), org, uid, "id"); err != nil {
		t.Fatalf("EnsureIndex %s/%s: %v", org, uid, err)
	}
}

func mustUpsert(t *testing.T, s *Store, org, uid string, docs []map[string]any) {
	t.Helper()
	idx, err := s.Index(context.Background(), org, uid)
	if err != nil {
		t.Fatalf("Index %s/%s: %v", org, uid, err)
	}
	if err := s.Upsert(context.Background(), org, uid, idx.PrimaryKey, docs); err != nil {
		t.Fatalf("Upsert %s/%s: %v", org, uid, err)
	}
}

// TestAdoptLegacyTables proves a store whose TABLES were renamed still yields its
// documents: the rows stay under the previous table names, nothing queries them,
// and the index reads as empty while every document sits intact one identifier
// away.
func TestAdoptLegacyTables(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()

		// A store in the PREVIOUS schema: same columns, previous table names.
		first, err := openStore(dir)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		for cur, prev := range map[string]string{"indexes": "search_indexes", "docs": "search_docs", "terms": "search_terms"} {
			if _, err := first.db.Exec(`ALTER TABLE ` + cur + ` RENAME TO ` + prev); err != nil {
				t.Fatalf("stage %s: %v", cur, err)
			}
		}
		now := time.Now().Unix()
		if _, err := first.db.Exec(
			`INSERT INTO search_indexes(org, uid, primary_key, filterable, created_at, updated_at)
		 VALUES('acme','convos','id','["user"]',$1,$2)`, now, now); err != nil {
			t.Fatalf("stage index row: %v", err)
		}
		if _, err := first.db.Exec(
			`INSERT INTO search_docs(org, uid, pk, usr, doc)
		 VALUES('acme','convos','1','u1','{"id":"1","title":"kubernetes migration"}')`); err != nil {
			t.Fatalf("stage doc row: %v", err)
		}
		if _, err := first.db.Exec(
			`INSERT INTO search_terms(org, uid, term, pk) VALUES('acme','convos','kubernetes','1')`); err != nil {
			t.Fatalf("stage term row: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		// Reopening runs migrate(), which must adopt those rows.
		s, err := openStore(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()

		if idx, err := s.Index(ctx, "acme", "convos"); err != nil {
			t.Errorf("index did not survive the table rename: %v", err)
		} else if idx.PrimaryKey != "id" {
			t.Errorf("primary key = %q, want id", idx.PrimaryKey)
		}
		hits, err := s.Search(ctx, "acme", "convos", "kubernetes", nil, 10, 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(hits) != 1 {
			t.Errorf("documents did not survive the table rename: %d hits", len(hits))
		}

		// The previous tables are gone, so a later boot has nothing left to adopt
		// and cannot resurrect rows that were since deleted.
		for _, prev := range []string{"search_indexes", "search_docs", "search_terms"} {
			if has, err := s.d.HasTable(ctx, s.db, prev); err != nil || has {
				t.Errorf("%s still exists after adoption: %v, %v", prev, has, err)
			}
		}
	})
}

// Text a column cannot hold on hanzo-sql is not stored on either engine: a document
// whose key or user is a NUL or bytes that are not UTF-8 cannot be keyed and is
// skipped as an empty key is, a delete of such a key finds nothing to delete, and an
// index cannot be named by it.
func TestWhatCannotBeStoredAsTextIsNotStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		mustEnsure(t, s, "acme", "notes")
		if err := s.Upsert(ctx, "acme", "notes", "id", []map[string]any{
			doc("id", "ok", "title", "kept"),
			doc("id", "a\x00b", "title", "nul key"),
			doc("id", "c\xffd", "title", "binary key"),
			doc("id", "u", "user", "x\x00y", "title", "nul user"),
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		pks, err := s.PKs(ctx, "acme", "notes")
		if err != nil || len(pks) != 1 || pks[0] != "ok" {
			t.Fatalf("PKs = %q, %v; want only the document that could be keyed", pks, err)
		}
		if err := s.Delete(ctx, "acme", "notes", []string{"a\x00b", "c\xffd", "ok"}); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if pks, _ := s.PKs(ctx, "acme", "notes"); len(pks) != 0 {
			t.Fatalf("PKs after delete = %q", pks)
		}
		if _, err := s.EnsureIndex(ctx, "acme", "a\x00b", "id"); err == nil {
			t.Fatal("an index was named by a NUL")
		}
	})
}

func TestARouteRefusesAUidThatCannotBeStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := indexApp(t)
		status, body := call(t, app, http.MethodGet, "/v1/index/indexes/a%00b", "acme", "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_index_uid") {
			t.Fatalf("a uid with a NUL: %d %s", status, body)
		}
	})
}
