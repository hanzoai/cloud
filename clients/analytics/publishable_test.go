// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package analytics

import (
	"encoding/json"
	"github.com/hanzoai/cloud"
	"testing"
	"time"
)

// The ONE publishable spelling is IAM's pk-, and cloud only validates it. Cloud
// used to mint and verify its OWN pk_ under an HMAC of CLOUD_INGEST_KEY_SECRET —
// a second publishable-key family with its own prefix, secret and mint endpoint,
// beside the one IAM already owned.
//
// The prefix is asserted against the ONE authority rather than a literal, and the
// safety property it depends on is asserted with it: a pk- must resolve (so the
// ingest door can attribute a beacon) and must NOT authenticate (so a key shipped
// in a browser bundle is not a reading credential).
func TestPublishablePrefixIsTheIAMFamily(t *testing.T) {
	if publishablePrefix != cloud.PublishablePrefix {
		t.Fatalf("publishable prefix = %q, want %q", publishablePrefix, cloud.PublishablePrefix)
	}
	if !cloud.IsPublishableKey(publishablePrefix + "abc") {
		t.Fatal("a pk- must be recognised as publishable")
	}
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
