package integrations

// commerce.go completes the payments & fulfillment connectors an online store runs
// on, alongside payments.go (Stripe/PayPal/Square) and messaging.go. Customer-held
// keys on the per-user /v1/connectors plane, verified live. Three fit the status-only
// keyVerify mechanism; Authorize.Net answers 200 for a bad credential (the decision
// is in the body), so it keeps a bespoke Verify over the shared verifyPost transport.
//
// Bundle / Return / Tax from the historical list are NOT third-party connectors:
// they map to the commerce engine's own bundle/return/tax primitives, not an external
// credential to custody.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func init() {
	// Coinbase Commerce — accept crypto payments. The API key rides the X-CC-Api-Key
	// header with the pinned API version; verify lists one charge.
	register(&Provider{
		ID: "coinbase_commerce", Name: "Coinbase Commerce",
		Description: "Accept cryptocurrency payments. Connect with an API key.",
		Category:    "Payments", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "coinbase_commerce",
			origin:   constOrigin("COINBASE_COMMERCE_API_BASE", "https://api.commerce.coinbase.com"),
			path:     "/charges?limit=1", place: headerAt, name: "X-CC-Api-Key", minLen: 8,
			extra: map[string]string{"X-CC-Version": "2018-03-22"},
		}),
	})

	// ShipStation — multi-carrier shipping/fulfillment. HTTP Basic "<apiKey>:<apiSecret>"
	// (basicRaw over the joined pair), verified by listing carriers.
	register(&Provider{
		ID: "shipstation", Name: "ShipStation",
		Description: "Order shipping and fulfillment. Connect with your `API_KEY:API_SECRET`.",
		Category:    "Commerce", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "shipstation",
			origin:   constOrigin("SHIPSTATION_API_BASE", "https://ssapi.shipstation.com"),
			path:     "/carriers", place: basicRaw, minLen: 8,
		}),
	})

	// Shipwire — order fulfillment & inventory. HTTP Basic "<username>:<password>"
	// (basicRaw), verified against the stock (inventory) endpoint.
	register(&Provider{
		ID: "shipwire", Name: "Shipwire",
		Description: "Fulfillment and inventory. Connect with your `USERNAME:PASSWORD`.",
		Category:    "Commerce", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "shipwire",
			origin:   constOrigin("SHIPWIRE_API_BASE", "https://api.shipwire.com"),
			path:     "/api/v3/stock?limit=1", place: basicRaw, minLen: 8,
		}),
	})

	// Authorize.Net — payment gateway. The credential is the two-part API Login ID +
	// Transaction Key ("<login>:<txnKey>"); authenticateTest answers HTTP 200 for BOTH
	// success and failure, discriminated only by messages.resultCode, so verify reads
	// the body. The raw pair is sealed under api_key; the gateway reader splits it.
	register(&Provider{
		ID: "authorizenet", Name: "Authorize.Net",
		Description: "Payment gateway. Connect with your `API_LOGIN_ID:TRANSACTION_KEY`.",
		Category:    "Payments", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify:  authorizeNetVerify,
	})
}

// authorizeNetVerify checks an Authorize.Net API Login ID + Transaction Key via the
// authenticateTest request and seals the pair only when the gateway reports Ok. It is
// fail-closed and token-free: a bad credential returns an error carrying no value, and
// nothing is stored. Env-overridable base for the httptest seam / the sandbox
// (apitest.authorize.net).
func authorizeNetVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	cred := strings.TrimSpace(in.Token)
	login, txnKey, err := splitPair(cred, "authorizenet", "API_LOGIN_ID", "TRANSACTION_KEY")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"authenticateTestRequest": map[string]any{
			"merchantAuthentication": map[string]string{"name": login, "transactionKey": txnKey},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("authorizenet verify: encode")
	}
	base := envBase("AUTHORIZENET_API_BASE")
	if base == "" {
		base = "https://api.authorize.net"
	}
	raw, err := verifyPost(ctx, "authorizenet", base+"/xml/v1/request.api", "application/json", body)
	if err != nil {
		return nil, err
	}
	var r struct {
		Messages struct {
			ResultCode string `json:"resultCode"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("authorizenet verify: decode")
	}
	if !strings.EqualFold(r.Messages.ResultCode, "Ok") {
		return nil, fmt.Errorf("authorizenet rejected the credential")
	}
	return &ExchangeResult{Tokens: map[string]string{apiKeySecret: cred}, ExternalID: login}, nil
}
