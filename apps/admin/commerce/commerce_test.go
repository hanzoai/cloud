package commerce

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// planClient points a Client at a stub commerce serving one canned
// /v1/billing/subscriptions body.
func planClient(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

// subsPeer serves commerce's finance.subs op on commerce's own socket — the way
// production answers it, now that commerce is a plugin in this binary rather
// than a deployment of its own.
//
// It replaces an httptest stub of /v1/billing/subscriptions. That endpoint belonged
// to a standalone commerce there is no longer any of, and a test that keeps
// stubbing it proves the reader can parse a shape nothing serves.
func subsPeer(t *testing.T, rows []plane.Sub) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ac")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[plane.SubsIn, plane.Subs](app, "/finance/subs",
		func(context.Context, *plane.SubsIn) (*plane.Subs, error) {
			return &plane.Subs{Rows: rows}, nil
		}, zip.WithOperationID(plane.FinanceSubs))
	go func() { _ = app.Listen(zip.SocketPath("commerce")) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	path := zip.SocketPath("commerce")
	for range 400 {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening — Plan would read no peer and answer pay-as-you-go", path)
}

// Plan must READ commerce's mrrCents, not re-derive MRR from price and
// interval. It used to re-derive it, with its own copy of commerce's
// normalization and no reading of quantity at all — so a 10-seat $20/seat plan
// showed $20 here and $200 in commerce's own rollup. The interval arithmetic is
// pinned where it lives now, in commerce's api/billing.
func TestPlanReadsCommerceMRR(t *testing.T) {
	subsPeer(t, []plane.Sub{
		{Status: "active", MRRCents: 20000, PlanName: "team"},
		{Status: "active", MRRCents: 1000, PlanName: "pro"},
	})
	c := planClient(t, `{}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 21000 {
		t.Errorf("MRR = %d, want 21000 (the sum of what commerce reported)", got.MRR)
	}
	if !got.Active {
		t.Error("Active = false with an active subscription")
	}
	if got.Name != "team" {
		t.Errorf("Name = %q, want team (the first active plan)", got.Name)
	}
}

// The seat-inclusive figure must survive verbatim. This is the case the old
// re-derivation got wrong: it saw price 2000 and reported 2000.
func TestPlanDoesNotRederiveFromPrice(t *testing.T) {
	// The seat-inclusive figure commerce computed. Price and interval are NOT on
	// this wire at all now — the reader cannot re-derive what it is never sent,
	// which is a stronger guarantee than a test that it chose not to.
	subsPeer(t, []plane.Sub{{Status: "active", MRRCents: 20000, PlanName: "team"}})
	c := planClient(t, `{}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 20000 {
		t.Errorf("MRR = %d, want 20000 — price/interval must not be re-normalized here", got.MRR)
	}
}

// Revenue and entitlement part ways here, and both answers are pinned.
//
// This asserted MRR 1500 from the trialing row, because the surface counted
// "active" and "trialing" alike. commerce's rollup never did, so the money
// board and the SaaS board reported different revenue for the same account.
// subscription.Status.CountsTowardMRR settles it: a trial is not revenue —
// nobody has been charged — so the expected total is 0, not a weakened
// assertion.
//
// The trial still names the plan and still marks the subject subscribed. It IS
// a live plan; it just is not money yet.
func TestPlanCountsNoRevenueForTrialOrCanceled(t *testing.T) {
	subsPeer(t, []plane.Sub{
		{Status: "canceled", MRRCents: 50000, PlanName: "enterprise"},
		{Status: "trialing", MRRCents: 1500, PlanName: "pro"},
	})
	c := planClient(t, `{}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 0 {
		t.Errorf("MRR = %d, want 0 (a trial is not revenue; canceled is over)", got.MRR)
	}
	if !got.Active {
		t.Error("Active = false, want true — a trialing subject is subscribed")
	}
	if got.Name != "pro" {
		t.Errorf("Name = %q, want pro", got.Name)
	}
}

// An active subscription alongside a trial contributes exactly its own MRR, so
// the trial neither adds to nor suppresses real revenue.
func TestPlanCountsActiveAlongsideTrial(t *testing.T) {
	subsPeer(t, []plane.Sub{
		{Status: "active", MRRCents: 9900, PlanName: "pro"},
		{Status: "trialing", MRRCents: 1500, PlanName: "enterprise"},
	})
	c := planClient(t, `{}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 9900 {
		t.Errorf("MRR = %d, want 9900 (the trial adds nothing)", got.MRR)
	}
}

// No subscriptions is an honest zero and "pay-as-you-go", never an error and
// never a fabricated tier.
func TestPlanWithNoSubscriptions(t *testing.T) {
	subsPeer(t, nil)
	c := planClient(t, `{}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 0 || got.Active || got.Name != "pay-as-you-go" {
		t.Errorf("Plan = %+v, want {pay-as-you-go 0 false}", got)
	}
}

// The same guard, now on the wire that carries it. MRR travels as commerce's own
// figure rather than being re-derived here, so what this pins is that the field
// SURVIVES the trip: if it stopped, every revenue board would report zero rather
// than fail, which is the failure mode worth a test.
func TestSubsWireCarriesMRRCents(t *testing.T) {
	var subs plane.Subs
	if err := json.Unmarshal([]byte(`{"rows":[{"status":"active","mrrCents":4242,"planName":"Pro"}]}`), &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs.Rows) != 1 || subs.Rows[0].MRRCents != 4242 {
		t.Fatalf("decoded %+v, want one row with MRRCents 4242", subs.Rows)
	}
	if subs.Rows[0].PlanName != "Pro" {
		t.Fatalf("plan name did not survive: %+v", subs.Rows[0])
	}
}

// The org namespace is selected by X-Org-Id, and the wallet is keyed under the
// BARE org slug — never X-IAM-Org-Id, never an "org/org" subject. Commerce's
// EdgeAuth trusts that one header (after verifying the service-token bearer) and
// resolves the billing namespace from it, so a wrong header or a wrong subject
// resolves an EMPTY wallet: that pair is the $0-fleet-revenue bug, where every
// board read zero against real balances (lux $10,000, maxpower $20,498).
//
// It is pinned HERE, on the client that sets the header, rather than in a caller's
// fake. It rides LEDGER rather than Plan: the money reads and now the plan read
// left HTTP for the plane, and the ledger is what still carries this header. When
// the last read moves, this test and the header go together.
func TestReadsCarryTheBareOrgSlugAsXOrgId(t *testing.T) {
	var gotOrg, gotStale, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg, gotStale = r.Header.Get("X-Org-Id"), r.Header.Get("X-IAM-Org-Id")
		gotUser = r.URL.Query().Get("user")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"transactions":[]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL).Ledger(context.Background(), "acme", 0); err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if gotOrg != "acme" {
		t.Errorf("X-Org-Id = %q, want acme — commerce resolves the namespace from this header alone", gotOrg)
	}
	if gotStale != "" {
		t.Errorf("X-IAM-Org-Id = %q, want unset — commerce reads X-Org-Id only", gotStale)
	}
	if gotUser != "acme" {
		t.Errorf("user = %q, want acme (the bare slug) — an org/org subject resolves an empty wallet", gotUser)
	}
}
