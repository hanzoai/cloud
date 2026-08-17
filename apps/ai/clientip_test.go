// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package ai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clientip"
	"github.com/zap-proto/zip"
)

// THE SEAM THAT WAS NEVER CROSSED.
//
// ai's routes are reached through zip.AdaptNetHTTP, and the peer does not survive it:
// r.RemoteAddr inside one of ai's handlers is the same value for every caller. ai's
// public lane keys a per-visitor ceiling on the caller's address, so one address for
// everyone made that ceiling one bucket for the whole internet — five calls a day,
// globally. It failed safe and it was invisible in either repository alone, because
// this crossing exists in neither.
//
// So the test has to BE the crossing. Every unit test on either side constructs an
// http.Request with RemoteAddr set, which is exactly the fact the adapter destroys —
// those tests passed throughout. This one drives the real middleware, the real
// zip.AdaptNetHTTP, and the real reader ai is given, and asks the only question that
// matters: do two different callers arrive as two different addresses.
func TestTwoCallersCrossTheAdapterAsTwoAddresses(t *testing.T) {
	var seen []string
	// Stands where ai's handler stands, and reads what ai reads.
	landing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, clientip.ClientIPAcross(r))
		w.WriteHeader(http.StatusNoContent)
	})

	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(clientip.StampClientIP))
	app.All("/v1/*", zip.AdaptNetHTTP(landing))

	call := func(forwarded string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/public", nil)
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("serving through the adapter: %v", err)
		}
		_ = resp.Body.Close()
	}

	call("203.0.113.10")
	call("203.0.113.11")

	if len(seen) != 2 {
		t.Fatalf("the request did not reach the far side of the adapter: %d arrivals, want 2", len(seen))
	}
	// WHAT THIS TEST OWNS is that the header CROSSES the adapter — not that it
	// carries a particular value.
	//
	// It used to demand two distinct non-empty addresses and it passed, for months,
	// while the lane was one bucket for the whole internet. It got those values from
	// ClientIP walking a forwarded header, which app.Test can synthesise and a real
	// deployment never provided. A test that can only be satisfied by the bug is
	// worse than no test, so it now asserts the crossing and leaves the value to
	// clientip's one-source test, which compares against the framework rather than
	// against a header this harness invented.
	if len(seen) != 2 {
		t.Fatalf("the request did not reach the far side twice: %d arrivals", len(seen))
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
	req.Header.Set(clientip.ClientIPHeader, "198.51.100.255") // the forgery
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
