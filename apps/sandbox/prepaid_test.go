package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
)

// TestMain FUNDS EVERY TEST BY DEFAULT, because the gate is fail-closed and the
// package's tests have no finance plane: without this, every lease answers 503
// ("the balance could not be read") and a hundred tests about pods, runtimes and
// volumes fail for a reason none of them is about.
//
// The stub is generous rather than exact — these tests assert what a sandbox does,
// not what it costs. The tests that DO assert the gate override it below, which is
// why balanceCents is a var.
//
// The free lane answers spent by default for the same reason: a test that empties
// the wallet is asserting the refusal unless it says otherwise.
func TestMain(m *testing.M) {
	balanceCents = func(context.Context, string, string) (int64, error) { return 1_000_000, nil }
	freeLease = func(context.Context, string, string) (*client.Allowance, error) {
		return &client.Allowance{Plan: "free", Limit: 20, Used: 20, Spent: true, Window: "day"}, nil
	}
	os.Exit(m.Run())
}

// TestALeaseIsRefusedWhenTheWalletCannotCoverItsFirstHour: prepaid means the
// refusal comes BEFORE the pod, and it names the number. A lease was previously
// taken, started and metered afterwards, so an empty wallet held compute and disk
// until the reaper came and every debit went negative behind it.
func TestALeaseIsRefusedWhenTheWalletCannotCoverItsFirstHour(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		prior := balanceCents
		t.Cleanup(func() { balanceCents = prior })
		balanceCents = func(context.Context, string, string) (int64, error) { return 0, nil }

		_, err := afford(context.Background(), account.Org("acme"), "exec", "", "")
		if err == nil {
			t.Fatal("an empty wallet must not take a lease")
		}
		// The refusal says the price, so a caller learns what to top up rather than
		// being told only that something was insufficient.
		if want := fmt.Sprintf("%d cents", quote(context.Background(), "exec", "")); !contains(err.Error(), want) {
			t.Errorf("the refusal must name the hour's price %q, got %v", want, err)
		}
	})
}

// TestAnUnreadableBalanceRefusesRatherThanAdmits is the whole of "fail closed".
// A gate that admits on its own failure is a prepaid gate with a hole in it, and
// the hole opens exactly when the finance plane is unwell — which is when it
// matters most. apps/ai made the opposite mistake one process over and took every
// completion down; this asserts the direction, not the wording.
func TestAnUnreadableBalanceRefusesRatherThanAdmits(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		prior := balanceCents
		t.Cleanup(func() { balanceCents = prior })
		balanceCents = func(context.Context, string, string) (int64, error) {
			return 0, fmt.Errorf("commerce is unwell")
		}
		if _, err := afford(context.Background(), account.Org("acme"), "exec", "", ""); err == nil {
			t.Fatal("a balance that cannot be read must refuse the lease, not admit it")
		}
	})
}

// TestALeaseWithNoWalletIsRefused: cloud.PayerOf answers the zero account for a
// caller whose spend lands nowhere. cloud.RuntimeCharge already bills nothing for such a row —
// this moves that refusal to before the pod instead of after it.
func TestALeaseWithNoWalletIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if _, err := afford(context.Background(), account.Account{}, "exec", "", ""); err == nil {
			t.Fatal("a lease naming no wallet must be refused")
		}
	})
}

// TestTheDiskIsPartOfThePrice: a lease that attaches a volume is quoted for it.
// The storage meter bills for as long as the volume exists, including every parked
// hour, so admitting a lease on the runtime price alone would admit one whose real
// first hour it cannot cover.
func TestTheDiskIsPartOfThePrice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		project := diskSize(Sandbox{Project: "p", Volume: "vol-1"})
		own := diskSize(Sandbox{Volume: diskName("m_1")})
		bare, withDisk, withOwn := quote(ctx, "dev", ""), quote(ctx, "dev", project), quote(ctx, "dev", own)
		if withDisk <= bare || withOwn <= bare {
			t.Fatalf("a disk must raise the quote: %d and %d with one vs %d without", withDisk, withOwn, bare)
		}
		// And it is the disk's SIZE that is charged, not a flat fee per disk: a
		// scratch sandbox's own disk is its working directory's size, and small.
		if gib(project) != 20 || gib(own) != 2 {
			t.Fatalf("a project disk is %d GiB and a sandbox's own is %d, want 20 and 2", gib(project), gib(own))
		}
		if diskSize(Sandbox{Project: "p"}) != "" {
			t.Fatal("a lease with no volume holds no disk")
		}
		// Sizes read the way Kubernetes reads them, rounded up to a whole GiB.
		for size, want := range map[string]int64{"512Mi": 1, "1Gi": 1, "1536Mi": 2, "20Gi": 20} {
			if got := gib(size); got != want {
				t.Errorf("gib(%q) = %d, want %d", size, got, want)
			}
		}
	})
}

// TestParkingStillBillsTheDisk is the leak the park state opened. holding() goes
// false when the pod stops, so a parked lease claims no runtime — and if storing()
// did not answer for the volume, a customer would keep 20 GiB indefinitely for
// nothing.
func TestParkingStillBillsTheDisk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		m := Sandbox{ID: "s1", Org: "acme", Payer: "acme", Class: "dev", Volume: "vol-1", Status: statusParked}
		if holding(m) {
			t.Fatal("a parked lease holds no pod, so it claims no runtime")
		}
		if !storing(m) {
			t.Fatal("a parked lease still holds its volume, so it must claim storage")
		}
		rows := spans(ctx, m)
		if len(rows) != 1 {
			t.Fatalf("a parked lease bills exactly its disk, got %d spans", len(rows))
		}
		if rows[0].Rate <= 0 {
			t.Fatalf("the disk span must carry a rate, got %d", rows[0].Rate)
		}
		// The two meters must claim under DIFFERENT ids, or the advance callback routes
		// both to one column and each undoes the other's claim.
		running := Sandbox{ID: "s1", Org: "acme", Payer: "acme", Class: "dev", Volume: "vol-1", Status: statusRunning}
		both := spans(ctx, running)
		if len(both) != 2 {
			t.Fatalf("a running lease with a volume bills runtime and disk, got %d", len(both))
		}
		if both[0].ID == both[1].ID {
			t.Fatalf("the two spans share the id %q, so one watermark serves both", both[0].ID)
		}
	})
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// broke empties the wallet and answers the free lane with standing (or err),
// counting every lease taken from it.
func broke(t *testing.T, standing *client.Allowance, err error) *int {
	t.Helper()
	priorBalance, priorFree := balanceCents, freeLease
	t.Cleanup(func() { balanceCents, freeLease = priorBalance, priorFree })
	balanceCents = func(context.Context, string, string) (int64, error) { return 0, nil }
	taken := 0
	freeLease = func(_ context.Context, _, payer string) (*client.Allowance, error) {
		taken++
		return standing, err
	}
	return &taken
}

// THE FREE LANE SERVES AN EMPTY WALLET. A free plan's coding run leases a desktop
// with no money behind it: the lease is taken from the plan's free sandboxes, bills
// nobody, and is held at most freeTTL.
func TestAnEmptyWalletLeasesFromTheFreeLane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		taken := broke(t, &client.Allowance{Plan: "free", Limit: 20, Used: 1, Window: "day"}, nil)
		for _, class := range []string{"exec", "dev", "desktop"} {
			got, err := afford(context.Background(), account.Org("acme"), class, "", "")
			if err != nil {
				t.Fatalf("%s: the free lane refused a free plan with sandboxes left: %v", class, err)
			}
			if !got.payer.Zero() || got.ttl != freeTTL {
				t.Fatalf("%s: admitted on %+v, want no payer and the free TTL %d", class, got, freeTTL)
			}
		}
		if *taken != 3 {
			t.Fatalf("three free leases took %d from the allowance", *taken)
		}
		// Its own small disk dies with the lease, so it is free too.
		if _, err := afford(context.Background(), account.Org("acme"), "dev", "", diskSize(Sandbox{Volume: diskName("m_1")})); err != nil {
			t.Fatalf("a free lease with its own disk was refused: %v", err)
		}
	})
}

// Only a lease that keeps nothing past its end is free: a project disk outlives
// the lease, and an android holds most of a node. Neither asks the allowance.
func TestTheFreeLaneKeepsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		taken := broke(t, &client.Allowance{Plan: "free", Limit: 20, Window: "day"}, nil)
		if _, err := afford(context.Background(), account.Org("acme"), "dev", "site", diskSize(Sandbox{Project: "site", Volume: "vol-1"})); err == nil {
			t.Fatal("a project lease was admitted free")
		}
		if _, err := afford(context.Background(), account.Org("acme"), "android", "", ""); err == nil {
			t.Fatal("an android lease was admitted free")
		}
		if *taken != 0 {
			t.Fatalf("leases the free lane cannot serve took %d from the allowance", *taken)
		}
	})
}

// A spent allowance refuses with the price and the count, so the person reads what
// to top up and why the free lane did not serve them.
func TestASpentFreeLaneRefusesWithTheCount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		broke(t, &client.Allowance{Plan: "free", Limit: 6, Used: 6, Spent: true, Window: "hour"}, nil)
		_, err := afford(context.Background(), account.Org("acme"), "desktop", "", "")
		if err == nil {
			t.Fatal("a spent free lane admitted a lease")
		}
		for _, want := range []string{fmt.Sprintf("%d cents", quote(context.Background(), "desktop", "")), "6 free sandboxes this hour", "top up"} {
			if !contains(err.Error(), want) {
				t.Errorf("the refusal %q does not say %q", err, want)
			}
		}
	})
}

// The free lane fails closed like the balance does: an allowance that cannot be
// asked admits nothing.
func TestAnUnreadableFreeLaneRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		broke(t, nil, fmt.Errorf("allowance is unwell"))
		if _, err := afford(context.Background(), account.Org("acme"), "exec", "", ""); err == nil {
			t.Fatal("an unreadable free allowance admitted a lease")
		}
	})
}

// A funded wallet pays and is held as long as it asked; a free lease is held at
// most freeTTL whatever it asked.
func TestAFreeLeaseIsHeldAtMostItsTTL(t *testing.T) {
	paid, free := terms{payer: account.Org("acme")}, terms{ttl: freeTTL}
	if got := lifetime(0, "desktop", paid); got != classes["desktop"].ttl {
		t.Errorf("a paid lease that asked nothing is held %d, want the class default %d", got, classes["desktop"].ttl)
	}
	if got := lifetime(0, "desktop", free); got != freeTTL {
		t.Errorf("a free lease that asked nothing is held %d, want %d", got, freeTTL)
	}
	if got := lifetime(1200, "desktop", free); got != 1200 {
		t.Errorf("a free lease that asked 1200 is held %d", got)
	}
	if got := lifetime(14400, "dev", free); got != freeTTL {
		t.Errorf("a free lease that asked four hours is held %d, want %d", got, freeTTL)
	}
}

// The lease a free plan takes is the row the meters read: no payer, so the runtime
// meter bills nobody, and an expiry no later than freeTTL whatever the class holds.
func TestAFreeLeaseIsRecordedWithNoPayer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		broke(t, &client.Allowance{Plan: "free", Limit: 20, Used: 1, Window: "day"}, nil)
		code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox", `{"class":"dev"}`)
		if code != http.StatusCreated {
			t.Fatalf("a free plan's dev lease = %d %s", code, body)
		}
		var out Sandbox
		if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
			t.Fatalf("lease reply %s: %v", body, err)
		}
		store, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		m, err := store.Get(context.Background(), "acme", out.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.Payer != "" {
			t.Fatalf("a free lease names the payer %q, so the meter bills them", m.Payer)
		}
		if held := m.ExpiresAt - m.CreatedAt; held <= 0 || held > freeTTL {
			t.Fatalf("a free dev lease is held %ds, want at most %d", held, freeTTL)
		}
	})
}

// TestAnUnlimitedPayerLeasesWithAnEmptyWallet: the Unlimited plan pays for a
// lease whatever the wallet holds — $0 included — and never consumes the
// free-sandbox allowance.
func TestAnUnlimitedPayerLeasesWithAnEmptyWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		taken := 0
		priorBalance, priorPlan, priorFree := balanceCents, planAllows, freeLease
		t.Cleanup(func() { balanceCents, planAllows, freeLease = priorBalance, priorPlan, priorFree })
		balanceCents = func(context.Context, string, string) (int64, error) { return 0, nil }
		planAllows = func(ctx context.Context, org string, payer account.Account) bool {
			return true
		}
		freeLease = func(context.Context, string, string) (*client.Allowance, error) {
			taken++
			return &client.Allowance{}, nil
		}

		for _, class := range []string{"exec", "dev", "desktop"} {
			got, err := afford(context.Background(), account.Org("acme"), class, "", "")
			if err != nil {
				t.Fatalf("%s: Unlimited was refused with an empty wallet: %v", class, err)
			}
			if got.payer.Subject() != "acme" {
				t.Fatalf("%s: admitted on %+v, want the Unlimited wallet itself", class, got)
			}
		}
		if taken != 0 {
			t.Fatalf("the Unlimited lane took %d free sandboxes, want none", taken)
		}
	})
}

// An Unlimited customer as commerce answers for one (plan unlimited, tier pro) is
// admitted at an empty wallet, with the real predicate running.
//
// MUTATION: compare the tier in cloud.TierAllows and the first payer is refused.
func TestAnUnlimitedCustomerOfCommerceIsAdmittedAtAnEmptyWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Cleanup(func() { cloud.SetPeers(cloud.Peers{}) })
		cloud.SetPeers(cloud.Peers{
			TierOf: func(ctx context.Context, subject string) (string, error) { return "pro", nil },
			PlanOf: func(ctx context.Context, subject string) (string, error) {
				if subject == account.Org("lease-unlimited").Subject() {
					return "unlimited", nil
				}
				return "pro-3x", nil
			},
		})
		taken := 0
		priorBalance, priorFree := balanceCents, freeLease
		t.Cleanup(func() { balanceCents, freeLease = priorBalance, priorFree })
		balanceCents = func(context.Context, string, string) (int64, error) { return 0, nil }
		freeLease = func(context.Context, string, string) (*client.Allowance, error) {
			taken++
			return &client.Allowance{Plan: "pro", Limit: 20, Used: 20, Spent: true, Window: "day"}, nil
		}

		if _, err := afford(context.Background(), account.Org("lease-unlimited"), "dev", "", ""); err != nil {
			t.Fatalf("an Unlimited customer at a $0 wallet was refused: %v", err)
		}
		if taken != 0 {
			t.Fatalf("the Unlimited customer drew on the free allowance %d times, want none", taken)
		}
		// The same tier on another plan is not Unlimited: it is refused on the count.
		if _, err := afford(context.Background(), account.Org("lease-pro-3x"), "dev", "", ""); err == nil || !contains(err.Error(), "free sandboxes this day are used") {
			t.Fatalf("a pro-3x customer at a $0 wallet read %v, want the free allowance refusal", err)
		}
	})
}

// TestANonUnlimitedPayerIsStillRefusedWithAnEmptyWallet: once the free lane is
// used, a non-Unlimited plan's $0 wallet is refused with the price and the count.
func TestANonUnlimitedPayerIsStillRefusedWithAnEmptyWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		taken := 0
		priorBalance, priorPlan, priorFree := balanceCents, planAllows, freeLease
		t.Cleanup(func() { balanceCents, planAllows, freeLease = priorBalance, priorPlan, priorFree })
		balanceCents = func(context.Context, string, string) (int64, error) { return 0, nil }
		planAllows = func(ctx context.Context, org string, payer account.Account) bool {
			return false
		}
		freeLease = func(context.Context, string, string) (*client.Allowance, error) {
			taken++
			return &client.Allowance{Plan: "free", Limit: 6, Used: 6, Spent: true, Window: "hour"}, nil
		}

		_, err := afford(context.Background(), account.Org("acme"), "desktop", "", "")
		if err == nil {
			t.Fatal("a non-Unlimited empty wallet must not take a lease")
		}
		for _, want := range []string{fmt.Sprintf("%d cents", quote(context.Background(), "desktop", "")), "6 free sandboxes this hour", "top up"} {
			if !contains(err.Error(), want) {
				t.Errorf("the refusal %q does not say %q", err, want)
			}
		}
		if taken != 1 {
			t.Fatalf("the free lane was taken %d times for a refused lease, want exactly 1", taken)
		}
	})
}

// A busy paid lease is carried toward the ceiling; a busy free one is held to its
// TTL, since the free lane grants freeTTL and no more.
func TestAFreeLeaseIsNotCarriedPastItsTTL(t *testing.T) {
	const now = 1_800_000_000
	paid := Sandbox{ID: "m_1", Payer: "acme", Status: statusRunning, ExpiresAt: now + 60}
	if !carried(paid, now) {
		t.Fatal("a busy paid lease a minute from its end is not carried")
	}
	free := paid
	free.Payer = ""
	if carried(free, now) {
		t.Fatal("a busy free lease is carried past freeTTL")
	}
}

// A free sandbox lease (admitted.payer.Zero()) must not record the class's
// one-shot startup fee against the caller's wallet.
func TestAFreeLeaseRecordsNoStartupFee(t *testing.T) {
	t.Setenv("SANDBOX_FEE_CENTS_DESKTOP", "25")
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		var debits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/billing/usage" {
				debits++
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		t.Cleanup(srv.Close)
		mc, err := meter.New(meter.Config{BaseURL: srv.URL, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		s.Bill = cloud.NewMeter(cloud.Deps{Metering: mc}, "sandbox")

		broke(t, &client.Allowance{Plan: "free", Limit: 20, Used: 1, Window: "day"}, nil)
		code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox", `{"class":"desktop"}`)
		if code != http.StatusCreated {
			t.Fatalf("free desktop lease = %d %s", code, body)
		}
		if debits != 0 {
			t.Fatalf("free desktop lease recorded %d startup fee debits, want 0", debits)
		}
	})
}
