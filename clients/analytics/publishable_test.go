// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package analytics

import (
	"encoding/json"
	"testing"
	"time"
)

const testSecret = "test-ingest-secret-0123456789"

// mint→verify is a round trip: a key minted for an org verifies back to exactly
// that org under the same secret.
func TestPublishableKeyRoundTrip(t *testing.T) {
	for _, org := range []string{"acme", "hanzo", "maxpower", "org-with-dashes", "MixedCase"} {
		key, ok := mintPublishableKey(testSecret, org)
		if !ok {
			t.Fatalf("mint failed for %q", org)
		}
		got, ok := verifyPublishableKey(testSecret, key)
		if !ok {
			t.Fatalf("verify failed for freshly minted key %q", key)
		}
		if got != org {
			t.Fatalf("round trip org = %q, want %q", got, org)
		}
	}
}

// The key is write-only by construction: pk_ is NOT accepted by the isAPIKey
// family, so it can never be minted into a bearer principal. (Guards the prefix
// choice — an accidental switch to a dash prefix would silently make the key
// readable.) We assert the prefix here; isAPIKey lives in the parent package.
func TestPublishablePrefixIsUnderscore(t *testing.T) {
	key, _ := mintPublishableKey(testSecret, "acme")
	if key[:3] != "pk_" {
		t.Fatalf("publishable key must start with pk_ (write-only lane), got %q", key[:3])
	}
}

// verify FAILS CLOSED on every malformed / forged / unconfigured case.
func TestVerifyFailsClosed(t *testing.T) {
	good, _ := mintPublishableKey(testSecret, "acme")
	cases := []struct {
		name, secret, key string
	}{
		{"no secret", "", good},
		{"wrong prefix", testSecret, "sk-abcdef"},
		{"empty", testSecret, ""},
		{"no delimiter", testSecret, "pk_YWNtZQ"},
		{"trailing delimiter", testSecret, "pk_YWNtZQ."},
		{"leading delimiter", testSecret, "pk_.YWNtZQ"},
		{"bad base64 org", testSecret, "pk_!!!.YWNtZQ"},
		{"bad base64 sig", testSecret, "pk_YWNtZQ.!!!"},
		{"wrong secret", "other-secret", good},
		{"tampered org keeps old sig", testSecret, forgeOrg(good, "evil")},
	}
	for _, tc := range cases {
		if org, ok := verifyPublishableKey(tc.secret, tc.key); ok {
			t.Errorf("%s: verify admitted a bad key → org %q (want fail closed)", tc.name, org)
		}
	}
}

// forgeOrg swaps the org segment of a real key while keeping its signature — the
// canonical forgery attempt the HMAC must reject.
func forgeOrg(key, newOrg string) string {
	// pk_<b64org>.<b64sig> — replace the b64org segment.
	dot := -1
	for i := 3; i < len(key); i++ {
		if key[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return key
	}
	enc := base64Raw(newOrg)
	return "pk_" + enc + key[dot:]
}

func base64Raw(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	src := []byte(s)
	var out []byte
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		n := copy(b[:], src[i:])
		out = append(out, alphabet[b[0]>>2])
		out = append(out, alphabet[(b[0]&0x03)<<4|b[1]>>4])
		if n > 1 {
			out = append(out, alphabet[(b[1]&0x0f)<<2|b[2]>>6])
		}
		if n > 2 {
			out = append(out, alphabet[b[2]&0x3f])
		}
	}
	return string(out)
}

// foldException lifts a type:'error' event's exception into properties.$exception
// and defaults the type, so the write core stores it as event_type='error'.
func TestFoldException(t *testing.T) {
	handled := false
	e := CaptureEvent{
		Error: &Exception{Type: "TypeError", Message: "x is not a function", Stack: "at f()", Handled: &handled},
	}
	got := foldException(e)
	if got.Type != "error" {
		t.Fatalf("type = %q, want error", got.Type)
	}
	if got.Error != nil {
		t.Fatalf("error object must be lifted out (nil after fold), got %+v", got.Error)
	}
	ex, ok := got.Properties["$exception"]
	if !ok {
		t.Fatal("properties.$exception missing after fold")
	}
	b, _ := json.Marshal(ex)
	var back Exception
	if json.Unmarshal(b, &back) != nil || back.Message != "x is not a function" {
		t.Fatalf("lifted exception malformed: %s", b)
	}

	// normalizeEvent must then store event_type='error'.
	row, ok := normalizeEvent("acme", time.Now().UTC(), got)
	if !ok {
		t.Fatal("normalize dropped a folded error event")
	}
	if row.eventType != "error" || row.event != "$error" {
		t.Fatalf("stored type/event = %q/%q, want error/$error", row.eventType, row.event)
	}

	// A non-error event is untouched.
	plain := CaptureEvent{Event: "click"}
	if foldException(plain).Type != "" {
		t.Fatal("non-error event was mutated by foldException")
	}
}

// canonicalType now recognizes error as first-class (still folds unknowns).
func TestCanonicalTypeError(t *testing.T) {
	if canonicalType("error") != "error" {
		t.Fatal("error must canonicalize to error")
	}
	if canonicalType("ERROR") != "error" {
		t.Fatal("error canonicalization must be case-insensitive")
	}
	if canonicalType("weird") != "event" {
		t.Fatal("unknown type must still fold to event")
	}
}
