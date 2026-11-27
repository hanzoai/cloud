package share

// Enabling a share mints an account on the fabric with the PLATFORM's admin
// credential, and any validated tenant can ask for one. So: provisioning bills
// the caller's own org, an unfunded caller never gets an account minted, and
// reading back an account you already have stays free — enable is idempotent, and
// a CLI re-reading its own token has bought nothing.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
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

func (f *fabric) token(ctx context.Context, h holding, create bool) (string, error) {
	if f.has {
		f.lookups++
		return "tok-" + h.org, nil
	}
	if !create {
		return "", errNoAccount
	}
	f.minted++
	f.has = true
	return "tok-" + h.org, nil
}

func (f *fabric) rotate(ctx context.Context, h holding) (int, error) { return 0, nil }

func (f *fabric) overview(ctx context.Context, accountToken string) (overviewResp, error) {
	return overviewResp{}, nil
}

func (f *fabric) configured() bool { return true }

// billedShare mounts the surface over cl, metered against l.
func billedShare(t *testing.T, l *planetest.Ledger, cl controller) *zip.App {
	t.Setenv("CLOUD_ENV", "mainnet")
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Metering: l.Client(t)}, "share"),
		State: state{cl: cl},
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

func servePlaneApp(t *testing.T, name string, setup func(*zip.App)) {
	t.Helper()
	a := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	setup(a)
	at := zip.SocketPath(name)
	go func() { _ = a.Listen(at) }()
	t.Cleanup(func() { _ = a.Shutdown() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("unix", at); err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never accepted", at)
		}
	}
}

// A paid subscriber org (e.g. Unlimited / Pro) with $0 prepaid wallet balance
// can provision its share account without being blocked by 402.
func TestPaidSubscriberOrgAtZeroWalletProvisions(t *testing.T) {
	l := planetest.Money(t, 0)
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	servePlaneApp(t, "commerce", func(a *zip.App) {
		a.Post("/finance/subs", func(ctx context.Context, _ *client.SubsIn) (*client.Subs, error) {
			if zip.CallerOf(ctx).Org == "acme" {
				return &client.Subs{Rows: []client.Sub{
					{Status: "active", MRRCents: 99500, PlanSlug: "unlimited", PlanName: "Unlimited"},
				}}, nil
			}
			return &client.Subs{}, nil
		}, zip.WithOperationID(client.FinanceSubs))
	})

	f := &fabric{}
	if code, body := call(t, billedShare(t, l, f), http.MethodPost, "/v1/share/enable", "acme"); code != http.StatusOK {
		t.Fatalf("paid subscriber enable at $0 wallet = %d (%s), want 200", code, body)
	}
	if f.minted != 1 {
		t.Fatalf("minted = %d, want 1", f.minted)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d, want 0 for subscription-covered provision", n)
	}
}

// `hanzo link` authenticates in the shared signup org ("hanzo") while the
// user's Unlimited subscription lives on their personal org ("jamessburnss1313")
// with $0 prepaid wallet balance. The subscriber provisions cleanly, while an
// unsubscribed neighbour in "hanzo" is still refused.
func TestCLISubscriberInHeldOrgWithPersonalOrgPlanProvisionsAndStrangerDoesNot(t *testing.T) {
	l := planetest.Money(t, 0)
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	james := iam.Self{ID: "9e08b8a2-4afd-42f8-b20d-283a4a63b44b", Row: "hanzo/jamessburnss1313"}
	mallory := iam.Self{ID: "uuid-mallory", Row: "hanzo/mallory"}

	servePlaneApp(t, "iam", func(a *zip.App) {
		rows := map[string]iam.Self{
			james.ID:    james,
			james.Row:   james,
			mallory.ID:  mallory,
			mallory.Row: mallory,
		}
		a.Post("/iam/self", func(ctx context.Context, _ *struct{}) (*iam.Self, error) {
			p, ok := rows[zip.CallerOf(ctx).User]
			if !ok {
				return nil, zip.ErrUnauthorized("no such subject")
			}
			return &p, nil
		}, zip.WithOperationID(iam.IAMSelf))
		a.Post("/iam/members", func(ctx context.Context, in *client.Scope) (*client.Memberships, error) {
			if zip.CallerOf(ctx).Org == "jamessburnss1313" && (in.User == james.ID || in.User == james.Row) {
				return &client.Memberships{Memberships: []client.Membership{
					{User: james.ID, Role: "owner", Name: "jamessburnss1313"},
				}}, nil
			}
			return &client.Memberships{}, nil
		}, zip.WithOperationID(client.IAMMembers))
	})

	servePlaneApp(t, "commerce", func(a *zip.App) {
		a.Post("/finance/subs", func(ctx context.Context, _ *client.SubsIn) (*client.Subs, error) {
			if zip.CallerOf(ctx).Org == "jamessburnss1313" {
				return &client.Subs{Rows: []client.Sub{
					{Status: "active", MRRCents: 99500, PlanSlug: "unlimited", PlanName: "Unlimited"},
				}}, nil
			}
			return &client.Subs{}, nil
		}, zip.WithOperationID(client.FinanceSubs))
	})

	f := &accounts{held: map[holding]string{}, envs: map[string]int{}}
	app := billedShare(t, l, f)

	callAs := func(user, name string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/share/enable", nil)
		req.Header.Set("X-Org-Id", "hanzo")
		req.Header.Set("X-User-Owner", "hanzo")
		req.Header.Set("X-User-Id", user)
		req.Header.Set("X-User-Name", name)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode, resp.Status
	}

	if code, status := callAs(james.ID, "jamessburnss1313"); code != http.StatusOK {
		t.Fatalf("james CLI enable = %d (%s), want 200", code, status)
	}
	if code, _ := callAs(mallory.ID, "mallory"); code == http.StatusOK {
		t.Fatal("unsubscribed neighbour in hanzo provisioned an account at $0 balance")
	}
}
