package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// chain projects a path to the entities it passes through, which is what most of
// these tests assert about.
func chain(from string, edges []Fact) []string {
	out := []string{from}
	for _, e := range edges {
		if e.Entity == out[len(out)-1] {
			out = append(out, e.Value)
		} else {
			out = append(out, e.Entity)
		}
	}
	return out
}

func record(t *testing.T, s *store, facts ...Fact) {
	t.Helper()
	if _, err := s.record(context.Background(), facts); err != nil {
		t.Fatalf("record: %v", err)
	}
}

// TestAPathIsTheShortestChain: of two routes, the one with fewer hops, and every
// hop carries the assertion it crossed so the path is its own provenance.
func TestAPathIsTheShortestChain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		record(t, s,
			mk(t, "a", "calls", "b", true, "deploy", t0),
			mk(t, "b", "calls", "c", true, "deploy", t0),
			mk(t, "c", "calls", "d", true, "deploy", t0),
			mk(t, "a", "reads", "x", true, "trace", t0),
			mk(t, "x", "calls", "d", true, "trace", t0),
		)

		edges, found, cut, err := s.path(context.Background(), "a", "d", nil, "out", point(t0.Add(time.Hour)))
		if err != nil || !found || cut {
			t.Fatalf("path a→d: found=%v cut=%v err=%v", found, cut, err)
		}
		if got := fmt.Sprint(chain("a", edges)); got != "[a x d]" {
			t.Fatalf("path = %s, want the two-hop [a x d] over the three-hop route", got)
		}
		if edges[0].Source != "trace" || edges[0].Evidence == "" || edges[0].ID == "" {
			t.Errorf("a hop must carry the assertion it crossed: %+v", edges[0])
		}

		// A path to itself is found and crosses nothing.
		edges, found, _, _ = s.path(context.Background(), "a", "a", nil, "out", point(t0.Add(time.Hour)))
		if !found || len(edges) != 0 {
			t.Errorf("a→a: found=%v hops=%d, want found with zero hops", found, len(edges))
		}
	})
}

// TestAWalkCrossesOnlyEdgesInForce is what makes a path an answer about the graph
// as it stood. An owner corrected to another team is no longer an edge to the
// first, and a retracted dependency is no edge at all — but both still are, for
// every instant before the correction or the retraction was knowable.
func TestAWalkCrossesOnlyEdgesInForce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		record(t, s,
			// An owner is one at a time, so the organization says so: a relation
			// that is an edge holds many values unless declared otherwise.
			mk(t, "relation:owner", "cardinality", "one", false, "schema", t0),
			mk(t, "api", "owner", "team:core", true, "registry", t0),
			mk(t, "api", "owner", "team:web", true, "registry", t0.Add(time.Hour)),
			mk(t, "api", "depends", "db", true, "deploy", t0),
			mk(t, "api", "depends", "", false, "deploy", t0.Add(time.Hour)),
		)
		before, after := t0.Add(30*time.Minute), t0.Add(2*time.Hour)

		for _, c := range []struct {
			to   string
			asOf time.Time
			want bool
		}{
			{"team:core", before, true},
			{"team:core", after, false},
			{"team:web", after, true},
			{"db", before, true},
			{"db", after, false},
		} {
			_, found, _, err := s.path(ctx, "api", c.to, nil, "out", point(c.asOf))
			if err != nil {
				t.Fatalf("path api→%s: %v", c.to, err)
			}
			if found != c.want {
				t.Errorf("path api→%s as of %s: found=%v, want %v", c.to, c.asOf.Format(time.Kitchen), found, c.want)
			}
		}

		// neighbors is the same walk, so it answers the same way.
		tr, err := s.walk(ctx, []string{"api"}, nil, "out", 1, point(after), "")
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		if has(tr.nodes, "team:core") || has(tr.nodes, "db") {
			t.Errorf("neighbors crossed a superseded or retracted edge: %v", tr.nodes)
		}
		// Into a node is won on the pointing entity's pair, not the node's.
		tr, _ = s.walk(ctx, []string{"team:core"}, nil, "in", 1, point(after), "")
		if has(tr.nodes, "api") {
			t.Errorf("an in-walk crossed the superseded owner edge: %v", tr.nodes)
		}
	})
}

// TestAPathHonorsTheRelationAllowList: naming the relations that mean cause is
// what turns this into a causal chain, so a shorter route through anything else
// is not one.
func TestAPathHonorsTheRelationAllowList(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		record(t, s,
			mk(t, "outage", "causes", "failover", true, "postmortem", t0),
			mk(t, "failover", "causes", "latency", true, "postmortem", t0),
			mk(t, "outage", "mentions", "latency", true, "chat", t0),
		)
		ctx, asOf := context.Background(), t0.Add(time.Hour)

		edges, found, _, _ := s.path(ctx, "outage", "latency", nil, "out", point(asOf))
		if !found || len(edges) != 1 {
			t.Fatalf("unrestricted: found=%v hops=%d, want the one-hop mention", found, len(edges))
		}
		edges, found, _, _ = s.path(ctx, "outage", "latency", []string{"causes"}, "out", point(asOf))
		if got := fmt.Sprint(chain("outage", edges)); !found || got != "[outage failover latency]" {
			t.Fatalf("causes only: found=%v path=%s, want [outage failover latency]", found, got)
		}
		if _, found, _, _ = s.path(ctx, "outage", "latency", []string{"blocks"}, "out", point(asOf)); found {
			t.Error("a relation nothing was asserted under reached latency")
		}
	})
}

// TestAPathSeesOnlyWhatWasKnowable: an edge filed after the instant is not
// crossed, so a path asked of the past is the path that existed then.
func TestAPathSeesOnlyWhatWasKnowable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		record(t, s,
			mk(t, "a", "depends", "b", true, "deploy", t0),
			mk(t, "b", "depends", "c", true, "deploy", t0.Add(48*time.Hour)),
		)
		ctx := context.Background()
		if _, found, _, _ := s.path(ctx, "a", "c", nil, "out", point(t0.Add(time.Hour))); found {
			t.Error("a path crossed an edge that was not yet knowable")
		}
		if _, found, _, _ := s.path(ctx, "a", "c", nil, "out", point(t0.Add(72*time.Hour))); !found {
			t.Error("once the edge is knowable the path exists")
		}
	})
}

// TestNoPathIsAnAnswer: unreachable is found=false with nothing truncated, which
// is different from a search the bound stopped.
func TestNoPathIsAnAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		record(t, s,
			mk(t, "a", "depends", "b", true, "deploy", t0),
			mk(t, "c", "depends", "a", true, "deploy", t0),
		)
		edges, found, cut, err := s.path(context.Background(), "a", "c", nil, "out", point(t0.Add(time.Hour)))
		if err != nil || found || cut || len(edges) != 0 {
			t.Fatalf("a→c against the edge: found=%v cut=%v edges=%d err=%v, want not found and not cut", found, cut, len(edges), err)
		}
		// Against the arrow it is there, so direction is the caller's to choose.
		if _, found, _, _ := s.path(context.Background(), "a", "c", nil, "in", point(t0.Add(time.Hour))); !found {
			t.Error("in from a did not reach c, which points at it")
		}
		if _, _, _, err := s.path(context.Background(), "a", "c", nil, "sideways", point(t0)); err == nil {
			t.Error("a direction outside out, in and both was admitted")
		}
	})
}

// TestAPathIsHeldToTheWalkBound: a fan-out past the ceiling is truncated, and a
// node the bound cut off is reported as not found within it — never as reached.
func TestAPathIsHeldToTheWalkBound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		facts := make([]Fact, 0, walkBound)
		for i := range walkBound {
			// One relation per edge: a relation holds one value in force, so a
			// fan-out is many relations, not many values of one.
			facts = append(facts, mk(t, "hub", fmt.Sprintf("r%05d", i), fmt.Sprintf("leaf:%05d", i), true, "scan", t0))
		}
		record(t, s, facts...)
		ctx, asOf := context.Background(), t0.Add(time.Hour)

		if _, found, cut, _ := s.path(ctx, "hub", "leaf:00000", nil, "out", point(asOf)); !found || cut {
			t.Errorf("a leaf inside the bound: found=%v cut=%v, want found", found, cut)
		}
		// The hub and 10000 leaves are one node over the ceiling; the last leaf by
		// key is the one it drops.
		if _, found, cut, _ := s.path(ctx, "hub", fmt.Sprintf("leaf:%05d", walkBound-1), nil, "out", point(asOf)); found || !cut {
			t.Errorf("a leaf past the bound: found=%v cut=%v, want not found and cut", found, cut)
		}
		if _, found, cut, _ := s.path(ctx, "hub", "elsewhere", nil, "out", point(asOf)); found || !cut {
			t.Errorf("an unreachable goal behind a truncated walk: found=%v cut=%v, want cut", found, cut)
		}
	})
}

// TestDecayIsTheProductAndTheWeakestLink pins the arithmetic, and that it is
// withheld rather than invented when an edge states no confidence.
func TestDecayIsTheProductAndTheWeakestLink(t *testing.T) {
	e := func(c float64) Fact { return Fact{Confidence: c} }

	product, weakest, ok := decay([]Fact{e(0.9), e(0.4), e(0.8)})
	if !ok || math.Abs(product-0.288) > 1e-9 || weakest != 1 {
		t.Errorf("0.9·0.4·0.8: product=%v weakest=%d ok=%v, want 0.288 at 1", product, weakest, ok)
	}
	if _, weakest, _ = decay([]Fact{e(0.5), e(0.3), e(0.3)}); weakest != 1 {
		t.Errorf("a tie names the first weakest edge; got %d", weakest)
	}
	if _, _, ok = decay([]Fact{e(0.9), e(0)}); ok {
		t.Error("an edge stating no confidence must withhold the product")
	}
	if _, _, ok = decay(nil); ok {
		t.Error("an empty chain has no weakest link")
	}
}

// TestThePathEndpointAnswersFoundFalseAsA200 carries the op out to the wire: the
// chain with its decay and weakest link, and an unreachable goal as an answer.
func TestThePathEndpointAnswersFoundFalseAsA200(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		for _, a := range []map[string]any{
			{"entity": "acme/outage", "relation": "causes", "value": "acme/failover", "names": true, "confidence": 0.9},
			{"entity": "acme/failover", "relation": "causes", "value": "acme/latency", "names": true, "confidence": 0.3},
		} {
			a["at"], a["source"], a["evidence"] = "2026-01-01T00:00:00Z", "postmortem", "pm://1"
			if code, b := call(t, app, http.MethodPost, "/v1/graph", "", map[string]any{"assertions": []any{a}}); code >= 300 {
				t.Fatalf("assert: %d %s", code, b)
			}
		}

		var out graphPathOut
		code, b := call(t, app, http.MethodPost, "/v1/graph/path", "", map[string]any{
			"from": "acme/outage", "to": "acme/latency", "relations": []string{"causes"},
		})
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("path: %d %s", code, b)
		}
		if !out.Found || out.Hops != 2 || len(out.Edges) != 2 {
			t.Fatalf("path = %+v, want found in 2 hops", out)
		}
		if math.Abs(out.Decay-0.27) > 1e-9 {
			t.Errorf("decay = %v, want 0.9·0.3", out.Decay)
		}
		if out.Weakest == nil || out.Weakest.Value != "acme/latency" {
			t.Errorf("weakest = %+v, want the 0.3 edge into latency", out.Weakest)
		}

		code, b = call(t, app, http.MethodPost, "/v1/graph/path", "", map[string]any{
			"from": "acme/latency", "to": "acme/outage",
		})
		out = graphPathOut{}
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("an unreachable goal must answer 200: %d %s", code, b)
		}
		if out.Found || out.Truncated || out.Weakest != nil || len(out.Edges) != 0 {
			t.Errorf("unreachable = %+v, want found=false with nothing else", out)
		}

		if code, _ := call(t, app, http.MethodPost, "/v1/graph/path", "", map[string]any{"from": "acme/outage"}); code != http.StatusBadRequest {
			t.Errorf("a path with no goal answered %d, want 400", code)
		}
	})
}
