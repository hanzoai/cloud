package account

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients/payout"
	luxlog "github.com/luxfi/log"
)

// TestOnboardFirstRun_ProvisionsOnceAndFundsPerIdentity proves the production
// signup path now: (1) drives ONE atomic IAM provision (not create-org + move-user),
// (2) funds the identity's one-time trial by crediting its CANONICAL org with a
// per-identity idempotency key, and (3) a retry converges — a replay grants no
// second trial, so it never double-funds (the anti-farm gate end-to-end).
func TestOnboardFirstRun_ProvisionsOnceAndFundsPerIdentity(t *testing.T) {
	var provisionCalls, creditCalls int
	var provBody, creditBody map[string]any
	trialLeft := true // the IAM claim grants the trial on the FIRST provision only

	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/get-user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data":   map[string]any{"owner": "landing", "name": "dave", "email": "dave@example.com"},
			})
		case "/v1/iam/admin/provision":
			provisionCalls++
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &provBody)
			granted := trialLeft
			trialLeft = false // once-per-identity: a replay claims nothing
			_ = json.NewEncoder(w).Encode(map[string]any{
				"org": "dave", "accessKey": "hk-x", "accessSecret": "sk-x", "trialGranted": granted,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer iamSrv.Close()

	commSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creditCalls++
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &creditBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "grant_1"})
	}))
	defer commSrv.Close()

	iam := &iamClient{base: iamSrv.URL, clientID: "c", clientSecret: "s", serviceToken: "svc", http: &http.Client{}}
	comm := payout.NewClient(commSrv.URL, "ctok")
	log := luxlog.New("test")

	// First-run onboarding.
	resp, err := onboardFirstRun(context.Background(), iam, comm, log, "dave", "dave", "Dave", true)
	if err != nil {
		t.Fatalf("onboardFirstRun: %v", err)
	}
	if resp.Org != "dave" || resp.Additional {
		t.Fatalf("resp = %+v, want org=dave additional=false", resp)
	}
	if provisionCalls != 1 {
		t.Fatalf("provision calls = %d, want 1 (ONE atomic op, not create+move)", provisionCalls)
	}
	if provBody["owner"] != "landing" || provBody["name"] != "dave" || provBody["orgSlug"] != "dave" {
		t.Fatalf("provision body = %v, want owner=landing name=dave orgSlug=dave", provBody)
	}
	if creditCalls != 1 {
		t.Fatalf("credit calls = %d, want 1 (funded on the trial grant)", creditCalls)
	}
	if creditBody["org"] != "dave" || creditBody["amountCents"].(float64) != 500 || creditBody["tag"] != "starter-credit" {
		t.Fatalf("credit body = %v, want org=dave amountCents=500 tag=starter-credit", creditBody)
	}
	if creditBody["idempotencyKey"] != "starter:dave@example.com" {
		t.Fatalf("idempotencyKey = %v, want the per-identity key starter:dave@example.com", creditBody["idempotencyKey"])
	}

	// Retry (same identity): provision replays with trialGranted=false → NO second
	// credit. The chain never double-funds.
	if _, err := onboardFirstRun(context.Background(), iam, comm, log, "dave", "dave", "Dave", true); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if provisionCalls != 2 {
		t.Fatalf("provision calls = %d, want 2 (retried)", provisionCalls)
	}
	if creditCalls != 1 {
		t.Fatalf("credit calls = %d, want STILL 1 — a replay grants no second trial, so no second credit", creditCalls)
	}
}
