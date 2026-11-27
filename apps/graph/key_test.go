package graph

// Canonical keys: an entity, a relation and an edge's value are folded to one
// spelling at admission and at every read that takes one, and a scalar is not.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// The two spellings of one name. Each pair is canonically equivalent and
// byte-distinct, which is the whole of what this file is about.
const (
	cafe         = "acme/caf\u00e9"  // composed (NFC)
	cafeApart    = "acme/cafe\u0301" // decomposed (NFD): e and a combining acute
	team         = "acme/\u00e9quipe"
	teamApart    = "acme/e\u0301quipe"
	depends      = "d\u00e9pend"
	dependsApart = "de\u0301pend"
)

// TestAKeyIsItsFold is the fold itself: NFC, no control characters, no space
// around it — and the controls go before composition, so a control between a
// letter and its accent does not keep them apart.
func TestAKeyIsItsFold(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"composed stays", cafe, cafe},
		{"decomposed composes", cafeApart, cafe},
		{"controls go", "acme/\x00svc\x07/\u0085api", "acme/svc/api"},
		{"space around goes", " \tacme/svc\n ", "acme/svc"},
		{"space inside stays", "acme/my svc", "acme/my svc"},
		{"a control between a letter and its accent", "acme/cafe\x00\u0301", cafe},
		{"nothing but controls is nothing", "\x00\x1b", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := key(tc.in); got != tc.want {
				t.Fatalf("key(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTwoSpellingsAreOneAssertion: the content address is taken over the fold,
// so the same statement in either spelling is one row — and a scalar, which is
// not a key, keeps the bytes it was given, so two spellings of it are two
// statements.
func TestTwoSpellingsAreOneAssertion(t *testing.T) {
	composed := mk(t, cafe, depends, team, true, "deploy", t0)
	apart := mk(t, cafeApart, dependsApart, teamApart, true, "deploy", t0)
	if composed.ID != apart.ID {
		t.Fatalf("one statement, two addresses: %s / %s", composed.ID, apart.ID)
	}
	if apart.Entity != cafe || apart.Relation != depends || apart.Value != team {
		t.Fatalf("admitted as %q %q %q, want every key composed", apart.Entity, apart.Relation, apart.Value)
	}

	scalar := mk(t, cafe, "name", "Cafe\u0301 \x07Noir", false, "crm", t0)
	if scalar.Value != "Cafe\u0301 \x07Noir" {
		t.Fatalf("a scalar was rewritten to %q", scalar.Value)
	}
	if mk(t, cafe, "name", "Caf\u00e9 \x07Noir", false, "crm", t0).ID == scalar.ID {
		t.Fatal("two different scalars share an address")
	}
}

// TestEverySpellingReadsOneEntity is the property over the wire, for every op
// that takes a key: whichever spelling filed the graph, either spelling reads
// it, and every row it reads carries the one spelling that was kept.
func TestEverySpellingReadsOneEntity(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		type spelling struct{ entity, relation, value string }
		composed := spelling{cafe, depends, team}
		apart := spelling{cafeApart, dependsApart, teamApart}
		for _, tc := range []struct {
			name       string
			filed, ask spelling
		}{
			{"filed decomposed, asked composed", apart, composed},
			{"filed composed, asked decomposed", composed, apart},
			{"filed decomposed, asked decomposed", apart, apart},
		} {
			t.Run(tc.name, func(t *testing.T) {
				app, _ := audited(t)
				assertFact(t, app, "", tc.filed.entity, tc.filed.relation, tc.filed.value, true)
				assertFact(t, app, "", tc.filed.entity, "city", "Paris", false)
				e, r, v := url.QueryEscape(tc.ask.entity), url.QueryEscape(tc.ask.relation), url.QueryEscape(tc.ask.value)

				for _, read := range []struct {
					by, path string
					want     int
				}{
					{"entity", "/v1/graph?entity=" + e, 2},
					{"relation", "/v1/graph?relation=" + r, 1},
					{"an edge's value", "/v1/graph?value=" + v, 1},
					{"a search's relation", "/v1/graph/search?q=acme&relation=" + r, 1},
				} {
					got := answered(t, app, "", read.path)
					if len(got) != read.want {
						t.Errorf("read by %s found %d, want %d", read.by, len(got), read.want)
					}
					for _, f := range got {
						if f.Entity != cafe {
							t.Errorf("read by %s: a row was kept as %q, want the composed key", read.by, f.Entity)
						}
					}
				}

				var res graphResolveOut
				post(t, app, "/v1/graph/resolve", map[string]any{"entity": tc.ask.entity, "relation": tc.ask.relation}, &res)
				if !res.Known || res.Winner.Value != team || res.Entity != cafe || res.Relation != depends {
					t.Errorf("resolve: %+v", res)
				}

				var walk graphNeighborsOut
				post(t, app, "/v1/graph/neighbors", map[string]any{"seeds": []string{tc.ask.entity}, "relation": tc.ask.relation}, &walk)
				if !slices.Equal(walk.Entities, []string{cafe, team}) {
					t.Errorf("neighbors reached %q, want the seed and what it depends on, composed", walk.Entities)
				}
				post(t, app, "/v1/graph/neighbors", map[string]any{"seeds": []string{tc.ask.value}, "direction": "in"}, &walk)
				if !slices.Equal(walk.Entities, []string{team, cafe}) {
					t.Errorf("neighbors in reached %q", walk.Entities)
				}

				var path graphPathOut
				post(t, app, "/v1/graph/path", map[string]any{"from": tc.ask.entity, "to": tc.ask.value, "relations": []string{tc.ask.relation}}, &path)
				if !path.Found || path.Hops != 1 {
					t.Errorf("path: found=%v hops=%d", path.Found, path.Hops)
				}

				var diff graphDiffOut
				post(t, app, "/v1/graph/diff", map[string]any{"entity": tc.ask.entity, "from": "2025-01-01T00:00:00Z"}, &diff)
				if len(diff.Asserted) != 2 {
					t.Errorf("diff asserted %d, want the entity's 2", len(diff.Asserted))
				}

				var parts graphCommunitiesOut
				post(t, app, "/v1/graph/communities", map[string]any{"relation": tc.ask.relation}, &parts)
				if parts.Edges != 1 || parts.Entities != 2 {
					t.Errorf("communities over one relation: %d edges among %d entities, want 1 and 2", parts.Edges, parts.Entities)
				}

				env := ask(t, app, `{ entity(key: "`+tc.ask.entity+`") { key assertions { value } } }`)
				got := env["data"].(map[string]any)["entity"].(map[string]any)
				if got["key"] != cafe || len(got["assertions"].([]any)) != 2 {
					t.Errorf("graphql entity: %v", got)
				}

				code, gone := erasure(t, app, orgAdmin, "", graphEraseIn{Entity: tc.ask.entity, Reason: "DSR-11"})
				if code != http.StatusOK || len(gone.IDs) != 2 || gone.Entity != cafe {
					t.Errorf("erase: status %d, %+v", code, gone)
				}
			})
		}
	})
}

// TestAScalarReadsByItsBytes: a value is a key only when it names an entity,
// so a property's scalar is kept and read byte for byte, and a read in the
// other spelling does not find it.
func TestAScalarReadsByItsBytes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		assertFact(t, app, "", "acme/shop", "name", "Cafe\u0301", false)

		got := answered(t, app, "", "/v1/graph?entity=acme/shop")
		if len(got) != 1 || got[0].Value != "Cafe\u0301" {
			t.Fatalf("the scalar came back as %q", valuesOf(got))
		}
		if n := len(answered(t, app, "", "/v1/graph?value="+url.QueryEscape("Cafe\u0301"))); n != 1 {
			t.Errorf("read by its own bytes found %d, want 1", n)
		}
		if n := len(answered(t, app, "", "/v1/graph?value="+url.QueryEscape("Caf\u00e9"))); n != 0 {
			t.Errorf("read by another spelling found %d: a scalar is not a key", n)
		}

		// The space around a scalar is bytes like any other.
		assertFact(t, app, "", "acme/shop", "city", " Paris ", false)
		got = answered(t, app, "", "/v1/graph?value="+url.QueryEscape(" Paris "))
		if len(got) != 1 || got[0].Value != " Paris " {
			t.Errorf("read by its own bytes found %q, want the one kept as sent", valuesOf(got))
		}
	})
}

// TestErasureReachesKeysFiledBeforeFolding: rows filed before admission folded
// are not rewritten. A read, folding what it is asked, does not find a key kept
// decomposed; an erasure asks for every spelling, so it removes them with the
// rest of the subject — the rows about it and the edges into it.
func TestErasureReachesKeysFiledBeforeFolding(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		// As a write before folding kept them: the bytes the caller sent.
		old := mk(t, cafe, "email", "c@example.test", false, "crm", t0)
		old.Entity = cafeApart
		old.ID = digest(old)
		into := mk(t, "acme/team/core", "member", cafe, true, "hr", t0)
		into.Value = cafeApart
		into.ID = digest(into)
		now := mk(t, cafeApart, "phone", "+1 555 0100", false, "crm", t0)
		if _, err := s.record(ctx, []Fact{old, into, now}); err != nil {
			t.Fatalf("record: %v", err)
		}

		read, err := s.read(ctx, filter{Entity: key(cafeApart)})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !slices.Equal(idsOf(read), []string{now.ID}) {
			t.Fatalf("a read by the key found %v, want only the row filed under it", idsOf(read))
		}

		for _, asked := range []string{cafe, cafeApart} {
			t.Run(asked, func(t *testing.T) {
				s := newStore(t)
				if _, err := s.record(ctx, []Fact{old, into, now}); err != nil {
					t.Fatalf("record: %v", err)
				}
				gone, _, remaining, err := s.erase(ctx, asked, eraseMax, noWitness)
				if err != nil || remaining != 0 {
					t.Fatalf("erase: remaining=%d err=%v", remaining, err)
				}
				if want := idsOf([]Fact{old, into, now}); !slices.Equal(idsOf(gone), want) {
					t.Fatalf("erased %v, want every spelling's rows %v", idsOf(gone), want)
				}
			})
		}
	})
}

// post sends one typed op's body and decodes its answer, failing on a refusal.
func post(t *testing.T, app *zip.App, path string, body, out any) {
	t.Helper()
	code, b := call(t, app, http.MethodPost, path, "", body)
	if code != http.StatusOK {
		t.Fatalf("POST %s: status %d: %s", path, code, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("POST %s: %v: %s", path, err, b)
	}
}
