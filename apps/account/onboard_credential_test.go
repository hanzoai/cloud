package account

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOnboardFirstRun_RevealsTheCredentialItMinted — provisioning mints the org's
// credential and the secret half is shown ONCE, on the response that mints it
// (IAM stores only its argon2id digest and blanks the plaintext, so there is no
// second chance to read it). Dropping it on the floor left a customer holding an
// account whose credential had been issued and could never be obtained.
func TestOnboardFirstRun_RevealsTheCredentialItMinted(t *testing.T) {
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/users/get":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"data":   map[string]any{"owner": "hanzo", "name": "dave"},
			})
		case "/v1/iam/admin/provision":
			_, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"org": "dave", "accessKey": "pk-live-abc", "accessSecret": "sk-live-xyz",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer iamSrv.Close()

	iam := &iamClient{base: iamSrv.URL, clientID: "c", clientSecret: "s", serviceToken: "svc", http: &http.Client{}}
	resp, err := onboardFirstRun(t.Context(), iam, "hanzo/dave", "dave", "Dave", true)
	if err != nil {
		t.Fatalf("onboardFirstRun: %v", err)
	}
	if resp.AccessKey != "pk-live-abc" {
		t.Fatalf("accessKey = %q, want the minted pk- (the caller has no other way to learn it)", resp.AccessKey)
	}
	if resp.AccessSecret != "sk-live-xyz" {
		t.Fatalf("accessSecret = %q, want the one-time reveal of the minted sk-", resp.AccessSecret)
	}
}

// TestOnboardFirstRun_RevealsNothingItDidNotMint — on a replay IAM returns the
// access key but no secret (it holds only the digest). The response must then
// carry no secret rather than an empty field a client could mistake for one.
func TestOnboardFirstRun_RevealsNothingItDidNotMint(t *testing.T) {
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/users/get":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "data": map[string]any{"owner": "hanzo", "name": "dave"},
			})
		case "/v1/iam/admin/provision":
			_ = json.NewEncoder(w).Encode(map[string]any{"org": "dave", "accessKey": "pk-live-abc"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer iamSrv.Close()

	iam := &iamClient{base: iamSrv.URL, clientID: "c", clientSecret: "s", serviceToken: "svc", http: &http.Client{}}
	resp, err := onboardFirstRun(t.Context(), iam, "hanzo/dave", "dave", "Dave", true)
	if err != nil {
		t.Fatalf("onboardFirstRun: %v", err)
	}
	if resp.AccessSecret != "" {
		t.Fatalf("accessSecret = %q, want empty — a replay re-reveals nothing", resp.AccessSecret)
	}
}
