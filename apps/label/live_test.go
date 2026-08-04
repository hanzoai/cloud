package label

// live_test.go runs the columnar half against a REAL warehouse.
//
// Everything else in this suite pins statement text and bindings, which is where
// the tenant boundary and the as-of rule live — but a statement that is correct
// in every property a string test can see is still a statement the engine may
// refuse. It did: the first cut of ResolvedSQL parsed fine and failed on a live
// engine with ILLEGAL_AGGREGATION, and the dataset plane's very first join would
// have found that out in production.
//
// So this runs the actual DDL, the actual insert, the actual resolve and the
// actual disposal through the same driver cloud deploys with, and it does it
// against `ghcr.io/hanzoai/datastore` — the fork the fleet runs, not a
// substitute:
//
//	docker run -d --name label-ch -p 29000:9000 ghcr.io/hanzoai/datastore:26.2.3.2
//	DATASTORE_ADDR=127.0.0.1:29000 DATASTORE_DB=default \
//	  go test -race -tags sqlite_math_functions ./apps/label/
//
// DATASTORE_DB=default on a FRESH engine, and the reason is worth one line: the
// client's readiness probe connects with the configured database as its auth_db,
// which on a warehouse that has never held our tables is a database that does not
// exist yet — and ensure(), which creates it, cannot run until the connection is
// ready. Naming `default` breaks the circle; ensure() then issues the same
// CREATE DATABASE + DDL over that live connection it issues in production. A
// deployed warehouse already holds `hanzo`, so nothing there needs this.
//
// It SKIPS when no warehouse is named, and says so. A skipped test is an honest
// gap; a test that quietly passed because there was nothing to talk to would be a
// claim of coverage this package does not have.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/tenant"
)

// live returns the tenant keys for one run, or skips. The org carries the run's
// nanosecond so a second run cannot read the first one's rows and conclude
// something about a statement it did not execute.
func live(t *testing.T) (tenant.Key, tenant.Key) {
	t.Helper()
	if os.Getenv("DATASTORE_ADDR") == "" {
		t.Skip("no DATASTORE_ADDR: the columnar half is unverified in this run")
	}
	// The connection is established in the background and never blocks the
	// process, so a test that asked Ready() the instant after Open would race the
	// dial and report an outage the deployment does not have.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := datastore.Wait(ctx); err != nil {
		t.Fatalf("DATASTORE_ADDR is set but the warehouse never became reachable: %v", err)
	}
	stamp := time.Now().UnixNano()
	mine, err := tenant.Mint("hanzo", fmt.Sprintf("live%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	// A NEIGHBOUR whose org slug differs only by brand — the exact pair a
	// single-word tenant key would collapse.
	theirs, err := tenant.Mint("zoo", fmt.Sprintf("live%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	return mine, theirs
}

// row builds one assertion WRITTEN on the day it became knowable — the live
// pipeline this plane is for. The write instant is what the guard compares
// against (Knowable is the later of it and the filer's `seen`), so a fixture
// admitted at the wall clock would describe a warehouse that learned a year of
// history in one instant and every horizon below would exclude everything.
func row(t *testing.T, subject string, at, seen time.Time, d Disposition, src Source, ev string, conf float64) Fact {
	t.Helper()
	f, err := admit(Fact{Kind: KindTransaction, Subject: subject, At: at, Seen: seen,
		Disposition: d, Source: src, Evidence: ev, By: "svc/u", Confidence: conf}, seen)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// resolvedAt runs the published contract at one horizon and returns the rows.
func resolvedAt(t *testing.T, tn tenant.Key, from, to time.Time, days int, now time.Time) []map[string]any {
	t.Helper()
	h := int64(days) * 24 * 3600
	rows, err := datastore.Query(t.Context(), ResolvedSQL(), tn.String(), from, to, h, h, now)
	if err != nil {
		t.Fatalf("the published resolved read failed on a live engine: %v\n%s", err, ResolvedSQL())
	}
	return rows
}

// TestTheWarehouseRunsEveryStatementThisPackagePublishes.
//
// One transaction, three assertions that became knowable months apart, and the
// two properties the whole plane rests on checked where they will actually run:
// a horizon that hides what was not yet knowable, and a precedence rule that
// picks the strongest claim that was.
func TestTheWarehouseRunsEveryStatementThisPackagePublishes(t *testing.T) {
	mine, theirs := live(t)
	ctx := t.Context()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	from, to := at.Add(-24*time.Hour), at.Add(24*time.Hour)
	// The materialisation instant: far enough past every horizon below that
	// maturity is never what excludes a row — only knowability is.
	now := at.Add(400 * 24 * time.Hour)

	review := row(t, "tx-1", at, at.Add(15*24*time.Hour), Unproductive, Review, "dec-1", 0.5)
	dispute := row(t, "tx-1", at, at.Add(90*24*time.Hour), Productive, Dispute, "dp-1", 1)
	chargeoff := row(t, "tx-1", at, at.Add(150*24*time.Hour), Productive, Chargeoff, "co-1", 1)

	if err := mirror(ctx, mine, []Fact{review, dispute, chargeoff}); err != nil {
		t.Fatalf("the DDL or the insert was refused by a live engine: %v", err)
	}
	// The neighbour asserts a CONTRADICTORY claim about an identically named
	// subject at the identical instant. Nothing below may see it.
	other := row(t, "tx-1", at, at.Add(24*time.Hour), Productive, Chargeoff, "co-x", 1)
	if err := mirror(ctx, theirs, []Fact{other}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		days           int
		source, id     string
		disposition    int
		contested      bool
		what           string
		expectNoAnswer bool
	}{
		{days: 10, what: "nothing was knowable ten days after the event", expectNoAnswer: true},
		{days: 30, source: "review", id: review.ID, disposition: 0, what: "only the analyst had spoken by day 30"},
		{days: 120, source: "dispute", id: dispute.ID, disposition: 1, contested: true, what: "the chargeback outranks the analyst once it is knowable"},
		{days: 200, source: "chargeoff", id: chargeoff.ID, disposition: 1, contested: true, what: "our own write-off outranks the chargeback"},
	} {
		rows := resolvedAt(t, mine, from, to, tc.days, now)
		if tc.expectNoAnswer {
			if len(rows) != 0 {
				t.Fatalf("%s, yet the engine answered %v", tc.what, rows)
			}
			continue
		}
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1 (%v)", tc.what, len(rows), rows)
		}
		r := rows[0]
		if got := fmt.Sprint(r["source"]); got != tc.source {
			t.Errorf("%s: source %s, want %s", tc.what, got, tc.source)
		}
		if got := fmt.Sprint(r["id"]); got != tc.id {
			t.Errorf("%s: id %s, want %s", tc.what, got, tc.id)
		}
		if got := fmt.Sprint(r["disposition"]); got != fmt.Sprint(tc.disposition) {
			t.Errorf("%s: disposition %s, want %d", tc.what, got, tc.disposition)
		}
		want := "false"
		if tc.contested {
			want = "true"
		}
		if got := truth(r["contested"]); got != want {
			t.Errorf("%s: contested %s, want %s", tc.what, got, want)
		}
	}

	// THE NEIGHBOUR. Same subject, same instant, opposite claim, and the only
	// thing keeping the two apart is the leading bound predicate.
	rows := resolvedAt(t, theirs, from, to, 30, now)
	if len(rows) != 1 || fmt.Sprint(rows[0]["id"]) != other.ID {
		t.Fatalf("the neighbour's own read did not answer with the neighbour's row: %v", rows)
	}

	// RE-DELIVERY IS FREE. The repair path re-sends rows the copy may already
	// hold, so a duplicate insert must not change any answer.
	if err := mirror(ctx, mine, []Fact{review, dispute, chargeoff}); err != nil {
		t.Fatal(err)
	}
	again := resolvedAt(t, mine, from, to, 120, now)
	if len(again) != 1 || fmt.Sprint(again[0]["id"]) != dispute.ID || truth(again[0]["contested"]) != "true" {
		t.Fatalf("a re-delivery changed the answer: %v", again)
	}

	// THE DISPOSAL, bounded by tenant AND id. The neighbour's row carries a
	// different digest here, so the id predicate is exercised inside one tenant:
	// disposing of the review must leave the dispute standing.
	if err := purge(ctx, mine, []string{review.ID}); err != nil {
		t.Fatalf("the disposal was refused by a live engine: %v", err)
	}
	left := resolvedAt(t, mine, from, to, 120, now)
	if len(left) != 1 || fmt.Sprint(left[0]["id"]) != dispute.ID {
		t.Fatalf("after disposing of the review, the dispute should stand alone: %v", left)
	}
	if truth(left[0]["contested"]) != "false" {
		t.Errorf("the only visible assertion left is the dispute, so nothing contests it: %v", left[0])
	}
	// And a disposal aimed at an id THIS tenant does not hold reaches nobody —
	// two tenants can assert byte-identical facts, so `org` is what stops one
	// tenant's boundary deleting another's row.
	if err := purge(ctx, mine, []string{other.ID}); err != nil {
		t.Fatal(err)
	}
	if rows := resolvedAt(t, theirs, from, to, 30, now); len(rows) != 1 {
		t.Fatalf("one tenant's disposal reached another tenant's rows: %v", rows)
	}
}

// TestGroundTruthFiledOverTheWireIsJoinableInTheWarehouse is the whole claim of
// this plane in one test: an assertion filed through the public op reaches the
// columnar copy, under the caller's minted tenant key, and the published contract
// finds it there. Every other test proves one half.
func TestGroundTruthFiledOverTheWireIsJoinableInTheWarehouse(t *testing.T) {
	mine, _ := live(t)
	app, _ := wireApp(t, "")
	org := strings.TrimPrefix(mine.String(), "hanzo/")

	// A REAL event, judged before its horizon closes — which is what the wire
	// path is for and the only shape in which a freshly filed label is visible at
	// its event's own as-of. Everything filed here is knowable NOW, because now is
	// when this plane learned it, so the event has to be recent enough that its
	// 120-day as-of is still ahead of us.
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-100 * 24 * time.Hour)
	out := post(t, app, org, "u_1", batch(
		assertion("transaction", "tx-wire", at, at.Add(20*24*time.Hour), Unproductive, Review, "dec-1", 0.4),
		assertion("transaction", "tx-wire", at, now.Add(-24*time.Hour), Productive, Dispute, "dp-1", 1)))
	if out.Recorded != 2 {
		t.Fatalf("recorded %d of 2: %+v", out.Recorded, out.Results)
	}
	if out.Mirror != "" {
		t.Fatalf("the live warehouse refused the delivery: %s", out.Mirror)
	}
	if out.Pending != 0 {
		t.Fatalf("%d assertions still pending after a successful delivery", out.Pending)
	}

	// The materialisation stands just past this event's own horizon: at+120d.
	rows := resolvedAt(t, mine, at.Add(-time.Hour), at.Add(time.Hour), 120, at.Add(121*24*time.Hour))
	if len(rows) != 1 {
		t.Fatalf("the warehouse holds %d resolved rows for what the wire filed, want 1", len(rows))
	}
	if got := fmt.Sprint(rows[0]["source"]); got != "dispute" {
		t.Fatalf("the winner in the warehouse is %s, want the dispute", got)
	}
	if truth(rows[0]["contested"]) != "true" {
		t.Fatalf("the analyst disagreed with the card network and the warehouse did not say so: %v", rows[0])
	}

	// AND THE WAREHOUSE APPLIES THE SAME GUARD THE RECORD PLANE DOES. Standing at
	// the event's 10-day as-of, nothing this plane learned today was knowable —
	// the derived copy carries `knowable`, so the answer here is the answer the
	// resolve op gives, not a weaker one computed from the filer's declared time.
	if early := resolvedAt(t, mine, at.Add(-time.Hour), at.Add(time.Hour), 10, at.Add(121*24*time.Hour)); len(early) != 0 {
		t.Fatalf("the warehouse resolved %v at a ten-day as-of for rows it learned today", early)
	}
}

// TestTheDisposalStatementHoldsAtItsOwnBound. maxDispose names ten thousand ids
// in one statement, and a placeholder count is exactly the kind of limit that
// holds in every test at three ids and fails on the one deployment that reaches
// the bound.
func TestTheDisposalStatementHoldsAtItsOwnBound(t *testing.T) {
	mine, _ := live(t)
	ctx := t.Context()
	ids := make([]string, 0, maxDispose)
	for i := range maxDispose {
		ids = append(ids, fmt.Sprintf("%064x", i))
	}
	if err := purge(ctx, mine, ids); err != nil {
		t.Fatalf("a disposal naming the bound's own %d ids was refused: %v", maxDispose, err)
	}
}

// truth renders whatever integer width the engine returns a boolean expression
// as. Asserting on the Go type would be asserting on the driver.
func truth(v any) string {
	if fmt.Sprint(v) == "0" || fmt.Sprint(v) == "false" {
		return "false"
	}
	return "true"
}
