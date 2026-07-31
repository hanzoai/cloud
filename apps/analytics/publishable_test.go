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
// and defaults the type, so the plane normalizer routes it to event.error.
func TestFoldException(t *testing.T) {
	handled := false
	e := CaptureEvent{
		Error: &Exception{Type: "TypeError", Message: "x is not a function", Stack: "at f()", Handled: &handled},
	}
	got := foldException(e)
	if got.Type != "error" {
		t.Fatalf("type = %q, want error", got.Type)
	}
	// The exception SURVIVES the fold, redacted. It used to be nilled here, which was
	// right while the property bag was the only place an error could go; the event also
	// becomes a fact now, and faultOf reads this field to fill event.error's message,
	// class and group. Erased, that table got rows with none of the three.
	if got.Error == nil {
		t.Fatal("fold erased the exception — event.error's message, class and group come from it")
	}
	if got.Error.Message != "x is not a function" || got.Error.Type != "TypeError" {
		t.Fatalf("folded exception = %+v, want the redacted original", got.Error)
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

	// normalize must then route it to the ERROR signal — event.error, the table the
	// /v1/errors lens reads — with the fault carrying the exception's class.
	f, ok := normalize("acme", time.Now().UTC(), got)
	if !ok {
		t.Fatal("normalize dropped a folded error event")
	}
	if f.signal != signalError {
		t.Fatalf("signal = %q, want %q", f.signal, signalError)
	}
	if f.fault == nil || f.fault.class != "TypeError" {
		t.Fatalf("fault = %+v, want class TypeError", f.fault)
	}
	if f.attributes["$exception"] == "" {
		t.Fatal("attributes[$exception] missing — the /v1/errors lens surfaces the exception from it")
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
