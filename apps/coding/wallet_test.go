package coding

// wallet_test.go — a run's sandbox is paid by the wallet its request is paid by.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// firstHour is what the stand-in gate below asks a dev lease to cover.
const firstHour = 15

// leasing serves the two plane ops a run reaches before its first command: the
// prepaid gate, which passes, and a sandbox lease gated the way apps/sandbox gates
// one (prepaid.go afford): the payer is cloud.PayerOf over the caller the plane
// carried, and a wallet short of the first hour is refused in the gate's own
// sentence. It answers which wallets the leases were weighed on.
func leasing(t *testing.T, held map[string]int64) func() []string {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	var (
		mu  sync.Mutex
		saw []string
	)
	sandboxes := zip.New(zip.Config{AppName: sandboxpeer.App, DisableStartupMessage: true})
	sandboxes.Post("/sandbox/lease", func(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
		payer := cloud.PayerOf(ctx).Wallet
		mu.Lock()
		saw = append(saw, payer.Subject())
		mu.Unlock()
		if have := held[payer.Subject()]; have < firstHour {
			return nil, zip.Errorf(http.StatusPaymentRequired,
				"a %s lease costs %d cents for its first hour and this wallet holds %d", in.Class, firstHour, have)
		}
		return nil, zip.Errorf(http.StatusTeapot, "leased")
	}, zip.WithOperationID(client.SandboxLease))
	commerce := zip.New(zip.Config{AppName: "commerce", DisableStartupMessage: true})
	commerce.Post("/commerce/authorize", func(context.Context, *client.AuthorizeIn) (*client.Verdict, error) {
		return &client.Verdict{OK: true}, nil
	}, zip.WithOperationID(client.FinanceAuthorize))
	for name, app := range map[string]*zip.App{sandboxpeer.App: sandboxes, "commerce": commerce} {
		plane.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), saw...)
	}
}

// A person signed in to the signup org pays from hanzo/<their name>: the edge,
// the model gate and the meter all resolve it from the name the boundary minted.
// Their token's user is their IAM sub, a UUID, and the run used to state only
// that, so the sandbox resolved hanzo/<sub> — a wallet no grant, deposit or debit
// ever names — and refused the run in a second while the person's own wallet
// and the org beside it both held money.
func TestARunLeasesOnTheWalletItsRequestPays(t *testing.T) {
	const sub = "2d4d67ab-30f1-474e-b81f-f60461852259"
	alice := account.Person(account.SignupOrg, "alice")
	saw := leasing(t, map[string]int64{
		alice.Subject():                          49_790,
		account.Org(account.SignupOrg).Subject(): 15_627_658,
	})

	req := zip.WithCaller(context.Background(), zip.Caller{
		Org: account.SignupOrg, Owner: account.SignupOrg, User: sub, Name: "alice",
	})
	if got := cloud.PayerOf(req).Wallet; got != alice {
		t.Fatalf("the request pays %q, want %q", got.Subject(), alice.Subject())
	}
	run, cancel, clk := runContext(account.SignupOrg, 60, 0)
	defer cancel()
	defer clk.end()
	run = paying(run, req)

	_, err := (sandboxRunner{}).Run(run, account.SignupOrg, sub, aRun(), nil)
	if err != nil && strings.Contains(err.Error(), "this wallet holds") {
		t.Fatalf("the run was refused on a wallet its request does not pay: %v (weighed %v)", err, saw())
	}
	if got := saw(); len(got) != 1 || got[0] != alice.Subject() {
		t.Fatalf("the lease was weighed on %v, want [%s]", got, alice.Subject())
	}
}

// The claim and the name cross together: an org owner's run, whose request
// names the org's pool, still pays from the pool.
func TestARunKeepsTheAccountItsRequestNamed(t *testing.T) {
	pool := account.Org(account.SignupOrg)
	saw := leasing(t, map[string]int64{pool.Subject(): 15_627_658})

	req := zip.WithCaller(context.Background(), zip.Caller{
		Org: account.SignupOrg, Owner: account.SignupOrg, User: "hanzo/z", Name: "z", Account: pool.String(),
	})
	run, cancel, clk := runContext(account.SignupOrg, 60, 0)
	defer cancel()
	defer clk.end()
	run = paying(run, req)

	_, err := (sandboxRunner{}).Run(run, account.SignupOrg, "hanzo/z", aRun(), nil)
	if err != nil && strings.Contains(err.Error(), "this wallet holds") {
		t.Fatalf("the run was refused: %v", err)
	}
	if got := fmt.Sprint(saw()); got != fmt.Sprint([]string{pool.Subject()}) {
		t.Fatalf("the lease was weighed on %s, want the pool its request named", got)
	}
}

// A follow-up is paid by what the run it follows was paid by. It is started on a
// context of its own, and it used to state the person alone, so an org owner's
// follow-up dropped the claim naming the pool and a person's dropped their name:
// the run that paid from one wallet was carried on by one that paid from another.
func TestAFollowUpIsPaidAsItsRunWas(t *testing.T) {
	r := newRig()
	r.admit = pushes(nil, admins("hanzo/sub-bob"))
	r.pod.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}
	r.serve(t)
	engineOnce = sync.Once{}
	t.Cleanup(func() { engineOnce = sync.Once{} })

	pool := account.Org("hanzo").String()
	ran := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", Name: "bob", Account: pool})
	f := &flight{org: "hanzo", subject: "sub-bob", session: "sess_prev", done: make(chan struct{}), ctx: ran,
		in: client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header"}}
	close(f.done)
	f.follow("sub-bob", client.CodingSteerIn{Text: "and the footer"}, false, func(string, ...any) {})

	r.mu.Lock()
	admitted := append([]zip.Caller(nil), r.admitted...)
	r.mu.Unlock()
	if len(admitted) != 2 || admitted[0] != admitted[1] || admitted[0].Account != pool || admitted[0].Name != "bob" {
		t.Fatalf("the follow-up must be paid as its run was (%s, bob): %+v", pool, admitted)
	}
	flights.Lock()
	next := flights.m["sess_abc123def456"]
	flights.Unlock()
	if next != nil {
		<-next.done
	}
}
