package sandbox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// budgetLedger stands in for the org's ledger at the one gate a lease asks
// (ledgerAllows): it records what it was asked and answers as the ledger would.
type budgetLedger struct {
	mu     sync.Mutex
	asked  []int64
	payers []account.Account
	refuse error // nil admits
}

func (b *budgetLedger) allows(_ *Service, _ context.Context, payer account.Account, cents int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked, b.payers = append(b.asked, cents), append(b.payers, payer)
	return b.refuse
}

func (b *budgetLedger) set(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refuse = err
}

func (b *budgetLedger) calls() ([]int64, []account.Account) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.asked...), append([]account.Account(nil), b.payers...)
}

// budgeting puts a ledger in front of every lease for the rest of the test.
func budgeting(t *testing.T) *budgetLedger {
	t.Helper()
	b := &budgetLedger{}
	prior := ledgerAllows
	t.Cleanup(func() { ledgerAllows = prior })
	ledgerAllows = b.allows
	return b
}

// podCount is how many pods the fake cluster holds, for any sandbox.
func podCount(t *testing.T, s *Service) int {
	t.Helper()
	list, err := s.State.rt.pods().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	return len(list.Items)
}

// overBudget reports that err is the ledger's budget refusal and that it carries
// its status with it: 402 spend_cap_exceeded, which the plane keeps across the
// crossing (a coding run's lease) and the HTTP renderer keeps on the wire. A bare
// sentinel would cross the plane as a 500.
func overBudget(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusPaymentRequired && he.Code == "spend_cap_exceeded"
}

// An org past its budget is refused BEFORE the pod, by its own ledger, with the
// billing refusal that names the budget (402 spend_cap_exceeded), even though its
// wallet holds plenty. The budget is the org's enforced spend cap in its own books;
// a lease that only asked the wallet was compute an over-budget org could keep
// starting, and the only thing left to stop it was the provider's account limit,
// which stops every org on the account at once.
//
// MUTATION: ask the ledger only when the one-shot fee is priced (it is 0 by
// default) and the lease is admitted, a pod starts, and this fails.
func TestAnOrgPastItsBudgetLeasesNoSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ledger := budgeting(t)
		ledger.set(meter.ErrSpendCapExceeded)
		ctx := context.Background()

		_, refused := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if refused == nil {
			t.Fatal("an org past its budget leased a sandbox")
		}
		if !overBudget(refused) {
			t.Fatalf("the refusal is %v, want the ledger's 402 spend_cap_exceeded", refused)
		}
		if n := podCount(t, s); n != 0 {
			t.Fatalf("a refused lease started %d pods; the refusal must come before the pod", n)
		}
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if ids, err := st.IDs(ctx, "acme"); err != nil || len(ids) != 0 {
			t.Fatalf("a refused lease left rows %v (%v)", ids, err)
		}

		// The caller is told it is their budget, on the page that raises it, and not
		// that something upstream is unavailable.
		he := cloud.Denial(refused, "api.hanzo.ai")
		if he.Status != http.StatusPaymentRequired || he.Code != "spend_cap_exceeded" {
			t.Fatalf("the refusal renders %d %q, want 402 spend_cap_exceeded", he.Status, he.Code)
		}

		asked, payers := ledger.calls()
		if len(asked) != 1 || payers[0] != account.Org("acme") {
			t.Fatalf("the ledger was asked %v for %v, want once for acme's own account", asked, payers)
		}
	})
}

// A lease within the budget is weighed at what its first hour will cost (the fee,
// the runtime hour and any disk), the same figure the wallet is weighed at, so a
// budget with less room than one hour refuses the lease rather than admitting it
// and overrunning by that hour.
func TestALeaseIsWeighedAgainstTheBudgetAtItsFirstHour(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ledger := budgeting(t)
		ctx := context.Background()

		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatalf("a lease within its budget was refused: %v", err)
		}
		asked, _ := ledger.calls()
		if want := quote(ctx, "dev", diskSize(m)); len(asked) != 1 || asked[0] != want {
			t.Fatalf("the ledger was asked %v, want once at the first hour's %d cents", asked, want)
		}
	})
}

// A parked sandbox is not given a pod again once its org is past its budget: a
// resume starts compute exactly as a lease does, so it asks the same ledger. The
// row stays parked with its disk, and the resume can be asked again once the
// budget is raised.
func TestAParkedSandboxIsNotResumedPastTheBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ledger := budgeting(t)
		ctx := context.Background()

		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatalf("park: %v", err)
		}

		ledger.set(meter.ErrSpendCapExceeded)
		if _, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID, Class: "dev", Disk: true}); !overBudget(err) {
			t.Fatalf("a resume past the budget answered %v, want the ledger's 402 spend_cap_exceeded", err)
		}
		if n := podCount(t, s); n != 0 {
			t.Fatalf("a refused resume started %d pods", n)
		}
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if back, err := st.Get(ctx, "acme", m.ID); err != nil || back.Status != statusParked {
			t.Fatalf("a refused resume must leave the sandbox parked, got %+v %v", back, err)
		}

		ledger.set(nil)
		if back, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID, Class: "dev", Disk: true}); err != nil || back.Status != statusRunning {
			t.Fatalf("a resume within the budget: %+v %v", back, err)
		}
	})
}

// A free lease bills nobody, so it spends nothing against a budget and the ledger
// is not asked: an org whose wallet is empty keeps its free sandboxes whatever its
// budget says.
func TestAFreeLeaseAsksNoBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ledger := budgeting(t)
		ledger.set(meter.ErrSpendCapExceeded)
		priorBalance, priorFree := balanceCents, freeLease
		t.Cleanup(func() { balanceCents, freeLease = priorBalance, priorFree })
		balanceCents = func(context.Context, string, string) (int64, error) { return 0, nil }
		freeLease = func(context.Context, string, string) (*client.Allowance, error) {
			return &client.Allowance{Plan: "free", Limit: 20, Used: 1, Window: "day"}, nil
		}

		m, err := Lease(s, context.Background(), "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("a free lease was refused: %v", err)
		}
		if m.Payer != "" {
			t.Fatalf("the free lease names the payer %q, want none", m.Payer)
		}
		if asked, _ := ledger.calls(); len(asked) != 0 {
			t.Fatalf("a free lease asked the ledger %v; it spends nothing", asked)
		}
	})
}

// Over HTTP the refusal is the billing 402 a client branches on, naming the page
// that raises the budget, and not a 503 from something upstream.
func TestAnOrgPastItsBudgetIsToldSoOverHTTP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		budgeting(t).set(meter.ErrSpendCapExceeded)

		code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox", `{"class":"exec"}`)
		if code != http.StatusPaymentRequired || !strings.Contains(string(body), "spend_cap_exceeded") {
			t.Fatalf("a lease past the budget answered %d %s, want 402 spend_cap_exceeded", code, body)
		}
		if n := podCount(t, s); n != 0 {
			t.Fatalf("a refused lease started %d pods", n)
		}
	})
}

// A working lease is carried into its next hour only while its org's budget has
// room for that hour, asked of the ledger at the hour's price (hourly), the figure
// the wallet is weighed at for the same carry. The wallet is not the budget: one
// with plenty still does not carry a lease past a spent cap, or a lease admitted
// under budget runs on past it until its max-life (a day; a desktop has none).
//
// MUTATION: drop `&& ask.allow(ctx, m)` from the sweep and the lease is carried.
func TestAWorkingLeaseIsNotCarriedPastItsOrgsBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b := budgeting(t)
		b.set(meter.ErrSpendCapExceeded)
		before, after := sweeping(t, []string{"m_1"}, balance(1<<40))
		if after["m_1"] != before["m_1"] {
			t.Fatalf("a lease past its org's budget was carried: expires %d, was %d", after["m_1"], before["m_1"])
		}
		asked, payers := b.calls()
		if len(asked) != 1 || payers[0].Org() != "acme" {
			t.Fatalf("the ledger was asked %v for %v, want once for acme", asked, payers)
		}
	})
}

// A lease whose budget has room is carried, and the ledger was asked for the next
// hour's price and nothing more: the fee was paid when the lease was taken.
func TestAWorkingLeaseIsCarriedWhileItsBudgetHasRoom(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b := budgeting(t)
		before, after := sweeping(t, []string{"m_1"}, balance(1<<40))
		if after["m_1"]-before["m_1"] < extendBy/2 {
			t.Fatalf("a lease inside its budget was not carried: expires %d, was %d", after["m_1"], before["m_1"])
		}
		asked, _ := b.calls()
		if want := hourly(context.Background(), "dev", ""); len(asked) != 1 || asked[0] != want {
			t.Fatalf("the ledger was asked %v, want once for the next hour (%d cents)", asked, want)
		}
	})
}

// A ledger that does not answer carries nothing and costs the sweep seconds, once
// for the payer: an unanswered budget is no for this sweep, not for good (the
// carry window is ten minutes and the sweep runs every minute), and one wedged
// ledger must not hold up every other org's leases.
//
// MUTATION: ask on the sweep's own context and this hangs; drop the mark for a
// silent ledger and it is asked three times.
func TestALedgerThatNeverAnswersCostsTheCarrySecondsAndOneAsk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var asks int
		prior := ledgerAllows
		t.Cleanup(func() { ledgerAllows = prior })
		ledgerAllows = func(_ *Service, ctx context.Context, _ account.Account, _ int64) error {
			asks++
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		before, after := sweeping(t, []string{"m_1", "m_2", "m_3"}, balance(1<<40))
		if took := time.Since(start); took > balanceWait+2*time.Second {
			t.Fatalf("one unanswered ledger held the sweep for %s", took)
		}
		if asks != 1 {
			t.Fatalf("a silent ledger was asked %d times in one sweep, want once", asks)
		}
		for id := range before {
			if after[id] != before[id] {
				t.Fatalf("%s was carried on a budget nobody could read", id)
			}
		}
	})
}

// A paid lease that reached its expiry because its org's budget stopped carrying
// it ended for money, and its tombstone says so and says it was the budget, so the
// run is told to raise the budget rather than to top up a wallet that is full.
//
// MUTATION: drop the budget branch in clocks.closes and this reads `lease-expired`.
func TestALeaseItsBudgetStoppedCarryingEndsAtTheCostCeiling(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b := budgeting(t)
		spent := func() { b.set(meter.ErrSpendCapExceeded) }
		now := time.Now().Unix()
		_, e := sweptAs(t, spent, `expires_at=$1, last_used_at=$2, connected_at=$2`, now-5, now)
		if e.Reason != costCeiling || !strings.Contains(e.Detail, "budget") {
			t.Fatalf("a run its budget stopped carrying ended as %q (%q), want %q naming the budget", e.Reason, e.Detail, costCeiling)
		}
	})
}
