package billing

// ledger_peer_test.go — the commerce LEDGER fixture, served the way production
// serves it: a plane peer on commerce's own socket.
//
// The ledger read used to reach that list two ways — the plane when commerce
// answered, an HTTP GET of /v1/billing/transactions when it did not. The HTTP
// half was the standalone commerce's endpoint, and there is no standalone commerce:
// it is a plugin in this binary. Worse, the fallback could only fire on ErrNoPeer — "this fleet runs no
// commerce" — and it answered that by dialling CLOUD_COMMERCE_HTTP_URL, which
// production points at commerce.hanzo.svc:8001, a Service selecting
// `app.kubernetes.io/name: cloud` on targetPort 8000. That is this pod's own
// public edge, so the call left the process and re-entered the binary that had
// just said it holds no ledger.
//
// So the fallback is gone and this fixture stands up the real thing. It mirrors
// usage_subcent_peer_test.go, which does the same for the usage op.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// txnRows is the org's ledger as the PEER hands it over: amounts as exact
// decimal Money, kinds as the ledger's own words, timestamps as unix seconds.
//
// Same shape as the JSON fixture it replaces — acme carries two deposits and
// three withdraws, one of them 40 days old so a 30-day window provably excludes
// it; globex carries a distinct set so an isolation break shows up as globex's
// numbers rather than as an empty page.
func txnRows(org string) []plane.Txn {
	now := time.Now().UTC()
	at := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	usd := func(cents int64) plane.Money {
		return plane.Money{Decimal: decimalOf(cents), Currency: "usd"}
	}
	switch org {
	case "acme":
		return []plane.Txn{
			{ID: "d1", Kind: string(finance.KindDeposit), Amount: usd(50000), Memo: "Startup grant", Ref: "trial", CreatedAt: at(48 * time.Hour)},
			{ID: "d2", Kind: string(finance.KindDeposit), Amount: usd(10000), Memo: "Top-up", Ref: "prepaid", CreatedAt: at(24 * time.Hour)},
			{ID: "w1", Kind: string(finance.KindUsage), Amount: usd(1200), Ref: "zen", CreatedAt: at(2 * time.Hour)},
			{ID: "w2", Kind: string(finance.KindUsage), Amount: usd(800), Ref: "embeddings", CreatedAt: at(3 * time.Hour)},
			{ID: "w3", Kind: string(finance.KindUsage), Amount: usd(300), Ref: "zen", CreatedAt: at(40 * 24 * time.Hour)},
		}
	case "globex":
		return []plane.Txn{
			{ID: "gd1", Kind: string(finance.KindDeposit), Amount: usd(900000), Memo: "Enterprise credit", Ref: "prepaid", CreatedAt: at(24 * time.Hour)},
			{ID: "gw1", Kind: string(finance.KindUsage), Amount: usd(5000), Ref: "gpu", CreatedAt: at(1 * time.Hour)},
		}
	}
	return nil
}

// balanceOf is the org's spendable balance in cents: deposits less usage, over the
// same rows the ledger op serves, so the two reads cannot disagree.
func balanceOf(org string) int64 {
	var cents int64
	for _, r := range txnRows(org) {
		v := centsOf(r.Amount.Decimal)
		if r.Kind == string(finance.KindDeposit) {
			cents += v
		} else {
			cents -= v
		}
	}
	return cents
}

// centsOf is decimalOf inverted, for whole-cent fixtures.
func centsOf(dec string) int64 {
	var whole, frac int64
	dot := -1
	for i := 0; i < len(dec); i++ {
		if dec[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return atoi(dec) * 100
	}
	whole, frac = atoi(dec[:dot]), atoi(dec[dot+1:])
	return whole*100 + frac
}

func atoi(s string) int64 {
	var v int64
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			v = v*10 + int64(s[i]-'0')
		}
	}
	return v
}

// decimalOf renders whole cents as the exact decimal string Money carries.
func decimalOf(cents int64) string {
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}
	return neg + itoa(cents/100) + "." + pad2(cents%100)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func pad2(v int64) string {
	s := itoa(v)
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// ledgerPeer serves commerce's finance.txns op on commerce's own socket, and
// waits until it is genuinely listening.
//
// The wait is not politeness: without it the billing read races the peer's bind,
// gets ErrNoPeer, and — now that there is no fallback — fails. A test that raced
// would look like the code is broken, which is the reverse of what it proves.
func ledgerPeer(t *testing.T, org string) {
	t.Helper()
	// Short run dir + a plane reset, so the socket path fits and no op from a
	// previous test answers this one. Same helper balance_outage_test.go uses.
	planeDir(t)
	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[plane.TxnsIn, plane.Txns](app, "/finance/txns",
		func(context.Context, *plane.TxnsIn) (*plane.Txns, error) {
			return &plane.Txns{Rows: txnRows(org)}, nil
		}, zip.WithOperationID(plane.FinanceTxns))
	// Balance rides the same peer: the finance surface reads both, and a peer that
	// served one and not the other would fail for a reason the test is not about.
	zip.Post[plane.BalanceIn, plane.Balance](app, "/finance/balance",
		func(context.Context, *plane.BalanceIn) (*plane.Balance, error) {
			return &plane.Balance{Amount: plane.Money{Decimal: decimalOf(balanceOf(org)), Currency: "usd"}}, nil
		}, zip.WithOperationID(plane.FinanceBalance))
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
	t.Fatalf("%s never began listening — the read would fail for the wrong reason", path)
}

// TestLedgerPeerKindsParse pins the one translation the peer boundary performs:
// the ledger's own words become the single vocabulary the projections read.
func TestLedgerPeerKindsParse(t *testing.T) {
	for _, r := range txnRows("acme") {
		if got := finance.ParseKind(r.Kind); got == "" {
			t.Fatalf("kind %q did not parse; the projections would classify it as nothing", r.Kind)
		}
	}
}
