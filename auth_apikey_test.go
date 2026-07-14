// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// iamKeys.lookup maps an IAM get-user?accessKey row to the SAME idClaims a JWT
// yields, so the one minting path serves a key and a session identically.
func TestIAMKeysLookup(t *testing.T) {
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("accessKey")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"hanzo","name":"z","email":"z@hanzo.ai","isAdmin":true}}`))
	}))
	defer srv.Close()

	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	c := k.resolve(context.Background(), "hk-abc123")
	if c == nil {
		t.Fatal("resolve returned nil for a valid key")
	}
	if c.Owner != "hanzo" || c.Name != "z" || c.Email != "z@hanzo.ai" || !c.IsAdmin {
		t.Fatalf("claims = %+v, want owner=hanzo name=z email=z@hanzo.ai isAdmin=true", c)
	}
	// The key is passed as accessKey and the confidential credential is sent.
	if gotKey != "hk-abc123" {
		t.Errorf("IAM got accessKey=%q, want hk-abc123", gotKey)
	}
	if gotAuth != "Basic test" {
		t.Errorf("IAM got auth=%q, want the confidential Basic credential", gotAuth)
	}
	// userID falls through to name (a key has no UUID subject) — the owner/name
	// path IAM's privileged lookups expect.
	if c.userID() != "z" || c.username() != "z" {
		t.Errorf("userID=%q username=%q, want both z", c.userID(), c.username())
	}
}

// An unconfigured resolver (no confidential credential) resolves nothing, so an
// API key stays anonymous rather than mis-resolved.
func TestIAMKeysUnconfigured(t *testing.T) {
	k := &iamKeys{base: "http://iam", auth: "", cache: newCache[string, *idClaims](time.Minute)}
	if c := k.resolve(context.Background(), "hk-abc"); c != nil {
		t.Fatalf("unconfigured resolver returned %+v, want nil", c)
	}
}

// An unknown key (IAM status != ok) resolves to nil — a bad key never grants trust.
func TestIAMKeysUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","msg":"Unauthorized operation"}`))
	}))
	defer srv.Close()
	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	if c := k.resolve(context.Background(), "hk-bad"); c != nil {
		t.Fatalf("unknown key resolved to %+v, want nil", c)
	}
}

// The cache serves a resolved key without a second IAM call (and caches a miss too,
// so a bad key cannot hammer IAM).
func TestIAMKeysCache(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"hanzo","name":"z"}}`))
	}))
	defer srv.Close()
	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	for i := 0; i < 3; i++ {
		if k.resolve(context.Background(), "hk-x") == nil {
			t.Fatal("resolve nil")
		}
	}
	if calls != 1 {
		t.Fatalf("IAM called %d times, want 1 (cached)", calls)
	}
}
