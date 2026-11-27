package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// wallets records which books and which wallet each balance read asked about.
type wallets struct {
	mu    sync.Mutex
	books []string
	subj  []string
}

func (w *wallets) read(_ context.Context, org, payer string) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.books, w.subj = append(w.books, org), append(w.subj, payer)
	return 1 << 40, nil
}

func (w *wallets) last() (books, subject string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.subj) == 0 {
		return "", ""
	}
	return w.books[len(w.books)-1], w.subj[len(w.subj)-1]
}

// reading swaps the balance read for one that records what it was asked.
func reading(t *testing.T) *wallets {
	t.Helper()
	w := &wallets{}
	prior := balanceCents
	t.Cleanup(func() { balanceCents = prior })
	balanceCents = w.read
	return w
}

// A person in the signup org leases on their own wallet. The org's own account
// there is the platform's pool, so a lease gated, charged and metered on the org
// was a stranger's computer paid for out of Hanzo's balance.
func TestASignupOrgPersonsLeaseIsTheirs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		w := reading(t)
		alice := account.Person(account.SignupOrg, "alice")

		r := httptest.NewRequest(http.MethodPost, "/v1/sandbox", strings.NewReader(`{"class":"exec"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Org-Id", account.SignupOrg)
		r.Header.Set("X-User-Owner", account.SignupOrg)
		r.Header.Set("X-User-Id", "8d2f0c4e-0000-4000-8000-000000000001")
		r.Header.Set("X-User-Name", "alice")
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lease = %d %s", resp.StatusCode, body)
		}
		var out Sandbox
		if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
			t.Fatalf("lease reply %s: %v", body, err)
		}

		if books, subject := w.last(); books != alice.Org() || subject != alice.Subject() {
			t.Fatalf("the lease was gated on %q in %q's books, want %q in %q's", subject, books, alice.Subject(), alice.Org())
		}
		store, err := storeFor(s, account.SignupOrg)
		if err != nil {
			t.Fatal(err)
		}
		m, err := store.Get(context.Background(), account.SignupOrg, out.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.Payer != alice.Subject() {
			t.Fatalf("the lease row names the payer %q, so its runtime is billed there; want %q", m.Payer, alice.Subject())
		}
		// The runtime meter reads the row's payer back through the one parse, and it
		// must name the wallet the lease was gated on.
		if got := account.PayerOf("", m.Payer); got != alice {
			t.Fatalf("the runtime meter would bill %q, want %q", got.Subject(), alice.Subject())
		}
	})
}

// The plane names the same wallet the request does: a sibling leasing for a
// person in the signup org states that person, and the lease is theirs.
func TestASignupOrgPersonsPlaneLeaseIsTheirs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		w := reading(t)
		alice := account.Person(account.SignupOrg, "alice")

		ctx := zip.WithCaller(context.Background(), zip.Caller{
			Org: account.SignupOrg, Owner: account.SignupOrg,
			User: "8d2f0c4e-0000-4000-8000-000000000001", Name: "alice",
		})
		out, err := planeLease(ctx, &client.LeaseIn{Class: "exec"})
		if err != nil {
			t.Fatalf("plane lease: %v", err)
		}
		if books, subject := w.last(); books != alice.Org() || subject != alice.Subject() {
			t.Fatalf("the plane lease was gated on %q in %q's books, want %q in %q's", subject, books, alice.Subject(), alice.Org())
		}
		store, err := storeFor(s, account.SignupOrg)
		if err != nil {
			t.Fatal(err)
		}
		m, err := store.Get(context.Background(), account.SignupOrg, out.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.Payer != alice.Subject() {
			t.Fatalf("the plane lease row names the payer %q, want %q", m.Payer, alice.Subject())
		}
	})
}

// A lease answers when its sandbox's own ceiling ends (Leased.EndsAt), from the sandbox's creation: the
// same instant the sweep ends it (clocks.ends), whoever pays for it.
//
// MUTATION: leave EndsAt out of the plane's answer and a run learns nothing of its sandbox's age.
func TestAPlaneLeaseSaysWhenItsSandboxEnds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		reading(t)
		ctx := zip.WithCaller(context.Background(), zip.Caller{
			Org: account.SignupOrg, Owner: account.SignupOrg,
			User: "8d2f0c4e-0000-4000-8000-000000000001", Name: "alice",
		})
		out, err := planeLease(ctx, &client.LeaseIn{Class: "exec"})
		if err != nil {
			t.Fatalf("plane lease: %v", err)
		}
		store, err := storeFor(s, account.SignupOrg)
		if err != nil {
			t.Fatal(err)
		}
		m, err := store.Get(context.Background(), account.SignupOrg, out.ID)
		if err != nil {
			t.Fatal(err)
		}
		if want := s.State.clk.ends(m); want == 0 || out.EndsAt != want {
			t.Fatalf("the lease says the sandbox ends at %d, the sweep at %d", out.EndsAt, want)
		}
		if out.EndsAt < time.Now().Unix() || out.EndsAt > time.Now().Add(s.State.clk.absolute).Unix()+1 {
			t.Fatalf("a fresh sandbox is told it ends at %d", out.EndsAt)
		}
	})
}
