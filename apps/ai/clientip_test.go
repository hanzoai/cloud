// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package ai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/ai/address"
	aictl "github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/cloud/clientip"
	"github.com/zap-proto/zip"
)

// THE SENDER AND THE READER, IN ONE PROCESS, ASKED THE ONLY QUESTION THAT MATTERS.
//
// This host resolves the caller and stamps it; ai's public lane keys a per-visitor
// ceiling on what it reads back. Those two halves are written in two repositories and
// they agree on a name — so the failure to test for is not a crash but a silence: a
// stamp under one name and a read under another compile perfectly, the read finds
// nothing, and the lane falls back to the socket peer. Behind the in-cluster ingress
// that peer is one value for everyone, and a ceiling per visitor becomes a ceiling for
// the internet.
//
// So this drives ai's OWN reader — controllers.Visitor, the function the ceiling
// actually calls — and not a stand-in with the same shape. A stand-in in this package
// reads through cloud's constant at both ends and would agree with itself no matter
// what ai was compiled to look for, which is the one thing that must not be assumed.
//
// TWO CALLERS, TWO VISITORS is the property the ceiling rests on, and every link in
// the chain is load-bearing for it: resolve, stamp, cross the adapter, read, believe.
// Break any one and both callers collapse onto the same visitor and this goes red.
func TestTheHostsStampIsWhatTheCeilingCounts(t *testing.T) {
	var seen []string
	// Stands where ai's handler stands, and asks ai what it makes of the caller.
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(clientip.StampClientIP))
	app.All("/v1/*", func(c *zip.Ctx) error {
		seen = append(seen, aictl.Visitor(c))
		return c.NoContent(http.StatusNoContent)
	})

	call := func(forwarded string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/public", nil)
		req.Header.Set("X-Forwarded-For", forwarded)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("serving: %v", err)
		}
		_ = resp.Body.Close()
	}

	call("203.0.113.10")
	call("203.0.113.11")

	if len(seen) != 2 {
		t.Fatalf("the request did not reach ai's reader: %d arrivals, want 2", len(seen))
	}
	if seen[0] == "" || seen[1] == "" {
		t.Fatalf("ai derived no visitor from a stamped caller: %q — the address did not survive the crossing", seen)
	}
	if seen[0] == seen[1] {
		t.Fatalf("two callers arrived as one visitor %q; the ceiling is one bucket for everyone", seen[0])
	}
}

// The host's answer REPLACES whatever the caller sent under the same name. The value
// crosses as a header because that is what the adapter reliably carries, so being
// unforgeable is a property of always overwriting it rather than of the carrier.
func TestACallerCannotNameItself(t *testing.T) {
	var seen string
	landing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = clientip.ClientIPAcross(r)
		w.WriteHeader(http.StatusNoContent)
	})

	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(clientip.StampClientIP))
	app.All("/v1/*", zip.AdaptNetHTTP(landing))

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/public", nil)
	req.Header.Set(address.Header, "198.51.100.255") // the forgery
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("serving through the adapter: %v", err)
	}
	_ = resp.Body.Close()

	if seen == "198.51.100.255" {
		t.Fatal("a caller named its own address and the stamp believed it — every visitor could then " +
			"mint a fresh ceiling per request")
	}
	// Empty is a legitimate answer when there is no address to resolve — what must
	// never happen is the caller's own claim arriving as the answer.
}
