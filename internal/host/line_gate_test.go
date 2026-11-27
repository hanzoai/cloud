package host

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	cloud "github.com/hanzoai/cloud/sdk"
)

// pooled is a member of a tenant org: every member spends the org's own account,
// the one a credit line covers.
var pooled = map[string]string{
	"X-User-Id":    "u_2",
	"X-User-Name":  "buyer",
	"X-Org-Id":     "acme",
	"X-User-Owner": "acme",
}

// An org with no cash and a credit line is served on the line, down to its limit,
// and the next priced call is refused 402 without running. A payment then pays the
// debt first and what is left is cash. Same edge, metering client and ledger the
// prepaid loop runs on: only the money source differs.
func TestALineIsSpentToItsLimitThenRefused(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	prev := finance.Current()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(prev) })
	index(t, &cloud.Config{}, cloud.Plugin{Name: probeSurface, Price: probeCents})
	ctx := context.Background()
	const limit = 4 * probeCents
	if _, err := fin.SetLimit(ctx, "acme", false, money.FromCents(limit)); err != nil {
		t.Fatal(err)
	}

	m := mustClient(t, forbiddenCommerce(t))
	var served atomic.Int64
	app := probeApp(t, m, &served)
	owed := func() int64 {
		f, err := fin.Funds(ctx, "acme", "acme", false)
		if err != nil {
			t.Fatal(err)
		}
		return f.Owed.Cents()
	}
	settled := func(want int64) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); owed() != want; time.Sleep(2 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("owed settled at %d¢, want %d¢", owed(), want)
			}
		}
	}

	for i := int64(1); i <= limit/probeCents; i++ {
		if code, body := call(t, app, http.MethodPost, probePath, pooled); code != http.StatusOK {
			t.Fatalf("call %d on the line: %d (%s)", i, code, body)
		}
		settled(i * probeCents)
	}
	code, body := call(t, app, http.MethodPost, probePath, pooled)
	if code != http.StatusPaymentRequired || !strings.Contains(string(body), "insufficient_balance") {
		t.Fatalf("a call past the line = %d (%s); want 402 insufficient_balance", code, body)
	}
	if got := served.Load(); got != limit/probeCents {
		t.Fatalf("the handler ran %d times; want %d — a call past the line must not run", got, limit/probeCents)
	}

	if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(limit + 50), Ref: "sq_1", Cash: true}); err != nil {
		t.Fatal(err)
	}
	f, err := fin.Funds(ctx, "acme", "acme", false)
	if err != nil {
		t.Fatal(err)
	}
	if f.Cash.Cents() != 50 || f.Owed.Sign() != 0 || f.Line.Cents() != limit {
		t.Fatalf("after paying %d¢: cash %d owed %d line %d; want 50, 0, %d",
			limit+50, f.Cash.Cents(), f.Owed.Cents(), f.Line.Cents(), limit)
	}
}
