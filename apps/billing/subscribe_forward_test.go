package billing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// seller serves the commerce plane's sale op on the commerce socket with sell, for
// the test's life.
func seller(t *testing.T, sell func(context.Context, *client.SaleIn) (*client.Sold, error)) {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	peer := zip.New(zip.Config{Logger: luxlog.New("test")})
	peer.Post("/billing/subscribe", sell, zip.WithOperationID(client.BillingSubscribe))
	sock := zip.SocketPath("commerce")
	listened := make(chan error, 1)
	go func() { listened <- peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		select {
		case err := <-listened:
			t.Fatalf("the stand-in commerce did not listen on %s: %v", sock, err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the stand-in commerce never listened on %s", sock)
}

// A plan sale is a raw route, and commerce resolves who pays from the caller the
// request forwards. commerce refuses a sale that arrives with no caller before
// any card is charged, so a sale that forgot to forward is a sale nobody can make.
// What was bought travels whole too: a wallet's token is only spendable as one,
// and a code dropped on the way would sell at full price.
func TestSubscribeCarriesTheCallerToCommerce(t *testing.T) {
	var seen atomic.Pointer[zip.Caller]
	var sold atomic.Pointer[client.SaleIn]
	var consent atomic.Pointer[string]
	seller(t, func(ctx context.Context, in *client.SaleIn) (*client.Sold, error) {
		c := zip.CallerOf(ctx)
		seen.Store(&c)
		sold.Store(in)
		consent.Store(in.Consent)
		return &client.Sold{Sale: &client.Sale{SubscriptionID: "sub_1", PlanID: in.PlanID, Status: "active"}}, nil
	})

	app := mountApp(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe/card",
		strings.NewReader(`{"planId":"dev","sourceId":"cnon:card-nonce-ok","wallet":true,"coupon":"HALF"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", "alice")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-IsOrgAdmin", "true") // the org's plan is its admin's to buy
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("subscribe answered %d (%s), want a sale", resp.StatusCode, body)
	}

	c := seen.Load()
	if c == nil {
		t.Fatal("commerce never saw the sale")
	}
	if c.Org != "acme" || c.User != "alice" {
		t.Errorf("commerce saw the sale as org %q user %q, want acme and alice: the caller did not travel with it", c.Org, c.User)
	}
	in := sold.Load()
	if in == nil || in.SourceID != "cnon:card-nonce-ok" || !in.Wallet || in.Coupon != "HALF" || !in.Chosen {
		t.Errorf("commerce saw the sale as %+v, want the token, wallet and code as sent, the org chosen", in)
	}
	if got := consent.Load(); got != nil {
		t.Errorf("a checkout that stated no choice was sent as %q", *got)
	}

	// The checkout's choice travels with the sale as @hanzo/event spells it; a
	// browser that sends GPC or DNT has allowed nothing, whatever the page says; and
	// a caller that is no browser states none.
	for _, tc := range []struct {
		hdr, want string
		none      bool
	}{{"", "analytics,marketing", false}, {"Sec-GPC", "", false}, {"DNT", "", false}, {"no browser", "", true}} {
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe/card",
			strings.NewReader(`{"planId":"dev","sourceId":"cnon:card-nonce-ok","consent":"marketing, analytics,all"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "alice")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-IsOrgAdmin", "true") // the org's plan is its admin's to buy
		switch tc.hdr {
		case "no browser":
		case "":
			req.Header.Set("Sec-Fetch-Site", "same-site")
		default:
			req.Header.Set("Sec-Fetch-Site", "same-site")
			req.Header.Set(tc.hdr, "1")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		_ = resp.Body.Close()
		got := consent.Load()
		if tc.none != (got == nil) || (got != nil && *got != tc.want) {
			t.Errorf("%q: commerce saw consent %v, want %q (none %v)", tc.hdr, got, tc.want, tc.none)
		}
	}
}

// A sale is for a chosen org only when the request selected the org it acts in; a
// request that selected none acts in the caller's default org and chose nothing.
func TestChoseIsTheSelectionTheSaleActsIn(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	var got bool
	app.Raw(http.MethodPost, "/probe", func(c *zip.Ctx) error {
		got = chose(c, "acme")
		return c.NoContent(http.StatusNoContent)
	})
	for _, tc := range []struct {
		sel  []string
		want bool
	}{{[]string{"acme"}, true}, {[]string{" acme "}, true}, {nil, false}, {[]string{""}, false}, {[]string{"alice"}, false}} {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		for _, s := range tc.sel {
			req.Header.Add("X-Org-Id", s)
		}
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("selected %q in acme: chose %v, want %v", tc.sel, got, tc.want)
		}
	}
}

func TestGuestSubscribeCarriesDeterministicSignupSubject(t *testing.T) {
	var seen atomic.Pointer[zip.Caller]
	var sold atomic.Pointer[client.SaleIn]
	seller(t, func(ctx context.Context, in *client.SaleIn) (*client.Sold, error) {
		c := zip.CallerOf(ctx)
		seen.Store(&c)
		sold.Store(in)
		return &client.Sold{Sale: &client.Sale{SubscriptionID: "sub_guest_1", InvoiceID: "inv_guest_1", PlanID: in.PlanID, Status: "active"}}, nil
	})

	app := mountApp(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe/card",
		strings.NewReader(`{"planId":"ultra","sourceId":"cnon:card-nonce-ok","level":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", "guest-key-1")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("guest subscribe: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("guest subscribe answered %d (%s), want 201", resp.StatusCode, body)
	}

	c := seen.Load()
	in := sold.Load()
	if c == nil || in == nil {
		t.Fatal("commerce never saw the guest sale")
	}
	if c.Org != "hanzo" || !strings.HasPrefix(c.User, "guest-") || in.Subject != "hanzo/"+c.User {
		t.Fatalf("commerce saw caller=%+v subject=%q, want hanzo/guest-*", *c, in.Subject)
	}
}
