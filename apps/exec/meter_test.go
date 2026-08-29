package exec

// Running a snippet leases a sandbox and runs a program in it, so the two facts
// worth pinning are that a funded caller's own org pays for it and that an
// unfunded one is refused BEFORE a pod exists. The second is the one that
// matters: a gate that only skips the debit still hands out the compute.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// billed mounts the real surface with its meter pointed at l.
func billed(t *testing.T, l *planetest.Ledger) *zip.App {
	t.Helper()
	t.Setenv("CODE_EXEC_API_KEY", "k")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// No cloud.Bridge() here: Mount installs it on the subsystem's own router, ahead
	// of its leaves. It used to be installed by hand right at this line, which is the
	// tell the audit turned on — a package whose only Bridge lives in a test is a
	// package proving its org path with the org absent everywhere else.
	if err := Use(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	// Mount binds a meter from deps; replace it with one that has a ledger behind
	// it. Rebinding rather than passing Metering through Deps keeps this test
	// pointed at the same client production uses — the package global every handler
	// reads — instead of at a second construction path.
	bindMeter(cloud.NewResourceMeter(cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, "exec"))
	t.Cleanup(func() { bindMeter(nil) })
	return app
}

// runAs posts one program as org. The service key admits the request and the
// identity headers name the caller who pays; a caller with no principal presents
// the key alone and has no wallet.
func runAs(t *testing.T, app *zip.App, org string) *http.Response {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai"+Path,
		strings.NewReader(`{"lang":"py","code":"print(1)"}`))
	rq.Header.Set("X-API-Key", "k")
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("POST %s: %v", Path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// A run bills the caller's own org at the declared fee.
func TestRunBillsTheCaller(t *testing.T) {
	sb := servePeer(t)
	l := planetest.Money(t, 100000)
	app := billed(t, l)

	if resp := runAs(t, app, "acme"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if sb.Ran() != 1 {
		t.Fatalf("programs run = %d, want 1", sb.Ran())
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a run must bill", l.Count())
	}
	org, cents, model, _ := l.Charged()
	if org != "acme" {
		t.Errorf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	if cents != defaultFeeCents {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, defaultFeeCents)
	}
	if model != runKind {
		t.Errorf("debit unit = %q, want %q", model, runKind)
	}
}

// An unfunded org never gets a pod. The gate has to precede the lease, or the
// compute is spent whatever the ledger says afterwards.
func TestUnfundedOrgNeverGetsAPod(t *testing.T) {
	sb := servePeer(t)
	l := planetest.Money(t, 0)
	app := billed(t, l)

	if resp := runAs(t, app, "acme"); resp.StatusCode == http.StatusOK {
		t.Fatal("an unfunded org ran a program; the gate must refuse it")
	}
	if sb.Ran() != 0 {
		t.Fatalf("programs run = %d for an unfunded org, want 0 — the gate must precede the lease", sb.Ran())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused run, want 0", n)
	}
}

// The shared service key carries no tenant, so there is no wallet to charge. The
// run still happens — this is the chat server's endpoint — and bills nobody.
func TestServiceKeyRunBillsNobody(t *testing.T) {
	sb := servePeer(t)
	l := planetest.Money(t, 0) // a balance that must never be consulted
	app := billed(t, l)

	if resp := runAs(t, app, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the service endpoint must keep working", resp.StatusCode)
	}
	if sb.Ran() != 1 {
		t.Fatalf("programs run = %d, want 1", sb.Ran())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d with no principal to bill, want 0", n)
	}
}
