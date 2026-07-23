// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package analytics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPublishableKeyMintAndUse is the end-to-end proof of the anonymous-site ingest
// flow (task: "mint a pk_ for the hanzo org so anonymous site ingest stops 403ing"):
//
//  1. A validated hanzo-org principal mints a pk_ at POST /v1/ingest/keys.
//  2. A subsequent POST /v1/event presents that key as a Bearer and NO principal — the
//     shape a marketing page's beacon has — and is tenant-resolved to hanzo.
//
// The load-bearing assertion is that step 2 is NOT 403: a 403 is the auth-fail code, so
// any other status means eventTenant accepted the pk_ and resolved the org. (With no
// datastore wired in the test it then 503s at the warehouse step — which itself proves
// the request got PAST auth into the write core.)
func TestPublishableKeyMintAndUse(t *testing.T) {
	t.Setenv(ingestKeySecretEnv, "test-ingest-secret-e2e-0123456789")
	app := mountApp(t)

	// 1. Mint as the hanzo org (validated principal: X-User-Id present, owner=hanzo).
	mintReq := httptest.NewRequest(http.MethodPost, "/v1/ingest/keys", nil)
	mintReq.Header.Set("X-User-Id", "u-admin")
	mintReq.Header.Set("X-Org-Id", "hanzo")
	mintResp, err := app.Fiber().Test(mintReq)
	if err != nil {
		t.Fatalf("mint request: %v", err)
	}
	defer func() { _ = mintResp.Body.Close() }()
	if mintResp.StatusCode != http.StatusOK {
		t.Fatalf("mint POST /v1/ingest/keys want 200, got %d", mintResp.StatusCode)
	}
	var minted struct {
		Key   string `json:"key"`
		Org   string `json:"org"`
		Scope string `json:"scope"`
	}
	body, _ := io.ReadAll(mintResp.Body)
	if err := json.Unmarshal(body, &minted); err != nil {
		t.Fatalf("mint response decode: %v (%s)", err, body)
	}
	if minted.Org != "hanzo" || minted.Scope != "ingest" {
		t.Fatalf("minted for the wrong org/scope: %+v", minted)
	}
	if !strings.HasPrefix(minted.Key, "pk_") {
		t.Fatalf("minted key is not a publishable key: %q", minted.Key)
	}

	// 2. Use the key on the canonical door with NO principal (anonymous site beacon).
	evReq := httptest.NewRequest(http.MethodPost, "/v1/event",
		strings.NewReader(`{"event":"$pageview","distinctId":"visitor-1"}`))
	evReq.Header.Set("Content-Type", "application/json")
	evReq.Header.Set("Authorization", "Bearer "+minted.Key)
	evResp, err := app.Fiber().Test(evReq)
	if err != nil {
		t.Fatalf("event request: %v", err)
	}
	defer func() { _ = evResp.Body.Close() }()
	if evResp.StatusCode == http.StatusForbidden {
		t.Fatalf("pk_ ingest was 403'd — anonymous site ingest still refused (tenant not resolved from the key)")
	}
}

// TestPublishableKeyAsQueryParam proves the sendBeacon shape: the key rides ?ingest_key=
// (navigator.sendBeacon cannot set headers), and is still accepted (not 403).
func TestPublishableKeyAsQueryParam(t *testing.T) {
	t.Setenv(ingestKeySecretEnv, "test-ingest-secret-e2e-0123456789")
	app := mountApp(t)

	key, ok := mintPublishableKey(ingestSecret(), "hanzo")
	if !ok {
		t.Fatal("mint failed")
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/event?ingest_key="+key,
		strings.NewReader(`{"event":"$pageview","distinctId":"v2"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("event request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("pk_ via ?ingest_key= was 403'd — sendBeacon path refused")
	}
}

// TestMintKeyRequiresPrincipal proves minting is NOT anonymous: without a validated
// principal the mint endpoint is 403 (only an org owner mints its own key).
func TestMintKeyRequiresPrincipal(t *testing.T) {
	t.Setenv(ingestKeySecretEnv, "test-ingest-secret-e2e-0123456789")
	app := mountApp(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/keys", nil) // no X-User-Id
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("mint request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous mint want 403, got %d", resp.StatusCode)
	}
}
