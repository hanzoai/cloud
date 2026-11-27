package x402

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/account"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

// asPerson drives one request as a validated member of org, the way the identity
// boundary mints them: the user id, the selected org, the home org and the
// username every wallet key is resolved from.
func (h *harness) asPerson(method, path, org, name, payment string) (int, []byte, http.Header) {
	h.t.Helper()
	hr := httptest.NewRequest(method, path, nil)
	hr.Header.Set("X-User-Id", "u_"+name)
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Owner", org)
	hr.Header.Set("X-User-Name", name)
	if payment != "" {
		hr.Header.Set(protocol.HeaderPaymentSignature, payment)
	}
	resp, err := h.app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		h.t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// signupPayment funds name's own wallet and the signup org's pool alike, then
// pays /paid/tool as name and answers the settlement the payment made.
func (h *harness) signupPayment(name string, price money.Amount) protocol.SettlementResponse {
	h.t.Helper()
	ctx := context.Background()
	org := account.SignupOrg
	wallet := account.Person(org, name).Subject()
	if _, err := h.ledger.FinanceClient.Deposit(ctx, depositUSD(org, wallet, funded, "fund-"+name)); err != nil {
		h.t.Fatalf("fund %s: %v", wallet, err)
	}
	code, b, hdr := h.asPerson(http.MethodGet, "/paid/tool", org, name, "")
	if code != http.StatusPaymentRequired {
		h.t.Fatalf("challenge as %s = %d (%s), want 402", wallet, code, b)
	}
	var req protocol.PaymentRequired
	if err := protocol.DecodeHeader(hdr.Get(protocol.HeaderPaymentRequired), &req); err != nil || len(req.Accepts) != 1 {
		h.t.Fatalf("decode challenge: %v (%+v)", err, req)
	}
	now := nowUnix()
	code, b, hdr = h.asPerson(http.MethodGet, "/paid/tool", org, name, h.pay(req.Accepts[0], randNonce(), now-60, now+300))
	if code != http.StatusOK {
		h.t.Fatalf("paid request as %s = %d (%s), want 200", wallet, code, b)
	}
	return h.settlement(hdr)
}

// A person in the signup org pays an x402 resource from their own wallet. The
// org's own account there is the platform's pool, and drawing a stranger's
// purchase from it is Hanzo paying for it.
func TestASignupOrgPersonPaysFromTheirOwnWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		price := money.FromCents(100)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		org := account.SignupOrg
		if _, err := h.ledger.FinanceClient.Deposit(context.Background(), depositUSD(org, org, funded, "fund-pool")); err != nil {
			t.Fatalf("fund the pool: %v", err)
		}

		h.signupPayment("alice", price)

		alice := account.Person(org, "alice").Subject()
		if got := funded.Sub(h.balance(org, alice)); got.Cmp(price) != 0 {
			t.Fatalf("alice's wallet %s paid %s, want exactly %s", alice, got.String(), price.String())
		}
		if got := funded.Sub(h.balance(org, org)); !got.IsZero() {
			t.Fatalf("the signup org's pool %q paid %s for alice's purchase, want nothing", org, got.String())
		}
		if got := h.credited(); got.Cmp(price) != 0 {
			t.Fatalf("payee credited %s, want %s", got.String(), price.String())
		}
	})
}

// A member of the signup org reads their own receipts and nobody else's. Every
// member keeps their books in the one org, so a read scoped by the org alone is
// every stranger's purchases handed to every other.
func TestASignupOrgPersonReadsOnlyTheirOwnReceipts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		price := money.FromCents(100)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		org := account.SignupOrg

		mine := h.signupPayment("alice", price)
		theirs := h.signupPayment("bob", price)

		code, b, _ := h.asPerson(http.MethodGet, "/v1/x402/settlements", org, "alice", "")
		if code != http.StatusOK {
			t.Fatalf("alice's settlements = %d (%s)", code, b)
		}
		var list settlementList
		if err := json.Unmarshal(b, &list); err != nil {
			t.Fatalf("decode settlements: %v (%s)", err, b)
		}
		if len(list.Settlements) != 1 || list.Settlements[0].ID != mine.Transaction {
			ids := []string{}
			for _, r := range list.Settlements {
				ids = append(ids, r.ID)
			}
			t.Fatalf("alice listed %v, want only her own %s (bob's is %s)", ids, mine.Transaction, theirs.Transaction)
		}

		if code, _, _ := h.asPerson(http.MethodGet, "/v1/x402/settlements/"+mine.Transaction, org, "alice", ""); code != http.StatusOK {
			t.Fatalf("alice reading her own receipt = %d, want 200", code)
		}
		if code, _, _ := h.asPerson(http.MethodGet, "/v1/x402/settlements/"+theirs.Transaction, org, "alice", ""); code != http.StatusNotFound {
			t.Fatalf("alice reading bob's receipt = %d, want 404", code)
		}
	})
}

// A job agreed by a person in the signup org is paid from that person's wallet.
// The buyer agrees it signed in, so the hold knows who; the release is stated later
// for the buyer's org alone (marketplace settle), and that org's own account is the
// platform's pool. Another member of the org may not settle the person's payment,
// and a caller stated for the signup org with no person pays nothing unheld.
func TestASignupOrgJobIsPaidFromTheBuyersWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		org := account.SignupOrg
		price := money.FromCents(100)
		terms := Terms{Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		pubRegistry(map[string]Terms{client.JobResource("j1"): terms, "/paid/tool": terms})
		ctx := context.Background()
		alice, bob := account.Person(org, "alice"), account.Person(org, "bob")
		for _, w := range []string{org, alice.Subject(), bob.Subject()} {
			if _, err := h.ledger.FinanceClient.Deposit(ctx, depositUSD(org, w, funded, "fund-"+w)); err != nil {
				t.Fatalf("fund %s: %v", w, err)
			}
		}
		member := func(name string) context.Context {
			return zip.WithCaller(ctx, zip.Caller{Org: org, Owner: org, User: "u_" + name, Name: name})
		}
		r := rail{s: live}

		out, err := r.planeAuthorize(member("alice"), &client.AuthorizationIn{Resource: client.JobResource("j1")})
		if err != nil || out.Challenge == "" {
			t.Fatalf("authorize with no payment = %+v %v", out, err)
		}
		var required protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &required); err != nil {
			t.Fatalf("decode challenge: %v", err)
		}
		now := nowUnix()
		payment := h.pay(required.Accepts[0], randNonce(), now-60, now+3600)
		if out, err := r.planeAuthorize(member("alice"), &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: payment,
			Until: now + 1800, Hold: true}); err != nil || !out.OK {
			t.Fatalf("hold as alice = %+v %v", out, err)
		}
		if got := funded.Sub(h.balance(org, alice.Subject())); got.Cmp(price) != 0 {
			t.Fatalf("alice's wallet set aside %s, want %s", got.String(), price.String())
		}

		if out, err := r.planeAuthorize(member("bob"), &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: payment,
			Until: now + 1800, Hold: true}); err != nil || out.OK || out.Code != reasonReplay {
			t.Fatalf("bob holding alice's payment = %+v %v, want refused", out, err)
		}
		if s, err := r.planeSettle(member("bob"), &client.SettleIn{Resource: client.JobResource("j1"), Payment: payment}); err != nil || s.OK {
			t.Fatalf("bob settling alice's payment = %+v %v, want refused", s, err)
		}

		release := cloud.For(ctx, org)
		if s, err := r.planeSettle(release, &client.SettleIn{Resource: client.JobResource("j1"), Payment: payment}); err != nil || !s.OK {
			t.Fatalf("the job's release = %+v %v, want settled", s, err)
		}
		if got := funded.Sub(h.balance(org, alice.Subject())); got.Cmp(price) != 0 {
			t.Fatalf("alice paid %s for her job, want exactly %s", got.String(), price.String())
		}
		for _, w := range []string{org, bob.Subject()} {
			if got := funded.Sub(h.balance(org, w)); !got.IsZero() {
				t.Fatalf("%s paid %s for alice's job, want nothing", w, got.String())
			}
		}
		if got := h.credited(); got.Cmp(price) != 0 {
			t.Fatalf("payee credited %s, want %s", got.String(), price.String())
		}

		other := h.pay(required.Accepts[0], randNonce(), now-60, now+3600)
		if s, err := r.planeSettle(release, &client.SettleIn{Resource: "/paid/tool", Payment: other}); err != nil || s.OK || s.Code != reasonUnbillable {
			t.Fatalf("an unheld payment stated for the signup org alone = %+v %v, want refused as unbillable", s, err)
		}
	})
}
