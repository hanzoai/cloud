package metering_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/metering"
)

// routingCommerce is a fake commerce that answers the balance, spend-cap
// authorize, and spend-alerts endpoints independently — so AuthorizeVerdict's two
// checks (funds, then cap) can be driven separately.
type routingCommerce struct {
	balance   string // GET /v1/billing/balance
	authorize string // GET /v1/billing/spend-alerts/authorize
	authCode  int    // status for the authorize endpoint (0 => 200)
	alerts    string // GET /v1/billing/spend-alerts
}

func (rc *routingCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rc.balance))
	})
	mux.HandleFunc("/v1/billing/spend-alerts/authorize", func(w http.ResponseWriter, r *http.Request) {
		if rc.authCode != 0 {
			w.WriteHeader(rc.authCode)
		}
		_, _ = w.Write([]byte(rc.authorize))
	})
	mux.HandleFunc("/v1/billing/spend-alerts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rc.alerts))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func capClient(t *testing.T, srv *httptest.Server) *metering.Client {
	t.Helper()
	c, err := metering.New(metering.Config{BaseURL: srv.URL, Token: "svc", Org: "acme"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// Funded + a spend_cap verdict => Verdict deny(spend_cap); Authorize maps it to
// ErrSpendCapExceeded (DISTINCT from ErrInsufficientBalance).
func TestAuthorizeVerdict_SpendCap(t *testing.T) {
	rc := &routingCommerce{
		balance:   `{"available":100000}`,
		authorize: `{"allow":false,"reason":"spend_cap","capCents":100,"spentCents":100,"warnPct":0}`,
	}
	c := capClient(t, rc.server(t))
	in := metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1, Project: "P", Service: "ai"}

	v, err := c.AuthorizeVerdict(context.Background(), in)
	if err != nil {
		t.Fatalf("AuthorizeVerdict err: %v", err)
	}
	if v.Allow || v.Reason != "spend_cap" || v.CapCents != 100 || v.SpentCents != 100 {
		t.Fatalf("verdict = %+v, want deny spend_cap cap/spent 100/100", v)
	}
	if err := c.Authorize(context.Background(), in); err != metering.ErrSpendCapExceeded {
		t.Fatalf("Authorize err = %v, want ErrSpendCapExceeded", err)
	}
}

// Funded + under cap but over soft threshold => allow with WarnPct passthrough.
func TestAuthorizeVerdict_Warn(t *testing.T) {
	rc := &routingCommerce{
		balance:   `{"available":100000}`,
		authorize: `{"allow":true,"reason":"","warnPct":85}`,
	}
	c := capClient(t, rc.server(t))
	v, err := c.AuthorizeVerdict(context.Background(), metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1})
	if err != nil {
		t.Fatalf("AuthorizeVerdict err: %v", err)
	}
	if !v.Allow || v.WarnPct != 85 {
		t.Fatalf("verdict = %+v, want allow warnPct 85", v)
	}
	if err := c.Authorize(context.Background(), metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1}); err != nil {
		t.Fatalf("Authorize err = %v, want nil (allow)", err)
	}
}

// Unfunded short-circuits to insufficient_balance — the cap is never consulted.
func TestAuthorizeVerdict_Insufficient(t *testing.T) {
	rc := &routingCommerce{
		balance:   `{"available":0}`,
		authorize: `{"allow":true}`,
	}
	c := capClient(t, rc.server(t))
	in := metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1}
	v, _ := c.AuthorizeVerdict(context.Background(), in)
	if v.Allow || v.Reason != "insufficient_balance" {
		t.Fatalf("verdict = %+v, want deny insufficient_balance", v)
	}
	if err := c.Authorize(context.Background(), in); err != metering.ErrInsufficientBalance {
		t.Fatalf("Authorize err = %v, want ErrInsufficientBalance", err)
	}
}

// The cap check FAILS OPEN: a funded caller is allowed even when the cap endpoint
// errors (the funds gate already protects the money; a caps blip must not deny).
func TestAuthorizeVerdict_CapFailsOpen(t *testing.T) {
	rc := &routingCommerce{
		balance:   `{"available":100000}`,
		authorize: `nope`,
		authCode:  http.StatusNotFound,
	}
	c := capClient(t, rc.server(t))
	v, err := c.AuthorizeVerdict(context.Background(), metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1})
	if err != nil {
		t.Fatalf("AuthorizeVerdict err: %v", err)
	}
	if !v.Allow {
		t.Fatalf("verdict = %+v, want allow (cap fails open when funded)", v)
	}
}

// ProjectValidated forwards as pv=1 on the scope-authorize query (and is absent
// when false), so commerce can degrade an unvalidated project cap to soft.
func TestAuthorizeVerdict_ProjectValidatedForwarded(t *testing.T) {
	var gotPV string
	seen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing/spend-alerts/authorize" {
			seen = true
			gotPV = r.URL.Query().Get("pv")
		}
		if r.URL.Path == "/v1/billing/balance" {
			_, _ = w.Write([]byte(`{"available":100000}`))
			return
		}
		_, _ = w.Write([]byte(`{"allow":true}`))
	}))
	t.Cleanup(srv.Close)
	c, err := metering.New(metering.Config{BaseURL: srv.URL, Token: "svc", Org: "acme"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = c.AuthorizeVerdict(context.Background(), metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1, Project: "P", ProjectValidated: true})
	if !seen || gotPV != "1" {
		t.Fatalf("pv (validated) = %q seen=%v, want \"1\"", gotPV, seen)
	}

	seen, gotPV = false, ""
	_, _ = c.AuthorizeVerdict(context.Background(), metering.AuthInput{User: "acme", Org: "acme", AmountCents: 1, Project: "P", ProjectValidated: false})
	if !seen || gotPV != "" {
		t.Fatalf("pv (unvalidated) = %q, want \"\" (absent)", gotPV)
	}
}

// ScopeRules returns only the rate-limited rows, decoded from the spend-alerts list.
func TestScopeRules(t *testing.T) {
	rc := &routingCommerce{
		alerts: `[{"project":"","service":"","rateLimitRpm":0},{"project":"P","service":"ai","rateLimitRpm":5}]`,
	}
	c := capClient(t, rc.server(t))
	rules, err := c.ScopeRules(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ScopeRules err: %v", err)
	}
	if len(rules) != 1 || rules[0].Project != "P" || rules[0].Service != "ai" || rules[0].RateLimitRpm != 5 {
		t.Fatalf("rules = %+v, want one {P,ai,5}", rules)
	}
}
