// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package fleet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeCompute stands in for Compute's credential mint, counting how often it was
// asked and signing each answer with a distinct token.
func fakeCompute(t *testing.T, life time.Duration) *int32 {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _, ok := r.BasicAuth(); !ok || id != "cloud" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		i := atomic.AddInt32(&n, 1)
		creds := credentials{
			Endpoint: "https://8.8.8.8:6443",
			CAData:   []byte("ca"),
			Token:    "minted-" + string(rune('0'+i)),
			Expiry:   time.Now().Add(life),
		}
		body, _ := json.Marshal(creds)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": json.RawMessage(body)})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMPUTE_URL", srv.URL)
	t.Setenv("COMPUTE_CLIENT_ID", "cloud")
	t.Setenv("COMPUTE_CLIENT_SECRET", "secret")
	return &n
}

// A managed record builds a config that names the apiserver and its CA and gets
// its bearer from a token source — never from anything at rest, because a
// managed record holds no token.
func TestManagedConfigCarriesTheEndpointAndMintsItsToken(t *testing.T) {
	fakeCompute(t, time.Hour)
	sealed, _ := json.Marshal(Managed{Provider: "DigitalOcean", ID: "cl-1",
		Endpoint: "https://8.8.8.8:6443", CAData: []byte("ca-pem")})

	cfg, err := restForManaged("acme", sealed)
	if err != nil {
		t.Fatalf("restForManaged: %v", err)
	}
	if cfg.Host != "https://8.8.8.8:6443" {
		t.Errorf("host = %q", cfg.Host)
	}
	if string(cfg.TLSClientConfig.CAData) != "ca-pem" {
		t.Errorf("caData = %q", cfg.TLSClientConfig.CAData)
	}
	if cfg.BearerToken != "" || cfg.BearerTokenFile != "" {
		t.Error("a managed config must hold no token of its own")
	}
	if cfg.WrapTransport == nil {
		t.Fatal("a managed config must fetch its bearer per connection")
	}

	// The bearer reaches the wire, and it is the one Compute minted.
	var seen string
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	t.Cleanup(apiserver.Close)
	rt := cfg.WrapTransport(http.DefaultTransport)
	req, _ := http.NewRequest(http.MethodGet, apiserver.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = resp.Body.Close()
	if seen != "Bearer minted-1" {
		t.Fatalf("apiserver saw %q, want the minted bearer", seen)
	}
}

// A token in hand is reused until it is nearly out of time, and replaced before
// it dies — a token that expires mid-request fails that request, so the last
// minutes of its life are spent on the next one.
func TestATokenIsReusedUntilItIsNearlyExpired(t *testing.T) {
	n := fakeCompute(t, time.Hour)
	m := &minter{org: "acme", cluster: "cl-1"}

	first, err := m.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	again, err := m.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if again.AccessToken != first.AccessToken {
		t.Errorf("a live token was replaced: %q then %q", first.AccessToken, again.AccessToken)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("minted %d times for one live token, want 1", got)
	}

	// Inside the replacement window: still valid, but not for long enough.
	m.tok = &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(early / 2)}
	fresh, err := m.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if fresh.AccessToken == "stale" {
		t.Error("a token inside its replacement window was handed out anyway")
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("minted %d times, want a second mint for the expiring token", got)
	}

	// Just outside it: plenty of life left, so nothing is minted.
	m.tok = &oauth2.Token{AccessToken: "good", Expiry: time.Now().Add(early * 5)}
	if tok, _ := m.Token(); tok.AccessToken != "good" {
		t.Errorf("a token well inside its life was replaced by %q", tok.AccessToken)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("minted %d times, want no mint for a healthy token", got)
	}
}

// The endpoint of a managed cluster goes through the same host guard an attached
// kubeconfig's does. A record is written by the create path rather than pasted
// by a customer, but the guard is where SSRF is refused and it does not get to
// have an exception.
func TestAManagedEndpointIsGuardedLikeAnyOther(t *testing.T) {
	for name, endpoint := range map[string]string{
		"plaintext":    "http://8.8.8.8:6443",
		"loopback":     "https://127.0.0.1:6443",
		"private":      "https://10.0.0.5:6443",
		"link local":   "https://169.254.169.254",
		"not a URL":    "cl-1",
		"no authority": "https://",
	} {
		t.Run(name, func(t *testing.T) {
			sealed, _ := json.Marshal(Managed{ID: "cl-1", Endpoint: endpoint})
			if _, err := restForManaged("acme", sealed); err == nil {
				t.Fatalf("%s endpoint was accepted", name)
			}
		})
	}
}

// Reaching a managed cluster is cloud acting as itself. Without its own Compute
// credential it says so, rather than falling back to an unauthenticated mint
// that would either fail obscurely or, worse, succeed.
func TestMintingNeedsCloudsOwnCredential(t *testing.T) {
	t.Setenv("COMPUTE_URL", "http://compute.invalid")
	t.Setenv("COMPUTE_CLIENT_ID", "")
	t.Setenv("COMPUTE_CLIENT_SECRET", "")
	_, err := (&minter{org: "acme", cluster: "cl-1"}).Token()
	if err == nil || !strings.Contains(err.Error(), "COMPUTE_CLIENT_ID") {
		t.Fatalf("want a refusal naming the missing credential, got %v", err)
	}
}

// Compute reports a logical failure as status:"error" inside a 200, so the status
// code alone does not decide. Reading one as a token would hand the apiserver an
// empty bearer and report the 401 as the cluster's fault.
func TestAnErrorEnvelopeIsNotAToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "msg": "cluster not found"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COMPUTE_URL", srv.URL)
	t.Setenv("COMPUTE_CLIENT_ID", "cloud")
	t.Setenv("COMPUTE_CLIENT_SECRET", "secret")

	_, err := (&minter{org: "acme", cluster: "cl-1"}).Token()
	if err == nil || !strings.Contains(err.Error(), "cluster not found") {
		t.Fatalf("want the upstream's reason, got %v", err)
	}
}
