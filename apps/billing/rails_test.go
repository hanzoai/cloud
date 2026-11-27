package billing

// rails_test.go — a deposit address is minted only for an asset the rail accepts,
// and a refusal says what it does accept.

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// menu is what the stand-in commerce offers: USDC on base, USDC and USDT on
// ethereum. base/usdt is on both axes and is not a pair.
var menu = client.CryptoOptions{
	Chains: []string{"base", "ethereum"},
	Tokens: []string{"usdc", "usdt"},
	Assets: []client.CryptoPair{{Chain: "base", Token: "usdc"}, {Chain: "ethereum", Token: "usdc"}, {Chain: "ethereum", Token: "usdt"}},
}

// railStandIn serves the crypto ops as commerce would, offering `offer` (nil: the
// rail is shut, 503), and records every mint it is asked for.
func railStandIn(t *testing.T, offer *client.CryptoOptions) *stand {
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
	peer.Post("/billing/crypto/options", func(context.Context, *cloud.Unit) (*client.CryptoOptions, error) {
		if offer == nil {
			return nil, zip.Errorf(503, "crypto deposits not configured")
		}
		return offer, nil
	}, zip.WithOperationID(client.BillingCryptoOptions))
	peer.Post("/billing/crypto/mint", func(_ context.Context, in *client.CryptoMintIn) (*client.CryptoDeposit, error) {
		s.note(client.BillingCryptoMint, *in)
		return &client.CryptoDeposit{ID: "dep_1", Status: "pending", Chain: in.Chain, Token: in.Token,
			DepositAddress: "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", RequiredConfirmations: 20, MinimumCents: 1}, nil
	}, zip.WithOperationID(client.BillingCryptoMint))
	peer.Post("/billing/gateway/terms", func(_ context.Context, in *client.GatewayTermsIn) (*client.GatewayTerms, error) {
		s.note(client.BillingGatewayTerms, *in)
		return &client.GatewayTerms{Version: 1, Scheme: "exact", PayTo: "0xTreasury000000000000000000000000000000",
			Nonce: "0xnonce", Salt: "salt_1", MinCents: 100, MaxCents: 500000, MaxValidity: 3600,
			Primary: "TransferWithAuthorization", Types: json.RawMessage(`{}`),
			Accepts: []client.GatewayAccept{{Network: "eip155:8453", Asset: "0xUSDC", Symbol: "USDC",
				Decimals: 6, MinAmount: "10000", MaxAmount: "500000000", MinValidity: 600}}}, nil
	}, zip.WithOperationID(client.BillingGatewayTerms))
	peer.Post("/billing/gateway/submit", func(_ context.Context, in *client.GatewaySubmitIn) (*client.GatewayPayment, error) {
		s.note(client.BillingGatewaySubmit, *in)
		return &client.GatewayPayment{ID: "rem_1", Status: "accepted", Network: in.Network,
			From: in.Authorization.From, Value: in.Authorization.Value, AmountCents: 500, Credited: true}, nil
	}, zip.WithOperationID(client.BillingGatewaySubmit))

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

func TestADepositIsMintedOnlyForAnAcceptedAsset(t *testing.T) {
	s := railStandIn(t, &menu)
	app := mountApp(t)

	for _, tc := range []struct{ name, body, says string }{
		{"an unknown chain", `{"chain":"dogechain","token":"usdc","amountCents":500}`, "USDC on dogechain is not accepted"},
		{"an unknown token", `{"chain":"base","token":"doge"}`, "DOGE on base is not accepted"},
		{"a pair whose halves are each offered", `{"chain":"base","token":"usdt"}`, "USDT on base is not accepted"},
		{"an unknown chain alone", `{"chain":"dogechain"}`, "chain dogechain is not accepted"},
	} {
		status, body := act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", tc.body, founder)
		if status != http.StatusBadRequest {
			t.Errorf("%s: %d %s; want 400", tc.name, status, clip(body))
			continue
		}
		var got struct {
			Detail string              `json:"detail"`
			Chains []string            `json:"chains"`
			Tokens []string            `json:"tokens"`
			Assets []client.CryptoPair `json:"assets"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("%s: the refusal is not JSON: %s", tc.name, clip(body))
		}
		if !strings.Contains(got.Detail, tc.says) || !strings.Contains(got.Detail, "USDC on base, USDC on ethereum, USDT on ethereum") {
			t.Errorf("%s: detail %q; want it to name what was asked and what is accepted", tc.name, got.Detail)
		}
		if len(got.Chains) != 2 || len(got.Tokens) != 2 || len(got.Assets) != 3 || got.Assets[0] != menu.Assets[0] {
			t.Errorf("%s: the refusal lists chains %v tokens %v assets %v; want the menu", tc.name, got.Chains, got.Tokens, got.Assets)
		}
	}
	if n := s.count(client.BillingCryptoMint); n != 0 {
		t.Fatalf("%d address(es) minted for assets the rail does not accept", n)
	}

	status, body := act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", `{"chain":"Base","token":"USDC","amountCents":500}`, founder)
	if status != http.StatusOK {
		t.Fatalf("an accepted asset: %d %s", status, clip(body))
	}
	in, _ := s.last(client.BillingCryptoMint).(client.CryptoMintIn)
	if in.Payer != "josh" || in.AmountCents != 500 {
		t.Fatalf("the mint reached commerce as %+v; want the founder's own wallet and the amount", in)
	}

	// An empty body takes the rail's default, and the rail answers for it.
	if status, body := act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", `{}`, founder); status != http.StatusOK {
		t.Fatalf("an empty body: %d %s", status, clip(body))
	}
}

func TestAShutRailIsUnavailableNotRefused(t *testing.T) {
	for name, offer := range map[string]*client.CryptoOptions{
		"no custody":      nil,
		"nothing watched": {Chains: []string{}, Tokens: []string{}, Assets: []client.CryptoPair{}},
	} {
		t.Run(name, func(t *testing.T) {
			s := railStandIn(t, offer)
			app := mountApp(t)
			status, body := act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", `{"chain":"base","token":"usdc"}`, founder)
			if status != http.StatusServiceUnavailable {
				t.Fatalf("%d %s; want 503", status, clip(body))
			}
			if n := s.count(client.BillingCryptoMint); n != 0 {
				t.Fatalf("a shut rail minted %d address(es)", n)
			}
		})
	}
}

// TestAKeyFundsAloneAndACookieCannot is the money step of self-service, measured
// rather than argued: the one rail an AGENT can walk by itself — terms, sign,
// submit — answers a caller whose whole credential is a key it PRESENTED, and
// refuses the same change carried by a browser cookie from a sibling host.
//
// Both halves matter and only the pair proves anything. A rail reachable by a key
// but also by an ambient cookie is every subdomain of the estate spending the
// caller's balance; a rail that refuses the cookie by refusing everyone is unusable
// by the caller this exists for, which has no browser at all. The estate's
// anti-forgery control (cloud.Intended) is what draws the line, and this is the
// proof that it stands in front of THIS deposit rather than around it.
func TestAKeyFundsAloneAndACookieCannot(t *testing.T) {
	s := railStandIn(t, &menu)
	app := mountApp(t)

	// act, but with the credential and the browser's signal left to the caller: the
	// two headers under test are exactly what it would otherwise supply for us.
	ask := func(method, path, body string, hdr map[string]string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
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
	plus := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		maps.Copy(m, founder)
		maps.Copy(m, extra)
		return m
	}

	// NO COOKIE ANYWHERE, a key that was presented, and a browser that says it came
	// from another site — which is the strongest test there is, since a key cannot
	// be attached by a page and so must not be asked about at all.
	key := plus(map[string]string{"Authorization": "Bearer sk-agent-0123456789", "Sec-Fetch-Site": "cross-site"})

	status, body := ask(http.MethodGet, "/v1/billing/crypto/gateway", "", key)
	if status != http.StatusOK {
		t.Fatalf("a key-bearing caller asking for the top-up terms: %d %s", status, clip(body))
	}
	var terms client.GatewayTerms
	if err := json.Unmarshal([]byte(body), &terms); err != nil {
		t.Fatalf("the terms are not JSON: %s", clip(body))
	}
	if terms.PayTo == "" || len(terms.Accepts) == 0 || terms.Nonce == "" {
		t.Fatalf("the terms do not name a treasury, a network and a nonce: %+v", terms)
	}
	if in, _ := s.last(client.BillingGatewayTerms).(client.GatewayTermsIn); in.Payer != "josh" {
		t.Fatalf("the nonce was issued to %q; want the caller's own wallet", in.Payer)
	}

	signed := `{"network":"eip155:8453","salt":"` + terms.Salt + `","authorization":` +
		`{"from":"0x00000000000000000000000000000000000000AA","to":"` + terms.PayTo + `",` +
		`"value":"5000000","validAfter":"0","validBefore":"9999999999","nonce":"` + terms.Nonce + `"},` +
		`"signature":"0x` + strings.Repeat("11", 65) + `"}`
	status, body = ask(http.MethodPost, "/v1/billing/crypto/gateway", signed, key)
	if status != http.StatusOK {
		t.Fatalf("a key-bearing caller presenting a signed payment: %d %s", status, clip(body))
	}
	var paid client.GatewayPayment
	if err := json.Unmarshal([]byte(body), &paid); err != nil {
		t.Fatalf("the payment answer is not JSON: %s", clip(body))
	}
	if !paid.Credited || paid.Status != "accepted" {
		t.Fatalf("the payment came back %+v; want accepted and credited", paid)
	}
	if in, _ := s.last(client.BillingGatewaySubmit).(client.GatewaySubmitIn); in.Payer != "josh" {
		t.Fatalf("the payment reached commerce as payer %q; want the caller, never a body value", in.Payer)
	}

	// THE SAME CHANGE, CARRIED BY A COOKIE from a sibling subdomain. SameSite sends
	// it; the browser still says `same-site`, and that is the refusal.
	cookie := plus(map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "same-site"})
	status, body = ask(http.MethodPost, "/v1/billing/crypto/gateway", signed, cookie)
	if status != http.StatusForbidden || !strings.Contains(body, cloud.Unasked) {
		t.Fatalf("a cookie-only same-site payment: %d %s; want 403 %q", status, clip(body), cloud.Unasked)
	}
	if n := s.count(client.BillingGatewaySubmit); n != 1 {
		t.Fatalf("%d payments reached commerce; want 1 — the refused one must never be relayed", n)
	}
}
