package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// GET /v1/admin/usage answered {"spendCents":0} for the whole fleet in production
// while /v1/usage/summary — same deployment, same customers — reported real money.
// The handler read commerce over HTTP at /v1/billing/usage/rollup, a route registered
// in no binary we ship, and dropped the error; and where the endpoint is not wired at
// all (the deployed shape: one process per app, so admin publishes no ledger and
// resolves no commerce base) the client answers (0, nil) and there is not even an
// error to drop.
//
// THE TRAP THESE TESTS EXIST TO AVOID. The suite was green throughout, because
// newFakeCommerce stands up an HTTP commerce that production does not have. A test
// that supplies the missing upstream proves the handler can read an upstream, which
// was never in doubt; it cannot see the defect, because the defect IS the upstream's
// absence. So every test below models the arrangement the binary actually ships in —
// NO commerce endpoint — and differs only in what money plane is there instead.
//
// The behaviour under test is one sentence: a total this endpoint reports is either
// backed by a read that answered, or it says which one did not.

// usageLedger is the co-resident finance ledger (types.FinanceClient), the seam
// core.OrgMoney prefers. It records the orgs it was asked about so a test can prove
// WHICH tenant a scoped caller reads, and can fail on demand so a test can prove an
// unreadable ledger is never rendered as a zero month.
type usageLedger struct {
	spend map[string]int64 // org -> metered cents over the window
	bal   map[string]int64 // org -> prepaid cents
	fail  map[string]bool  // org -> this org's read errors
	err   error            // every org's read errors
	asked []string
}

func (l *usageLedger) refuse(org string) error {
	if l.err != nil {
		return l.err
	}
	if l.fail[org] {
		return errors.New("ledger: " + org + " unreadable")
	}
	return nil
}

func (l *usageLedger) SumUsageSince(_ context.Context, org string, _ bool, _ int64) (int64, error) {
	l.asked = append(l.asked, org)
	if err := l.refuse(org); err != nil {
		return 0, err
	}
	return l.spend[org], nil
}

func (l *usageLedger) Balance(_ context.Context, org, _, _ string, _ bool) (money.Amount, error) {
	if err := l.refuse(org); err != nil {
		return money.Zero(), err
	}
	return money.FromCents(l.bal[org]), nil
}

func (l *usageLedger) Deposit(context.Context, types.DepositInput) (string, error) { return "", nil }
func (l *usageLedger) RecordUsage(context.Context, types.UsageInput) error         { return nil }

// publishLedger installs l as the process-wide money seam for the length of the test.
func publishLedger(t *testing.T, l *usageLedger) {
	t.Helper()
	finance.Publish(l)
	t.Cleanup(func() { finance.Publish(nil) })
}

// readUsage drives GET /v1/admin/usage and returns the decoded envelope plus the raw
// bytes, because one of the properties under test (a healthy answer carries no
// sources key) is about the bytes and not about the decoded value.
func readUsage(t *testing.T, do func(string, string, map[string]string) (*http.Response, []byte), path string, hdr map[string]string) (usageData, string, string, []byte) {
	t.Helper()
	resp, body := do("GET", path, hdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d (body=%s)", path, resp.StatusCode, body)
	}
	var env struct {
		Status string     `json:"status"`
		Msg    string     `json:"msg"`
		Data   *usageData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode %s: %v (body=%s)", path, err, body)
	}
	if env.Data == nil {
		return usageData{}, env.Status, env.Msg, body
	}
	return *env.Data, env.Status, env.Msg, body
}

var superHdrUsage = map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "admin/z"}

// degradedMoney returns the money source row, or fails the test when the answer
// reported none — which is the confident zero this whole file is about.
func degradedMoney(t *testing.T, d usageData) core.SourceStatus {
	t.Helper()
	if len(d.Sources) == 0 {
		t.Fatalf("a money read that did not answer was reported as a clean total: %+v", d)
	}
	for _, s := range d.Sources {
		if s.Name == "commerce" {
			return s
		}
	}
	t.Fatalf("no money source named in sources: %+v", d.Sources)
	return core.SourceStatus{}
}

// TestUsage_ReadsTheLedgerWithNoHTTPCommerce is the defect, in production's own
// arrangement: NO commerce endpoint anywhere, the money in the co-resident ledger.
// The old handler had one way to ask and it was the missing one, so this answered 0
// for every org. It must answer the ledger's real figures.
func TestUsage_ReadsTheLedgerWithNoHTTPCommerce(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	publishLedger(t, &usageLedger{
		spend: map[string]int64{"hanzo": 12_000, "acme": 3_831},
		bal:   map[string]int64{"hanzo": 5_000, "acme": 1_000},
	})

	// commerceURL "" IS the deployed shape — apps are their own binaries, so the
	// process serving /v1/admin/* has no commerce base and no commerce beside it.
	do := mount(t, iam.server.URL, "", "")

	d, status, msg, body := readUsage(t, do, "/v1/admin/usage", superHdrUsage)
	if status != core.OK {
		t.Fatalf("status = %q (%s), want ok", status, msg)
	}
	if d.Totals.SpendCents != 15_831 { // hanzo 12000 + acme 3831
		t.Errorf("fleet spend = %d, want 15831 — the ledger holds it and this endpoint reported 0",
			d.Totals.SpendCents)
	}
	// A read that answered says nothing extra: the healthy wire is what it always was.
	if len(d.Sources) != 0 {
		t.Errorf("a healthy read must name no degraded source, got %+v", d.Sources)
	}
	if strings.Contains(string(body), `"sources"`) {
		t.Errorf("healthy response must be byte-identical to the old wire (no sources key): %s", body)
	}
	// The literals stay literals — this fix touched the money total and nothing else.
	if d.Totals.Tokens != 0 || d.Totals.Requests != 0 {
		t.Errorf("tokens/requests must stay 0 (no fleet counter exists to read): %+v", d.Totals)
	}
	if d.Series == nil || len(d.Series) != 0 || d.ByProduct == nil || len(d.ByProduct) != 0 {
		t.Errorf("series/byProduct must stay empty arrays, never fabricated: %+v", d)
	}
}

// TestUsage_NoMoneyPlaneIsNotACleanZero is the property that let the defect hide for
// so long: with NOTHING to read — no ledger, no commerce endpoint — the answer was a
// confident 0, indistinguishable from a fleet that genuinely spent nothing.
func TestUsage_NoMoneyPlaneIsNotACleanZero(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	// No publishLedger, no commerce URL: there is no money plane at all.
	do := mount(t, iam.server.URL, "", "")

	d, status, _, _ := readUsage(t, do, "/v1/admin/usage", superHdrUsage)
	if status != core.OK {
		t.Fatalf("status = %q, want ok (a degraded read still renders the board)", status)
	}
	if d.Totals.SpendCents != 0 {
		t.Fatalf("nothing to read must not invent a number, got %d", d.Totals.SpendCents)
	}
	src := degradedMoney(t, d)
	if src.OK {
		t.Errorf("money source must report not-ok when there was nothing to read: %+v", src)
	}
	if src.Error == "" {
		t.Errorf("a not-ok source must carry the reason: %+v", src)
	}
	if src.Rows != 0 {
		t.Errorf("rows = %d, want 0 — no org's money answered", src.Rows)
	}
}

// TestUsage_LedgerErrorIsNotACleanZero is the same property with the ledger present
// and failing, which is the other way a zero can be wrong.
func TestUsage_LedgerErrorIsNotACleanZero(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	publishLedger(t, &usageLedger{err: errors.New("ledger down")})
	do := mount(t, iam.server.URL, "", "")

	d, _, _, _ := readUsage(t, do, "/v1/admin/usage", superHdrUsage)
	if d.Totals.SpendCents != 0 {
		t.Fatalf("a failing ledger must not produce a number, got %d", d.Totals.SpendCents)
	}
	if src := degradedMoney(t, d); src.OK {
		t.Errorf("money source must be degraded when every read failed: %+v", src)
	}
}

// TestUsage_PartialFleetSaysHowMuchItCovers: one org answers, one does not. The
// honest answer is the real partial total AND the fact that it is partial — an
// undercount presented as the fleet is the failure mode /overview already refuses.
func TestUsage_PartialFleetSaysHowMuchItCovers(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	publishLedger(t, &usageLedger{
		spend: map[string]int64{"hanzo": 12_000, "acme": 3_831},
		bal:   map[string]int64{"hanzo": 5_000},
		fail:  map[string]bool{"acme": true},
	})
	do := mount(t, iam.server.URL, "", "")

	d, _, _, _ := readUsage(t, do, "/v1/admin/usage", superHdrUsage)
	if d.Totals.SpendCents != 12_000 {
		t.Errorf("spend = %d, want 12000 (hanzo read; acme failed)", d.Totals.SpendCents)
	}
	src := degradedMoney(t, d)
	if src.OK {
		t.Errorf("a partial fold must not read healthy: %+v", src)
	}
	if src.Rows != 1 {
		t.Errorf("rows = %d, want 1 — exactly one org's money is in this total", src.Rows)
	}
}

// TestUsage_UnlistableDirectoryIsNotAZero: the fleet branch swallowed a failing
// ListOrgs too, and answered 0 with nothing behind it. There is no partial total to
// give when the directory itself cannot be read, so it must refuse — the shape
// /v1/admin/orgs already uses for a read that failed outright.
func TestUsage_UnlistableDirectoryIsNotAZero(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"status":"error","msg":"iam boom"}`)
	}))
	defer bad.Close()
	publishLedger(t, &usageLedger{spend: map[string]int64{"hanzo": 12_000}})
	do := mount(t, bad.URL, "", "")

	resp, body := do("GET", "/v1/admin/usage", superHdrUsage)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Status string     `json:"status"`
		Msg    string     `json:"msg"`
		Data   *usageData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != core.Err || env.Msg == "" {
		t.Errorf("an unlistable directory must surface an error envelope, got status=%q msg=%q", env.Status, env.Msg)
	}
	if env.Data != nil {
		t.Errorf("a failed read carries no data, got %+v", env.Data)
	}
}

// TestUsage_SingleOrgReadsThatOrgOnly keeps the ?org= branch working: a SuperAdmin
// aiming the read at one tenant gets that tenant's figure and no one else's.
func TestUsage_SingleOrgReadsThatOrgOnly(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	l := &usageLedger{
		spend: map[string]int64{"hanzo": 12_000, "acme": 3_831},
		bal:   map[string]int64{"hanzo": 5_000, "acme": 1_000},
	}
	publishLedger(t, l)
	do := mount(t, iam.server.URL, "", "")

	d, _, _, _ := readUsage(t, do, "/v1/admin/usage?org=acme", superHdrUsage)
	if d.Totals.SpendCents != 3_831 {
		t.Errorf("acme spend = %d, want 3831", d.Totals.SpendCents)
	}
	if len(d.Sources) != 0 {
		t.Errorf("a healthy single-org read names no degraded source: %+v", d.Sources)
	}
	if len(l.asked) != 1 || l.asked[0] != "acme" {
		t.Errorf("ledger asked %v, want exactly [acme] — a named org must not fan out", l.asked)
	}
}

// TestUsage_ScopedCallerStaysPinnedToTheirOwnOrg is the auth property this endpoint
// must never lose: a white-label admin naming ANOTHER tenant reads their own, and the
// money seam is asked for their own org and nothing else. Reading through a shared
// helper must not become a way to widen a cross-tenant read.
func TestUsage_ScopedCallerStaysPinnedToTheirOwnOrg(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	l := &usageLedger{
		spend: map[string]int64{"hanzo": 12_000, "maxpower": 777},
		bal:   map[string]int64{"hanzo": 5_000, "maxpower": 100},
	}
	publishLedger(t, l)
	do := mount(t, iam.server.URL, "", "")

	d, _, _, _ := readUsage(t, do, "/v1/admin/usage?org=hanzo", orgAdminHdr)
	if d.Totals.SpendCents != 777 {
		t.Errorf("scoped spend = %d, want 777 (maxpower's own) — ?org= must not widen the read", d.Totals.SpendCents)
	}
	if len(l.asked) != 1 || l.asked[0] != "maxpower" {
		t.Errorf("ledger asked %v, want exactly [maxpower]", l.asked)
	}
}
