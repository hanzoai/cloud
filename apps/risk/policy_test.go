package risk

// policy_test.go — the decision regime is durable on its own terms, versioned,
// cited by every score, per tenant, append-only and bounded in bytes.
//
// Every test here names the defect it would catch and how to reintroduce it. Two
// of them go through the WIRE rather than the plane, deliberately: a property
// proved only against a plane method survives someone deleting the op that reaches
// it, and a surface nobody reaches is not a surface.

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestPolicy_GoingLiveSurvivesTheRollout is the headline: the defect this record
// exists to fix.
//
// An organisation takes its model out of shadow BEFORE the model has learned
// anything. The regime used to live on the same row as the learned state, whose
// writer correctly declines to write when there is no learned mass — so the call
// answered 200, reported live, and wrote nothing. This binary deploys Recreate at
// ONE replica, so the next rollout rebuilt from the default regime (shadow) and the
// model decided nothing. No error, no log, nothing to alert on: a model silently
// disarmed, which is the one state that must never be reachable quietly.
//
// Mutation proof: in [plane.appetite], drop the p.enact call and restore the old
// `p.persist(r)`-only ending — this fails with the tenant back in shadow. Or in
// [plane.restoreRegime], return before applying rec — same failure.
func TestPolicy_GoingLiveSurvivesTheRollout(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)

	p1, err := newPlane(baseAt(t, dir))
	if err != nil {
		t.Fatalf("newPlane: %v", err)
	}
	holdFolds(t, p1)
	ver1, err := p1.appetite(k, 0.02, 0.10, true /* live */, "u_"+orgA)
	if err != nil {
		t.Fatalf("appetite: %v", err)
	}
	if st, _, err := p1.state(k); err != nil {
		t.Fatalf("state: %v", err)
	} else if st.Config.Shadow {
		t.Fatalf("the call did not take the model live at all: shadow=%v", st.Config.Shadow)
	}
	if ver1 != 1 {
		t.Fatalf("the change reported version %d, want the 1 it enacted", ver1)
	}
	if err := p1.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The rollout: a fresh process over the SAME data directory, with NOTHING
	// learned — the exact state the old writer refused to record.
	p2, err := newPlane(baseAt(t, dir))
	if err != nil {
		t.Fatalf("newPlane after rollout: %v", err)
	}
	holdFolds(t, p2)
	t.Cleanup(func() { _ = p2.close(context.Background()) })
	after, _, err := p2.state(k)
	if err != nil {
		t.Fatalf("state after rollout: %v", err)
	}
	if after.Config.Shadow {
		t.Fatalf("the organisation is back in SHADOW after a rollout: it stated live=true, was told " +
			"live=true, and its model now decides nothing")
	}
	if after.Config.Appetite.Review != 0.02 {
		t.Fatalf("the stated appetite did not survive the rollout: stated 0.02, in force %.4f",
			after.Config.Appetite.Review)
	}
	ver, err := p2.regimeNow(k)
	if err != nil {
		t.Fatalf("regimeNow: %v", err)
	}
	if ver != 1 {
		t.Fatalf("the regime in force after the rollout is version %d, want 1", ver)
	}
}

// TestPolicy_EveryScoreCitesTheRegimeItWasDecidedUnder: a score is defensible
// only against the regime that produced its cut, so the version travels with the
// verdict — over the WIRE, which is where a caller defending a decision reads it.
//
// Mutation proof: drop `Policy: d.Version` from [verdict] and both citations read
// 0; drop `r.pol = rec.Version` from [plane.appetite] and the second citation
// stays 1.
func TestPolicy_EveryScoreCitesTheRegimeItWasDecidedUnder(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})
	const ev = `{"event":{"id":"e1","kind":"account","subject":"u_1"}}`

	// Before any regime is stated the honest citation is 0 — the default posture.
	code, body := req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA, ev)
	if code != http.StatusOK {
		t.Fatalf("score = %d %s", code, body)
	}
	if got := citedPolicy(t, body); got != 0 {
		t.Fatalf("a score under no stated regime cited version %d, want 0 — the default posture is a "+
			"fact to report, not a version to invent", got)
	}

	// State one, and the next score cites it.
	code, body = req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA,
		`{"review":0.02,"sample":0.10,"live":true}`)
	if code != http.StatusOK {
		t.Fatalf("appetite = %d %s", code, body)
	}
	code, body = req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA, ev)
	if code != http.StatusOK {
		t.Fatalf("score = %d %s", code, body)
	}
	if got := citedPolicy(t, body); got != 1 {
		t.Fatalf("a score under the first stated regime cited version %d, want 1", got)
	}

	// Restate it DIFFERENTLY, and the citation moves with it — which is the whole
	// point: the earlier decision above is still attributable to version 1.
	code, body = req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA,
		`{"review":0.05,"sample":0.20,"live":true}`)
	if code != http.StatusOK {
		t.Fatalf("second appetite = %d %s", code, body)
	}
	code, body = req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA, ev)
	if code != http.StatusOK {
		t.Fatalf("score = %d %s", code, body)
	}
	if got := citedPolicy(t, body); got != 2 {
		t.Fatalf("a score after a restated regime cited version %d, want 2 — a decision that cites the "+
			"wrong regime is a decision explained against a threshold it was not measured by", got)
	}
}

// citedPolicy reads the version a score cited.
func citedPolicy(t *testing.T, body []byte) int {
	t.Helper()
	var out struct {
		Policy *int `json:"policy"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode score: %v (%s)", err, body)
	}
	if out.Policy == nil {
		t.Fatalf("the score carries no `policy` field at all, so a decision cannot be joined to the "+
			"regime that produced its cut: %s", body)
	}
	return *out.Policy
}

// TestPolicy_ARestatementOfTheSameRegimeMintsNoVersion: a regime is a VALUE. Two
// restatements with the same numbers are one adopted policy, so a client that
// restates its config on every deploy costs nothing — and the version keeps
// meaning "the Nth distinct policy" rather than "the Nth save".
//
// It is also what makes the rate bound below a bound on GOVERNANCE rather than on
// requests: an identical restatement cannot be looped into a disk full of rows.
//
// Mutation proof: delete the `if ok && held.Regime == r` early return in
// [plane.enact] and the version reaches 4.
func TestPolicy_ARestatementOfTheSameRegimeMintsNoVersion(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	r := regime{Review: 0.02, Sample: 0.1, Live: true}

	first, minted, err := p.enact(k, r, "u_1", time.Now())
	if err != nil {
		t.Fatalf("first enact: %v", err)
	}
	if !minted || first.Version != 1 {
		t.Fatalf("first enact: minted=%v version=%d, want true 1", minted, first.Version)
	}
	for i := 0; i < 3; i++ {
		again, minted, err := p.enact(k, r, "u_1", time.Now())
		if err != nil {
			t.Fatalf("restatement %d: %v", i, err)
		}
		if minted {
			t.Fatalf("restating the SAME regime minted version %d — a version is then a count of "+
				"saves, and the cheapest way to fill a tenant's disk", again.Version)
		}
		if again.Version != 1 {
			t.Fatalf("restatement %d answered version %d, want the one in force (1)", i, again.Version)
		}
	}
	// A DIFFERENT regime does mint.
	next, minted, err := p.enact(k, regime{Review: 0.03, Sample: 0.1, Live: true}, "u_1", time.Now())
	if err != nil {
		t.Fatalf("distinct enact: %v", err)
	}
	if !minted || next.Version != 2 {
		t.Fatalf("a DISTINCT regime gave minted=%v version=%d, want true 2 — the record is not "+
			"recording changes at all", minted, next.Version)
	}
}

// TestPolicy_HistoryIsPerTenantOverTheWire: a caller reads its OWN history and
// nothing else, over the wire — which is the isolation a caller can actually reach.
//
// It deliberately does NOT claim to prove the query's tenant predicate. Two
// different orgs are two different FILES ([plane.for_] keys the shelf on the bare
// org), so this test passes with the predicate deleted: the isolation it observes
// is provided by the file, not by the WHERE clause. That is the mutation this test
// cannot detect, and [TestPolicy_TwoBrandsShareAFileAndNotAHistory] is the one
// that can — the predicate is load-bearing exactly where two tenants share a file.
//
// Mutation proof: return the whole history regardless of the caller — point
// [ops.policy] at a fixed tenant — and orgB reads orgA's regimes.
func TestPolicy_HistoryIsPerTenantOverTheWire(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})

	for _, spec := range []string{
		`{"review":0.02,"sample":0.10,"live":true}`,
		`{"review":0.04,"sample":0.20,"live":true}`,
	} {
		if code, body := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA, spec); code != http.StatusOK {
			t.Fatalf("orgA appetite = %d %s", code, body)
		}
	}
	if code, body := req(t, app, http.MethodPut, "/v1/risk/policy", orgB, "u_"+orgB,
		`{"review":0.01,"sample":0.05,"live":false}`); code != http.StatusOK {
		t.Fatalf("orgB appetite = %d %s", code, body)
	}

	a := readPolicy(t, app, orgA)
	if a.Version != 2 || len(a.History) != 2 {
		t.Fatalf("orgA: version=%d history=%d, want 2 and 2", a.Version, len(a.History))
	}
	// Newest first, and the older version is still readable — which is the whole
	// reason the record exists.
	if a.History[0].Version != 2 || a.History[1].Version != 1 {
		t.Fatalf("orgA history is not newest-first: %d then %d", a.History[0].Version, a.History[1].Version)
	}
	if a.History[1].Review != 0.02 {
		t.Fatalf("orgA version 1 reads review=%.4f, want the 0.02 it was stated at — a superseded "+
			"regime that reads as the current one cannot defend the decisions taken under it",
			a.History[1].Review)
	}
	if a.History[0].By != "u_"+orgA {
		t.Fatalf("orgA version 2 is attributed to %q, want the validated principal %q",
			a.History[0].By, "u_"+orgA)
	}

	b := readPolicy(t, app, orgB)
	if b.Version != 1 || len(b.History) != 1 {
		t.Fatalf("orgB: version=%d history=%d, want 1 and 1 — it stated one regime and can see one",
			b.Version, len(b.History))
	}
	for _, v := range b.History {
		if v.Review == 0.02 || v.Review == 0.04 || v.By == "u_"+orgA {
			t.Fatalf("orgB can read orgA's regime: %+v", v)
		}
	}
}

// readPolicy reads one organisation's policy history over the wire.
func readPolicy(t *testing.T, app *zip.App, org string) riskPolicyOut {
	t.Helper()
	code, body := req(t, app, http.MethodGet, "/v1/risk/policy", org, "u_"+org, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/policy for %s = %d %s", org, code, body)
	}
	var out riskPolicyOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode policy: %v (%s)", err, body)
	}
	return out
}

// TestPolicy_TheRateBoundBindsAndIsNamed: the history is append-only, so the
// number of DISTINCT regimes one organisation may adopt per window is bounded —
// and the refusal NAMES the organisation's own bound rather than reading as a
// fault. The regime in force is untouched by a refusal.
//
// The bound is read off THIS tenant's own table, so no other tenant's traffic can
// spend it.
//
// Mutation proof: delete the `if recent >= maxPolicyPerWindow` refusal in
// [plane.enact] and the version passes the ceiling.
func TestPolicy_TheRateBoundBindsAndIsNamed(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k, other := key(t, brandA, orgA), key(t, brandA, orgB)
	now := time.Now().UTC()

	for i := 0; i < maxPolicyPerWindow; i++ {
		// Distinct regimes, all inside ONE window.
		if _, minted, err := p.enact(k, regime{Review: 0.02, Sample: float64(i) / 1000, Live: true}, "u_1", now); err != nil || !minted {
			t.Fatalf("enact %d: minted=%v err=%v", i, minted, err)
		}
	}
	_, _, err := p.enact(k, regime{Review: 0.02, Sample: 0.999, Live: true}, "u_1", now)
	if err == nil {
		t.Fatalf("the %dst distinct regime in one window was accepted — an append-only history with no "+
			"rate bound is a tenant-writable disk", maxPolicyPerWindow+1)
	}
	msg := err.Error()
	for _, want := range []string{fmt.Sprint(maxPolicyPerWindow), "unchanged"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not name %q, so an operator cannot tell a bound from a fault: %s",
				want, msg)
		}
	}
	// The regime in force is UNCHANGED by a refusal.
	held, ok, err := p.inForce(k)
	if err != nil || !ok {
		t.Fatalf("inForce after refusal: ok=%v err=%v", ok, err)
	}
	if held.Version != maxPolicyPerWindow || held.Regime.Sample != float64(maxPolicyPerWindow-1)/1000 {
		t.Fatalf("a refused change moved the regime in force: version=%d sample=%.4f",
			held.Version, held.Regime.Sample)
	}
	// AND it is the tenant's OWN bound: another organisation is unaffected, which
	// is only true because the count is read off its own table.
	if _, minted, err := p.enact(other, regime{Review: 0.02, Sample: 0.5, Live: true}, "u_2", now); err != nil || !minted {
		t.Fatalf("one organisation exhausting its own rate bound refused another's first change: "+
			"minted=%v err=%v — that is a shared cap wearing a per-tenant name", minted, err)
	}
	// A later window admits again, so the bound is a bound and not a lockout.
	if _, minted, err := p.enact(k, regime{Review: 0.02, Sample: 0.999, Live: true}, "u_1", now.Add(policyWindow+time.Second)); err != nil || !minted {
		t.Fatalf("the next window did not admit a change: minted=%v err=%v", minted, err)
	}
}

// TestPolicy_BoundsArePublishedInTheDimensionThatBinds: [policyVersions] is a
// count, and a count is a byte bound only if a real worst-case row fits
// [maxPolicyRowBytes]. This MEASURES one against a real file rather than
// recomputing the same formula.
//
// Mutation proof: raise maxField or drop a term from maxPolicyRowBytes and the
// measured row exceeds the published figure.
func TestPolicy_BoundsArePublishedInTheDimensionThatBinds(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	sh, err := p.for_(k)
	if err != nil {
		t.Fatalf("shelf: %v", err)
	}
	// The widest legal row: an identity at the bound, and a distinct regime each
	// time. The clock advances so the RATE bound — a different bound, tested above
	// — is not what this measures.
	by := strings.Repeat("z", maxField)
	const sample = 300
	before := shelfBytes(t, sh)
	at := time.Now().UTC()
	for i := 0; i < sample; i++ {
		if _, minted, err := p.enact(k, regime{Review: 0.5, Sample: float64(i) / 1000, Live: true},
			by, at.Add(time.Duration(i)*2*time.Hour)); err != nil || !minted {
			t.Fatalf("enact %d: minted=%v err=%v", i, minted, err)
		}
	}
	per := (shelfBytes(t, sh) - before) / sample
	if per > maxPolicyRowBytes {
		t.Fatalf("a worst-case retained version costs %d bytes against a published %d — the %d KiB "+
			"per-tenant policy budget is understated by %.1fx",
			per, maxPolicyRowBytes, policyBudget>>10, float64(per)/float64(maxPolicyRowBytes))
	}
	// The version cap IS the byte budget divided by the row bound, so the product
	// is the budget (to within the one row integer division drops).
	if policyVersions*maxPolicyRowBytes > policyBudget || (policyVersions+1)*maxPolicyRowBytes <= policyBudget {
		t.Fatalf("the retention cap (%d) is no longer the byte budget (%d) divided by the row bound (%d)",
			policyVersions, policyBudget, maxPolicyRowBytes)
	}
	// An identity over the bound is REFUSED, not truncated: an audit record whose
	// author was silently shortened names somebody else.
	if _, _, err := p.enact(k, regime{Review: 0.4, Sample: 0.4, Live: true},
		strings.Repeat("z", maxField+1), at); err == nil {
		t.Fatalf("a %d-byte identity was accepted, so every published policy ceiling is a count of "+
			"values with no bound", maxField+1)
	}
	t.Logf("measured: worst-case version %d bytes (published %d); retention %d versions x %d = %d KiB",
		per, maxPolicyRowBytes, policyVersions, maxPolicyRowBytes, policyBudget>>10)
}

// TestPolicy_RetentionIsCountedAndNotSilent: the history is bounded on disk, so at
// the ceiling the oldest version is disposed of — and what was disposed of is
// REPORTED. A bounded audit record that does not say what it no longer holds is a
// record that lies by omission: a decision citing a disposed version can no longer
// be reconstructed from it, and the reader must be able to tell.
//
// Mutation proof: return 0 from [plane.disposed] and the reported figure stops
// tracking the loss; delete the [plane.disposeOldPolicy] call and the history
// grows past its byte budget.
func TestPolicy_RetentionIsCountedAndNotSilent(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	at := time.Now().UTC()

	const over = 3
	for i := 0; i < policyVersions+over; i++ {
		if _, minted, err := p.enact(k, regime{Review: 0.5, Sample: float64(i) / 10000, Live: true},
			"u_1", at.Add(time.Duration(i)*2*time.Hour)); err != nil || !minted {
			t.Fatalf("enact %d: minted=%v err=%v", i, minted, err)
		}
	}
	hist, disposed, err := p.history(k, policyVersions)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) > policyVersions {
		t.Fatalf("the history holds %d versions against a retention of %d — the byte budget it is "+
			"derived from is not enforced", len(hist), policyVersions)
	}
	if disposed != over {
		t.Fatalf("retention disposed of %d versions and reported %d — a bounded record that "+
			"under-reports its own loss cannot be audited", over, disposed)
	}
	// The version in force is never the one disposed of.
	held, ok, err := p.inForce(k)
	if err != nil || !ok {
		t.Fatalf("inForce: ok=%v err=%v", ok, err)
	}
	if held.Version != policyVersions+over {
		t.Fatalf("the version in force is %d, want the newest (%d)", held.Version, policyVersions+over)
	}
}

// TestPolicy_IsAppendOnly is the STRUCTURAL half. The record's whole value is that
// a superseded regime is still readable, which is a property of the STATEMENTS in
// this package and not of any one test: one UPDATE against `policy` anywhere and a
// version means whatever it was last edited to mean.
//
// The single DELETE is the retention disposal, which is named here so adding a
// second one takes a deliberate edit.
//
// Mutation proof: write `UPDATE policy SET ...` anywhere in the package and this
// fails.
func TestPolicy_IsAppendOnly(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	const disposal = "DELETE FROM policy WHERE tenant = ? AND version <= ("
	var deletes, updates []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				sql := strings.ToUpper(lit.Value)
				if !strings.Contains(sql, "POLICY") {
					return true
				}
				where := fmt.Sprintf("%s:%d", name, fset.Position(lit.Pos()).Line)
				if strings.Contains(sql, "UPDATE POLICY") || strings.Contains(sql, "ON CONFLICT") {
					updates = append(updates, where)
				}
				if strings.Contains(sql, "DELETE FROM POLICY") && !strings.Contains(lit.Value, disposal) {
					deletes = append(deletes, where)
				}
				return true
			})
		}
	}
	if len(updates) > 0 {
		t.Errorf("statement(s) that MUTATE a recorded regime: %s\n"+
			"A version that can be edited cannot defend the decisions that cited it.", strings.Join(updates, ", "))
	}
	if len(deletes) > 0 {
		t.Errorf("DELETE against policy outside the retention disposal: %s\n"+
			"Retention is the ONE reason a version may leave, and it is counted.", strings.Join(deletes, ", "))
	}
}

// TestPolicy_ARegimePredatingTheRecordIsAdopted: every organisation already live
// in production stated its appetite before this record existed, and it is on the
// model row and nowhere else. Resolving the regime only from the policy record
// would return every one of them to shadow on the first rollout after this ships —
// causing exactly the defect the record fixes, to every existing tenant at once.
//
// Mutation proof: delete the adoption branch in [plane.restoreRegime] and this
// fails with the tenant in shadow at the default appetite.
func TestPolicy_ARegimePredatingTheRecordIsAdopted(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)

	// A shelf as it exists TODAY in production: a model row whose config carries a
	// live regime, and no policy table row at all.
	p1, err := newPlane(baseAt(t, dir))
	if err != nil {
		t.Fatalf("newPlane: %v", err)
	}
	holdFolds(t, p1)
	if _, err := p1.appetite(k, 0.03, 0.15, true, "u_"+orgA); err != nil {
		t.Fatalf("appetite: %v", err)
	}
	sh, err := p1.for_(k)
	if err != nil {
		t.Fatalf("shelf: %v", err)
	}
	// Learn something, so the legacy writer actually records the config the way it
	// does for a real tenant, then remove the policy record this fix added.
	if _, err := p1.learn(k, ob(t, "e1", kindAccount, "u_1", 10, time.Now().UTC().Add(-time.Hour))); err != nil {
		t.Fatalf("learn: %v", err)
	}
	// The policy row goes BEFORE the shutdown, which is the only order that leaves a
	// shelf shaped like today's production one: a model row carrying the regime in
	// its config, written by the legacy writer at close, and no policy row at all.
	if _, err := sh.db.Exec(`DELETE FROM policy WHERE tenant = ?`, string(k)); err != nil {
		t.Fatalf("simulate a pre-record shelf: %v", err)
	}
	if err := p1.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	p2, err := newPlane(baseAt(t, dir))
	if err != nil {
		t.Fatalf("newPlane after rollout: %v", err)
	}
	holdFolds(t, p2)
	t.Cleanup(func() { _ = p2.close(context.Background()) })
	after, _, err := p2.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.Config.Shadow {
		t.Fatalf("an organisation whose regime predates the policy record was returned to SHADOW — " +
			"the fix would disarm every existing live tenant on its first rollout")
	}
	if after.Config.Appetite.Review != 0.03 {
		t.Fatalf("the adopted appetite is %.4f, want the 0.03 the tenant had stated",
			after.Config.Appetite.Review)
	}
	// And it was adopted as a REAL version, so from now on there is one source.
	hist, _, err := p2.history(k, policyVersions)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 || hist[0].Version != 1 {
		t.Fatalf("the adopted regime is not a version: %+v", hist)
	}
	if hist[0].By != adopter {
		t.Fatalf("the adopted version is attributed to %q, want %q — a version this plane minted on "+
			"the organisation's behalf must not read as one the organisation stated", hist[0].By, adopter)
	}
}

// TestPolicy_TwoBrandsShareAFileAndNotAHistory is the half the wire test cannot
// reach, and it is the one that makes the query's tenant predicate load-bearing.
//
// A shelf file is the FLEET's per-org file: [plane.for_] resolves it from the BARE
// org, deliberately, so two brands' identically named organisations share one file.
// Inside it the QUALIFIED key is the only thing that separates them — there is no
// second file to hide behind and no directory boundary to lean on. A history read
// that dropped the predicate would answer one brand's organisation with the
// other's policy, which is that organisation's governance disclosed.
//
// Mutation proof: drop `WHERE tenant = ?` from [plane.history] (or from
// [plane.inForce]) and the brands read each other's regimes.
func TestPolicy_TwoBrandsShareAFileAndNotAHistory(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	ha, za := key(t, brandA, orgA), key(t, brandB, orgA) // SAME org slug, two brands

	sha, err := p.for_(ha)
	if err != nil {
		t.Fatalf("shelf %s: %v", ha, err)
	}
	shz, err := p.for_(za)
	if err != nil {
		t.Fatalf("shelf %s: %v", za, err)
	}
	if sha != shz {
		t.Fatalf("the two brands do not share a file, so this test is not exercising the predicate " +
			"it exists to hold — the fixture has stopped modelling the deployment")
	}

	now := time.Now().UTC()
	if _, minted, err := p.enact(ha, regime{Review: 0.02, Sample: 0.10, Live: true}, "u_hanzo", now); err != nil || !minted {
		t.Fatalf("enact %s: minted=%v err=%v", ha, minted, err)
	}
	if _, minted, err := p.enact(ha, regime{Review: 0.04, Sample: 0.20, Live: true}, "u_hanzo", now); err != nil || !minted {
		t.Fatalf("second enact %s: minted=%v err=%v", ha, minted, err)
	}
	if _, minted, err := p.enact(za, regime{Review: 0.30, Sample: 0.90, Live: false}, "u_zoo", now); err != nil || !minted {
		t.Fatalf("enact %s: minted=%v err=%v", za, minted, err)
	}

	// Each brand's organisation numbers its OWN history from 1: a version is a
	// position in one organisation's history, not a row count in a shared file.
	hist, _, err := p.history(za, policyVersions)
	if err != nil {
		t.Fatalf("history %s: %v", za, err)
	}
	if len(hist) != 1 {
		t.Fatalf("brand %q's %q reads %d versions of a file it shares with brand %q's %q, want 1",
			brandB, orgA, len(hist), brandA, orgA)
	}
	if hist[0].Version != 1 || hist[0].By != "u_zoo" || hist[0].Regime.Review != 0.30 {
		t.Fatalf("brand %q's only version reads %+v — it is reading the other brand's regime",
			brandB, hist[0])
	}
	held, ok, err := p.inForce(za)
	if err != nil || !ok {
		t.Fatalf("inForce %s: ok=%v err=%v", za, ok, err)
	}
	if held.Regime.Review != 0.30 || held.By != "u_zoo" {
		t.Fatalf("the regime in force for brand %q's %q is %+v — the wrong brand's policy is deciding "+
			"that organisation's traffic", brandB, orgA, held)
	}
	// And the other direction, so neither brand is merely lucky about ordering.
	hist, _, err = p.history(ha, policyVersions)
	if err != nil {
		t.Fatalf("history %s: %v", ha, err)
	}
	if len(hist) != 2 || hist[0].Version != 2 {
		t.Fatalf("brand %q's %q reads %d versions, want its own 2", brandA, orgA, len(hist))
	}
	for _, v := range hist {
		if v.By == "u_zoo" {
			t.Fatalf("brand %q's history contains brand %q's version: %+v", brandA, brandB, v)
		}
	}
}

// TestPolicy_AnAppetiteOutsideTheContractIsRefusedAndChangesNothing holds the ONE
// door the appetite bounds now live behind.
//
// Those bounds used to be spelled twice — once at [ops.appetite] and once in
// [admitRegime] — and collapsing them to one spelling is right. But the surviving
// spelling had no test: [admitRegime] was made to `return nil` unconditionally and
// the WHOLE package stayed green, so the published contract
// (`review ∈ (0, 0.5]`, `sample ∈ [0, 1]`) was enforced by code that could be
// deleted without a single failure. That is the same shape as a control switching
// itself off, one layer down — the bound is present, and nothing measures it.
//
// It asserts BOTH halves, because a refusal that half-applies is worse than no
// bound: the call is refused with the caller's own value named, AND the regime in
// force is untouched — no version minted, nothing in the history, and the model
// still deciding under what it decided under before.
//
// Over the WIRE and not the plane, because the wire is where the contract is
// published and where the op's deleted copy used to answer.
//
// Mutation proof: make [admitRegime] `return nil` and every out-of-contract case
// below is accepted 200; delete the `if err := admitRegime(r)` call in
// [plane.enact] and the same.
func TestPolicy_AnAppetiteOutsideTheContractIsRefusedAndChangesNothing(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})

	// A regime the contract admits, so there is something in force to protect.
	if code, body := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA,
		`{"review":0.02,"sample":0.10,"live":true}`); code != http.StatusOK {
		t.Fatalf("the admissible regime was refused %d %s — the test would prove nothing", code, body)
	}
	held := readPolicy(t, app, orgA)
	if held.Version != 1 {
		t.Fatalf("version in force is %d, want 1", held.Version)
	}

	for _, bad := range []struct {
		what string
		body string
	}{
		{"a review share of zero — no cut can be derived from it", `{"review":0,"sample":0.1,"live":true}`},
		{"a negative review share", `{"review":-0.02,"sample":0.1,"live":true}`},
		{"a review share past the published half", `{"review":0.51,"sample":0.1,"live":true}`},
		{"a review share stated as a count rather than a share", `{"review":50,"sample":0.1,"live":true}`},
		{"a negative sample rate", `{"review":0.02,"sample":-0.1,"live":true}`},
		{"a sample rate above one", `{"review":0.02,"sample":1.5,"live":true}`},
	} {
		code, body := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA, bad.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: PUT %s answered %d %s, want 400 — the published contract is enforced by "+
				"nothing", bad.what, bad.body, code, body)
		}
		// The refusal names the field, so a caller can act on it without reading
		// this source.
		if !strings.Contains(string(body), "review") && !strings.Contains(string(body), "sample") {
			t.Fatalf("%s: the refusal names neither field: %s", bad.what, body)
		}
	}

	// NOTHING MOVED. A refused policy change that still minted a version, or still
	// took the model live, would be the disarm this record exists to prevent —
	// arrived at through the door that refused.
	after := readPolicy(t, app, orgA)
	if after.Version != held.Version || len(after.History) != len(held.History) {
		t.Fatalf("a refused appetite moved the record: version %d→%d, history %d→%d",
			held.Version, after.Version, len(held.History), len(after.History))
	}
	if len(after.History) == 0 || after.History[0].Review != 0.02 || after.History[0].Sample != 0.10 {
		t.Fatalf("the regime in force after six refusals is %+v, want the 0.02/0.10 it was left at",
			after.History)
	}
}
