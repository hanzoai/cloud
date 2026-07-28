package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// mintIAM is a stand-in IAM bootstrap endpoint. It asserts the request shape the
// real one enforces (bearer service token, org, name==clientId contract) and can
// be told to fail or answer with a wrong identity.
type mintIAM struct {
	token    string
	calls    atomic.Int32
	status   int    // 0 ⇒ 200
	clientID string // "" ⇒ echo the requested name (the real server's default)
}

func (m *mintIAM) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got != m.token {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"status":"error","msg":"a valid service token is required"}`))
			return
		}
		var req struct {
			Organization string   `json:"organization"`
			Name         string   `json:"name"`
			GrantTypes   []string `json:"grantTypes"`
			Cert         string   `json:"cert"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Organization == "" || req.Name != req.Organization+"-platform-kms" {
			t.Errorf("upsert body violates the naming contract: org=%q name=%q", req.Organization, req.Name)
		}
		if len(req.GrantTypes) != 1 || req.GrantTypes[0] != "client_credentials" {
			t.Errorf("grantTypes = %v, want [client_credentials]", req.GrantTypes)
		}
		if req.Cert == "" {
			// An app without a signing cert exists but cannot sign — its tokens
			// 500 at the token endpoint. Found live; pinned here.
			t.Error("upsert omitted the signing cert")
		}
		if m.status != 0 {
			w.WriteHeader(m.status)
			_, _ = w.Write([]byte(`{"status":"error"}`))
			return
		}
		cid := m.clientID
		if cid == "" {
			cid = req.Name
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "action": "created",
			"data": map[string]any{"clientId": cid, "clientSecret": "s3cr3t-" + req.Organization},
		})
	}
}

func minterFor(url, token string, kms cloud.KMSClient) *kmsOrgIdentity {
	return &kmsOrgIdentity{kms: kms, mint: &identityMinter{
		upsertURL:    url + "/v1/iam/admin/applications/upsert",
		serviceToken: token,
		cert:         "cert-hanzo",
		hc:           &http.Client{Timeout: 5 * time.Second},
	}}
}

// TestMintProvisionsOnceThenReads: the first call mints at IAM and seals both
// fields; every later call is a pure read that never touches IAM again.
func TestMintProvisionsOnceThenReads(t *testing.T) {
	iam := &mintIAM{token: "svc-tok"}
	srv := httptest.NewServer(iam.handler(t))
	defer srv.Close()
	kms := newFakeKMS()
	p := minterFor(srv.URL, "svc-tok", kms)

	id, secret, err := p.EnsureOrgIdentity(context.Background(), "acme")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if id != "acme-platform-kms" || secret != "s3cr3t-acme" {
		t.Fatalf("minted identity = (%q,%q)", id, secret)
	}
	// Sealed at the exact refs the read path uses.
	for ref, want := range map[string]string{
		kmsAuthRef("acme", "clientId"):     "acme-platform-kms",
		kmsAuthRef("acme", "clientSecret"): "s3cr3t-acme",
	} {
		got, err := kms.GetSecret(context.Background(), ref)
		if err != nil || string(got) != want {
			t.Fatalf("sealed %s = (%q,%v), want %q", ref, got, err, want)
		}
	}
	// Second call: read path, IAM untouched.
	if _, _, err := p.EnsureOrgIdentity(context.Background(), "acme"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if n := iam.calls.Load(); n != 1 {
		t.Fatalf("IAM called %d times, want exactly 1 (later calls must be reads)", n)
	}
}

// TestMintUnconfiguredStaysFailClosed: no minter ⇒ the pre-existing behavior,
// byte for byte — an error naming the missing seal, and nothing written anywhere.
func TestMintUnconfiguredStaysFailClosed(t *testing.T) {
	kms := newFakeKMS()
	p := &kmsOrgIdentity{kms: kms} // mint nil
	_, _, err := p.EnsureOrgIdentity(context.Background(), "acme")
	if err == nil || !strings.Contains(err.Error(), "no per-tenant KMS credential provisioned") {
		t.Fatalf("err = %v, want the fail-closed pending error", err)
	}
	if _, err := kms.GetSecret(context.Background(), kmsAuthRef("acme", "clientId")); err == nil {
		t.Fatal("read-only provider sealed a credential")
	}
}

// TestMintIAMDownSealsNothing: an IAM that errors must leave the org pending
// with NOTHING sealed — a credential is never fabricated from a failure.
func TestMintIAMDownSealsNothing(t *testing.T) {
	iam := &mintIAM{token: "svc-tok", status: 500}
	srv := httptest.NewServer(iam.handler(t))
	defer srv.Close()
	kms := newFakeKMS()
	p := minterFor(srv.URL, "svc-tok", kms)

	if _, _, err := p.EnsureOrgIdentity(context.Background(), "acme"); err == nil {
		t.Fatal("IAM 500 did not surface as an error")
	}
	if _, err := kms.GetSecret(context.Background(), kmsAuthRef("acme", "clientSecret")); err == nil {
		t.Fatal("a failed mint sealed a credential")
	}
}

// TestMintWrongServiceTokenRefused: the upsert self-authenticates; a wrong token
// is a 401 and the org stays pending with nothing sealed.
func TestMintWrongServiceTokenRefused(t *testing.T) {
	iam := &mintIAM{token: "the-real-token"}
	srv := httptest.NewServer(iam.handler(t))
	defer srv.Close()
	kms := newFakeKMS()
	p := minterFor(srv.URL, "a-wrong-token", kms)

	if _, _, err := p.EnsureOrgIdentity(context.Background(), "acme"); err == nil {
		t.Fatal("wrong service token was accepted")
	}
	if _, err := kms.GetSecret(context.Background(), kmsAuthRef("acme", "clientId")); err == nil {
		t.Fatal("a refused mint sealed a credential")
	}
}

// TestMintRefusesSurpriseClientID pins the audience contract: aud == clientId ==
// "<org>-platform-kms". An IAM answering with any other clientId is a different
// identity, and sealing it would bind the org's sync to something unverified.
func TestMintRefusesSurpriseClientID(t *testing.T) {
	iam := &mintIAM{token: "svc-tok", clientID: "not-what-was-asked"}
	srv := httptest.NewServer(iam.handler(t))
	defer srv.Close()
	kms := newFakeKMS()
	p := minterFor(srv.URL, "svc-tok", kms)

	_, _, err := p.EnsureOrgIdentity(context.Background(), "acme")
	if err == nil || !strings.Contains(err.Error(), "not-what-was-asked") {
		t.Fatalf("err = %v, want a refusal naming the surprise clientId", err)
	}
	if _, err := kms.GetSecret(context.Background(), kmsAuthRef("acme", "clientId")); err == nil {
		t.Fatal("a mismatched identity was sealed")
	}
}

// TestMintSealFailureIsPendingNotSplit: if sealing fails the caller gets an
// error (pending); the next call re-mints — the upsert is idempotent and
// preserves the secret, so a retry converges rather than rotating.
func TestMintSealFailureIsPendingNotSplit(t *testing.T) {
	iam := &mintIAM{token: "svc-tok"}
	srv := httptest.NewServer(iam.handler(t))
	defer srv.Close()
	kms := newFakeKMS()
	kms.fail = true
	p := minterFor(srv.URL, "svc-tok", kms)

	if _, _, err := p.EnsureOrgIdentity(context.Background(), "acme"); err == nil {
		t.Fatal("seal failure did not surface as an error")
	}
	// Store healthy again: the retry converges through a fresh mint.
	kms.fail = false
	id, _, err := p.EnsureOrgIdentity(context.Background(), "acme")
	if err != nil || id != "acme-platform-kms" {
		t.Fatalf("retry after seal failure: id=%q err=%v", id, err)
	}
}
