package risk

// feature_test.go — THE PROOF that a cross-tenant feature read is impossible.
//
// Three independent arguments, because one is a claim and three are a boundary:
//
//  1. TYPE — the datastore is reachable from exactly one file, and every function
//     in it that builds a feature statement takes a [tenant], which has no
//     exported constructor. A cross-tenant read has nothing to be spelled with.
//  2. STATEMENT — every statement the package can build opens with `org = ?` and
//     binds the tenant as its FIRST argument. Interpolation would put the value
//     in the text and leave the args empty; binding puts it in the args and
//     leaves the text a constant, and that difference is what is asserted.
//  3. BEHAVIOUR — an organisation naming another organisation's subject gets
//     ZERO ROWS, not a refusal. A refusal would be a probe oracle: "403" and
//     "404" would tell a caller that the subject exists somewhere.
//
// Every one of these is mutation-proven: reintroduce the defect and the named
// test is the one that fails.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shapes is EVERY shape a feature read can take. The [query] struct has no
// free-text field, so this is not a sample — it is the closed set, and a new
// field on query that this list does not cover is a compile-time visible change.
func shapes(now time.Time) []query {
	var out []query
	for _, kind := range append([]string{""}, kinds...) {
		for _, subject := range []string{"", "s_1", "'; DROP TABLE hanzo.risk_feature; --"} {
			for _, limit := range []int{0, 10, maxRows * 2} {
				out = append(out, query{kind: kind, subject: subject, start: now.Add(-time.Hour), end: now, limit: limit})
			}
		}
	}
	return out
}

// TestFeatureRead_OrgIsAlwaysTheLeadingBoundPredicate builds every statement the
// package can build and asserts the tenant leads it and is BOUND.
//
// Mutation proof: move `org = ?` after the window in featureWhere, or interpolate
// it with Sprintf, and this fails.
func TestFeatureRead_OrgIsAlwaysTheLeadingBoundPredicate(t *testing.T) {
	now := time.Now().UTC()
	for _, brandID := range []string{brandA, brandB} {
		for _, org := range []string{orgA, orgB} {
			k := key(t, brandID, org)
			for _, q := range shapes(now) {
				where, args := featureWhere(k, q)
				if !strings.HasPrefix(where, "org = ?") {
					t.Fatalf("predicate does not open with the tenant: %q", where)
				}
				if len(args) == 0 || args[0] != string(k) {
					t.Fatalf("first bound argument is %v, want the tenant %q", args, string(k))
				}
				if strings.Contains(where, org) || strings.Contains(where, string(k)) {
					t.Fatalf("the tenant is INTERPOLATED into %q — it must only ever be bound", where)
				}
				if q.subject != "" && strings.Contains(where, q.subject) {
					t.Fatalf("the subject is interpolated into %q", where)
				}
				if got := strings.Count(where, "?"); got != len(args) {
					t.Fatalf("%q has %d placeholders and %d arguments", where, got, len(args))
				}
			}
		}
	}
}

// TestFeatureRead_EveryLiveStatementBindsTheTenant drives the real read paths —
// not the builder — and asserts the same property of what actually reached the
// warehouse. The builder being right is worth nothing if a path bypasses it.
func TestFeatureRead_EveryLiveStatementBindsTheTenant(t *testing.T) {
	probe.reset(true)
	k := key(t, brandA, orgA)
	ctx := context.Background()
	end := time.Now().UTC()

	if _, err := rows(ctx, k, query{start: end.Add(-time.Hour), end: end}); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if _, err := rows(ctx, k, query{kind: kindAccount, subject: "u_1", start: end.Add(-time.Hour), end: end}); err != nil {
		t.Fatalf("rows(subject): %v", err)
	}
	if _, err := dictionary(ctx, k, end.Add(-time.Hour), end); err != nil {
		t.Fatalf("dictionary: %v", err)
	}
	p := newTestPlane(t)
	if _, err := p.warm(ctx, k); err != nil {
		t.Fatalf("warm: %v", err)
	}

	read := probe.reads()
	if len(read) == 0 {
		t.Fatal("no feature statement was recorded at all — the test proves nothing")
	}
	for _, s := range read {
		if !strings.Contains(s.SQL, "WHERE org = ?") {
			t.Errorf("statement does not open its predicate with the bound tenant:\n%s", s.SQL)
		}
		if len(s.Args) == 0 || s.Args[0] != string(k) {
			t.Errorf("statement bound %v first, want the tenant %q:\n%s", s.Args, string(k), s.SQL)
		}
	}
}

// TestFeatureRead_ForeignSubjectReturnsZeroRows is the behavioural half: one
// organisation naming another's subject reads NOTHING, and is told nothing.
//
// Mutation proof: drop the org term from featureWhere and org B starts reading
// org A's rows, which is exactly what this fails on.
func TestFeatureRead_ForeignSubjectReturnsZeroRows(t *testing.T) {
	probe.reset(true)
	a := key(t, brandA, orgA)
	b := key(t, brandA, orgB)
	probe.hold(string(a), map[string]any{
		"subject_kind": kindAccount, "subject": "u_secret", "bucket": surfaceAt(1),
		"events": uint32(9), "spend_nano": int64(1_000_000_000),
	})

	end := time.Now().UTC()
	got, err := rows(context.Background(), b, query{kind: kindAccount, subject: "u_secret", start: end.Add(-time.Hour), end: end})
	if err != nil {
		t.Fatalf("a foreign subject must read empty, not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("org %q read %d row(s) of org %q's surface", orgB, len(got), orgA)
	}
	// ...and the organisation that owns it still reads it, so the emptiness above
	// is isolation and not a broken fixture.
	own, err := rows(context.Background(), a, query{kind: kindAccount, subject: "u_secret", start: end.Add(-time.Hour), end: end})
	if err != nil || len(own) != 1 {
		t.Fatalf("the owning organisation read %d row(s), err=%v — the fixture is not exercising the boundary", len(own), err)
	}
}

// TestFeatureRead_TwoBrandsAreTwoTenants is the collision a bare org key would
// cause: `acme` on one issuer and `acme` on another are unrelated organisations,
// and keyed on the bare org they would be one set of rows.
func TestFeatureRead_TwoBrandsAreTwoTenants(t *testing.T) {
	probe.reset(true)
	ha := key(t, brandA, orgA)
	za := key(t, brandB, orgA)
	if ha == za {
		t.Fatal("two brands produced one tenant key")
	}
	probe.hold(string(ha), map[string]any{
		"subject_kind": kindPerson, "subject": "p_1", "bucket": surfaceAt(1), "events": uint32(3),
	})
	end := time.Now().UTC()
	got, err := rows(context.Background(), za, query{start: end.Add(-time.Hour), end: end})
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("brand %q's %q read %d row(s) of brand %q's %q", brandB, orgA, len(got), brandA, orgA)
	}
}

// TestFeatureRead_RefusesAnUnqualifiedTenant closes the last door: a key that did
// not come from the mint is refused at the read, not folded into something
// plausible.
func TestFeatureRead_RefusesAnUnqualifiedTenant(t *testing.T) {
	probe.reset(true)
	end := time.Now().UTC()
	for _, bad := range []tenant{"", "acme", "/acme", "hanzo/", "nosuchbrand/acme", "hanzo/a/b"} {
		if _, err := rows(context.Background(), bad, query{start: end.Add(-time.Hour), end: end}); err == nil {
			t.Errorf("rows accepted the unqualified key %q", string(bad))
		}
		if err := rollup(context.Background(), bad, rollups[0], end.Add(-time.Hour), end); err == nil {
			t.Errorf("rollup accepted the unqualified key %q", string(bad))
		}
		if _, err := newTestPlane(t).roll(context.Background(), bad); err == nil {
			t.Errorf("roll accepted the unqualified key %q", string(bad))
		}
	}
}

// TestRollup_ReadsTheBareOrgAndWritesTheQualifiedKey pins the ONE place the two
// halves of the key legitimately differ. The source planes carry the bare IAM
// slug because they were written before this plane existed; this plane's own
// index is qualified. Confusing the two in either direction is a cross-tenant
// read, so the direction is asserted rather than commented.
func TestRollup_ReadsTheBareOrgAndWritesTheQualifiedKey(t *testing.T) {
	probe.reset(true)
	k := key(t, brandA, orgA)
	end := time.Now().UTC()
	if err := ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, r := range rollups {
		if err := rollup(context.Background(), k, r, end.Add(-time.Hour), end); err != nil {
			t.Fatalf("rollup %q: %v", r.Name, err)
		}
	}
	var folds int
	for _, s := range probe.all() {
		if !strings.HasPrefix(strings.TrimSpace(s.SQL), "INSERT INTO hanzo.risk_feature") {
			continue
		}
		folds++
		if len(s.Args) < 2 {
			t.Fatalf("rollup bound %d args:\n%s", len(s.Args), s.SQL)
		}
		if s.Args[0] != string(k) {
			t.Errorf("rollup WRITES %v, want the qualified key %q", s.Args[0], string(k))
		}
		if s.Args[1] != orgA {
			t.Errorf("rollup READS %v, want the bare org %q", s.Args[1], orgA)
		}
		if strings.Contains(s.SQL, orgA) || strings.Contains(s.SQL, string(k)) {
			t.Errorf("a tenant value is interpolated into the rollup:\n%s", s.SQL)
		}
	}
	if folds != len(rollups) {
		t.Fatalf("%d rollup statements ran, want %d", folds, len(rollups))
	}
}

// TestRollup_ClosesTheLoop is THE MOAT, end to end and in one test: an
// organisation emits into a source plane, the rollup folds that window into its
// own feature surface, and the model reads it back as the organisation's own
// history.
//
// It exists because the rollup was declared, tested in isolation, and CALLED BY
// NOTHING — so the per-org feature surface was a table that was never written,
// the warm read it and found nothing, and the whole moat was prose. A unit test
// of `rollup` passes in exactly that world. This one does not.
//
// Mutation proof: remove the p.roll call from plane.fold and the read below comes
// back empty.
func TestRollup_ClosesTheLoop(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()
	// The organisation emits into the SOURCE planes, under its bare slug, the way
	// the one ingest door writes them.
	for i := 0; i < 12; i++ {
		probe.emit(orgA, emitted{
			Plane: "account", Subject: "u_7",
			At: now.Add(-time.Duration(i+1) * time.Hour), Spend: 250_000_000,
		})
	}
	// A DIFFERENT organisation emits too, so "the loop closes" cannot be satisfied
	// by folding everybody's rows into everybody's surface.
	probe.emit(orgB, emitted{Plane: "account", Subject: "u_7", At: now.Add(-time.Hour), Spend: 999_000_000})

	f := p.fold(context.Background(), k)
	if f.Rolled == 0 {
		t.Fatalf("the fold rolled no window at all: %+v", f)
	}
	if f.Folded == 0 {
		t.Fatalf("the fold read nothing back after rolling %d window(s): %s", f.Rolled, f.Gap)
	}
	// Read the surface the way the catalogue does, and check WHOSE rows arrived.
	rs, err := rows(context.Background(), k, query{start: now.Add(-warmWindow), end: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("read the feature surface: %v", err)
	}
	if len(rs) == 0 {
		t.Fatal("the organisation's own events never became features — the loop does not close")
	}
	var spend float64
	for _, r := range rs {
		if r.Subject != "u_7" || r.Kind != kindAccount {
			t.Fatalf("a foreign row reached this organisation's surface: %+v", r)
		}
		spend += r.Value["spend"]
	}
	if spend != float64(len(rs))*250_000_000 {
		t.Fatalf("the folded spend is %.0f over %d rows — another organisation's numbers are in it", spend, len(rs))
	}
	// And the other organisation's surface holds only its own.
	other, err := rows(context.Background(), key(t, brandA, orgB), query{start: now.Add(-warmWindow), end: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("read organisation B's surface: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("organisation B's surface has %d rows without B ever being folded", len(other))
	}
}

// TestRollup_RollsEachWindowOnce: the feature surface is a SummingMergeTree, so
// re-inserting a window that already landed does not replace it — the rows merge
// by addition and the organisation's own history doubles. Idempotency cannot come
// from the engine, so it comes from a durable per-tenant watermark.
//
// Mutation proof: stop advancing the watermark in plane.roll and the second fold
// doubles every number.
func TestRollup_RollsEachWindowOnce(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	p := planeAt(t, dir)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		probe.emit(orgA, emitted{Plane: "account", Subject: "u_1", At: now.Add(-time.Duration(i+1) * time.Hour), Spend: 1_000})
	}
	if _, err := p.roll(context.Background(), k); err != nil {
		t.Fatalf("roll: %v", err)
	}
	once := probe.rowsFor(string(k))
	if once == 0 {
		t.Fatal("the first roll wrote nothing — the test proves nothing")
	}
	if _, err := p.roll(context.Background(), k); err != nil {
		t.Fatalf("second roll: %v", err)
	}
	if twice := probe.rowsFor(string(k)); twice != once {
		t.Fatalf("rolling twice wrote %d rows where one roll wrote %d — every number in this organisation's "+
			"history is now inflated, and a SummingMergeTree will never converge back", twice, once)
	}
	// The watermark is DURABLE, so a rollout does not re-roll what already landed.
	if err := p.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	if _, err := second.roll(context.Background(), k); err != nil {
		t.Fatalf("roll after restart: %v", err)
	}
	if after := probe.rowsFor(string(k)); after != once {
		t.Fatalf("a restart re-rolled windows that had already landed: %d rows, want %d", after, once)
	}
}

// TestFeatureSurface_IsTheOnlyDatastoreDoor is the TYPE-level argument, enforced
// by reading the package's own source.
//
// The claim "there is no path from this package to the warehouse that does not
// carry a tenant" is only true while the warehouse is reachable from one file. A
// second importer somewhere else would be a second place to forget the predicate,
// and nothing else in the suite would notice.
func TestFeatureSurface_IsTheOnlyDatastoreDoor(t *testing.T) {
	const door = "feature.go"
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "cloud/apps/datastore") && name != door {
				offenders = append(offenders, name)
			}
		}
		// The feature and baseline tables must be NAMED in one place too: a
		// statement composed elsewhere is a statement outside the predicate.
		if name != door && name != "baseline.go" {
			if bytesContain(src, featureTable) || bytesContain(src, baselineTable) {
				offenders = append(offenders, name+" (names a table directly)")
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("the warehouse is reachable from %s — it must be reachable only from %s, "+
			"because that is the file every read takes a tenant in", strings.Join(offenders, ", "), door)
	}
	// And the door really is a door: feature.go must import it, or the assertion
	// above is vacuously true.
	src, err := os.ReadFile(door)
	if err != nil {
		t.Fatalf("read %s: %v", door, err)
	}
	f, err := parser.ParseFile(fset, door, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", door, err)
	}
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		if imp, ok := n.(*ast.ImportSpec); ok && strings.Contains(imp.Path.Value, "cloud/apps/datastore") {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("%s does not import the warehouse — the one-door assertion is vacuous", door)
	}
}

func bytesContain(src []byte, sub string) bool { return strings.Contains(string(src), sub) }

// TestFeatureRead_FailsClosedWithoutTheWarehouse: an unreachable warehouse is an
// honest gap, never zeroes. A model that learns from a fabricated absence of
// activity has learned that the organisation is quiet.
func TestFeatureRead_FailsClosedWithoutTheWarehouse(t *testing.T) {
	probe.reset(false)
	defer probe.reset(true)
	end := time.Now().UTC()
	if _, err := rows(context.Background(), key(t, brandA, orgA), query{start: end.Add(-time.Hour), end: end}); err == nil {
		t.Fatal("a read against an unreachable warehouse returned no error — an empty surface and an absent one are different facts")
	}
}

// TestDictionary_ReportsBlindDimensionsHonestly: a dimension this organisation's
// surface does not carry is BLIND, not zero. The model reads its neutral value
// there, and a reviewer has to be able to tell that apart from an absence of risk.
func TestDictionary_ReportsBlindDimensionsHonestly(t *testing.T) {
	probe.reset(true)
	k := key(t, brandA, orgA)
	probe.hold(string(k), map[string]any{
		"subject_kind": kindPerson, "subject": "p_1", "bucket": surfaceAt(1),
		"events": uint32(4), "sessions": uint32(1),
	})
	end := time.Now().UTC()
	cov, err := dictionary(context.Background(), k, end.Add(-time.Hour), end)
	if err != nil {
		t.Fatalf("dictionary: %v", err)
	}
	seen := map[string]coverage{}
	for _, c := range cov {
		seen[c.Dim.Name] = c
	}
	if c := seen["events"]; c.Blind() || c.Present != 1 || c.Mean != 4 {
		t.Errorf("events read %+v, want present with mean 4", c)
	}
	if c := seen["spend"]; !c.Blind() {
		t.Errorf("spend read %+v, want BLIND — this organisation's surface carries no metered spend", c)
	}
}

// TestRollup_NeverSplitsABucket: the surface is written at five-minute buckets
// (toStartOfFiveMinute) and merged by ADDITION, so a window that ends inside a
// bucket is not merely inefficient — it inflates every uniqExact column. A
// subject active either side of the cut contributes `1` twice, and the surface
// then reports two distinct addresses, sessions or paths where there was one.
// Those columns are model inputs, so the model trains on numbers no traffic
// produced.
//
// The roll therefore aligns its edge DOWN to the bucket, which makes every
// watermark aligned too. This drives it at three deliberately unaligned wall
// clocks and holds every boundary that reached the warehouse.
//
// Mutation proof: drop the .Truncate(featureBucket) in plane.roll and the
// boundaries below land mid-bucket.
func TestRollup_NeverSplitsABucket(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	p := planeAt(t, dir)
	defer func() { _ = p.close() }()
	k := key(t, brandA, orgA)
	// The only rolls that reach the warehouse are the three this test drives — and
	// so the clock below is read by one goroutine.
	holdFolds(t, p)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{0, 7*time.Minute + 13*time.Second, 63*time.Minute + 41*time.Second} {
		at := base.Add(off)
		p.now = func() time.Time { return at }
		if _, err := p.roll(context.Background(), k); err != nil {
			t.Fatalf("roll at %s: %v", at, err)
		}
	}
	var checked int
	for _, s := range probe.all() {
		if !strings.HasPrefix(strings.TrimSpace(s.SQL), "INSERT") || len(s.Args) < 4 {
			continue
		}
		for _, i := range []int{2, 3} {
			lit, ok := s.Args[i].(string)
			if !ok {
				t.Fatalf("a rollup window boundary is %T, not the literal the statement binds", s.Args[i])
			}
			at, err := time.Parse("2006-01-02 15:04:05", lit)
			if err != nil {
				t.Fatalf("boundary %q does not parse: %v", lit, err)
			}
			if !at.Equal(at.Truncate(featureBucket)) {
				t.Fatalf("a rollup covered [%v] — the window ends inside a %s bucket, so every uniqExact "+
					"column in that bucket is counted once per partial insert", s.Args[2:4], featureBucket)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no rollup statement reached the warehouse — the test proves nothing")
	}
}

// TestSearch_RollsBeforeItReadsItsHistory: the fold that brings a tenant's
// surface current runs in the BACKGROUND, so a search that only waited for
// residency would race it and answer "your history is empty" on first use — the
// one answer a search must never give wrongly, because an empty history and a
// clean history are the same 404 to a caller.
//
// Mutation proof: remove the p.roll call at the top of plane.begin and this comes
// back "history over the window is empty".
func TestSearch_RollsBeforeItReadsItsHistory(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()
	// Only the SOURCE plane has anything. Nothing has rolled, and nothing will
	// unless begin does it.
	for i := 0; i < 24; i++ {
		probe.emit(orgA, emitted{
			Plane: "account", Subject: "u_" + itoa(i%3),
			At: now.Add(-time.Duration(i+1) * 20 * time.Minute), Spend: 100_000_000,
		})
	}
	run, err := p.begin(context.Background(), k, 24*time.Hour, nil, nil)
	if err != nil {
		t.Fatalf("the first search over an unrolled surface: %v", err)
	}
	if run.Events == 0 {
		t.Fatal("the search admitted a run over zero events")
	}
}
