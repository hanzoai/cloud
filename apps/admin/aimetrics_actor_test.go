package admin

import (
	"context"
	"strings"
	"testing"
)

// TestAimTopActors_RanksBySpend. The question this row exists to answer is
// WHOSE bill it is, so it ranks by cost — a chatty cheap caller is not who an
// operator is looking for. Bounded like every other leaderboard on this board.
func TestAimTopActors_RanksBySpend(t *testing.T) {
	sql := aimTopActorsSQL()
	if !strings.Contains(sql, "ORDER BY cost_cents DESC, requests DESC LIMIT 12") {
		t.Errorf("topActors must order by cost desc, limit %d; got %q", aimTopN, sql)
	}
	if !strings.Contains(sql, "user_id AS actor") || !strings.Contains(sql, "GROUP BY actor") {
		t.Errorf("topActors must group the ledger by its user_id; got %q", sql)
	}
	// An unattributed row names nobody, so it is not a row on a leaderboard of
	// who spent. A row naming an APPLICATION is deliberately NOT excluded: that
	// is the finding, and hiding it would hide the thing worth reading.
	if !strings.Contains(sql, "user_id != ''") {
		t.Errorf("topActors must skip rows that name nobody; got %q", sql)
	}
}

// TestAimTopActors_Rows proves the decode, including the empty case: no rows is
// an empty list, never nil, so the board renders "nobody spent in this window"
// rather than a missing key an SDK reads as absent.
func TestAimTopActors_Rows(t *testing.T) {
	got := aimActorsFromRows([]map[string]any{
		{"actor": "acme/alice", "requests": int64(12), "tokens": int64(3400), "cost_cents": int64(210)},
		{"actor": "hanzo/hanzo-cloud", "requests": int64(388), "tokens": int64(9), "cost_cents": int64(2179)},
	})
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].Actor != "acme/alice" || got[0].Requests != 12 || got[0].Tokens != 3400 || got[0].CostCents != 210 {
		t.Fatalf("row 0 decoded wrong: %+v", got[0])
	}
	if got[1].Actor != "hanzo/hanzo-cloud" {
		t.Fatalf("an application-named row must survive the read — it is the finding; got %+v", got[1])
	}
	if empty := aimActorsFromRows(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("no rows must decode to an empty list, got %#v", empty)
	}
}

// TestAimetricsRefusesANonSuperAdmin pins that this read reaches NO new audience.
// Per-actor spend is cross-tenant by construction, so it lives behind the ONE
// predicate that already gates this board — core.Admit, owner == "admin". Off
// the HTTP path there is no attested caller and the answer is a refusal, which
// is where an unauthenticated caller lands too.
func TestAimetricsRefusesANonSuperAdmin(t *testing.T) {
	out, err := aimetrics(context.Background(), &rangeIn{Range: "7d"})
	if err == nil {
		t.Fatalf("a caller with no attested principal must be refused, got %+v", out)
	}
	if !strings.Contains(err.Error(), "SuperAdmin") {
		t.Fatalf("refusal must name the predicate it applied, got %v", err)
	}
}
