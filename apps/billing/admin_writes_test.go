package billing

// Who may change what an org pays.
//
// The org's plan, its cards, a charge on its card on file and its auto-reload are
// its admins' to change, and the platform's in support mode. A member is refused
// with a 403 whose detail says who to ask, before commerce is asked anything. A
// personal org is an org of one and its owner is its admin. A member of the shared
// signup org pays from a wallet of their own and changes that wallet — but not the
// org's auto-reload, which is one rule for the org and charges the org's card.
//
// Every row drives a stand-in commerce on the real plane socket, so "allowed" is
// commerce receiving the act with the authority the endpoint decided, and
// "refused" is commerce receiving nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// The callers, as the identity boundary mints them from a validated IAM token.
var (
	member = map[string]string{"X-User-Id": "carol", "X-User-Name": "carol", "X-Org-Id": "acme", "X-User-Owner": "acme"}
	admin  = map[string]string{"X-User-Id": "dana", "X-User-Name": "dana", "X-Org-Id": "acme", "X-User-Owner": "acme", "X-User-IsOrgAdmin": "true"}
	// A SuperAdmin stepped into acme (IAM assume): their own token, the admin org
	// home, acme in their set as an admin.
	support = map[string]string{"X-User-Id": "woo", "X-User-Name": "woo", "X-Org-Id": "acme", "X-User-Owner": "admin",
		"X-User-IsAdmin": "true", "X-User-IsOrgAdmin": "true"}
	// The founder of a personal org: home org of one, admin of it.
	founder = map[string]string{"X-User-Id": "josh", "X-User-Name": "josh", "X-Org-Id": "josh", "X-User-Owner": "josh", "X-User-IsOrgAdmin": "true"}
	// A member of the shared signup org, billed from their own wallet hanzo/alice.
	signup = map[string]string{"X-User-Id": "alice", "X-User-Name": "alice", "X-Org-Id": "hanzo", "X-User-Owner": "hanzo"}
)

// stand is a stand-in commerce: every money op this package relays, recording
// what arrived.
type stand struct {
	mu   sync.Mutex
	seen map[string][]any
}

func (s *stand) note(op string, in any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[op] = append(s.seen[op], in)
}

func (s *stand) last(op string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.seen[op]); n > 0 {
		return s.seen[op][n-1]
	}
	return nil
}

func (s *stand) count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen[op])
}

// standIn listens as commerce on the plane socket for the length of the test.
func standIn(t *testing.T) *stand {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	s := &stand{seen: map[string][]any{}}
	peer := zip.New(zip.Config{Logger: luxlog.New("test")})
	peer.Post("/billing/subscribe", func(_ context.Context, in *client.SaleIn) (*client.Sold, error) {
		s.note(client.BillingSubscribe, *in)
		return &client.Sold{Sale: &client.Sale{SubscriptionID: "sub_1", PlanID: in.PlanID, Status: "active"}}, nil
	}, zip.WithOperationID(client.BillingSubscribe))
	peer.Post("/billing/subscription/cancel", func(_ context.Context, in *client.SubscriptionRef) (*client.Subscription, error) {
		s.note(client.BillingSubscriptionCancel, *in)
		return &client.Subscription{ID: in.ID, Status: "active", CancelAtPeriodEnd: true}, nil
	}, zip.WithOperationID(client.BillingSubscriptionCancel))
	peer.Post("/billing/subscription/reactivate", func(_ context.Context, in *client.SubscriptionRef) (*client.Subscription, error) {
		s.note(client.BillingSubscriptionReactivate, *in)
		return &client.Subscription{ID: in.ID, Status: "active"}, nil
	}, zip.WithOperationID(client.BillingSubscriptionReactivate))
	peer.Post("/billing/subscription/seats", func(_ context.Context, in *client.SeatsIn) (*client.Subscription, error) {
		s.note(client.BillingSubscriptionSeats, *in)
		return &client.Subscription{ID: in.ID, Status: "active", Assigned: in.Assigned}, nil
	}, zip.WithOperationID(client.BillingSubscriptionSeats))
	peer.Post("/billing/subscription/resize", func(_ context.Context, in *client.ResizeIn) (*client.Subscription, error) {
		s.note(client.BillingSubscriptionResize, *in)
		return &client.Subscription{ID: in.ID, Status: "active"}, nil
	}, zip.WithOperationID(client.BillingSubscriptionResize))
	peer.Post("/billing/subscriptions", func(_ context.Context, in *client.SubsIn) (*client.Subscriptions, error) {
		s.note(client.BillingSubscriptions, *in)
		return &client.Subscriptions{Rows: []client.Subscription{{ID: "sub_1", UserID: in.UserID, DefaultPaymentMethod: "pm_org"}}, Count: 1}, nil
	}, zip.WithOperationID(client.BillingSubscriptions))
	peer.Post("/billing/auto-recharge/set", func(_ context.Context, in *client.AutoRechargeSetIn) (*client.AutoRecharge, error) {
		s.note(client.BillingAutoRechargeSet, *in)
		return &client.AutoRecharge{Subject: in.Subject, Enabled: in.Edit.Enabled, AmountCents: in.Edit.AmountCents,
			ThresholdCents: in.Edit.ThresholdCents, Stored: true, PerDay: 3}, nil
	}, zip.WithOperationID(client.BillingAutoRechargeSet))
	peer.Post("/billing/method/save", func(_ context.Context, in *client.MethodSaveIn) (*client.Rendered, error) {
		s.note(client.BillingMethodSave, *in)
		return &client.Rendered{Body: []byte(`{"id":"pm_1"}`), Created: true}, nil
	}, zip.WithOperationID(client.BillingMethodSave))
	peer.Post("/billing/method/detach", func(_ context.Context, in *client.MethodRef) (*client.Detachment, error) {
		s.note(client.BillingMethodDetach, *in)
		return &client.Detachment{Deleted: true, ID: in.ID}, nil
	}, zip.WithOperationID(client.BillingMethodDetach))
	peer.Post("/billing/topup", func(_ context.Context, in *client.SavedCardIn) (*client.Charged, error) {
		s.note(client.BillingTopup, *in)
		return &client.Charged{TransactionID: "tx_1", Status: "paid"}, nil
	}, zip.WithOperationID(client.BillingTopup))
	peer.Post("/billing/topup/card", func(_ context.Context, in *client.CardIn) (*client.Charged, error) {
		s.note(client.BillingTopupCard, *in)
		return &client.Charged{TransactionID: "tx_2", Status: "paid"}, nil
	}, zip.WithOperationID(client.BillingTopupCard))

	sock := zip.SocketPath("commerce")
	listened := make(chan error, 1)
	go func() { listened <- peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(sock); err == nil {
			return s
		}
		select {
		case err := <-listened:
			t.Fatalf("the stand-in commerce did not listen on %s: %v", sock, err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the stand-in commerce never listened on %s", sock)
	return nil
}

// send drives one request and answers its status and body.
func act(t *testing.T, app *zip.App, method, path, body string, who map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	for k, v := range who {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// write is one money write and the commerce op it relays to.
type write struct {
	method, path, body, op, says string
}

var writes = []write{
	{http.MethodPost, "/v1/billing/subscribe/card", `{"planId":"dev","sourceId":"cnon:card-nonce-ok"}`, client.BillingSubscribe, "change its plan"},
	{http.MethodPost, "/v1/billing/subscriptions/sub_1/cancel", `{}`, client.BillingSubscriptionCancel, "cancel its plan"},
	{http.MethodPost, "/v1/billing/subscriptions/sub_1/reactivate", `{}`, client.BillingSubscriptionReactivate, "resume its plan"},
	{http.MethodPut, "/v1/billing/subscriptions/sub_1/seats", `{"assigned":[{"member":"acme/dana","plan":"team-premium"}]}`, client.BillingSubscriptionSeats, "assign its seats"},
	{http.MethodPost, "/v1/billing/subscriptions/sub_1/resize", `{"lines":[{"planId":"team-premium","quantity":1}]}`, client.BillingSubscriptionResize, "change its seats"},
	{http.MethodPut, "/v1/billing/recharge", `{"enabled":true,"thresholdCents":500,"amountCents":2500}`, client.BillingAutoRechargeSet, "change its auto-reload"},
	{http.MethodPost, "/v1/billing/methods", `{"type":"card","providerRef":"cnon:card-nonce-ok"}`, client.BillingMethodSave, "change its cards"},
	{http.MethodPost, "/v1/billing/portal/methods", `{"type":"card","providerRef":"cnon:card-nonce-ok"}`, client.BillingMethodSave, "change its cards"},
	{http.MethodDelete, "/v1/billing/methods/pm_1", ``, client.BillingMethodDetach, "remove its cards"},
	{http.MethodDelete, "/v1/billing/portal/methods/pm_1", ``, client.BillingMethodDetach, "remove its cards"},
	{http.MethodPost, "/v1/billing/topup", `{"paymentMethodId":"pm_1","amountCents":2500}`, client.BillingTopup, "charge its card on file"},
}

// A member of a pooled org is refused every write that moves the org's money, with
// a 403 whose detail is the plain sentence, and commerce is asked nothing.
func TestAMemberIsToldToAskAnAdmin(t *testing.T) {
	s := standIn(t)
	app := mountApp(t)
	for _, w := range writes {
		before := s.count(w.op)
		status, body := act(t, app, w.method, w.path, w.body, member)
		if status != http.StatusForbidden {
			t.Errorf("%s %s as a member: %d %s, want 403", w.method, w.path, status, clip(body))
			continue
		}
		var problem struct {
			Detail string `json:"detail"`
			Status int    `json:"status"`
		}
		if err := json.Unmarshal([]byte(body), &problem); err != nil || problem.Status != 403 {
			t.Errorf("%s %s: the refusal is not a problem document: %s", w.method, w.path, body)
		}
		if want := "Only an admin of acme can " + w.says + ". Ask an admin to do it."; problem.Detail != want {
			t.Errorf("%s %s: detail %q, want %q", w.method, w.path, problem.Detail, want)
		}
		if s.count(w.op) != before {
			t.Errorf("%s %s as a member reached commerce", w.method, w.path)
		}
	}
}

// The org's admin, a SuperAdmin stepped into the org, and the founder of a personal
// org are each admitted to every write, and commerce receives the act.
func TestAnAdminChangesWhatTheOrgPays(t *testing.T) {
	s := standIn(t)
	app := mountApp(t)
	for name, who := range map[string]map[string]string{
		"the org's admin":          admin,
		"a SuperAdmin in support":  support,
		"a personal org's founder": founder,
	} {
		for _, w := range writes {
			before := s.count(w.op)
			status, body := act(t, app, w.method, w.path, w.body, who)
			if status/100 != 2 {
				t.Errorf("%s: %s %s answered %d %s, want 2xx", name, w.method, w.path, status, clip(body))
				continue
			}
			if s.count(w.op) != before+1 {
				t.Errorf("%s: %s %s never reached commerce", name, w.method, w.path)
			}
		}
	}
	// What the endpoint decided travels as a value: an admin's cancel holds every
	// subscription in the org, and names the wallet it pays from.
	ref, _ := s.last(client.BillingSubscriptionCancel).(client.SubscriptionRef)
	if !ref.Privileged || ref.Subject == "" || ref.ID != "sub_1" || !ref.AtPeriodEnd {
		t.Errorf("an admin's cancel reached commerce as %+v, want privileged, its wallet named, sub_1, at period end", ref)
	}
	// An admin's seats reach commerce as stated, with the authority the endpoint
	// decided; the body names no subscription, the path does.
	seats, _ := s.last(client.BillingSubscriptionSeats).(client.SeatsIn)
	if !seats.Privileged || seats.ID != "sub_1" || len(seats.Assigned) != 1 ||
		seats.Assigned[0] != (client.SubscriptionSeat{Member: "acme/dana", Plan: "team-premium"}) {
		t.Errorf("an admin's seats reached commerce as %+v, want privileged, sub_1, dana on a premium seat", seats)
	}
	// A change of seats reaches commerce the same way: the path names the row.
	resized, _ := s.last(client.BillingSubscriptionResize).(client.ResizeIn)
	if !resized.Privileged || resized.ID != "sub_1" || resized.Quantity != 0 || len(resized.Lines) != 1 ||
		resized.Lines[0] != (client.SaleLine{PlanID: "team-premium", Quantity: 1}) {
		t.Errorf("an admin's change of seats reached commerce as %+v, want privileged, sub_1, one premium seat", resized)
	}
}

// A team's other seat types ride the sale to commerce as the buyer stated them.
func TestATeamsSeatTypesReachTheSale(t *testing.T) {
	s := standIn(t)
	app := mountApp(t)
	status, body := act(t, app, http.MethodPost, "/v1/billing/subscribe/card",
		`{"planId":"team","quantity":1,"lines":[{"planId":"team-premium","quantity":1}],"sourceId":"cnon:card-nonce-ok"}`, admin)
	if status/100 != 2 {
		t.Fatalf("a mixed team's sale: %d %s", status, clip(body))
	}
	in, _ := s.last(client.BillingSubscribe).(client.SaleIn)
	if in.PlanID != "team" || in.Quantity != 1 || len(in.Lines) != 1 || in.Lines[0] != (client.SaleLine{PlanID: "team-premium", Quantity: 1}) {
		t.Fatalf("the sale reached commerce as %+v, want team x1 and one team-premium seat", in)
	}
}

// A member of the shared signup org changes their own wallet's plan, cards and
// auto-reload, and only theirs: commerce is told the wallet, and not that the caller
// holds the org.
func TestAMemberChangesTheirOwnWallet(t *testing.T) {
	s := standIn(t)
	app := mountApp(t)
	for _, w := range writes {
		before := s.count(w.op)
		status, body := act(t, app, w.method, w.path, w.body, signup)
		if status/100 != 2 || s.count(w.op) != before+1 {
			t.Errorf("%s %s for a member's own wallet: %d %s, want it to reach commerce", w.method, w.path, status, clip(body))
		}
	}
	ref, _ := s.last(client.BillingSubscriptionReactivate).(client.SubscriptionRef)
	if ref.Privileged || ref.Subject != "hanzo/alice" {
		t.Errorf("a member's resume reached commerce as %+v, want their wallet hanzo/alice and no privilege", ref)
	}
	sale, _ := s.last(client.BillingSubscribe).(client.SaleIn)
	if sale.Subject != "hanzo/alice" {
		t.Errorf("a member's sale was filed under %q, want hanzo/alice", sale.Subject)
	}
	reload, _ := s.last(client.BillingAutoRechargeSet).(client.AutoRechargeSetIn)
	if reload.Subject != "hanzo/alice" || !reload.Edit.Enabled || reload.Edit.AmountCents != 2500 {
		t.Errorf("a member's auto-reload reached commerce as %+v, want their wallet hanzo/alice, on, $25", reload)
	}
}

// A top-up with a fresh card funds the org from the caller's own card and moves
// none of the org's money out, so a member may still make one.
func TestAMemberStillAddsFundsWithTheirOwnCard(t *testing.T) {
	s := standIn(t)
	app := mountApp(t)
	status, body := act(t, app, http.MethodPost, "/v1/billing/topup/token", `{"sourceId":"cnon:card-nonce-ok","amountCents":2500}`, member)
	if status/100 != 2 || s.count(client.BillingTopupCard) != 1 {
		t.Fatalf("a member adding funds with their own card: %d %s", status, clip(body))
	}
}

// The plans list is what the caller may see. An admin sees the org's, and may
// narrow by wallet; a member of a pooled org sees the org's plan without the card
// it renews on; a member of the signup org sees their own wallet's and nobody
// else's, whatever userId they name.
func TestThePlansListIsWhatTheCallerMaySee(t *testing.T) {
	s := standIn(t)
	app := mountApp(t)
	for _, tc := range []struct {
		name string
		who  map[string]string
		path string
		user string
		card string
	}{
		{"an admin", admin, "/v1/billing/subscriptions", "", "pm_org"},
		{"an admin narrowing", admin, "/v1/billing/subscriptions?userId=acme/carol", "acme/carol", "pm_org"},
		{"a SuperAdmin in support", support, "/v1/billing/subscriptions", "", "pm_org"},
		{"a pooled member", member, "/v1/billing/subscriptions?userId=acme/dana", "acme", ""},
		{"a signup member", signup, "/v1/billing/subscriptions?userId=hanzo/bob", "hanzo/alice", "pm_org"},
	} {
		status, body := act(t, app, http.MethodGet, tc.path, "", tc.who)
		if status != http.StatusOK {
			t.Errorf("%s listing plans: %d %s", tc.name, status, clip(body))
			continue
		}
		in, _ := s.last(client.BillingSubscriptions).(client.SubsIn)
		if in.UserID != tc.user {
			t.Errorf("%s: commerce was asked for wallet %q, want %q", tc.name, in.UserID, tc.user)
		}
		var out client.Subscriptions
		if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Rows) != 1 {
			t.Errorf("%s: %s", tc.name, clip(body))
			continue
		}
		if out.Rows[0].DefaultPaymentMethod != tc.card {
			t.Errorf("%s saw the renewal card %q, want %q", tc.name, out.Rows[0].DefaultPaymentMethod, tc.card)
		}
	}
}

// Every one of these is on the audit trail: the member's refusal as a denial, and
// the SuperAdmin's cancel inside acme with the admin org named as where they came
// from — the record that tells an auditor the platform acted in a tenant.
func TestEveryBillingWriteIsOnTheTrail(t *testing.T) {
	standIn(t)
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	app := mountAudited(t, rec)

	if status, body := act(t, app, http.MethodPost, "/v1/billing/subscriptions/sub_1/cancel", `{}`, member); status != 403 {
		t.Fatalf("member cancel: %d %s", status, clip(body))
	}
	if status, body := act(t, app, http.MethodPost, "/v1/billing/subscriptions/sub_1/cancel", `{}`, support); status != 200 {
		t.Fatalf("support cancel: %d %s", status, clip(body))
	}

	rows, _, err := rec.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var denied, acted *audit.Record
	for i := range rows {
		r := &rows[i]
		if r.Outcome.Result == "begun" || r.Path != "/v1/billing/subscriptions/sub_1/cancel" {
			continue
		}
		switch r.Actor.Sub {
		case "carol":
			denied = r
		case "woo":
			acted = r
		}
	}
	if denied == nil || denied.Outcome.Status != 403 || denied.Outcome.Result != "deny" || denied.Actor.Org != "acme" {
		t.Errorf("the member's refusal on the trail: %+v", denied)
	}
	if acted == nil || acted.Outcome.Status != 200 || acted.Actor.Org != "acme" || acted.Actor.Home != "admin" || !acted.Auth.IsAdmin {
		t.Errorf("the SuperAdmin's cancel on the trail: %+v, want acme acted in from the admin org", acted)
	}
}

// mountAudited is mountApp behind the audit trail, the way the host composes them.
func mountAudited(t *testing.T, rec *audit.Recorder) *zip.App {
	t.Helper()
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.AuditTrail(rec))
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}
