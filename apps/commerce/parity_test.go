// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// parity_test.go — ONE PAYMENT, ONE FINANCE ENTRY, THROUGH COMMERCE'S OWN WRITERS.
//
// cutover_test.go proves the adapter dedups by key. This drives the real thing: a card
// charged on Square's sandbox through the plane, so that commerce's TakePayment posts
// its credit (with the Ledger this host names) into the real finance ledger; then the
// caller's retry of the same request (which replays the sealed charge and posts again);
// then the provider's own webhook for the same payment, signed and delivered twice
// through commerce's real handler; then commerce's reconciler sweep. Every one of them
// is a writer that could credit this payment, and the finance ledger must hold one.
//
// Mutation proof: make commerce's credit key anything but the processor's reference
// (api/billing bankAt: IdempotencyKey) and the retry posts a second entry — this reads
// 8400, not 4200.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/payment/processor"
	squareprovider "github.com/hanzoai/commerce/payment/providers/square"
	"github.com/zap-proto/zip"
)

const parityURL = "https://api.test.example/v1/commerce/webhooks/square"

func TestOneCardPaymentIsOneFinanceEntryThroughEveryCommerceWriter(t *testing.T) {
	const secret = "whsec_parity_cutover"
	pl := servePlane(t)
	app := billingTopup(t, memory())
	org := fresh(gateOrg)
	key := fresh("parity")
	hdr := map[string]string{"X-Org-Id": org, "X-User-Id": gateUser, "X-Idempotency-Key": key}
	want := wallet(org)

	// 1. The in-session charge: commerce takes the card and posts the credit itself.
	code, body := browse(t, app, hdr)
	if code != http.StatusOK {
		t.Fatalf("the top-up answered %d %s, want 200", code, body)
	}
	ref := chargedRef(t, body)
	if got := balance(t, pl.fin, org, want); got != gateCents {
		t.Fatalf("after the charge the wallet holds %d, want %d: commerce did not post its credit", got, gateCents)
	}

	// 2. The caller retries the same request: the sealed charge is replayed and its credit
	// posted again, under the same key.
	if code, body := browse(t, app, hdr); code != http.StatusOK {
		t.Fatalf("the retry answered %d %s, want 200", code, body)
	}

	// 3. The provider's webhook for the same payment, twice.
	prev, _ := processor.Global().Registered(processor.Square)
	processor.Global().Register(squareprovider.NewProvider(squareprovider.Config{
		AccessToken: "test-token", LocationID: "L1", WebhookSignatureKey: secret, WebhookURL: parityURL,
		Environment: "sandbox", Org: org,
	}))
	t.Cleanup(func() {
		if prev != nil {
			processor.Global().Register(prev)
		}
	})
	for i := 0; i < 2; i++ {
		event := fmt.Sprintf(`{"merchant_id":"M1","type":"payment.updated","event_id":"evt_parity_%d_%s","created_at":%q,`+
			`"data":{"type":"payment","id":%q,"object":{"payment":{"id":%q,"status":"COMPLETED","reference_id":%q,`+
			`"amount_money":{"amount":%d,"currency":"USD"}}}}}`,
			i, key, time.Now().UTC().Format(time.RFC3339), ref, ref, want, gateCents)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(parityURL))
		mac.Write([]byte(event))
		req := httptest.NewRequest(http.MethodPost, "/v1/commerce/webhooks/square", strings.NewReader(event))
		req.Header.Set("x-square-hmacsha256-signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		hook := zip.New(zip.Config{DisableStartupMessage: true})
		hook.Raw(zip.MethodAll, "/v1/commerce/webhooks/:provider", commercebilling.HandleProviderWebhook)
		resp, err := hook.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook delivery %d: %v %v, want 200", i, err, resp)
		}
	}

	// 4. The reconciler sweep.
	if _, err := commercebilling.RenewDue(context.Background()); err != nil {
		t.Fatalf("the sweep: %v", err)
	}

	if got := balance(t, pl.fin, org, want); got != gateCents {
		t.Fatalf("the finance ledger holds %d for one %d payment after the charge, its retry, two webhooks and the sweep — "+
			"exactly one entry is the rule", got, gateCents)
	}
}
