// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package authors

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// basisBody is the decoded audit payload. computeProof is a *string precisely so a
// test can tell "absent" (nil) from "some placeholder" (non-nil) — the invariant the
// whole ledger disclosure rests on.
type basisBody struct {
	IsAuthor        bool   `json:"isAuthor"`
	ID              string `json:"id"`
	Status          string `json:"status"`
	AsOf            int64  `json:"asOf"`
	ShareBps        int64  `json:"shareBps"`
	DefaultShareBps int64  `json:"defaultShareBps"`
	ShareSource     string `json:"shareSource"`
	Period          string `json:"period"`
	Method          struct {
		SpendBasis   string `json:"spendBasis"`
		Period       string `json:"period"`
		Earning      string `json:"earning"`
		ComputePrice string `json:"computePrice"`
		RateCard     struct {
			MicroUSDPerVCPUHour int64  `json:"microUsdPerVcpuHour"`
			MicroUSDPerGBHour   int64  `json:"microUsdPerGbHour"`
			Basis               string `json:"basis"`
		} `json:"rateCard"`
		Sizing []struct {
			Type string  `json:"type"`
			VCPU float64 `json:"vcpu"`
			GB   float64 `json:"gb"`
		} `json:"sizing"`
	} `json:"method"`
	Ledger []struct {
		ID           string  `json:"id"`
		Period       string  `json:"period"`
		DeployingOrg string  `json:"deployingOrg"`
		SpendCents   int64   `json:"spendCents"`
		ShareBps     int64   `json:"shareBps"`
		EarningCents int64   `json:"earningCents"`
		Consistent   bool    `json:"consistent"`
		ComputeProof *string `json:"computeProof"`
		CreatedAt    int64   `json:"createdAt"`
		Attribution  []struct {
			RepoURL   string `json:"repoUrl"`
			Project   string `json:"project"`
			CreatedAt int64  `json:"createdAt"`
		} `json:"attribution"`
	} `json:"ledger"`
	Reconciliation struct {
		LedgerRows         int   `json:"ledgerRows"`
		LedgerEarningCents int64 `json:"ledgerEarningCents"`
		AccruedCents       int64 `json:"accruedCents"`
		PaidCents          int64 `json:"paidCents"`
		PendingCents       int64 `json:"pendingCents"`
		Balanced           bool  `json:"balanced"`
		Consistent         bool  `json:"consistent"`
	} `json:"reconciliation"`
	Window struct {
		LedgerReturned  int  `json:"ledgerReturned"`
		LedgerLimit     int  `json:"ledgerLimit"`
		DeploysReturned int  `json:"deploysReturned"`
		DeploysLimit    int  `json:"deploysLimit"`
		Truncated       bool `json:"truncated"`
	} `json:"window"`
}

// earner stands up ONE approved author (org "orgA") owning acme/widgets via the OAuth
// method. Deploying orgs, spend and sweeps are each test's own business.
func earner(t *testing.T, app *zip.App, s *cloud.Service[state], fg *fakeGitHub) string {
	t.Helper()
	id, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	if st, b := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"}); st != http.StatusCreated {
		t.Fatalf("verify want 201, got %d (%s)", st, b)
	}
	approve(t, app, id)
	return id
}

// deploys records one attribution edge (deployingOrg deployed repo as project).
func deploys(t *testing.T, app *zip.App, deployingOrg, repo, project string) {
	t.Helper()
	if st, b := req(t, app, http.MethodPost, "/v1/authors/deploys/record", deployingOrg, false,
		map[string]any{"repoUrl": repo, "project": project}); st >= http.StatusMultipleChoices {
		t.Fatalf("record deploy(%s,%s) got %d (%s)", deployingOrg, repo, st, b)
	}
}

// latch appends ONE ledger row (+ the balance increment) through the SAME transaction
// the sweep uses, so a test can place a row in a chosen period at a chosen instant.
func latch(t *testing.T, s *cloud.Service[state], authorID, org, period string, spend, shareBps, now int64) {
	t.Helper()
	accrualID, _ := genID("aca")
	ledgerID, _ := genID("alg")
	won, err := s.State.store.LatchAccrual(context.Background(), accrualID, ledgerID, authorID, org, period,
		shareBps, spend, spend*shareBps/bpsDenom, now)
	if err != nil || !won {
		t.Fatalf("latch(%s,%s): won=%v err=%v", org, period, won, err)
	}
}

// getBasis fetches an author's OWN basis and decodes it; any non-200 fails the test.
func getBasis(t *testing.T, app *zip.App, org, query string) (basisBody, []byte) {
	t.Helper()
	st, body := req(t, app, http.MethodGet, "/v1/authors/basis"+query, org, false, nil)
	if st != http.StatusOK {
		t.Fatalf("GET basis(%s%s) want 200, got %d (%s)", org, query, st, body)
	}
	var v basisBody
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode basis: %v (%s)", err, body)
	}
	return v, body
}

// TestBasisReconciles is the arithmetic proof: two deploying orgs accrue through the
// real sweep, every returned row satisfies earning = spend × share / 10000 (integer
// floor — orgC's 3333c is deliberately not divisible), the ledger foots EXACTLY to the
// balance, and a payout moves paid/pending without disturbing that footing.
func TestBasisReconciles(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()
	idA := earner(t, app, s, fg)

	spends := map[string]int64{"orgB": 10000, "orgC": 3333} // 2000c + 666c (floored)
	for org, spend := range spends {
		deploys(t, app, org, "acme/widgets", "proj-"+org)
		fc.setSpend(org, spend)
	}
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	want := int64(0)
	for _, spend := range spends {
		want += spend * defaultShareBps / bpsDenom
	}
	if want != 2666 {
		t.Fatalf("fixture drift: want %d, expected 2666", want)
	}

	v, _ := getBasis(t, app, "orgA", "")
	if !v.IsAuthor || v.ID != idA || v.Status != StatusApproved {
		t.Fatalf("basis identity wrong: %+v", v)
	}
	if len(v.Ledger) != 2 {
		t.Fatalf("ledger rows returned = %d, want 2", len(v.Ledger))
	}

	sum := int64(0)
	for _, r := range v.Ledger {
		if got := r.SpendCents * r.ShareBps / bpsDenom; r.EarningCents != got || !r.Consistent {
			t.Fatalf("row %s: earning=%d want %d consistent=%v", r.ID, r.EarningCents, got, r.Consistent)
		}
		if r.SpendCents != spends[r.DeployingOrg] {
			t.Fatalf("row %s: spend=%d, want %d for %s", r.ID, r.SpendCents, spends[r.DeployingOrg], r.DeployingOrg)
		}
		sum += r.EarningCents
	}
	if sum != want {
		t.Fatalf("row earnings sum = %d, want %d (no rounding drift)", sum, want)
	}

	a, _ := s.State.store.GetByID(ctx, idA)
	rc := v.Reconciliation
	if rc.LedgerRows != 2 || rc.LedgerEarningCents != want || rc.AccruedCents != a.AccruedCents {
		t.Fatalf("reconciliation = %+v, want rows 2 / earning %d / accrued %d", rc, want, a.AccruedCents)
	}
	if !rc.Balanced || !rc.Consistent || rc.PendingCents != rc.AccruedCents-rc.PaidCents {
		t.Fatalf("books do not reconcile: %+v", rc)
	}
	if v.Window.Truncated || v.Window.LedgerReturned != 2 || v.Window.LedgerLimit != ledgerLimit {
		t.Fatalf("window = %+v", v.Window)
	}

	// A payout moves paid/pending; the ledger↔balance footing is untouched (a payout
	// settles accrued royalty, it never un-earns it).
	if st, b := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/payout", "admin", true,
		map[string]any{"amountCents": 1000, "method": methodCredits}); st != http.StatusOK {
		t.Fatalf("payout want 200, got %d (%s)", st, b)
	}
	v2, _ := getBasis(t, app, "orgA", "")
	rc2 := v2.Reconciliation
	if rc2.PaidCents != 1000 || rc2.PendingCents != want-1000 || !rc2.Balanced || rc2.LedgerEarningCents != want {
		t.Fatalf("after payout: %+v, want paid 1000 / pending %d / balanced", rc2, want-1000)
	}
}

// TestBasisIsPureRead guards the audit-read invariant: the basis never sweeps,
// accrues, pays, or writes. Phase 1 reads a fixture that IS accruable and proves
// nothing moves; the dashboard read then accrues it, proving the fixture was live.
// Phase 2 repeats the 3× read against a non-zero balance.
func TestBasisIsPureRead(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()
	idA := earner(t, app, s, fg)
	deploys(t, app, "orgB", "acme/widgets", "proj-b")
	fc.setSpend("orgB", 10000) // a sweep here WOULD accrue 2000c

	assertStill := func(phase string, accrued, paid int64, wantRows int, wantEarn int64) {
		t.Helper()
		a, _ := s.State.store.GetByID(ctx, idA)
		rows, earn, _ := s.State.store.LedgerTotals(ctx, idA)
		if a.AccruedCents != accrued || a.PaidCents != paid || rows != wantRows || earn != wantEarn {
			t.Fatalf("%s: audit read MOVED money — accrued=%d paid=%d rows=%d earn=%d, want %d/%d/%d/%d",
				phase, a.AccruedCents, a.PaidCents, rows, earn, accrued, paid, wantRows, wantEarn)
		}
	}

	for i := 0; i < 3; i++ {
		if v, _ := getBasis(t, app, "orgA", ""); len(v.Ledger) != 0 || v.Reconciliation.AccruedCents != 0 {
			t.Fatalf("read %d accrued something: %+v", i, v.Reconciliation)
		}
	}
	assertStill("before any sweep", 0, 0, 0, 0)

	// The dashboard (which sweeps lazily on read) does what the audit read refused to.
	const want = 10000 * defaultShareBps / bpsDenom
	if st, b := req(t, app, http.MethodGet, "/v1/authors", "orgA", false, nil); st != http.StatusOK {
		t.Fatalf("dashboard want 200, got %d (%s)", st, b)
	}
	assertStill("after the dashboard's lazy sweep", want, 0, 1, want)

	for i := 0; i < 3; i++ {
		if v, _ := getBasis(t, app, "orgA", ""); v.Reconciliation.LedgerEarningCents != want {
			t.Fatalf("read %d disturbed the ledger: %+v", i, v.Reconciliation)
		}
	}
	assertStill("after 3 more audit reads", want, 0, 1, want)
}

// TestBasisComputeProofAbsent asserts on the MARSHALLED BYTES that a missing hanzod
// attestation reads as an explicit null on EVERY row — absence must be legible, never
// merely missing, and never a hash / txn id / "pending" standing in for a proof.
func TestBasisComputeProofAbsent(t *testing.T) {
	app, s, fc, fg := mount(t)
	idA := earner(t, app, s, fg)
	for _, org := range []string{"orgB", "orgC"} {
		deploys(t, app, org, "acme/widgets", "proj-"+org)
		fc.setSpend(org, 10000)
	}
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	v, body := getBasis(t, app, "orgA", "")
	if len(v.Ledger) != 2 {
		t.Fatalf("want 2 rows, got %d", len(v.Ledger))
	}
	if n := bytes.Count(body, []byte(`"computeProof":null`)); n != len(v.Ledger) {
		t.Fatalf(`"computeProof":null appears %d times, want %d — absence must be explicit: %s`, n, len(v.Ledger), body)
	}
	if n := bytes.Count(body, []byte(`"computeProof"`)); n != len(v.Ledger) {
		t.Fatalf("computeProof key count = %d, want %d", n, len(v.Ledger))
	}
	for _, r := range v.Ledger {
		if r.ComputeProof != nil {
			t.Fatalf("row %s carries a fabricated proof %q", r.ID, *r.ComputeProof)
		}
	}
	// The store agrees: nothing was written to make the response look attested.
	rows, _ := s.State.store.ListLedger(context.Background(), idA, "", 100)
	for _, r := range rows {
		if r.ComputeProof != nil {
			t.Fatalf("stored row %s has a proof: %q", r.ID, *r.ComputeProof)
		}
	}
}

// TestBasisOrgScoped: the subject is the principal, full stop. An id or org supplied
// in the URL or the body is ignored, a non-author org gets an honest answer instead of
// a 404 that would leak existence, and no caller ever sees another org's rows.
func TestBasisOrgScoped(t *testing.T) {
	app, s, fc, fg := mount(t)
	idA := earner(t, app, s, fg)
	deploys(t, app, "orgB", "acme/widgets", "proj-b")
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	// orgZ is an author of its own, with nothing earned.
	idZ, _ := connectOrg(t, app, s, "orgZ", "zdev")
	st, body := req(t, app, http.MethodGet, "/v1/authors/basis?id="+idA+"&org=orgA", "orgZ", false,
		map[string]any{"id": idA, "org": "orgA"})
	if st != http.StatusOK {
		t.Fatalf("orgZ basis want 200, got %d (%s)", st, body)
	}
	var z basisBody
	_ = json.Unmarshal(body, &z)
	if z.ID != idZ || len(z.Ledger) != 0 || z.Reconciliation.AccruedCents != 0 {
		t.Fatalf("supplied id/org honoured — orgZ got %+v (want its own %s, empty)", z, idZ)
	}
	if bytes.Contains(body, []byte(idA)) || bytes.Contains(body, []byte("orgB")) {
		t.Fatalf("orgZ's basis leaked orgA's author: %s", body)
	}

	// An org with no author record: honest, never 404, never someone else's data.
	st2, body2 := req(t, app, http.MethodGet, "/v1/authors/basis", "orgQ", false, nil)
	if st2 != http.StatusOK {
		t.Fatalf("non-author basis want 200, got %d (%s)", st2, body2)
	}
	var q map[string]json.RawMessage
	if err := json.Unmarshal(body2, &q); err != nil {
		t.Fatalf("decode: %v (%s)", err, body2)
	}
	if string(q["isAuthor"]) != "false" || string(q["defaultShareBps"]) != "2000" {
		t.Fatalf("non-author shape wrong: %s", body2)
	}
	for _, leak := range []string{"ledger", "reconciliation", "id"} {
		if _, ok := q[leak]; ok {
			t.Fatalf("non-author response carries %q: %s", leak, body2)
		}
	}

	// No principal at all → 403, never a guess.
	if code, _ := req(t, app, http.MethodGet, "/v1/authors/basis", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("anonymous basis want 403, got %d", code)
	}
}

// TestBasisShareOverride: an author reads the rate their pay is computed at, and
// whether it is the platform default. A later share change never rewrites history —
// the row keeps the share AS APPLIED while the top level shows the current one.
func TestBasisShareOverride(t *testing.T) {
	app, s, fc, fg := mount(t)
	idA := earner(t, app, s, fg) // approved at the default
	deploys(t, app, "orgB", "acme/widgets", "proj-b")
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	v, _ := getBasis(t, app, "orgA", "")
	if v.ShareBps != defaultShareBps || v.ShareSource != "default" || v.DefaultShareBps != defaultShareBps {
		t.Fatalf("default author reads %d/%s (default %d)", v.ShareBps, v.ShareSource, v.DefaultShareBps)
	}

	// Renegotiated to 25%.
	if st, b := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/approve", "admin", true,
		map[string]any{"shareBps": 2500}); st != http.StatusOK {
		t.Fatalf("approve override want 200, got %d (%s)", st, b)
	}
	v2, _ := getBasis(t, app, "orgA", "")
	if v2.ShareBps != 2500 || v2.ShareSource != "override" || v2.DefaultShareBps != defaultShareBps {
		t.Fatalf("override author reads %d/%s (default %d)", v2.ShareBps, v2.ShareSource, v2.DefaultShareBps)
	}
	if len(v2.Ledger) != 1 || v2.Ledger[0].ShareBps != defaultShareBps {
		t.Fatalf("share change rewrote history: row share = %d, want %d", v2.Ledger[0].ShareBps, defaultShareBps)
	}
	if !v2.Reconciliation.Consistent || !v2.Reconciliation.Balanced {
		t.Fatalf("override broke reconciliation: %+v", v2.Reconciliation)
	}
}

// TestBasisPeriodFilter: ?period= narrows the ROWS while the reconciliation stays
// account-wide (a window must never be mistaken for the total). Only the shape the
// accrual latch mints is accepted.
func TestBasisPeriodFilter(t *testing.T) {
	app, s, _, fg := mount(t)
	idA := earner(t, app, s, fg)
	now := time.Now().Unix()
	latch(t, s, idA, "orgB", "2026-07", 4200, defaultShareBps, now)
	latch(t, s, idA, "orgC", "2020-01", 1000, defaultShareBps, now-100)
	const total = 4200*defaultShareBps/bpsDenom + 1000*defaultShareBps/bpsDenom

	v, body := getBasis(t, app, "orgA", "?period=2026-07")
	if len(v.Ledger) != 1 || v.Ledger[0].Period != "2026-07" || v.Period != "2026-07" {
		t.Fatalf("period filter returned %d rows / echo %q (%s)", len(v.Ledger), v.Period, body)
	}
	if v.Reconciliation.LedgerRows != 2 || v.Reconciliation.LedgerEarningCents != total || !v.Reconciliation.Balanced {
		t.Fatalf("narrowed window shrank the reconciliation: %+v (want rows 2 / earning %d)", v.Reconciliation, total)
	}

	// Unnarrowed: every row, and NO period key at all (omitted, not empty).
	vAll, bodyAll := getBasis(t, app, "orgA", "")
	if len(vAll.Ledger) != 2 {
		t.Fatalf("unnarrowed rows = %d, want 2", len(vAll.Ledger))
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(bodyAll, &m)
	if _, ok := m["period"]; ok {
		t.Fatalf("unnarrowed response carries a period key: %s", bodyAll)
	}

	for _, bad := range []string{"2026-7", "2026", "july", "2026-07-01", "2026-07' OR '1'='1"} {
		if st, b := req(t, app, http.MethodGet, "/v1/authors/basis?period="+url.QueryEscape(bad), "orgA", false, nil); st != http.StatusBadRequest {
			t.Fatalf("period %q want 400, got %d (%s)", bad, st, b)
		}
	}
	// A well-formed period with no rows is empty, not an error and not everything.
	vNone, _ := getBasis(t, app, "orgA", "?period=1999-01")
	if len(vNone.Ledger) != 0 || vNone.Reconciliation.LedgerRows != 2 {
		t.Fatalf("empty period: rows=%d reconciliation=%+v", len(vNone.Ledger), vNone.Reconciliation)
	}
}

// TestBasisAttribution: a row names only the author's OWN deploy edges for THAT
// deploying org, and never an edge recorded after the row — an attribution that cannot
// be established stays absent rather than guessed.
func TestBasisAttribution(t *testing.T) {
	app, s, _, fg := mount(t)
	ctx := context.Background()
	idA := earner(t, app, s, fg)
	fg.setAdmin("tok_a", "acme", "tools")
	if st, b := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/tools"}); st != http.StatusCreated {
		t.Fatalf("verify tools want 201, got %d (%s)", st, b)
	}
	deploys(t, app, "orgB", "acme/widgets", "proj-b")
	deploys(t, app, "orgC", "acme/tools", "proj-c")

	edges, _ := s.State.store.ListDeploys(ctx, idA, 10)
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(edges))
	}
	var edgeC DeployEvent
	for _, e := range edges {
		if e.DeployingOrg == "orgC" {
			edgeC = e
		}
	}

	latch(t, s, idA, "orgB", "2026-07", 10000, defaultShareBps, time.Now().Unix())
	latch(t, s, idA, "orgC", "2026-07", 10000, defaultShareBps, edgeC.CreatedAt-1) // row PRE-DATES the edge

	v, _ := getBasis(t, app, "orgA", "")
	if len(v.Ledger) != 2 {
		t.Fatalf("rows = %d, want 2", len(v.Ledger))
	}
	for _, r := range v.Ledger {
		switch r.DeployingOrg {
		case "orgB":
			if len(r.Attribution) != 1 || r.Attribution[0].RepoURL != "github.com/acme/widgets" || r.Attribution[0].Project != "proj-b" {
				t.Fatalf("orgB row attribution = %+v, want only its own widgets edge", r.Attribution)
			}
		case "orgC":
			if len(r.Attribution) != 0 {
				t.Fatalf("orgC row (written before the edge) claims attribution: %+v", r.Attribution)
			}
		default:
			t.Fatalf("unexpected deploying org %q", r.DeployingOrg)
		}
	}
}

// TestAdminBasisIdentical: support sees EXACTLY what the author sees. Everything but
// the read clock (asOf, which is by definition per-request) is byte-identical.
func TestAdminBasisIdentical(t *testing.T) {
	app, s, fc, fg := mount(t)
	idA := earner(t, app, s, fg)
	deploys(t, app, "orgB", "acme/widgets", "proj-b")
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	_, mine := getBasis(t, app, "orgA", "")
	st, adminBody := req(t, app, http.MethodGet, "/v1/admin/authors/"+idA+"/basis", "admin", true, nil)
	if st != http.StatusOK {
		t.Fatalf("admin basis want 200, got %d (%s)", st, adminBody)
	}
	theirs := envData(t, adminBody)

	// Re-marshal both through map[string]any (Go sorts map keys, so this is canonical)
	// with the per-request clock removed, then demand exact bytes.
	strip := func(raw []byte) []byte {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode: %v (%s)", err, raw)
		}
		if _, ok := m["asOf"]; !ok {
			t.Fatalf("asOf missing — the model must be labelled current: %s", raw)
		}
		delete(m, "asOf")
		out, _ := json.Marshal(m)
		return out
	}
	// envData hands back the data object field-by-field; re-marshal it whole.
	theirsRaw, _ := json.Marshal(theirs)
	if a, b := strip(mine), strip(theirsRaw); !bytes.Equal(a, b) {
		t.Fatalf("admin mirror drifted from the author's own view:\nauthor: %s\nadmin:  %s", a, b)
	}

	// Fail-closed and honest about existence.
	if code, _ := req(t, app, http.MethodGet, "/v1/admin/authors/"+idA+"/basis", "orgA", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin admin-basis want 403, got %d", code)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/admin/authors/aut_nope/basis", "admin", true, nil); code != http.StatusNotFound {
		t.Fatalf("unknown id want 404, got %d", code)
	}
}

// TestBasisPublishesTheCostModel: the disclosed model is blueprint's, not a copy — a
// retune of the rate card or the class footprints must show up here with no edit to
// this package.
func TestBasisPublishesTheCostModel(t *testing.T) {
	app, s, _, fg := mount(t)
	earner(t, app, s, fg)

	v, _ := getBasis(t, app, "orgA", "")
	if v.Method.SpendBasis != "org-metered-total" || v.Method.Period != "utc-month" {
		t.Fatalf("method basis = %+v", v.Method)
	}
	if v.Method.Earning != formulaEarning || v.Method.ComputePrice != formulaComputePrice {
		t.Fatalf("formulas drifted: %+v", v.Method)
	}
	if v.Method.RateCard.MicroUSDPerVCPUHour != 12000 || v.Method.RateCard.MicroUSDPerGBHour != 6000 || v.Method.RateCard.Basis == "" {
		t.Fatalf("rate card = %+v", v.Method.RateCard)
	}
	want := map[string][2]float64{
		"cache": {0.5, 1}, "db": {1, 2}, "other": {0.25, 0.5}, "web": {0.5, 0.5}, "worker": {0.5, 1},
	}
	if len(v.Method.Sizing) != len(want) {
		t.Fatalf("sizing rows = %d, want %d", len(v.Method.Sizing), len(want))
	}
	for _, row := range v.Method.Sizing {
		w, ok := want[row.Type]
		if !ok || row.VCPU != w[0] || row.GB != w[1] {
			t.Fatalf("sizing %+v not in the published table", row)
		}
	}
	if v.AsOf <= 0 {
		t.Fatalf("asOf = %d — the model must be labelled current", v.AsOf)
	}
}
