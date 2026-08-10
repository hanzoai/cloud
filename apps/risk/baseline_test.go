package risk

// baseline_test.go — THE PROOF that the aggregate path cannot reconstruct one
// organisation's rows.
//
// The cross-org surface is one table. These tests hold it to the three
// properties that make it safe, in the order they matter:
//
//  1. It has NO TENANT COLUMN — asserted against both the Go type and the DDL, so
//     there is no field to recover an organisation from however the read is
//     written. This is the property that makes the leak uncomputable.
//  2. It refuses a bucket below k-anonymity, at BOTH enforcement points.
//  3. The anonymous lane contributes nothing, and the refusal is at the MINT, so
//     there is no filter to forget.

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// forbidden is every word that could name, or help name, an organisation or a
// person. A baseline column matching any of these is a tenant column wearing
// another word, and the whole argument rests on there being none.
var forbidden = []string{
	"org", "organisation", "organization", "tenant", "brand", "owner", "account",
	"subject", "distinct", "person", "user", "session", "device", "ip", "email",
	"id", "hash", "pseudonym", "token", "fingerprint", "customer", "name",
}

// TestBaseline_HasNoTenantColumn reflects over the published row AND parses the
// DDL. Both, because they are two artifacts and only one of them is compiled: a
// column added to the DDL alone would be invisible to reflection, and a field
// added to the struct alone would be invisible to the schema.
//
// Mutation proof: add `org String` to baselineDDL, or an Org field to band, and
// this fails.
func TestBaseline_HasNoTenantColumn(t *testing.T) {
	rt := reflect.TypeFor[band]()
	for field := range rt.Fields() {
		check(t, "band."+field.Name, field.Name)
	}
	for _, col := range ddlColumns(t, baselineDDL) {
		check(t, baselineTable+"."+col, col)
	}
	// ...and the shape really is the aggregate it claims: three quantiles and the
	// two counts that prove the bucket is anonymous, keyed by day, kind and dim.
	want := []string{"bucket", "subject_kind", "dim", "q10", "q50", "q90", "orgs", "n"}
	if got := ddlColumns(t, baselineDDL); !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline columns are %v, want exactly %v", got, want)
	}
}

// enums are the names that CONTAIN a forbidden word and are nevertheless safe,
// each because it is a bounded set of constants rather than an identifier. The
// list is closed and every entry is separately argued for below, so an exemption
// is a decision and not a hole.
var enums = map[string]bool{
	// subject_kind is one of three package constants (person, session, account).
	// It says what SORT of thing a quantile is over, never which one.
	"subject_kind": true,
	// orgs is a COUNT of organisations, and counting them is exactly what
	// k-anonymity is measured in.
	"orgs": true,
	"Orgs": true,
	"Kind": true,
}

// check fails when a name contains a forbidden word as a WORD. The match is on
// identifier segments — camelCase and snake_case both — so a substring like the
// `n` in `n` or the `or` in `order` cannot trip it and a real `org` cannot hide.
func check(t *testing.T, where, name string) {
	t.Helper()
	if enums[name] {
		return
	}
	for _, part := range segments(name) {
		for _, bad := range forbidden {
			if part == bad {
				t.Errorf("%s names %q — the network baseline must carry no tenant-recoverable column", where, bad)
			}
		}
	}
}

// segments splits an identifier into lower-cased words across both conventions.
func segments(name string) []string {
	var out []string
	cur := strings.Builder{}
	for _, r := range name {
		switch {
		case r == '_' || r == ' ':
			if cur.Len() > 0 {
				out = append(out, strings.ToLower(cur.String()))
				cur.Reset()
			}
		case r >= 'A' && r <= 'Z':
			if cur.Len() > 0 {
				out = append(out, strings.ToLower(cur.String()))
				cur.Reset()
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, strings.ToLower(cur.String()))
	}
	return out
}

var ddlCol = regexp.MustCompile(`(?m)^\s{2,}([a-z_][a-z0-9_]*)\s+[A-Za-z(]`)

func ddlColumns(t *testing.T, ddl string) []string {
	t.Helper()
	body := ddl
	if i := strings.Index(ddl, "("); i >= 0 {
		body = ddl[i:]
	}
	if i := strings.Index(body, ") ENGINE"); i >= 0 {
		body = body[:i]
	}
	var out []string
	for _, m := range ddlCol.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("parsed no columns out of the DDL — the assertion would be vacuous:\n%s", ddl)
	}
	return out
}

// TestBaseline_SubjectKindIsAnEnumNotAnIdentifier is the one exempted column,
// argued rather than waved through: the baseline is grouped BY the KIND of
// subject — one of three package constants — and never by a subject.
//
// The distinction is in the statement's two levels. The INNER select groups by
// (day, kind, org, subject) because a quantile has to be over comparable things:
// one number per subject per day. The OUTER select — the one that becomes a
// published row — groups by (day, kind) only, and the only thing it does with an
// organisation is COUNT how many contributed.
func TestBaseline_SubjectKindIsAnEnumNotAnIdentifier(t *testing.T) {
	if len(kinds) > 8 {
		t.Fatalf("subject_kind has grown to %d values — at some point an enum becomes an identifier", len(kinds))
	}
	stmt := populate(dims[0])
	inner, outer, ok := strings.Cut(stmt, "\t\t)")
	if !ok {
		t.Fatalf("the populate statement has no inner select to separate:\n%s", stmt)
	}
	if !strings.Contains(inner, "GROUP BY b, subject_kind, org, subject") {
		t.Errorf("the inner grouping is not (day, kind, org, subject):\n%s", inner)
	}
	if !strings.Contains(outer, "GROUP BY b, subject_kind") {
		t.Errorf("the published grouping is not (day, kind):\n%s", outer)
	}
	if strings.Contains(outer, "subject,") || strings.Contains(outer, ", subject ") {
		t.Errorf("the published grouping reaches a subject:\n%s", outer)
	}
	// The published projection may touch an organisation for exactly one purpose:
	// counting how many contributed. Every bare `org` in it must be inside a
	// uniqExact — `orgs`, the destination COLUMN that holds that count, is a
	// different token and is deliberately not matched.
	head := stmt[:strings.Index(stmt, "FROM (")]
	for _, m := range regexp.MustCompile(`uniqExact\(org\)|\borg\b`).FindAllString(head, -1) {
		if m != "uniqExact(org)" {
			t.Errorf("the published projection names an organisation outside a count:\n%s", head)
		}
	}
}

// TestBaseline_RefusesBelowKAnon drives the gate directly at its boundary — 24
// organisations is refused, 25 is published — and then proves the SAME predicate
// is what the reader applies, so a row written before the gate existed is still
// refused on the way out.
func TestBaseline_RefusesBelowKAnon(t *testing.T) {
	for _, tc := range []struct {
		orgs uint32
		n    uint64
		want bool
	}{
		{kAnonOrgs - 1, kAnonRows, false}, // one organisation short
		{kAnonOrgs, kAnonRows - 1, false}, // one bucket short
		{kAnonOrgs, kAnonRows, true},      // exactly at the floor
		{1, 1 << 40, false},               // one organisation, however much of it
		{0, 0, false},
	} {
		if got := publishable(tc.orgs, tc.n); got != tc.want {
			t.Errorf("publishable(%d orgs, %d rows) = %v, want %v", tc.orgs, tc.n, got, tc.want)
		}
	}

	// The reader drops what the writer should never have written.
	probe.reset(true)
	rows := []map[string]any{
		{"bucket": time.Now().UTC(), "subject_kind": kindAccount, "dim": "events",
			"q10": 1.0, "q50": 2.0, "q90": 3.0,
			"orgs": uint32(kAnonOrgs - 1), "n": uint64(kAnonRows)},
		{"bucket": time.Now().UTC(), "subject_kind": kindAccount, "dim": "events",
			"q10": 1.0, "q50": 2.0, "q90": 3.0,
			"orgs": uint32(kAnonOrgs), "n": uint64(kAnonRows)},
	}
	probe.holdBands(rows...)
	end := time.Now().UTC()
	got, err := baseline(context.Background(), "", end.Add(-24*time.Hour), end)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d band(s), want 1 — the sub-k-anonymous one must be dropped on read", len(got))
	}
	if got[0].Orgs != kAnonOrgs {
		t.Fatalf("the published band has %d organisations, want %d", got[0].Orgs, kAnonOrgs)
	}
}

// TestBaseline_TheFloorIsBoundIntoTheStatement proves the gate is also enforced
// where the row is WRITTEN, and that the floor arrives as a bound value rather
// than a number pasted into the text.
func TestBaseline_TheFloorIsBoundIntoTheStatement(t *testing.T) {
	probe.reset(true)
	end := time.Now().UTC()
	if _, err := recompute(context.Background(), end.Add(-24*time.Hour), end); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	var wrote int
	for _, s := range probe.all() {
		if !strings.HasPrefix(strings.TrimSpace(s.SQL), "INSERT INTO "+baselineTable) {
			continue
		}
		wrote++
		if !strings.Contains(s.SQL, "HAVING uniqExact(org) >= ? AND sum(subjects) >= ?") {
			t.Errorf("the populate statement carries no k-anonymity floor:\n%s", s.SQL)
		}
		if len(s.Args) != 5 {
			t.Fatalf("populate bound %d args, want 5 (dim, start, end, orgs, rows):\n%s", len(s.Args), s.SQL)
		}
		if s.Args[3] != kAnonOrgs || s.Args[4] != kAnonRows {
			t.Errorf("populate bound the floor as %v/%v, want %d/%d", s.Args[3], s.Args[4], kAnonOrgs, kAnonRows)
		}
	}
	if wrote != len(dims) {
		t.Fatalf("recompute wrote %d dimension(s), want %d", wrote, len(dims))
	}
}

// TestBaseline_NoOrganisationCanDominate is the k-anonymity defect stated as an
// experiment, and it is a defect about WEIGHT rather than about counting.
//
// The floor asked for twenty-five contributors and a thousand rows. One tenant
// with ten thousand subjects and twenty-four with one each satisfies both — and
// then the published "network" median is that one tenant's median, its own
// distribution republished under a name that says it is everyone's. Any
// competitor of theirs can read it.
//
// One organisation, one vote fixes it, and the fix is measurable: the same
// contributions reduced per organisation FIRST give the quiet majority's answer,
// and each contributor's share is 1/orgs ≤ [maxShare].
//
// Mutation proof: pool the subjects instead of voting (the commented line) and
// the assertion below reports the dominant tenant's own number.
func TestBaseline_NoOrganisationCanDominate(t *testing.T) {
	const dominant, quiet = 10_000.0, 1.0
	contributions := map[string][]float64{}
	contributions["whale"] = make([]float64, 10_000)
	for i := range contributions["whale"] {
		contributions["whale"][i] = dominant
	}
	for i := range kAnonOrgs - 1 {
		contributions["small_"+itoa(i)] = []float64{quiet}
	}

	// What the published band is computed over: ONE value per organisation.
	votes := make([]float64, 0, len(contributions))
	var pooled []float64 // what it used to be: every subject, unweighted
	for _, subjects := range contributions {
		votes = append(votes, vote(subjects))
		pooled = append(pooled, subjects...)
	}
	if got := vote(votes); got != quiet {
		t.Fatalf("the published median is %.0f — one organisation with %d of the %d subjects moved it. "+
			"Counting contributors is not bounding weight.", got, len(contributions["whale"]), len(pooled))
	}
	// The control: without the per-organisation reduction the same data publishes
	// the whale's own number, which is the defect this test refutes.
	if got := vote(pooled); got != dominant {
		t.Fatalf("the unweighted median is %.0f, want %.0f — the control does not reproduce the defect, "+
			"so the assertion above proves nothing", got, dominant)
	}
	if share := 1.0 / float64(len(votes)); share > maxShare {
		t.Fatalf("one contributor holds %.3f of the band, above the stated ceiling %.3f", share, maxShare)
	}
}

// TestBaseline_TheStatementVotesPerOrganisation holds the SQL to the reduction
// [vote] models. Without the middle stage the quantiles are a weighted average in
// which the largest tenant is the answer, and no amount of prose about
// k-anonymity changes that.
//
// Mutation proof: delete the `GROUP BY b, subject_kind, org` stage from populate
// and this fails.
func TestBaseline_TheStatementVotesPerOrganisation(t *testing.T) {
	for _, d := range dims {
		stmt := populate(d)
		// One value per subject, then one per organisation, then the quantile.
		perSubject := strings.Index(stmt, "GROUP BY b, subject_kind, org, subject")
		perOrg := strings.Index(stmt, "GROUP BY b, subject_kind, org\n")
		perDay := strings.Index(stmt, "GROUP BY b, subject_kind\n")
		if perSubject < 0 || perOrg < 0 || perDay < 0 {
			t.Fatalf("populate(%q) is missing one of the three reduction stages:\n%s", d.Name, stmt)
		}
		if !(perSubject < perOrg && perOrg < perDay) {
			t.Fatalf("populate(%q) reduces in the wrong order — the organisation stage must sit between "+
				"the subject stage and the quantile:\n%s", d.Name, stmt)
		}
		// The quantile reads the per-organisation value, never the per-subject one.
		if !strings.Contains(stmt, "quantile(0.50)(x)") || !strings.Contains(stmt, "quantileExact(0.50)(sx) AS x") {
			t.Fatalf("populate(%q) does not take the published quantile over one value per organisation:\n%s", d.Name, stmt)
		}
		// AND THE PUBLISHED QUANTILE IS NOT AN ELEMENT. quantileExact over the org
		// votes selects one of them, so at the k-anonymity floor every published
		// figure IS one contributing organisation's own daily median. The outer
		// estimator must interpolate; the inner one, inside a single organisation,
		// has nothing to disclose and stays exact.
		if strings.Contains(stmt, "quantileExact(0.10)(x)") ||
			strings.Contains(stmt, "quantileExact(0.50)(x)") ||
			strings.Contains(stmt, "quantileExact(0.90)(x)") {
			t.Fatalf("populate(%q) publishes an ELEMENT-EXACT quantile over organisation votes — at the %d-org "+
				"floor that is one organisation's own median, published verbatim:\n%s", d.Name, kAnonOrgs, stmt)
		}
		// And no extreme level, which is the maximum however it is estimated.
		if strings.Contains(stmt, "0.99") {
			t.Fatalf("populate(%q) publishes a 99th percentile over %d votes, which is the largest "+
				"contributor's own value:\n%s", d.Name, kAnonOrgs, stmt)
		}
	}
}

// TestBaseline_StatementIsConstant: nothing a caller sends can reach the
// statement as an identifier. The only thing composed into it is a column name
// from [dims], which is code; the dimension's published name arrives BOUND.
func TestBaseline_StatementIsConstant(t *testing.T) {
	// The allowlist is DERIVED, not restated: the two tables this package owns,
	// their own columns as their own DDL declares them, and the feature columns
	// from [dims]. Anything else appearing in a statement was composed in from
	// somewhere, which is exactly what must not be possible.
	allowed := map[string]bool{baselineTable: true, featureTable: true}
	for _, c := range ddlColumns(t, baselineDDL) {
		allowed[c] = true
	}
	for _, c := range ddlColumns(t, featureDDL) {
		allowed[c] = true
	}
	for _, d := range dims {
		allowed[d.Column] = true
	}
	for _, d := range dims {
		stmt := populate(d)
		// Every identifier-shaped token that is not SQL, not a quantile literal and
		// not an allowlisted column would be something composed in from elsewhere.
		for _, raw := range regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.]*`).FindAllString(stmt, -1) {
			tok := strings.ToLower(raw)
			if allowed[raw] || sqlWord[tok] {
				continue
			}
			t.Errorf("populate(%q) composes an unexpected identifier %q — the statement must be a constant over the allowlist:\n%s", d.Name, tok, stmt)
		}
	}
	// A dimension that is not in the allowlist cannot be named at all: the read
	// resolves through dimBy and refuses.
	probe.reset(true)
	end := time.Now().UTC()
	if _, err := baseline(context.Background(), "'; DROP TABLE hanzo.risk_baseline; --", end.Add(-time.Hour), end); err == nil {
		t.Fatal("the baseline read accepted a dimension that is not in the allowlist")
	}
}

// sqlWord is the vocabulary the constant statements are written in. It is here
// rather than in a linter because the assertion above needs to distinguish "SQL"
// from "something this package composed in".
var sqlWord = map[string]bool{
	"insert": true, "into": true, "select": true, "from": true, "where": true,
	"group": true, "by": true, "having": true, "order": true, "limit": true, "and": true,
	"as": true, "sum": true, "count": true, "uniqexact": true, "todate": true,
	"quantileexact": true, "quantile": true, "b": true, "x": true, "bucket": true, "subject": true,
	// The aliases of the three-stage reduction: `sx` is one subject's day, `x` the
	// organisation's single vote over its subjects, `subjects` how many went into
	// it. They are code in the same way `b` and `x` are.
	"sx": true, "subjects": true,
	"subject_kind": true, "org": true, "dim": true, "q10": true, "q50": true,
	"q90": true, "n": true, "hanzo": true, "risk_baseline": true,
	"risk_feature": true, "tostartoffiveminute": true, "time": true,
}

// TestBaseline_ExcludesTheAnonymousLane proves the reserved anonymous tenant
// contributes to NEITHER the feature surface NOR the baseline — and that the
// exclusion is at the MINT rather than in a filter.
//
// A filter is a place to forget. A refusal at the only constructor is not: there
// is no tenant key for the anonymous lane, so there is no rollup that could write
// its rows and therefore no quantile it could move.
func TestBaseline_ExcludesTheAnonymousLane(t *testing.T) {
	for _, brandID := range []string{brandA, brandB} {
		if _, err := qualify(brandID, public); err == nil {
			t.Fatalf("qualify(%q, %q) minted a tenant for the anonymous lane", brandID, public)
		}
	}
	// There is no way to reach a read with it either: an unqualified key is
	// refused, and the only qualified keys are the ones qualify produced.
	probe.reset(true)
	end := time.Now().UTC()
	for _, bad := range []tenant{tenant(public), tenant(brandA + sep + public)} {
		if bad.qualified() {
			t.Fatalf("%q passes the qualified() shape check", string(bad))
		}
		if _, err := rows(context.Background(), bad, query{start: end.Add(-time.Hour), end: end}); err == nil {
			t.Errorf("a feature read accepted %q", string(bad))
		}
		if err := rollup(context.Background(), bad, rollups[0], end.Add(-time.Hour), end); err == nil {
			t.Errorf("a rollup accepted %q", string(bad))
		}
	}
	// And nothing the baseline job runs ever names it.
	if _, err := recompute(context.Background(), end.Add(-24*time.Hour), end); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	for _, s := range probe.all() {
		if strings.Contains(s.SQL, public) {
			t.Errorf("a statement names the anonymous lane:\n%s", s.SQL)
		}
	}
}

// TestBaseline_TakesNoTenant is the signature-level statement of the whole
// argument: the cross-org read has no parameter a tenant could be passed in, so
// no caller — including a future one inside this package — can narrow it to one
// organisation and read that organisation back out of the aggregate.
func TestBaseline_TakesNoTenant(t *testing.T) {
	rt := reflect.TypeOf(baseline)
	for in := range rt.Ins() {
		if in == reflect.TypeFor[tenant]() {
			t.Fatal("baseline() takes a tenant — the aggregate must not be narrowable to one organisation")
		}
	}
	rt = reflect.TypeOf(recompute)
	for in := range rt.Ins() {
		if in == reflect.TypeFor[tenant]() {
			t.Fatal("recompute() takes a tenant — the aggregate must not be computed over one organisation")
		}
	}
}

// TestBaseline_TheScheduleOnlyPublishesCompleteDays: a quantile over a PARTIAL
// day is a quantile over whoever happened to be awake, and it would be
// republished the next day with a different answer. The job therefore works a
// whole day behind, on a day boundary, and never touches the present.
func TestBaseline_TheScheduleOnlyPublishesCompleteDays(t *testing.T) {
	probe.reset(true)
	run(context.Background(), luxlog.New("risktest"))

	var windows int
	now := time.Now().UTC()
	for _, s := range probe.all() {
		if !strings.HasPrefix(strings.TrimSpace(s.SQL), "INSERT INTO "+baselineTable) {
			continue
		}
		windows++
		start, end := s.Args[1].(string), s.Args[2].(string)
		from, err := time.Parse("2006-01-02 15:04:05", start)
		if err != nil {
			t.Fatalf("parse %q: %v", start, err)
		}
		to, err := time.Parse("2006-01-02 15:04:05", end)
		if err != nil {
			t.Fatalf("parse %q: %v", end, err)
		}
		if !to.Before(now.Add(-baselineLag).Add(time.Second)) {
			t.Errorf("the job published up to %s, which is inside the lag it keeps from the present", to)
		}
		if to.Sub(from) != baselineEvery {
			t.Errorf("the job published a %s window, want exactly %s", to.Sub(from), baselineEvery)
		}
		if !to.Equal(to.Truncate(24 * time.Hour)) {
			t.Errorf("the job published up to %s, which is not a day boundary", to)
		}
	}
	if windows != len(dims) {
		t.Fatalf("the job wrote %d dimension(s), want %d", windows, len(dims))
	}
}

// TestBaseline_ThePublishedTableHasAReader: the daily recompute writes
// hanzo.risk_baseline whether or not anything reads it, and for a while nothing
// did — a warehouse cost paid every day and collected on never. That is the same
// "declared but unwired" defect as a reader with no writer, inverted, and it is
// invisible from either half on its own.
//
// The reader is the catalogue's NETWORK lens, and it carries no tenant: the same
// bands go to every caller, drawn from a table whose shape has no org column.
//
// Mutation proof: drop the `out.Network, err = bands(...)` line from ops.features
// and the published table goes back to having no reader.
func TestBaseline_ThePublishedTableHasAReader(t *testing.T) {
	probe.reset(true)
	day := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	// Two bands, one of which the k-anonymity floor must drop on the way out — so
	// this proves the READER applies the floor too, not only that it reads.
	probe.holdBands(
		map[string]any{"bucket": day, "subject_kind": kindAccount, "dim": "events",
			"q10": 1.0, "q50": 7.0, "q90": 9.0, "orgs": uint32(kAnonOrgs), "n": uint64(kAnonRows)},
		map[string]any{"bucket": day, "subject_kind": kindAccount, "dim": "spend",
			"q10": 1.0, "q50": 2.0, "q90": 3.0, "orgs": uint32(kAnonOrgs - 1), "n": uint64(kAnonRows)},
	)

	app := mountBilled(t, &ledger{available: 100_000_000})
	code, out := req(t, app, http.MethodGet, "/v1/risk/features?days=400", orgA, "u_"+orgA, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/features?days=400 = %d %s", code, out)
	}
	var cat riskCatalog
	if err := json.Unmarshal(out, &cat); err != nil {
		t.Fatalf("decode catalogue: %v", err)
	}
	if len(cat.Network) != 1 {
		t.Fatalf("the catalogue published %d network band(s), want 1 — the baseline is written "+
			"every day and read by nobody: %s", len(cat.Network), out)
	}
	if b := cat.Network[0]; b.Dim != "events" || b.Q50 != 7 || b.Orgs != kAnonOrgs {
		t.Fatalf("the published band is %+v, want the events band over exactly the floor", b)
	}
	// ...and it names no organisation, which is the whole shape of the table.
	if strings.Contains(string(out), orgA) && !strings.Contains(string(out), `"tenant"`) {
		t.Fatalf("the network lens leaked an organisation: %s", out)
	}
}

// TestBaseline_TheScheduleIsNotReachableFromARequest: there is no route to the
// job, and that is a security property rather than a missing feature. A caller
// who could choose the window could choose one only their own organisation was
// active in, and read their own rows back out of a k-anonymous aggregate.
//
// The published table's READER is a different thing and is allowed: it returns
// whole days the schedule already computed and re-applies the floor on the way
// out, so a window choice selects which published bands come back and never what
// went into one.
func TestBaseline_TheScheduleIsNotReachableFromARequest(t *testing.T) {
	served, _, _ := riskOps(t)
	for op := range served {
		if strings.Contains(strings.ToLower(op), "baseline") || strings.Contains(strings.ToLower(op), "network") {
			t.Errorf("%s reaches the network baseline from a request", op)
		}
	}
}
