package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func diffOf(t *testing.T, s *store, entity string, from, to time.Time, limit int) ([]delta, bool) {
	t.Helper()
	// These fixtures state every fact of one moment and learn them at
	// different times, so their diff is of what was HEARD between from and to:
	// the two points share a valid instant and differ in what was known.
	out, cut, err := s.diff(context.Background(), entity, when{valid: to, known: from}, when{valid: to, known: to}, limit)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	return out, cut
}

// TestADiffReportsASupersessionOnce: an owner corrected and then restated by a
// second source is ONE change — the claim in force at each end differs once,
// however many rows arrived between them.
func TestADiffReportsASupersessionOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		core := mk(t, "api", "owner", "team:core", false, "registry", t0)
		web := mk(t, "api", "owner", "team:web", false, "registry", t0.Add(time.Hour))
		again := mk(t, "api", "owner", "team:web", false, "handbook", t0.Add(2*time.Hour))
		record(t, s, core, web, again)

		out, cut := diffOf(t, s, "", t0.Add(30*time.Minute), t0.Add(3*time.Hour), walkBound)
		if cut || len(out) != 1 {
			t.Fatalf("diff = %+v cut=%v, want exactly one change", out, cut)
		}
		d := out[0]
		if d.kind != "superseded" || d.was.ID != core.ID || d.now.ID != again.ID {
			t.Errorf("change = %s %q→%q, want superseded team:core→team:web as last restated", d.kind, d.was.Value, d.now.Value)
		}
	})
}

// TestADiffIsTheOrderNotTheRows: a late assertion that loses the order changes
// nothing, and neither does one that wins but restores what was already in
// force. Rows arrived in the interval in both cases; the answer did not move.
func TestADiffIsTheOrderNotTheRows(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		at := t0.Add(time.Hour)
		record(t, s,
			mk(t, "api", "owner", "team:core", false, "registry", t0),
			// Equally knowable, so confidence decides: web wins, platform loses.
			withConfidence(mk(t, "api", "owner", "team:web", false, "survey", at), 0.9),
			withConfidence(mk(t, "api", "owner", "team:platform", false, "guess", at), 0.2),

			mk(t, "db", "tier", "gold", false, "registry", t0),
			mk(t, "db", "tier", "silver", false, "survey", at),
			mk(t, "db", "tier", "gold", false, "audit", t0.Add(2*time.Hour)),
		)

		out, _ := diffOf(t, s, "", t0.Add(30*time.Minute), t0.Add(3*time.Hour), walkBound)
		if len(out) != 1 {
			t.Fatalf("diff = %+v, want the one pair whose claim moved", out)
		}
		if out[0].kind != "superseded" || out[0].now.Value != "team:web" {
			t.Errorf("change = %s →%q, want superseded to the winner team:web", out[0].kind, out[0].now.Value)
		}
		for _, d := range out {
			if d.now.Value == "team:platform" || d.was.Value == "team:platform" {
				t.Error("an assertion that lost the order was reported as a change")
			}
			if d.now.Entity == "db" {
				t.Error("a tier that left and came back within the interval is unchanged at its ends")
			}
		}
	})
}

// TestADiffReportsARetraction: a retraction winning an edge is reported as that,
// with the retraction itself beside what it withdrew; a claim made and withdrawn
// inside one interval is no change; and a claim after a retraction comes into
// force like any first one.
func TestADiffReportsARetraction(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		edge := mk(t, "api", "depends", "db", true, "deploy", t0)
		gone := mk(t, "api", "depends", "", false, "deploy", t0.Add(time.Hour))
		back := mk(t, "api", "depends", "cache", true, "deploy", t0.Add(3*time.Hour))
		record(t, s, edge, gone, back)

		out, _ := diffOf(t, s, "api", t0.Add(30*time.Minute), t0.Add(2*time.Hour), walkBound)
		if len(out) != 1 || out[0].kind != "retracted" {
			t.Fatalf("diff = %+v, want one retraction", out)
		}
		if out[0].was.ID != edge.ID || out[0].now.ID != gone.ID {
			t.Errorf("retraction was=%q now=%q, want the edge and the retraction that won it", out[0].was.Value, out[0].now.Value)
		}

		out, _ = diffOf(t, s, "api", t0.Add(-time.Hour), t0.Add(2*time.Hour), walkBound)
		if len(out) != 0 {
			t.Errorf("asserted and retracted inside the interval: %+v, want nothing", out)
		}

		out, _ = diffOf(t, s, "api", t0.Add(2*time.Hour), t0.Add(4*time.Hour), walkBound)
		if len(out) != 1 || out[0].kind != "asserted" || out[0].now.ID != back.ID {
			t.Errorf("diff = %+v, want cache asserted over the retraction", out)
		}
	})
}

// TestADiffIsBoundedByItsInstants: an assertion knowable exactly at From is the
// baseline, one knowable exactly at To is in the diff, and one knowable after To
// is not — the same `knowable <= as_of` every other read here uses.
func TestADiffIsBoundedByItsInstants(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		from, to := t0.Add(time.Hour), t0.Add(2*time.Hour)
		record(t, s,
			mk(t, "at-from", "owner", "team:core", false, "registry", from),
			mk(t, "at-to", "owner", "team:core", false, "registry", to),
			mk(t, "after", "owner", "team:core", false, "registry", to.Add(time.Second)),
		)

		out, _ := diffOf(t, s, "", from, to, walkBound)
		if len(out) != 1 || out[0].now.Entity != "at-to" || out[0].kind != "asserted" {
			t.Fatalf("diff = %+v, want only at-to asserted", out)
		}
	})
}

// TestTheWholeGraphDiffIsBounded: the org-wide form examines at most Limit pairs
// in key order and says when there were more.
func TestTheWholeGraphDiffIsBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		for _, e := range []string{"a", "b", "c"} {
			record(t, s, mk(t, e, "owner", "team:core", false, "registry", t0.Add(time.Hour)))
		}
		out, cut := diffOf(t, s, "", t0, t0.Add(2*time.Hour), 2)
		if !cut || len(out) != 2 || out[0].now.Entity != "a" || out[1].now.Entity != "b" {
			t.Fatalf("limit 2 over 3 pairs: %+v cut=%v, want a and b and cut", out, cut)
		}
		if out, cut = diffOf(t, s, "", t0, t0.Add(2*time.Hour), 3); cut || len(out) != 3 {
			t.Errorf("limit 3 over 3 pairs: %d changes cut=%v, want all three and not cut", len(out), cut)
		}
	})
}

// TestTheDiffEndpointAnswersInThreeLists carries the op to the wire, with its
// refusals.
func TestTheDiffEndpointAnswersInThreeLists(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		assertFact(t, app, "", "acme/svc/api", "owner", "acme/team/core", true)

		var out graphDiffOut
		code, b := call(t, app, http.MethodPost, "/v1/graph/diff", "", map[string]any{"from": "2025-01-01T00:00:00Z"})
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("diff: %d %s", code, b)
		}
		if len(out.Asserted) != 1 || out.Asserted[0].Value != "acme/team/core" {
			t.Errorf("asserted = %+v, want the one owner filed", out.Asserted)
		}
		if out.Superseded == nil || out.Retracted == nil || out.To == "" || out.Bound != walkBound {
			t.Errorf("an empty list is [] and the bound is published: %s", b)
		}

		for name, body := range map[string]map[string]any{
			"no from":        {"to": "2026-01-01T00:00:00Z"},
			"to before from": {"from": "2026-01-02T00:00:00Z", "to": "2026-01-01T00:00:00Z"},
			"not an instant": {"from": "yesterday"},
		} {
			if code, _ := call(t, app, http.MethodPost, "/v1/graph/diff", "", body); code != http.StatusBadRequest {
				t.Errorf("%s answered %d, want 400", name, code)
			}
		}
	})
}
