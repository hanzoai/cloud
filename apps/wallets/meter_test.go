package wallets

// Creating a wallet on the ring runs a distributed keygen — and, for a Safe, a
// contract deploy with real gas. Signing runs a threshold round. The surface is
// org-scoped with no admin gate, so these are the facts that matter: a funded
// tenant's own org pays, an unfunded one is refused BEFORE any round starts, and
// in-process KMS custody stays free.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// ring is a stand-in for the MPC cluster that counts the rounds it was asked for.
// It answers as whichever Kind it is registered under, so one type covers the
// paid custodies and the free one.
type ring struct {
	kind        Kind
	provisioned int
	signed      int
}

func (r *ring) Kind() Kind { return r.kind }

func (r *ring) Provision(ctx context.Context, w *Wallet) (string, error) {
	r.provisioned++
	w.KeyRef = "ref/" + w.ID
	return "0x000000000000000000000000000000000000dead", nil
}

func (r *ring) Sign(ctx context.Context, w *Wallet, digest []byte) ([]byte, error) {
	r.signed++
	return make([]byte, 65), nil
}

func (r *ring) Rotate(ctx context.Context, w *Wallet) (string, error) {
	return w.Address, nil
}

// billed builds the surface with a counted custody of the given kind, billed
// against l.
func billed(t *testing.T, l *planetest.Ledger, kind Kind) (*zip.App, *ring) {
	t.Helper()
	r := &ring{kind: kind}
	s, app := newService(t, map[Kind]Custody{kind: r}, kind)
	s.Bill = cloud.NewResourceMeter(cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, "wallets")
	return app, r
}

// create posts one wallet of the service's default custody and returns the status.
func create(t *testing.T, app *zip.App, org string) (int, []byte) {
	t.Helper()
	acct := mkAccount(t, app, org)
	return req(t, app, http.MethodPost, "/v1/wallets", org, map[string]any{
		"accountId": acct, "name": "w", "tier": "hot",
	})
}

// A ring keygen bills the caller's own org at the declared fee.
func TestRingKeygenBillsTheCaller(t *testing.T) {
	l := planetest.Money(t, 100000)
	app, r := billed(t, l, KindMPC)

	if code, body := create(t, app, "acme"); code != http.StatusOK {
		t.Fatalf("create = %d (%s)", code, body)
	}
	if r.provisioned != 1 {
		t.Fatalf("ring asked for %d keygens, want 1", r.provisioned)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a keygen must bill", l.Count())
	}
	org, cents, model, _ := l.Charged()
	if org != "acme" {
		t.Errorf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	if cents != price[keygen] {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, price[keygen])
	}
	if model != keygen {
		t.Errorf("debit unit = %q, want %q", model, keygen)
	}
}

// An unfunded org never reaches the ring. The refusal has to precede the keygen,
// or a Safe contract is already deployed and the gas is already spent.
func TestUnfundedOrgNeverReachesTheRing(t *testing.T) {
	l := planetest.Money(t, 0)
	app, r := billed(t, l, KindMPC)

	if code, _ := create(t, app, "acme"); code == http.StatusOK {
		t.Fatal("an unfunded org created a ring wallet; the gate must refuse it")
	}
	if r.provisioned != 0 {
		t.Fatalf("ring asked for %d keygens for an unfunded org, want 0 — the gate must precede the round", r.provisioned)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused create, want 0", n)
	}
}

// KMS custody is an in-process keygen. It buys nothing, so it is neither gated
// nor billed — and it is the DEFAULT custody, so the ordinary wallet is free.
func TestKMSCustodyIsFree(t *testing.T) {
	l := planetest.Money(t, 0) // a balance that must never be consulted
	app, r := billed(t, l, KindKMS)

	if code, body := create(t, app, "acme"); code != http.StatusOK {
		t.Fatalf("create with in-process custody = %d (%s), want 200", code, body)
	}
	if r.provisioned != 1 {
		t.Fatalf("provisioned %d, want 1", r.provisioned)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for an in-process keygen, want 0 — it buys nothing", n)
	}
}

// A threshold signature is its own act and its own charge.
func TestRingSignBillsSeparately(t *testing.T) {
	l := planetest.Money(t, 100000)
	app, r := billed(t, l, KindMPC)

	_, body := create(t, app, "acme")
	var w Wallet
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode wallet: %v (%s)", err, body)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("the keygen did not bill; nothing after this proves anything")
	}

	// A 32-byte digest, which is the only shape the surface accepts.
	digest := "0x" + strings.Repeat("ab", 32)
	if code, b := req(t, app, http.MethodPost, "/v1/wallets/"+w.ID+"/sign", "acme",
		map[string]any{"digest": digest}); code != http.StatusOK {
		t.Fatalf("sign = %d (%s)", code, b)
	}
	if r.signed != 1 {
		t.Fatalf("ring asked for %d signatures, want 1", r.signed)
	}
	if !planetest.Wait(func() bool { return l.Count() == 2 }) {
		t.Fatalf("debits = %d after keygen+sign, want 2 — a round is its own charge", l.Count())
	}
	if _, cents, model, _ := l.Charged(); model != signing || cents != price[signing] {
		t.Errorf("second debit = %dc/%q, want %dc/%q", cents, model, price[signing], signing)
	}
}
