package share

// Enabling a share mints an account on the fabric with the PLATFORM's admin
// credential, and any validated tenant can ask for one. So: provisioning bills
// the caller's own org, an unfunded caller never gets an account minted, and
// reading back an account you already have stays free — enable is idempotent, and
// a CLI re-reading its own token has bought nothing.

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fabric is a controller that counts the accounts it was asked to mint, and can
// start out already holding one.
type fabric struct {
	has     bool
	minted  int
	lookups int
}

func (f *fabric) token(ctx context.Context, org string, create bool) (string, error) {
	if f.has {
		f.lookups++
		return "tok-" + org, nil
	}
	if !create {
		return "", errNoAccount
	}
	f.minted++
	f.has = true
	return "tok-" + org, nil
}

func (f *fabric) overview(ctx context.Context, accountToken string) (overviewResp, error) {
	return overviewResp{}, nil
}

func (f *fabric) configured() bool { return true }

// billedShare mounts the surface over f, metered against l.
func billedShare(t *testing.T, l *planetest.Ledger, f *fabric) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, "share"),
		State: state{cl: f},
	}
	routes(app, s)
	return app
}

// Provisioning bills the caller's own org at the platform provision fee.
func TestProvisioningAnAccountBillsTheCaller(t *testing.T) {
	l := planetest.Money(t, 100000)
	f := &fabric{}

	if code, body := call(t, billedShare(t, l, f), http.MethodPost, "/v1/share/enable", "acme"); code != http.StatusOK {
		t.Fatalf("enable = %d (%s)", code, body)
	}
	if f.minted != 1 {
		t.Fatalf("minted %d accounts, want 1", f.minted)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — provisioning on the platform's credential must bill", l.Count())
	}
	org, cents, model, _ := l.Charged()
	if org != "acme" {
		t.Errorf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	if cents != cloud.DefaultResourceFeeCents {
		t.Errorf("debit = %dc, want the provision fee %dc", cents, cloud.DefaultResourceFeeCents)
	}
	if model != account {
		t.Errorf("debit unit = %q, want %q", model, account)
	}
}

// An unfunded org never gets an account minted on our admin credential.
func TestUnfundedOrgGetsNoAccount(t *testing.T) {
	l := planetest.Money(t, 0)
	f := &fabric{}

	if code, _ := call(t, billedShare(t, l, f), http.MethodPost, "/v1/share/enable", "acme"); code == http.StatusOK {
		t.Fatal("an unfunded org provisioned a tunnel account; the gate must refuse it")
	}
	if f.minted != 0 {
		t.Fatalf("minted %d accounts for an unfunded org, want 0 — the gate must precede the mint", f.minted)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused provision, want 0", n)
	}
}

// Reading back an existing account is free and ungated: enable is idempotent, so
// a tenant at zero balance can always fetch the credential they already have.
func TestReadingBackAnAccountIsFree(t *testing.T) {
	l := planetest.Money(t, 0) // a balance that must not be consulted
	f := &fabric{has: true}

	if code, body := call(t, billedShare(t, l, f), http.MethodPost, "/v1/share/enable", "acme"); code != http.StatusOK {
		t.Fatalf("enable for an existing account = %d (%s), want 200 — a read-back is not a purchase", code, body)
	}
	if f.minted != 0 {
		t.Fatalf("minted %d accounts when one already existed, want 0", f.minted)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a read-back, want 0", n)
	}
}
