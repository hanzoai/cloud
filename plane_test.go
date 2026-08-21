package cloud_test

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/internal/planetest"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The tenant boundary ACROSS the internal plane.
//
// An aggregator reads another app's per-org data by asking that app over this
// socket, so "whose data is this?" is answered on the wire rather than by a Go
// call in one address space. That makes it exactly the shape of bug this fleet
// has already shipped once: an org-scoped list that returned 200 with another
// tenant's rows because a scope filter was quietly dropped.
//
// So the boundary is asserted adversarially against the REAL transport — a real
// socket, real ops, real identity forwarding — not a stub. The rule under test
// is the one every org-scoped op must follow:
//
//	the org comes from the CALLER, never from the argument.

// books stands in for any per-org store: a map that must only ever be read at
// the key the caller's identity names.
var books = map[string]int64{"acme": 5000, "initech": 99}

// serveBank publishes an op with the canonical scoping rule on a real socket,
// so what the tests below attack is the real declare/call path.
func serveBank(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))

	app := zip.New(zip.Config{AppName: "bank"})
	zip.Post[plane.BalanceIn, plane.Balance](app, "/bank/balance",
		func(ctx context.Context, in *plane.BalanceIn) (*plane.Balance, error) {
			// Fail CLOSED on an absent tenant. Answering with a default, the first
			// key, or zero would each be a different way of inventing an answer
			// nobody is authorized to receive.
			who := cloud.Who(ctx).Org
			if who == "" {
				return nil, zip.ErrForbidden("no org on the call")
			}
			return &plane.Balance{Amount: plane.Money{
				Decimal:  decimalOf(books[who]),
				Currency: "USD",
			}}, nil
		}, zip.WithOperationID("bank_balance"))

	go func() { _ = app.Listen(zip.SocketPath("bank")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "bank")
}

func decimalOf(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100, 'f', 2, 64)
}

func waitFor(t *testing.T, app string) {
	t.Helper()
	for range 200 {
		if c, derr := net.Dial("unix", zip.SocketPath(app)); derr == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", zip.SocketPath(app))
}

// ask drives one real round trip over the socket.
func ask(ctx context.Context, in *plane.BalanceIn) (*plane.Balance, error) {
	return cloud.Ask[plane.BalanceIn, plane.Balance](ctx, "bank", "bank_balance", in)
}

// The adversarial case: a caller acting for acme must NEVER be able to read
// initech, no matter what it puts in the argument or the subject.
func TestTenantScopesTheAnswer(t *testing.T) {
	serveBank(t)

	got, err := ask(cloud.For(context.Background(), "acme"), &plane.BalanceIn{Currency: "usd"})
	if err != nil {
		t.Fatalf("acme read: %v", err)
	}
	if got.Amount.Decimal != "50.00" {
		t.Fatalf("acme read %s, want 50.00", got.Amount.Decimal)
	}

	// Every attempt to name the other tenant in the ARGUMENT. None can work,
	// because the input type has no field that names an org — see the structural
	// test below — and the subject is not one.
	for _, subject := range []string{"initech", "initech/admin", "../initech", "acme initech"} {
		got, err := ask(cloud.For(context.Background(), "acme"), &plane.BalanceIn{
			Subject: subject, Currency: "usd",
		})
		if err != nil {
			t.Fatalf("subject %q: %v", subject, err)
		}
		if got.Amount.Decimal != "50.00" {
			t.Fatalf("subject %q read %s — it reached another tenant", subject, got.Amount.Decimal)
		}
	}
}

// No caller identity at all is REFUSED, not defaulted. An unattributed call that
// reads the first tenant it finds is the same bug wearing a different hat.
func TestNoTenantIsRefused(t *testing.T) {
	serveBank(t)

	_, err := ask(context.Background(), &plane.BalanceIn{Currency: "usd"})
	if err == nil {
		t.Fatal("a call with no org was answered")
	}
	if !strings.Contains(err.Error(), "no org") {
		t.Fatalf("refusal did not name the reason: %v", err)
	}
}

// The STRUCTURAL half of the rule, and the one that keeps holding after every
// future edit: no input on this plane may carry an org. If a field named Org
// ever appears on one, a caller can name the tenant it is answered for, and
// every guard above becomes advisory.
func TestNoPlaneInputCanNameAnOrg(t *testing.T) {
	inputs := []any{
		plane.AuthorizeIn{}, plane.RecordIn{}, plane.BalanceIn{},
		plane.SecretIn{}, plane.FilesIn{}, plane.Visibility{}, plane.ReserveIn{},
		plane.FlagIn{},
	}
	for _, in := range inputs {
		typ := reflect.TypeOf(in)
		for field := range typ.Fields() {
			if name := field.Name; name == "Org" || name == "Owner" {
				t.Errorf("%s.%s: a plane input must not name a tenant — the org rides the caller",
					typ.Name(), name)
			}
		}
	}
}

// A refusal crosses with the status the op chose. 402 vs 403 vs 404 vs 503 is
// "unfunded" vs "not yours" vs "no such thing" vs "not ready", and collapsing
// them is how a board ends up lying about why something failed.
func TestStatusCrossesWhole(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	app := zip.New(zip.Config{AppName: "moody"})
	zip.Post[struct{}, struct{}](app, "/moody/refuse",
		func(context.Context, *struct{}) (*struct{}, error) {
			return nil, zip.Errorf(402, "add funds")
		}, zip.WithOperationID("moody_refuse"))
	go func() { _ = app.Listen(zip.SocketPath("moody")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "moody")

	_, err := cloud.Ask[struct{}, struct{}](context.Background(), "moody", "moody_refuse", &struct{}{})
	if err == nil {
		t.Fatal("a refusal was reported as success")
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("status did not survive the crossing: %v", err)
	}
	if he.Status != 402 {
		t.Fatalf("status %d, want 402", he.Status)
	}
}

// An app that is not running names the app it could not reach, rather than
// returning a zero value. A zero balance and an unreachable ledger must never
// look alike.
func TestAbsentPeerNamesTheApp(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	_, err := cloud.Ask[struct{}, struct{}](context.Background(), "nowhere", "nowhere_op", &struct{}{})
	if err == nil {
		t.Fatal("a call to an app that is not running succeeded")
	}
	if !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("error does not name the app: %v", err)
	}
}
