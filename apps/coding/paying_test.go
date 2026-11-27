package coding

// paying_test.go — a run pays from the account the request that started it named.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// The run context states the tenant alone; the claim is stated on it beside the
// tenant, so the sandbox the run leases is paid from that account — an org owner
// in the signup org from the org's pool, not a personal wallet.
func TestARunPaysFromTheAccountItsRequestNamed(t *testing.T) {
	req := zip.WithCaller(context.Background(), zip.Caller{
		Org: account.SignupOrg, Owner: account.SignupOrg, User: "z", Account: "org:" + account.SignupOrg,
	})
	run, cancel, clk := runContext(account.SignupOrg, 60, 0)
	defer cancel()
	defer clk.end()
	run = paying(run, req)

	who := zip.CallerOf(run)
	who.User = "z" // as sandboxRunner.Run states the person
	if got := meter.PayerFrom(zip.WithCaller(run, who)); got != account.Org(account.SignupOrg) {
		t.Fatalf("the run pays from %q, want the org pool its request named", got.Subject())
	}

	bare, cancel2, clk2 := runContext(account.SignupOrg, 60, 0)
	defer cancel2()
	defer clk2.end()
	if got := paying(bare, context.Background()); zip.CallerOf(got).Account != "" {
		t.Fatalf("a request with no claim stated %q", zip.CallerOf(got).Account)
	}
}
