package apps

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// TestWireInstallsAIHostBindings walks the PRODUCTION path — Wire() then
// cloud.BuildDeps, exactly what every cmd/*/main.go does — and proves each host
// binding the inference module resolves is installed and dispatches to the real
// in-process implementation. The module's setters are package globals with no
// registry to inspect, so a cut that quietly stopped calling them would compile,
// boot, serve, and bill nobody; this is the assertion that makes that impossible.
func TestWireInstallsAIHostBindings(t *testing.T) {
	// The ledger is a per-org SQLite file, and an encryption-capable build refuses to
	// open one unencrypted. One deterministic key roots the at-rest layer for the
	// process; the files themselves are fresh per t.TempDir().
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(key))

	// Start from an empty module so nothing here can pass on a leftover hook.
	aiobject.SetTierReader(nil)
	aiobject.SetBalanceReader(nil)
	aiobject.SetUsageRecorder(nil)
	finance.Publish(nil)
	t.Cleanup(func() {
		cloud.RegisterBillingInstaller(nil)
		finance.Publish(nil)
	})

	Wire()

	if aiobject.BalanceReader() != nil || aiobject.TierReader() != nil {
		t.Fatal("Wire() must only REGISTER the money bindings; they need the built deps")
	}

	cloud.BuildDeps(&cloud.Config{
		Brand:   "hanzo",
		DataDir: t.TempDir(),
		Enable:  []string{"commerce"}, // money layer co-resident (the unified binary)
	})

	tier, balance, usage := aiobject.TierReader(), aiobject.BalanceReader(), aiobject.UsageRecorder()
	if tier == nil {
		t.Error("per-tier SKU gate reader not installed: every tier-gated SKU fails OPEN")
	}
	if balance == nil {
		t.Fatal("prepaid balance reader not installed: the spend gate admits everyone")
	}
	if usage == nil {
		t.Fatal("usage recorder not installed: completions serve free")
	}

	// The bindings must reach the SAME wallet in both directions. Credit a person's
	// wallet inside an org ledger, then read and debit it through the module's hooks:
	// swapping subject and namespace (the shipped bug this guards) would read the org
	// pool instead, so the balance would not be the 500 cents deposited here.
	ctx := context.Background()
	fin := finance.Current()
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme/bob", Amount: money.FromCents(500),
		Currency: "usd", Ref: "test-grant",
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}

	got, err := balance(ctx, "acme/bob", "acme", "usd")
	if err != nil {
		t.Fatalf("balance hook: %v", err)
	}
	if got != 500 {
		t.Fatalf("balance hook read %d cents, want 500 — the gate is not reading the subject's wallet", got)
	}

	// The debit parses the module's decimal-USD string exactly, never floored to a
	// whole cent. Two 1.005 USD calls take 2.01 off 5.00, leaving 2.99 — a floored
	// debit would leave 3.00, and the gate's coarse cents read separates the two.
	// That the balance moves at all is the same-wallet proof: a debit keyed on the
	// org pool would leave this subject's 500 untouched.
	for _, id := range []string{"test-call-1", "test-call-2"} {
		if err := usage(ctx, aiobject.UsageEvent{
			Subject: "acme/bob", Namespace: "acme", USD: "1.005", Currency: "usd",
			Model: "zen", Provider: "hanzo", RequestID: id,
		}); err != nil {
			t.Fatalf("usage hook: %v", err)
		}
	}
	got, err = balance(ctx, "acme/bob", "acme", "usd")
	if err != nil {
		t.Fatalf("balance hook after debit: %v", err)
	}
	if got != 299 {
		t.Fatalf("balance after two 1.005 USD debits = %d cents, want 299", got)
	}
}

// TestIngestDialerFallsBackInline proves the durable-queue binding keeps the
// module's fail-soft contract. The dialer resolves the embedded engine at dial time,
// so with no engine it must report the module's OWN ErrTasksNotConfigured — the
// sentinel the ingest handler matches on to run the ingest inline. Any other error
// would surface as a 5xx on a path that is supposed to degrade silently.
func TestIngestDialerFallsBackInline(t *testing.T) {
	Wire()

	if _, err := dialTasks("acme"); !errors.Is(err, aiobject.ErrTasksNotConfigured) {
		t.Fatalf("dialTasks with no embedded engine = %v, want ErrTasksNotConfigured", err)
	}
	if _, err := aiobject.EnqueueIngest(context.Background(), "acme", &aiobject.IngestRequest{}, "en"); !errors.Is(err, aiobject.ErrTasksNotConfigured) {
		t.Fatalf("EnqueueIngest through the installed dialer = %v, want ErrTasksNotConfigured (handler falls back to inline)", err)
	}
}
