package cloud

// Tests for the ONE spend predicate and the gate that applies it.
//
// They drive real requests through the zip/fiber stack against a fake finance
// ledger published on the SAME process-wide client the ai gate and the edge meter
// resolve through (finance.Publish), so the address the gate reads is the address a
// debit would write. Nothing about the predicate is mocked.

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/zap-proto/zip"
)

// ── harness ─────────────────────────────────────────────────────────────────────

// spendLedger is an in-memory finance ledger. It answers every address with the same
// balance (the tables are about the VERDICT), but records the address it was asked
// for so a dedicated test can assert the gate reads the wallet the debit writes.
type spendLedger struct {
	credit  money.Amount
	err     error
	reads   int
	ledger  string // last Balance() org
	account string // last Balance() subject
}

var _ money.FinanceClient = (*spendLedger)(nil)

func (f *spendLedger) Balance(_ context.Context, org, subject, _ string, _ bool) (money.Amount, error) {
	f.reads++
	f.ledger, f.account = org, subject
	if f.err != nil {
		return money.Zero(), f.err
	}
	return f.credit, nil
}

func (f *spendLedger) Deposit(context.Context, money.DepositInput) (string, error) { return "", nil }

func (f *spendLedger) RecordUsage(context.Context, money.UsageInput) error { return nil }

func (f *spendLedger) SumUsageSince(context.Context, string, bool, string, string, int64) (int64, error) {
	return 0, nil
}

// publishLedger installs a fake ledger as the process-wide money client, restoring the
// prior one on cleanup. nil models a split deploy: no co-resident money layer.
func publishLedger(t *testing.T, f *spendLedger) {
	t.Helper()
	prev := finance.Current()
	if f == nil {
		finance.Publish(nil)
	} else {
		finance.Publish(f)
	}
	t.Cleanup(func() { finance.Publish(prev) })
}

// atto builds an exact 18-decimal credit amount from a whole-atto integer, so the
// admit boundary is asserted at the true unit — no cents, no rounding.
func atto(n int64) money.Amount { return money.FromAtto(big.NewInt(n)) }

// planStub is the subscription authority. paid/err drive the licence leg.
type planStub struct {
	paid bool
	err  error
	// CommerceClient is embedded (nil) purely so planStub satisfies the interface
	// SpendGate takes; only ActivePaidPlan is ever called.
	CommerceClient
}

func (p *planStub) ActivePaidPlan(context.Context, string) (string, bool, error) {
	if p.err != nil {
		return "", false, p.err
	}
	return "pro", p.paid, nil
}

// call drives one request and returns status + body.
func call(t *testing.T, app *zip.App, method, path string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, buf[:n]
}

// member is a plain validated, non-admin principal in org `hanzo` — precisely the
// self-serve signup shape the free-inference hole lives in.
var member = map[string]string{
	"X-User-Id":    "u_1",
	"X-User-Name":  "stranger",
	"X-Org-Id":     "hanzo",
	"X-User-Owner": "hanzo",
}

// TestAPaidReadNamesAnOperationTheFleetServes reads the routers' own documents. A
// name here that no surface answers to is not inert: it makes standing a condition
// of a path nobody charges for, which is a 402 in front of free work. spend.go has
// carried that failure twice under other names; this is the same check for this list.
func TestAPaidReadNamesAnOperationTheFleetServes(t *testing.T) {
	for _, root := range paidReads {
		app, _, _ := strings.Cut(strings.TrimPrefix(root, "/v1/"), "/")
		doc := filepath.Join("plugin", app, "openapi.json")
		raw, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("%s names app %q, which publishes no document at %s: %v", root, app, doc, err)
		}
		var spec struct {
			Paths map[string]map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		var served bool
		for path, ops := range spec.Paths {
			if _, ok := ops["get"]; ok && PathUnder(path, root) {
				served = true
				break
			}
		}
		if !served {
			t.Errorf("%s is declared a paid read, but %s publishes no GET at or beneath it. "+
				"Requiring standing for an address nobody serves refuses free work.", root, doc)
		}
	}
}
