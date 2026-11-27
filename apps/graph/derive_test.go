package graph

// Rules and what they derive, over HTTP: a rule is an assertion, so every test
// here enacts through the same POST /v1/graph that files anything else.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// put files one assertion, `at` 2026-01-01 unless fields say otherwise, and
// returns what the endpoint answered. authority nil is an ordinary member.
func put(t *testing.T, app *zip.App, authority map[string]string, fields map[string]any) (int, string) {
	t.Helper()
	a := map[string]any{"at": "2026-01-01T00:00:00Z", "source": "test", "evidence": fmt.Sprint("test://", fields["entity"])}
	maps.Copy(a, fields)
	code, b := callAs(t, app, authority, http.MethodPost, "/v1/graph", "", map[string]any{"assertions": []any{a}})
	return code, string(b)
}

// enact files a rule as an admin of the org, and fails unless it was admitted.
func enact(t *testing.T, app *zip.App, name, text string) {
	t.Helper()
	if code, b := put(t, app, orgAdmin, map[string]any{"entity": rulePrefix + name, "relation": ruleKey, "value": text}); code >= 300 {
		t.Fatalf("enact %s: %d %s", name, code, b)
	}
}

// edge files (from, relation, to) as an edge, and fails unless it was admitted.
func edge(t *testing.T, app *zip.App, from, relation, to string, fields map[string]any) {
	t.Helper()
	a := map[string]any{"entity": from, "relation": relation, "value": to, "names": true, "confidence": 0.9}
	maps.Copy(a, fields)
	if code, b := put(t, app, nil, a); code >= 300 {
		t.Fatalf("file %s %s %s: %d %s", from, relation, to, code, b)
	}
}

// withdraw files the retraction of (entity, relation), begun at `at` and
// knowable from `seen`.
func withdraw(t *testing.T, app *zip.App, entity, relation, at string, seen time.Time) {
	t.Helper()
	if code, b := put(t, app, nil, map[string]any{"entity": entity, "relation": relation, "value": "", "at": at, "seen": seen.Format(time.RFC3339)}); code >= 300 {
		t.Fatalf("withdraw %s %s: %d %s", entity, relation, code, b)
	}
}

func derive(t *testing.T, app *zip.App, body map[string]any) graphDeriveOut {
	t.Helper()
	var out graphDeriveOut
	post(t, app, "/v1/graph/derive", body, &out)
	return out
}

// pairs is each conclusion as subject>object, sorted.
func pairs(cs []graphConclusion) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Subject+">"+c.Object)
	}
	slices.Sort(out)
	return out
}

func ancestry(t *testing.T, app *zip.App) {
	t.Helper()
	enact(t, app, "ancestor-base", "ancestor(X, Y) :- parent(X, Y)")
	enact(t, app, "ancestor-step", "ancestor(X, Z) :- parent(X, Y), ancestor(Y, Z)")
}

// lineage files parent edges along nodes, in one batch.
func lineage(t *testing.T, app *zip.App, nodes []string) {
	t.Helper()
	var batch []any
	for i := range len(nodes) - 1 {
		batch = append(batch, map[string]any{
			"entity": nodes[i], "relation": "parent", "value": nodes[i+1], "names": true,
			"at": "2026-01-01T00:00:00Z", "source": "test", "evidence": "test://chain", "confidence": 0.9,
		})
	}
	if code, b := call(t, app, http.MethodPost, "/v1/graph", "", map[string]any{"assertions": batch}); code >= 300 {
		t.Fatalf("lineage: %d %s", code, b)
	}
}

// TestAFiveChainDerivesTenAncestors is recursion through the store: every
// ordered pair along the chain, each proved by its rule and the edges it rests
// on, with the decay of the chain it crosses.
func TestAFiveChainDerivesTenAncestors(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		lineage(t, app, []string{"a", "b", "c", "d", "e"})

		out := derive(t, app, map[string]any{"predicates": []string{"ancestor"}})
		want := []string{"a>b", "a>c", "a>d", "a>e", "b>c", "b>d", "b>e", "c>d", "c>e", "d>e"}
		if got := pairs(out.Derived); !slices.Equal(got, want) {
			t.Fatalf("derived %v, want %v", got, want)
		}
		if out.Truncated || len(out.Lemmas) != 0 || len(out.Lost) != 0 || len(out.Gained) != 0 {
			t.Errorf("truncated=%v lemmas=%d lost=%d gained=%d, want none of them", out.Truncated, len(out.Lemmas), len(out.Lost), len(out.Gained))
		}
		for _, c := range out.Derived {
			hops := strings.Index("abcde", c.Object) - strings.Index("abcde", c.Subject)
			switch {
			case hops == 1 && (c.Rule != "rule:ancestor-base" || len(c.Supports) != 1 || c.Supports[0].Assertion == ""):
				t.Errorf("%s>%s is one edge and is proved by %s from %+v", c.Subject, c.Object, c.Rule, c.Supports)
			case hops > 1 && (c.Rule != "rule:ancestor-step" || len(c.Supports) != 2 || c.Supports[0].Assertion == "" || c.Supports[1].Assertion != ""):
				t.Errorf("%s>%s crosses %d edges and is proved by %s from %+v", c.Subject, c.Object, hops, c.Rule, c.Supports)
			}
			if want := pow(0.9, hops); c.Decay < want-1e-9 || c.Decay > want+1e-9 || c.Weakest == "" {
				t.Errorf("%s>%s crosses %d edges of 0.9: decay %v weakest %q, want %v", c.Subject, c.Object, hops, c.Decay, c.Weakest, want)
			}
			if c.From != "2026-01-01T00:00:00Z" || c.Until != "" || c.RuleID == "" {
				t.Errorf("%s>%s: from %q until %q rule_id %q", c.Subject, c.Object, c.From, c.Until, c.RuleID)
			}
		}
	})
}

func pow(x float64, n int) float64 {
	out := 1.0
	for range n {
		out *= x
	}
	return out
}

// TestNegationDerivesExactlyTheGoodNodes: a unary predicate is "something in
// force says X p something", and `not` is absence from a complete relation.
func TestNegationDerivesExactlyTheGoodNodes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		enact(t, app, "safe", "safe(X) :- node(X), not bad(X)")
		for _, n := range []string{"n1", "n2", "n3", "n4", "n5"} {
			assertFact(t, app, "", n, "node", "yes", false)
		}
		for _, n := range []string{"n2", "n4", "elsewhere"} {
			assertFact(t, app, "", n, "bad", "true", false)
		}
		out := derive(t, app, map[string]any{"predicates": []string{"safe"}})
		if got := pairs(out.Derived); !slices.Equal(got, []string{"n1>", "n3>", "n5>"}) {
			t.Fatalf("safe = %v, want exactly n1, n3 and n5", got)
		}
	})
}

// TestARuleIsRefusedUnlessItCanBeEvaluated: a rule that does not parse, is
// unsafe, or would leave the rules unstratifiable — now or once a rule already
// filed becomes knowable — is refused at assertion and never filed.
func TestARuleIsRefusedUnlessItCanBeEvaluated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		refused := func(name, text, says string) {
			t.Helper()
			code, b := put(t, app, orgAdmin, map[string]any{"entity": rulePrefix + name, "relation": ruleKey, "value": text})
			if code != http.StatusBadRequest || !strings.Contains(b, says) {
				t.Errorf("rule:%s %q answered %d, want 400 saying %q: %s", name, text, code, says, b)
			}
		}
		refused("self", "p(X) :- q(X), not p(X)", "no stratum can hold it")
		refused("unsafe", "p(X) :- q(Y)", "X in the head is bound by no positive atom")
		refused("garbled", "p(X) :- q(X", "expected )")
		refused("", "p(X) :- q(X)", "a rule is named")
		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:edge", "relation": ruleKey, "value": "rule:self", "names": true}); code != http.StatusBadRequest || !strings.Contains(b, "its text, not an entity") {
			t.Errorf("a rule stated as an edge answered %d: %s", code, b)
		}

		// Each alone stratifies; together they recurse through a negation.
		enact(t, app, "a", "a(X) :- n(X), not b(X)")
		refused("b", "b(X) :- n(X), a(X)", "no stratum can hold it")
		// So does one filed now against one that becomes knowable in two minutes:
		// the set would stop evaluating the moment the first is heard.
		later := time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339)
		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:c", "relation": ruleKey, "value": "c(X) :- n(X), not d(X)", "seen": later}); code >= 300 {
			t.Fatalf("rule:c: %d %s", code, b)
		}
		refused("d", "d(X) :- n(X), c(X)", "no stratum can hold it")
		// And two arities of one predicate are one predicate misused.
		refused("arity", "a(X, Y) :- n(X), n(Y)", "a takes one argument")

		for _, f := range answered(t, app, "", "/v1/graph?relation=rule") {
			if !slices.Contains([]string{"rule:a", "rule:c"}, f.Entity) {
				t.Errorf("a refused rule was filed: %+v", f)
			}
		}
		// A withdrawal is always admitted, and then b stratifies.
		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:a", "relation": ruleKey, "value": "", "seen": time.Now().UTC().Add(time.Minute).Format(time.RFC3339)}); code >= 300 {
			t.Fatalf("withdraw rule:a: %d %s", code, b)
		}
		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:b", "relation": ruleKey, "value": "b(X) :- n(X), a(X)", "seen": time.Now().UTC().Add(time.Minute).Format(time.RFC3339)}); code >= 300 {
			t.Errorf("rule:b after rule:a was withdrawn: %d %s", code, b)
		}
	})
}

// TestARuleIsWrittenByAnAdmin: a rule is a declaration. Anything about a
// `rule:` entity is refused to a member, and to platform sudo in a tenant that
// has not made them its own, and nothing is filed; an admin's is filed and is
// on the audit trail.
func TestARuleIsWrittenByAnAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		for _, who := range []map[string]string{nil, superElsewhere} {
			for _, a := range []map[string]any{
				{"entity": "rule:x", "relation": ruleKey, "value": "x(X) :- n(X)"},
				{"entity": "rule:x", "relation": "note", "value": "who wrote this"},
			} {
				if code, b := put(t, app, who, a); code != http.StatusForbidden || !strings.Contains(b, "rule:x is a declaration") {
					t.Errorf("%v wrote %v: %d %s", who, a, code, b)
				}
			}
		}
		if got, trail := answered(t, app, "", "/v1/graph"), trailOf(t, rec, "graph.declare"); len(got) != 0 || len(trail) != 0 {
			t.Fatalf("a refused rule write filed %d rows and %d trail records", len(got), len(trail))
		}
		enact(t, app, "x", "x(X) :- n(X)")
		filed := answered(t, app, "", "/v1/graph?entity=rule:x")
		if trail := trailOf(t, rec, "graph.declare"); len(filed) != 1 || len(trail) != 1 || trail[0].Resource.ID != claim.Digest(filed[0].ID) {
			t.Fatalf("an admin's rule: %d rows, trail %+v", len(filed), trail)
		}
	})
}

// conclude is a derivation that files, as the authority given.
func conclude(t *testing.T, app *zip.App, authority map[string]string, body map[string]any) (int, graphDeriveOut, string) {
	t.Helper()
	body["file"] = true
	code, b := callAs(t, app, authority, http.MethodPost, "/v1/graph/derive", "", body)
	var out graphDeriveOut
	if code == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("derive answered something that is not its shape: %v: %s", err, b)
		}
	}
	return code, out, string(b)
}

// TestADerivationIsFiledByAnAdminOnTheTrail is the one write derive makes. A
// member and platform sudo are refused and nothing is filed or recorded; an
// admin's conclusions become assertions — the rule their source, its ID their
// evidence, the decay their confidence — in one write the trail holds under the
// digest of their IDs. Filed, they are asserted, so filing again files nothing.
func TestADerivationIsFiledByAnAdminOnTheTrail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		ancestry(t, app)
		lineage(t, app, []string{"a", "b", "c", "d", "e"})
		for _, who := range []map[string]string{nil, superElsewhere} {
			if code, _, b := conclude(t, app, who, map[string]any{"predicates": []string{"ancestor"}}); code != http.StatusForbidden {
				t.Errorf("%v filed a derivation: %d %s", who, code, b)
			}
		}
		if got, trail := answered(t, app, "", "/v1/graph?relation=ancestor"), trailOf(t, rec, "graph.derive"); len(got) != 0 || len(trail) != 0 {
			t.Fatalf("a refused filing left %d rows and %d trail records", len(got), len(trail))
		}

		code, out, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"ancestor"}})
		if code != http.StatusOK || out.Filed == nil || out.Filed.Recorded != 10 || out.Filed.Refused != 0 || len(out.Derived) != 10 {
			t.Fatalf("an admin's filing: %d filed=%+v derived=%d: %s", code, out.Filed, len(out.Derived), b)
		}
		proved := map[string]graphConclusion{}
		for _, c := range out.Derived {
			proved[c.Subject+">"+c.Object] = c
		}
		rows := answered(t, app, "", "/v1/graph?relation=ancestor")
		ids := make([]string, 0, len(rows))
		for _, f := range rows {
			c, ok := proved[f.Entity+">"+f.Value]
			if !ok || !f.Names || f.Source != c.Rule || f.Evidence != c.RuleID || f.Confidence != c.Decay || f.At != out.AsOf || f.Until != "" {
				t.Errorf("filed %+v from %+v", f, c)
			}
			ids = append(ids, f.ID)
		}
		slices.Sort(ids)
		trail := trailOf(t, rec, "graph.derive")
		if len(ids) != 10 || len(trail) != 1 || trail[0].Resource.ID != claim.Digest(ids...) || trail[0].Actor.Org != "acme" || trail[0].Outcome.Result != "success" {
			t.Fatalf("%d rows filed, trail %+v", len(ids), trail)
		}
		var after struct {
			Predicates []string `json:"predicates"`
			Filed      int      `json:"filed"`
		}
		if err := json.Unmarshal(trail[0].After, &after); err != nil || after.Filed != 10 || !slices.Equal(after.Predicates, []string{"ancestor"}) {
			t.Fatalf("the trail says %s", trail[0].After)
		}

		if code, out, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"ancestor"}}); code != http.StatusOK || len(out.Derived) != 0 || out.Filed == nil || out.Filed.Recorded != 0 {
			t.Fatalf("filing again: %d %s", code, b)
		}
		if n := len(trailOf(t, rec, "graph.derive")); n != 1 {
			t.Fatalf("a filing that filed nothing is on the trail: %d records", n)
		}
	})
}

// TestAFiledObjectIsAnEntityByItsProof: a conclusion is filed as an edge exactly
// when its object is an entity where the proof read it — an edge's value or any
// subject — and as a property when it was a scalar or the rule's constant.
func TestAFiledObjectIsAnEntityByItsProof(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		enact(t, app, "boss", "boss(X, Y) :- reports(X, Y)")
		enact(t, app, "chief", "chief(X, Z) :- boss(X, Y), boss(Y, Z)")
		enact(t, app, "label", "label(X, L) :- title(X, L)")
		enact(t, app, "titled", "titled(L, X) :- title(X, L)")
		enact(t, app, "owns", "owns(Y, X) :- titled(X, Y)")
		enact(t, app, "vip", `vip(X, "yes") :- boss(X, Y)`)
		edge(t, app, "ann", "reports", "bob", nil)
		edge(t, app, "bob", "reports", "cy", nil)
		assertFact(t, app, "", "ann", "title", "Engineer", false)

		code, out, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"boss", "chief", "label", "owns", "titled", "vip"}})
		if code != http.StatusOK || out.Filed == nil || out.Filed.Recorded != 8 {
			t.Fatalf("%d %s", code, b)
		}
		want := map[string]bool{
			"boss ann bob": true, "boss bob cy": true, "chief ann cy": true,
			"label ann Engineer": false, "titled Engineer ann": true, "owns ann Engineer": false,
			"vip ann yes": false, "vip bob yes": false,
		}
		got := map[string]bool{}
		for _, f := range answered(t, app, "", "/v1/graph") {
			if strings.HasPrefix(f.Source, rulePrefix) {
				got[f.Relation+" "+f.Entity+" "+f.Value] = f.Names
			}
		}
		if !maps.Equal(got, want) {
			t.Errorf("filed (relation entity value → names) %v, want %v", got, want)
		}
	})
}

// TestAFilingIsRefusedUnlessItCanBeFiledWhole: a what-if, a past instant or a
// one-argument predicate is not the graph's conclusion now in a shape it holds,
// and a deployment with no trail files nothing it cannot record.
func TestAFilingIsRefusedUnlessItCanBeFiledWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bare := mountGraph(t)
		if code, _, b := conclude(t, bare, orgAdmin, map[string]any{"predicates": []string{"ancestor"}}); code != http.StatusServiceUnavailable {
			t.Fatalf("no trail: %d %s, want 503", code, b)
		}
		_ = Shutdown()

		app, rec := audited(t)
		ancestry(t, app)
		enact(t, app, "safe", "safe(X) :- node(X), not bad(X)")
		lineage(t, app, []string{"a", "b", "c"})
		assertFact(t, app, "", "a", "node", "yes", false)
		id := answered(t, app, "", "/v1/graph?entity=a&relation=parent")[0].ID
		for _, tc := range []struct {
			body map[string]any
			says string
		}{
			{map[string]any{"predicates": []string{"ancestor"}, "as_of": "2026-03-01T00:00:00Z"}, "as it stands now"},
			{map[string]any{"predicates": []string{"ancestor"}, "as_known": "2026-03-01T00:00:00Z"}, "as it stands now"},
			{map[string]any{"predicates": []string{"ancestor"}, "without": []string{id}}, "as it stands now"},
			{map[string]any{"predicates": []string{"safe"}}, "safe takes one argument"},
		} {
			if code, _, b := conclude(t, app, orgAdmin, tc.body); code != http.StatusBadRequest || !strings.Contains(b, tc.says) {
				t.Errorf("filing %v: %d %s, want 400 saying %q", tc.body, code, b, tc.says)
			}
		}
		if got, trail := answered(t, app, "", "/v1/graph?relation=ancestor"), trailOf(t, rec, "graph.derive"); len(got) != 0 || len(trail) != 0 {
			t.Fatalf("a refused filing left %d rows and %d trail records", len(got), len(trail))
		}
	})
}

// TestARetractedSupportDropsItsConclusionFromThen is the two times. A support
// retracted at T2 leaves the conclusion holding at T1 and gone at T3 — and
// holding again at T3 as known before the retraction was filed. An until ends
// the conclusions that rest on it where it ends, and a rule is read as known.
func TestARetractedSupportDropsItsConclusionFromThen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		lineage(t, app, []string{"a", "b", "c"})
		edge(t, app, "c", "parent", "d", map[string]any{"until": "2026-07-01T00:00:00Z"})
		before := time.Now().UTC()
		withdraw(t, app, "b", "parent", "2026-06-01T00:00:00Z", before.Add(time.Minute))
		after := before.Add(2 * time.Minute)

		at := func(asOf string, known time.Time) []string {
			out := derive(t, app, map[string]any{"predicates": []string{"ancestor"}, "as_of": asOf, "as_known": known.Format(time.RFC3339)})
			var got []string
			for _, c := range out.Derived {
				got = append(got, c.Subject+">"+c.Object)
				if c.Subject == "a" && c.Object == "d" && c.Until != "2026-07-01T00:00:00Z" {
					t.Errorf("a>d rests on an edge until 2026-07-01 and says until %q", c.Until)
				}
			}
			slices.Sort(got)
			return got
		}
		for _, tc := range []struct {
			asOf  string
			known time.Time
			want  []string
		}{
			{"2026-03-01T00:00:00Z", after, []string{"a>b", "a>c", "a>d", "b>c", "b>d", "c>d"}},
			{"2026-06-15T00:00:00Z", after, []string{"a>b", "c>d"}},
			{"2026-08-01T00:00:00Z", after, []string{"a>b"}},
			{"2026-08-01T00:00:00Z", before, []string{"a>b", "a>c", "b>c"}},
		} {
			if got := at(tc.asOf, tc.known); !slices.Equal(got, tc.want) {
				t.Errorf("as of %s known %s: %v, want %v", tc.asOf, tc.known.Format(time.RFC3339), got, tc.want)
			}
		}

		// A rule filed to be knowable later is not a rule before then.
		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:kin", "relation": ruleKey, "value": "kin(X, Y) :- ancestor(X, Y)", "seen": after.Format(time.RFC3339)}); code >= 300 {
			t.Fatalf("rule:kin: %d %s", code, b)
		}
		code, b := call(t, app, http.MethodPost, "/v1/graph/derive", "", map[string]any{"predicates": []string{"kin"}, "as_known": before.Format(time.RFC3339)})
		if code != http.StatusBadRequest || !strings.Contains(string(b), "no rule in force concludes kin") {
			t.Errorf("a rule not yet knowable derived: %d %s", code, b)
		}
		if got := derive(t, app, map[string]any{"predicates": []string{"kin"}, "as_of": "2026-03-01T00:00:00Z", "as_known": after.Format(time.RFC3339)}); len(got.Derived) != 6 || len(got.Lemmas) != 6 {
			t.Errorf("kin once knowable: %d derived, %d lemmas, want 6 and the 6 ancestors they rest on", len(got.Derived), len(got.Lemmas))
		}
	})
}

// TestAConclusionOnTwoPathsFallsOnlyWithBoth: one path withdrawn, it stands on
// the other; both withdrawn, it is gone.
func TestAConclusionOnTwoPathsFallsOnlyWithBoth(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		enact(t, app, "hop-road", "hop(X, Y) :- road(X, Y)")
		enact(t, app, "hop-rail", "hop(X, Y) :- rail(X, Y)")
		enact(t, app, "reach-base", "reach(X, Y) :- hop(X, Y)")
		enact(t, app, "reach-step", "reach(X, Z) :- hop(X, Y), reach(Y, Z)")
		edge(t, app, "a", "road", "b", nil)
		edge(t, app, "b", "road", "d", nil)
		edge(t, app, "a", "rail", "c", nil)
		edge(t, app, "c", "rail", "d", nil)
		now := time.Now().UTC()
		withdraw(t, app, "a", "road", "2026-01-01T00:00:00Z", now.Add(time.Minute))
		withdraw(t, app, "c", "rail", "2026-01-01T00:00:00Z", now.Add(2*time.Minute))

		reaches := func(known time.Time) (bool, graphDeriveOut) {
			out := derive(t, app, map[string]any{"predicates": []string{"reach"}, "as_known": known.Format(time.RFC3339)})
			return slices.Contains(pairs(out.Derived), "a>d"), out
		}
		if ok, out := reaches(now); !ok || len(out.Lemmas) != 4 {
			t.Fatalf("both paths: a>d=%v with %d hop lemmas, want it and all four", ok, len(out.Lemmas))
		}
		ok, out := reaches(now.Add(90 * time.Second))
		if !ok {
			t.Fatal("a>d fell with one of its two paths")
		}
		for _, l := range out.Lemmas {
			if l.Rule == "rule:hop-road" && l.Subject == "a" {
				t.Errorf("a withdrawn road is still a hop: %+v", l)
			}
		}
		if ok, _ := reaches(now.Add(3 * time.Minute)); ok {
			t.Fatal("a>d stands with both paths withdrawn")
		}
	})
}

// TestEveryCitedIDIsARowInForce: each assertion a proof cites — a support, a
// rule, the weakest link — is a row this graph holds, and resolving its pair at
// the point holds it.
func TestEveryCitedIDIsARowInForce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		enact(t, app, "safe", "safe(X) :- node(X), not bad(X)")
		enact(t, app, "kept", "kept(X, Y) :- ancestor(X, Y), safe(Y), X != Y")
		lineage(t, app, []string{"a", "b", "c", "d"})
		for _, n := range []string{"a", "b", "c", "d"} {
			assertFact(t, app, "", n, "node", "yes", false)
		}
		assertFact(t, app, "", "c", "bad", "true", false)
		// A superseded account of b's node, so resolution has something to decide.
		if code, b := put(t, app, nil, map[string]any{"entity": "b", "relation": "node", "value": "surely", "at": "2026-02-01T00:00:00Z"}); code >= 300 {
			t.Fatalf("%d %s", code, b)
		}
		known := time.Now().UTC().Format(time.RFC3339)
		const asOf = "2026-05-01T00:00:00Z"
		out := derive(t, app, map[string]any{"predicates": []string{"kept"}, "as_of": asOf, "as_known": known})
		if got := pairs(out.Derived); !slices.Equal(got, []string{"a>b", "a>d", "b>d", "c>d"}) {
			t.Fatalf("kept = %v", got)
		}

		rows := map[string]wireFact{}
		for _, f := range answered(t, app, "", "/v1/graph") {
			rows[f.ID] = f
		}
		cited := map[string]bool{}
		for _, c := range slices.Concat(out.Derived, out.Lemmas) {
			cited[c.RuleID], cited[c.Weakest] = true, true
			for _, s := range c.Supports {
				if s.Assertion != "" {
					cited[s.Assertion] = true
				}
			}
		}
		delete(cited, "")
		if len(cited) < 8 {
			t.Fatalf("only %d IDs cited; the proofs are not being read", len(cited))
		}
		for id := range cited {
			f, ok := rows[id]
			if !ok {
				t.Errorf("cited %s, which is no row", id)
				continue
			}
			var r graphResolveOut
			_, b := call(t, app, http.MethodPost, "/v1/graph/resolve", "", map[string]any{
				"entity": f.Entity, "relation": f.Relation, "as_of": asOf, "as_known": known,
			})
			if err := json.Unmarshal(b, &r); err != nil || !slices.ContainsFunc(r.Held, func(h wireFact) bool { return h.ID == id }) {
				t.Errorf("cited %s (%s %s %q), which is not in force at the point: %s", id, f.Entity, f.Relation, f.Value, b)
			}
		}
	})
}

// TestWithoutSaysExactlyWhatFalls: the what-if runs twice and reports the
// difference — what falls without an edge, and what a negation stops refuting
// without the fact it rested on.
func TestWithoutSaysExactlyWhatFalls(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		enact(t, app, "safe", "safe(X) :- node(X), not bad(X)")
		lineage(t, app, []string{"a", "b", "c", "d", "e"})
		assertFact(t, app, "", "n1", "node", "yes", false)
		assertFact(t, app, "", "n2", "node", "yes", false)
		assertFact(t, app, "", "n2", "bad", "true", false)

		id := func(entity, relation string) string {
			for _, f := range answered(t, app, "", "/v1/graph?entity="+entity+"&relation="+relation) {
				return f.ID
			}
			t.Fatalf("no %s %s", entity, relation)
			return ""
		}
		out := derive(t, app, map[string]any{"predicates": []string{"ancestor", "safe"}, "without": []string{id("c", "parent"), id("n2", "bad")}})
		var lost, gained []string
		for _, a := range out.Lost {
			lost = append(lost, a.Predicate+":"+a.Subject+">"+a.Object)
		}
		for _, a := range out.Gained {
			gained = append(gained, a.Predicate+":"+a.Subject+">"+a.Object)
		}
		wantLost := []string{"ancestor:a>d", "ancestor:a>e", "ancestor:b>d", "ancestor:b>e", "ancestor:c>d", "ancestor:c>e"}
		if !slices.Equal(lost, wantLost) || !slices.Equal(gained, []string{"safe:n2>"}) {
			t.Errorf("lost %v gained %v, want lost %v and gained safe:n2", lost, gained, wantLost)
		}
		// The answer itself is the graph with everything in it.
		if len(out.Derived) != 11 {
			t.Errorf("derived %d, want the 10 ancestors and n1", len(out.Derived))
		}
		// Without a rule is a what-if too.
		out = derive(t, app, map[string]any{"predicates": []string{"ancestor"}, "without": []string{id("rule:ancestor-step", ruleKey)}})
		if len(out.Lost) != 6 || len(out.Gained) != 0 {
			t.Errorf("without the step rule: lost %d gained %d, want the 6 ancestors two or more edges apart", len(out.Lost), len(out.Gained))
		}
		// An ID that names nothing read is refused rather than answered as "nothing falls".
		code, b := call(t, app, http.MethodPost, "/v1/graph/derive", "", map[string]any{"predicates": []string{"ancestor"}, "without": []string{"nope"}})
		if code != http.StatusBadRequest || !strings.Contains(string(b), "no assertion this derivation reads") {
			t.Errorf("without an unknown id: %d %s", code, b)
		}
	})
}

// TestTheCeilingIsReportedNeverASilentShortAnswer: a chain of 142 has 10,011
// ancestors, eleven past the ceiling. The answer says it was cut, by which
// bound, and every tuple it does list is true. A what-if over it is refused.
func TestTheCeilingIsReportedNeverASilentShortAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		nodes := make([]string, 142)
		for i := range nodes {
			nodes[i] = fmt.Sprintf("n%03d", i)
		}
		lineage(t, app, nodes)
		out := derive(t, app, map[string]any{"predicates": []string{"ancestor"}})
		if !out.Truncated || out.Cut != "tuples" || out.Bound != deriveMax || len(out.Derived) != deriveMax {
			t.Fatalf("truncated=%v cut=%q bound=%d derived=%d, want cut by tuples at %d", out.Truncated, out.Cut, out.Bound, len(out.Derived), deriveMax)
		}
		for _, c := range out.Derived {
			if c.Subject >= c.Object {
				t.Fatalf("the cut answer lists %s>%s, which is false", c.Subject, c.Object)
			}
		}
		if code, _, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"ancestor"}}); code != http.StatusBadRequest || !strings.Contains(b, "cut short is not filed") {
			t.Errorf("filing a cut derivation: %d %s", code, b)
		}
		if got := answered(t, app, "", "/v1/graph?relation=ancestor&limit=1"); len(got) != 0 {
			t.Errorf("a cut derivation filed %d rows", len(got))
		}
		first := answered(t, app, "", "/v1/graph?entity=n000&relation=parent")[0].ID
		code, b := call(t, app, http.MethodPost, "/v1/graph/derive", "", map[string]any{"predicates": []string{"ancestor"}, "without": []string{first}})
		if code != http.StatusBadRequest || !strings.Contains(string(b), "cannot be told") {
			t.Errorf("a what-if over a cut evaluation: %d %s", code, b)
		}
	})
}

// TestAPredicateNoRuleConcludesIsRefused: an empty answer to a typo would read
// as "nothing follows".
func TestAPredicateNoRuleConcludesIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		for _, body := range []map[string]any{
			{"predicates": []string{"ancestr"}},
			{"predicates": []string{"parent"}},
			{},
		} {
			if code, b := call(t, app, http.MethodPost, "/v1/graph/derive", "", body); code != http.StatusBadRequest {
				t.Errorf("derive %v: %d %s", body, code, b)
			}
		}
	})
}

// plant files facts into acme's default graph judged by nothing, as a row an
// earlier version of this plane admitted would stand.
func plant(t *testing.T, facts ...Fact) {
	t.Helper()
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := mounted.stores.For(ns)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.record(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
}

// TestRulesFiledAtOnceAreJudgedTogether: two rules, each admissible alone and
// unstratifiable together, sent at one instant. A rule is judged in the
// transaction that files it, so exactly one is admitted and the rules in force
// still evaluate.
func TestRulesFiledAtOnceAreJudgedTogether(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rules := [][2]string{{"a", "a(X) :- n(X), not b(X)"}, {"b", "b(X) :- n(X), a(X)"}}
		for trial := range 40 {
			app, _ := audited(t)
			codes := make([]int, len(rules))
			var wg sync.WaitGroup
			for i, r := range rules {
				wg.Go(func() {
					codes[i], _ = put(t, app, orgAdmin, map[string]any{"entity": rulePrefix + r[0], "relation": ruleKey, "value": r[1]})
				})
			}
			wg.Wait()
			slices.Sort(codes)
			if !slices.Equal(codes, []int{http.StatusOK, http.StatusBadRequest}) {
				t.Fatalf("trial %d: rules a and b answered %v, want one admitted and one refused", trial, codes)
			}
			if got := answered(t, app, "", "/v1/graph?relation=rule"); len(got) != 1 {
				t.Fatalf("trial %d: %d rules filed", trial, len(got))
			} else if code, b := call(t, app, http.MethodPost, "/v1/graph/derive", "", map[string]any{"predicates": []string{got[0].Entity[len(rulePrefix):]}}); code != http.StatusOK {
				t.Fatalf("trial %d: derive %s: %d %s", trial, got[0].Entity, code, b)
			}
		}
	})
}

// TestAFilingOneValueCannotHoldIsRefused: a relation that holds one value at a
// time holds one of two values the rules conclude of a subject, or of one they
// conclude beside the one asserted, so such a filing is refused whole. Declared
// many, every value is filed and holds, and filing again files nothing.
func TestAFilingOneValueCannotHoldIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		enact(t, app, "late", `risk(X, "late") :- overdue(X)`)
		enact(t, app, "big", `risk(X, "big") :- large(X)`)
		assertFact(t, app, "", "p1", "overdue", "yes", false)
		assertFact(t, app, "", "p1", "large", "yes", false)
		assertFact(t, app, "", "p2", "overdue", "yes", false)
		assertFact(t, app, "", "p2", "risk", "low", false)

		if code, _, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"risk"}}); code != http.StatusBadRequest || !strings.Contains(b, "risk holds one value at a time") {
			t.Fatalf("filing two risks of p1: %d %s", code, b)
		}
		if got := answered(t, app, "", "/v1/graph?relation=risk"); len(got) != 1 {
			t.Fatalf("a refused filing left %d risk rows, want p2's one", len(got))
		}

		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "relation:risk", "relation": cardinalityKey, "value": "many"}); code >= 300 {
			t.Fatalf("declare risk many: %d %s", code, b)
		}
		if code, out, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"risk"}}); code != http.StatusOK || out.Filed == nil || out.Filed.Recorded != 3 {
			t.Fatalf("filing risk held many: %d %s", code, b)
		}
		if out := derive(t, app, map[string]any{"predicates": []string{"risk"}}); len(out.Derived) != 0 {
			t.Errorf("filed, and still derived: %v", pairs(out.Derived))
		}
		if code, out, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"risk"}}); code != http.StatusOK || out.Filed.Recorded != 0 {
			t.Errorf("filing again: %d %s", code, b)
		}
	})
}

// TestWithoutARuleAUnaryPredicateStaysUnary: without its rule, safe is named by
// no rule and keeps the shape the question asks it in, so what falls is the
// conclusion and the assertion that states safe outright holds in both.
func TestWithoutARuleAUnaryPredicateStaysUnary(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		enact(t, app, "safe", "safe(X) :- node(X)")
		assertFact(t, app, "", "n1", "node", "yes", false)
		assertFact(t, app, "", "n9", "safe", "true", false)
		rule := answered(t, app, "", "/v1/graph?entity=rule:safe&relation=rule")[0].ID
		out := derive(t, app, map[string]any{"predicates": []string{"safe"}, "without": []string{rule}})
		if !slices.Equal(out.Lost, []graphAtom{{Predicate: "safe", Subject: "n1"}}) || len(out.Gained) != 0 {
			t.Errorf("lost %+v gained %+v, want lost safe n1 and nothing gained", out.Lost, out.Gained)
		}
	})
}

// TestARowThatStatesNoRuleEnactsNothing: a note filed under a `rule:` key
// before rules were admitted is not a rule, and neither a derivation nor a new
// rule is refused for it.
func TestARowThatStatesNoRuleEnactsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		ancestry(t, app)
		lineage(t, app, []string{"a", "b"})
		plant(t, mk(t, "rule:refunds", ruleKey, "Refunds within 30 days", false, "handbook", time.Now().UTC()))
		if got := pairs(derive(t, app, map[string]any{"predicates": []string{"ancestor"}}).Derived); !slices.Equal(got, []string{"a>b"}) {
			t.Errorf("ancestor beside a note = %v", got)
		}
		enact(t, app, "kin", "kin(X, Y) :- ancestor(X, Y)")
	})
}

// TestARuleTakesNoUntil: rules are read at forever, where a rule that ends has
// ended, so a rule with an until is refused and the one it would replace stands.
func TestARuleTakesNoUntil(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		enact(t, app, "kin", "kin(X, Y) :- parent(X, Y)")
		edge(t, app, "a", "parent", "b", nil)
		code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:kin", "relation": ruleKey, "value": "kin(X, Y) :- parent(Y, X)",
			"at": "2026-09-01T00:00:00Z", "until": "2027-09-01T00:00:00Z"})
		if code != http.StatusBadRequest || !strings.Contains(b, "takes no until") {
			t.Errorf("a rule with an until: %d %s", code, b)
		}
		if got := pairs(derive(t, app, map[string]any{"predicates": []string{"kin"}}).Derived); !slices.Equal(got, []string{"a>b"}) {
			t.Errorf("kin = %v, want the rule in force", got)
		}
	})
}

// TestADerivationFilesNoDeclaration: a conclusion about a `relation:` or
// `rule:` key rests on data any member files, so a filing that holds one is
// refused and nothing is filed or recorded.
func TestADerivationFilesNoDeclaration(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		enact(t, app, "titled", "titled(L, X) :- title(X, L)")
		assertFact(t, app, "", "ann", "title", "relation:owner", false)
		if code, _, b := conclude(t, app, orgAdmin, map[string]any{"predicates": []string{"titled"}}); code != http.StatusBadRequest || !strings.Contains(b, "relation:owner") {
			t.Errorf("filing a conclusion about relation:owner: %d %s", code, b)
		}
		if got, trail := answered(t, app, "", "/v1/graph?relation=titled"), trailOf(t, rec, "graph.derive"); len(got) != 0 || len(trail) != 0 {
			t.Errorf("a refused filing left %d rows and %d trail records", len(got), len(trail))
		}
	})
}

// TestARetractionIsAdmittedPastTheCeiling: a withdrawal leaves fewer rules in
// force, which stratify whenever more did, so it is admitted however many rule
// assertions the organization holds.
func TestARetractionIsAdmittedPastTheCeiling(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		now := time.Now().UTC()
		rows := make([]Fact, walkBound+1)
		for i := range rows {
			name := fmt.Sprintf("r%05d", i)
			rows[i] = mk(t, rulePrefix+name, ruleKey, name+"(X) :- n(X)", false, "test", now)
		}
		plant(t, rows...)
		if code, b := put(t, app, orgAdmin, map[string]any{"entity": "rule:r00000", "relation": ruleKey, "value": ""}); code != http.StatusOK {
			t.Errorf("withdraw rule:r00000 past the ceiling: %d %s", code, b)
		}
	})
}
