package account

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOnboardFirstRun_ProvisionsOnce proves the production signup path drives ONE
// atomic IAM provision (not the old create-org + move-user pair): it resolves the
// zero-org caller's authoritative (owner, name) and provisions the org + hashed
// credential in a single call. No trial credit is granted — the org starts at a zero
// balance and usage is pre-paid.
func TestOnboardFirstRun_ProvisionsOnce(t *testing.T) {
	var provisionCalls int
	var provBody map[string]any

	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/get-user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data":   map[string]any{"owner": "landing", "name": "dave"},
			})
		case "/v1/iam/admin/provision":
			provisionCalls++
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &provBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"org": "dave", "accessKey": "hk-x", "accessSecret": "sk-x",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer iamSrv.Close()

	iam := &iamClient{base: iamSrv.URL, clientID: "c", clientSecret: "s", serviceToken: "svc", http: &http.Client{}}

	resp, err := onboardFirstRun(context.Background(), iam, "dave", "dave", "Dave", true)
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

	// Retry converges: provision is idempotent (same org), no orphan.
	if _, err := onboardFirstRun(context.Background(), iam, "dave", "dave", "Dave", true); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if provisionCalls != 2 {
		t.Fatalf("provision calls = %d, want 2 (retried, converges)", provisionCalls)
	}
}
