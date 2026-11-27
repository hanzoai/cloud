package billing

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestCryptoOptionsRichMetadataAndMinimumEnforcement(t *testing.T) {
	richMenu := client.CryptoOptions{
		Chains: []string{"base", "ethereum"},
		Tokens: []string{"usdc"},
		Assets: []client.CryptoPair{
			{
				Chain:              "base",
				Token:              "usdc",
				Contract:           "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
				Decimals:           6,
				MinimumCents:       500,
				MinAmount:          "5000000",
				FeeCents:           0,
				Confirmations:      20,
				AddressTagRequired: false,
			},
			{
				Chain:              "ethereum",
				Token:              "usdc",
				Contract:           "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
				Decimals:           6,
				MinimumCents:       550,
				MinAmount:          "5500000",
				FeeCents:           50,
				Confirmations:      12,
				AddressTagRequired: false,
			},
		},
		Gateway: []string{"eip155:8453"},
	}

	s := railStandIn(t, &richMenu)
	app := mountApp(t)

	// 1. Capability discovery returns full contract, decimals, minimum, fee, confirmations.
	status, body := act(t, app, http.MethodGet, "/v1/billing/crypto/options", "", founder)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/billing/crypto/options: %d %s", status, clip(body))
	}
	var gotOpts client.CryptoOptions
	if err := json.Unmarshal([]byte(body), &gotOpts); err != nil {
		t.Fatalf("decode options: %v (%s)", err, clip(body))
	}
	if len(gotOpts.Assets) != 2 || gotOpts.Assets[0].Contract == "" || gotOpts.Assets[0].Decimals != 6 || gotOpts.Assets[0].Confirmations != 20 {
		t.Fatalf("unexpected options metadata: %+v", gotOpts)
	}

	// 2. Negative amountCents is rejected 400 before touching commerce.
	status, body = act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", `{"chain":"base","token":"usdc","amountCents":-100}`, founder)
	if status != http.StatusBadRequest || !strings.Contains(body, "cannot be negative") {
		t.Fatalf("negative amountCents: %d %s; want 400", status, clip(body))
	}

	// 3. Sub-minimum amount ($4.00 < $5.00 minimum) is rejected 400 with minimumCents disclosed.
	status, body = act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", `{"chain":"base","token":"usdc","amountCents":400}`, founder)
	if status != http.StatusBadRequest || !strings.Contains(body, "below the minimum deposit") {
		t.Fatalf("sub-minimum amountCents: %d %s; want 400", status, clip(body))
	}
	if n := s.count(client.BillingCryptoMint); n != 0 {
		t.Fatalf("expected 0 mints after invalid/sub-minimum requests, got %d", n)
	}

	// 4. Exact $5.00 floor (500 cents) and $50.00 (5000 cents) both succeed.
	for _, amt := range []string{"500", "5000"} {
		status, body = act(t, app, http.MethodPost, "/v1/billing/crypto/deposit", `{"chain":"base","token":"usdc","amountCents":`+amt+`}`, founder)
		if status != http.StatusOK {
			t.Fatalf("valid %s-cent deposit: %d %s", amt, status, clip(body))
		}
	}
	if n := s.count(client.BillingCryptoMint); n != 2 {
		t.Fatalf("expected 2 mints, got %d", n)
	}
	if billingRailMetrics.count("crypto_native", "mint", "below_minimum") < 1 ||
		billingRailMetrics.count("crypto_native", "mint", "ok") < 2 {
		t.Fatalf("expected rail metrics to record below_minimum and ok outcomes")
	}
}
