// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// middleware_starter_test.go covers what apps/starter_test.go cannot reach from
// there: the middleware's own behaviour on a request. The money proofs — that a grant
// lands at the address the gate reads, and lands once — run in package apps against
// the REAL ledger adapter, because only that package can import it.
//
// What is proven here is the part that makes the seam safe to mount app-wide:
//   - it costs ONE ledger look per wallet per process and NOTHING thereafter,
//   - it funds nobody it should not (anonymous callers, the shared signup org),
//   - it never turns a request into an error.
//
// The ledger is a counting fake on purpose. These tests assert HOW OFTEN the money
// layer is touched and BY WHOM; a real ledger would make the count harder to read and
// would re-prove what package apps already proves.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/commerce/billing/creditledger"
	"github.com/zap-proto/zip"
)

// countingFinance is a finance.Client that records how often the money layer is read
// and what address each read named. Balance/usage default to zero, i.e. "a new
// account", so the grant path is the one under test unless a test says otherwise.
type countingFinance struct {
	balanceCalls atomic.Int64
	usageCalls   atomic.Int64
	balanceCents int64
	usageCents   int64
	mu           sync.Mutex
	addrs        []string // (ledger, subject) pairs the middleware asked about
}

func (f *countingFinance) Balance(_ context.Context, org, subject, _ string, _ bool) (money.Amount, error) {
	f.balanceCalls.Add(1)
	f.mu.Lock()
	f.addrs = append(f.addrs, org+"|"+subject)
	f.mu.Unlock()
	return money.FromCents(f.balanceCents), nil
}
func (f *countingFinance) SumUsageSince(_ context.Context, _ string, _ bool, _ int64) (int64, error) {
	f.usageCalls.Add(1)
	return f.usageCents, nil
}
func (f *countingFinance) Deposit(_ context.Context, _ types.DepositInput) (string, error) {
	return "dep_fake", nil
}
func (f *countingFinance) RecordUsage(_ context.Context, _ types.UsageInput) error { return nil }

// countingLedger records every credit the middleware issues.
type countingLedger struct {
	credits atomic.Int64
	mu      sync.Mutex
	inputs  []creditledger.CreditInput
}

func (l *countingLedger) Credit(_ context.Context, in creditledger.CreditInput) (string, int64, error) {
	l.credits.Add(1)
	l.mu.Lock()
	l.inputs = append(l.inputs, in)
	l.mu.Unlock()
	return "tx_fake", in.AmountCents, nil
}
func (l *countingLedger) Balance(_ context.Context, _, _ string) (int64, error) { return 0, nil }

// wireFakes installs the counting pair as the process-wide money layer and clears the
// hot-path cache, so each test starts from a cold process.
func wireFakes(t *testing.T) (*countingFinance, *countingLedger) {
	t.Helper()
	fin, led := &countingFinance{}, &countingLedger{}
	finance.Publish(fin)
	creditledger.Set(led)
	starterSeen = sync.Map{}
	t.Cleanup(func() {
		finance.Publish(nil)
		creditledger.Set(nil)
		starterSeen = sync.Map{}
	})
	return fin, led
}

// starterApp mounts StarterGrant in front of a trivial handler.
func starterApp() *zip.App {
	app := zip.New(zip.Config{})
	app.Use(StarterGrant())
	app.Get("/probe", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) })
	return app
}

// hit drives ONE request as the given principal and returns its status (0 on
// transport error). owner=="" sends no principal headers at all — an anonymous
// caller. These headers are what SanitizeIdentity mints from a verified token;
// forging them at the edge is that middleware's concern and has its own tests, so
// here they simply stand in for an already-validated principal.
//
// starterHit takes no *testing.T and never calls Fatalf, so the concurrency test can call it
// from goroutines (t.Fatalf off the test goroutine is undefined behaviour).
// selected is the org the caller is ACTING IN (X-Org-Id — principal.BillingOrg, the
// payer of record); home is their own org (X-User-Owner, the validated `owner` claim).
// They differ only for an org-switched member, which is exactly the case the grant
// must refuse.
func starterHit(app *zip.App, home, selected, userID string) int {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if home != "" {
		req.Header.Set("X-User-Id", userID)
		req.Header.Set("X-User-Owner", home)
	}
	if selected != "" {
		req.Header.Set("X-Org-Id", selected)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		return 0
	}
	return resp.StatusCode
}

// starterProbe drives n requests from a caller AT HOME (selected == home), the normal
// case, and returns the last status.
func starterProbe(t *testing.T, n int, owner, userID string) int {
	t.Helper()
	app := starterApp()
	status := 0
	for i := 0; i < n; i++ {
		status = starterHit(app, owner, owner, userID)
	}
	return status
}

// TestStarterGrant_HotPathTouchesTheLedgerOnce is the cost proof. The middleware is
// mounted app-wide, so a signed-in user hits it on EVERY request; if it read the
// ledger each time it would put a SQLite transaction in front of all authenticated
// traffic. Fifty requests from one principal must produce exactly one look.
func TestStarterGrant_HotPathTouchesTheLedgerOnce(t *testing.T) {
	fin, led := wireFakes(t)

	if got := starterProbe(t, 50, "acme", "alice"); got != http.StatusOK {
		t.Fatalf("probe status %d, want 200", got)
	}
	if n := fin.balanceCalls.Load(); n != 1 {
		t.Fatalf("balance reads = %d over 50 requests, want exactly 1 — the hot path is hitting the ledger", n)
	}
	if n := led.credits.Load(); n != 1 {
		t.Fatalf("credits = %d over 50 requests, want exactly 1", n)
	}
}

// TestStarterGrant_CreditsTheGateAddress proves the middleware hands the ledger the
// wallet principal.WalletOf resolved — the same address the gate reads — rather than
// an org name it made up. For a real org that is the pool: Subject == the bare slug.
func TestStarterGrant_CreditsTheGateAddress(t *testing.T) {
	_, led := wireFakes(t)
	starterProbe(t, 1, "acme", "alice")

	if led.credits.Load() != 1 {
		t.Fatalf("expected exactly one credit, got %d", led.credits.Load())
	}
	in := led.inputs[0]
	want := account.Payer(account.Credential{Owner: "acme", Name: "alice"}).Subject()
	if in.Org != "acme" || in.Subject != want {
		t.Fatalf("credited (org=%q subject=%q), want (acme, %q) — the address is not account.Payer's", in.Org, in.Subject, want)
	}
	if in.IdempotencyKey != "starter:"+want {
		t.Fatalf("idempotency key = %q, want %q (the ADDRESS, nothing else)", in.IdempotencyKey, "starter:"+want)
	}
	if in.AmountCents != 500 {
		t.Fatalf("amount = %d, want 500 (server-authoritative)", in.AmountCents)
	}
}

// TestStarterGrant_AnonymousIsNotFunded: no validated principal, no wallet, no money.
// An anonymous caller must never cause a ledger write — otherwise an unauthenticated
// request could mint credit against a forged org.
func TestStarterGrant_AnonymousIsNotFunded(t *testing.T) {
	fin, led := wireFakes(t)
	if got := starterProbe(t, 5, "", ""); got != http.StatusOK {
		t.Fatalf("anonymous probe status %d, want 200 (funding must never reject)", got)
	}
	if fin.balanceCalls.Load() != 0 || led.credits.Load() != 0 {
		t.Fatalf("anonymous caller touched money: balance=%d credits=%d, want 0/0",
			fin.balanceCalls.Load(), led.credits.Load())
	}
}

// TestStarterGrant_SignupOrgIsNotFunded pins the exclusion. Members of the shared
// signup org are strangers who each pay from their own wallet inside Hanzo's staff
// books; they have a login, not an account, and they get their grant when they land
// in a real org. Funding them here would pay twice for one human and put consumer
// credit in the platform's own ledger.
func TestStarterGrant_SignupOrgIsNotFunded(t *testing.T) {
	fin, led := wireFakes(t)
	if got := starterProbe(t, 3, account.SignupOrg, "alice"); got != http.StatusOK {
		t.Fatalf("signup-org probe status %d, want 200", got)
	}
	if fin.balanceCalls.Load() != 0 || led.credits.Load() != 0 {
		t.Fatalf("signup-org member was funded: balance=%d credits=%d, want 0/0",
			fin.balanceCalls.Load(), led.credits.Load())
	}
}

// TestStarterGrant_DistinctAccountsEachGetOne proves the cache keys on the wallet and
// not on something process-wide: three principals in three orgs each get funded once.
func TestStarterGrant_DistinctAccountsEachGetOne(t *testing.T) {
	_, led := wireFakes(t)
	for _, org := range []string{"acme", "globex", "initech"} {
		starterProbe(t, 4, org, "founder")
	}
	if n := led.credits.Load(); n != 3 {
		t.Fatalf("credits = %d, want 3 (one per account, regardless of request count)", n)
	}
}

// TestStarterGrant_SwitchedOrgIsNotFunded is the ABUSE GATE, and the reason the grant
// keys on the caller's home org rather than on whichever wallet is currently paying.
//
// The payer of record is the SELECTED org, and IAM's signed `orgs` claim lets a person
// act in any org they belong to. Creating orgs is unlimited and personal slugs
// auto-suffix, so funding "the wallet that is paying" would mint $5 per org to anyone
// who can click New Organization. A caller acting away from home gets nothing.
func TestStarterGrant_SwitchedOrgIsNotFunded(t *testing.T) {
	fin, led := wireFakes(t)
	app := starterApp()

	// alice's home is acme; she has switched into globex, an org she also belongs to.
	for i := 0; i < 5; i++ {
		if got := starterHit(app, "acme", "globex", "alice"); got != http.StatusOK {
			t.Fatalf("switched-org probe status %d, want 200", got)
		}
	}
	if fin.balanceCalls.Load() != 0 || led.credits.Load() != 0 {
		t.Fatalf("a switched-into org was funded: balance=%d credits=%d, want 0/0 — $5 can be minted per org created",
			fin.balanceCalls.Load(), led.credits.Load())
	}

	// Same person, back home: funded exactly once.
	starterHit(app, "acme", "acme", "alice")
	if n := led.credits.Load(); n != 1 {
		t.Fatalf("credits at home = %d, want 1 — the home-org guard is refusing a legitimate grant", n)
	}
}

// TestStarterGrant_FundedAccountIsNotGranted is the retroactive-payout guard at the
// middleware level: on the first request after a deploy every existing org is
// "unseen", and a funded one must still be skipped.
func TestStarterGrant_FundedAccountIsNotGranted(t *testing.T) {
	fin, led := wireFakes(t)
	fin.balanceCents = 2500 // an existing customer with money

	starterProbe(t, 3, "acme", "alice")

	if n := led.credits.Load(); n != 0 {
		t.Fatalf("credits = %d for a funded account, want 0 — a retroactive starter credit was paid", n)
	}
}

// TestStarterGrant_SpentAccountIsNotGranted: zero balance is not enough to call an
// account new. An org that has metered usage has a history, so it is skipped even at
// exactly $0.
func TestStarterGrant_SpentAccountIsNotGranted(t *testing.T) {
	fin, led := wireFakes(t)
	fin.balanceCents = 0
	fin.usageCents = 1000 // has spent before

	starterProbe(t, 3, "acme", "alice")

	if n := led.credits.Load(); n != 0 {
		t.Fatalf("credits = %d for a spent-out account, want 0 — zero balance was mistaken for a new account", n)
	}
}

// TestStarterGrant_ConcurrentRequestsGrantOnce covers the cold-start burst: many
// requests for the same brand-new account arriving together, before anything is
// cached. The ledger's address-keyed Ref is the real barrier (proven in package apps
// against the real store); this asserts the middleware does not multiply the work.
func TestStarterGrant_ConcurrentRequestsGrantOnce(t *testing.T) {
	_, led := wireFakes(t)
	app := starterApp()

	const racers = 20
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < racers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			starterHit(app, "acme", "acme", "alice")
		}()
	}
	start.Done()
	done.Wait()

	if n := led.credits.Load(); n != 1 {
		t.Fatalf("credits = %d from %d concurrent cold-start requests, want 1", n, racers)
	}
}

// TestStarterGrant_LedgerFailureDoesNotFailTheRequest: funding is not authorization.
// With no money layer at all the middleware must be a pass-through — the account
// simply holds no credit, and the GATE (which reads the ledger, not a grant flag) is
// what refuses it.
func TestStarterGrant_LedgerFailureDoesNotFailTheRequest(t *testing.T) {
	finance.Publish(nil)
	creditledger.Set(nil)
	starterSeen = sync.Map{}
	t.Cleanup(func() { starterSeen = sync.Map{} })

	if got := starterProbe(t, 3, "acme", "alice"); got != http.StatusOK {
		t.Fatalf("status %d with no ledger, want 200 — a funding failure must never reject a request", got)
	}
}
