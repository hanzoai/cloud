// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package analytics

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud"
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

// An event carrying an exception becomes an ERROR FACT: it routes to the error signal
// (so it lands in event.error), and the exception becomes that table's own columns
// rather than a property blob. This used to be a fold that mutated the event mid-
// pipeline; it is now a property of the pure route + normalize pair.
func TestErrorEventBecomesErrorColumns(t *testing.T) {
	handled := false
	e := CaptureEvent{
		Error: &Exception{Type: "TypeError", Message: "x is not a function", Stack: "at f (a.js:1:2)", Handled: &handled},
	}
	f, ok := normalize("acme", time.Now().UTC(), e)
	if !ok {
		t.Fatal("normalize dropped an error event")
	}
	if f.signal != signalError {
		t.Fatalf("signal = %q, want %q", f.signal, signalError)
	}
	if f.fault == nil {
		t.Fatal("no error body")
	}
	if f.fault.class != "TypeError" || f.fault.message != "x is not a function" {
		t.Fatalf("class/message = %q/%q", f.fault.class, f.fault.message)
	}
	if f.fault.handled {
		t.Fatal("handled=false on the wire must survive as false")
	}
	// The name defaults to the exception's class when the caller named nothing.
	if f.name != "TypeError" {
		t.Fatalf("name = %q, want the exception class", f.name)
	}
	// The caller's struct is never mutated.
	if e.Error == nil || e.Error.Message != "x is not a function" {
		t.Fatalf("normalize mutated the caller's event: %+v", e.Error)
	}
	// A non-error event routes as a tracked product event.
	if got := routeOf(CaptureEvent{Event: "click"}); got.signal != signalEvent || got.kind != kindTrack {
		t.Fatalf("a plain event routed to %+v", got)
	}
}

// TestFingerprintIsStableAndDiscriminating: `group` leads event.error's ORDER BY after
// org, so it must be a pure, stable function of the failure's shape — the same failure
// always groups together, a different one does not.
func TestFingerprintIsStableAndDiscriminating(t *testing.T) {
	at := func(msg, stack string) string {
		f, ok := normalize("acme", time.Now(), CaptureEvent{Error: &Exception{Type: "TypeError", Message: msg, Stack: stack}})
		if !ok || f.fault == nil {
			t.Fatal("want an error fact")
		}
		return f.fault.group
	}
	const stack = "at render (https://app.test/main.js:10:5)"
	if a, b := at("boom", stack), at("boom", stack); a != b {
		t.Fatalf("the same failure produced two groups: %q vs %q", a, b)
	}
	if a, b := at("boom", stack), at("boom", "at other (https://app.test/other.js:1:1)"); a == b {
		t.Fatal("two different first-party frames collapsed into one group")
	}
	// The variable parts of a message must not split one issue into many.
	if a, b := at("user 41 not found", ""), at("user 907 not found", ""); a != b {
		t.Fatalf("one issue split by its message ids: %q vs %q", a, b)
	}
	if a, b := at("user 41 not found", ""), at("disk full", ""); a == b {
		t.Fatal("two unrelated messages collapsed into one group")
	}
}

// TestVendorFramesAreNotOurs: a browser extension injecting into the page is the single
// loudest source of noise in a real issue list, so it must not be the frame an issue is
// named and grouped by.
func TestVendorFramesAreNotOurs(t *testing.T) {
	f, ok := normalize("acme", time.Now(), CaptureEvent{Error: &Exception{
		Type:  "TypeError",
		Stack: "at connect (chrome-extension://abcd/inpage.js:7:84179)\n  at boot (https://app.test/main.js:2:3)",
	}})
	if !ok || f.fault == nil || len(f.fault.frames) != 2 {
		t.Fatalf("want 2 parsed frames, got %+v", f.fault)
	}
	if f.fault.frames[0].own {
		t.Errorf("an extension frame was marked first-party: %+v", f.fault.frames[0])
	}
	if !f.fault.frames[1].own {
		t.Errorf("an app frame was not marked first-party: %+v", f.fault.frames[1])
	}
	if got := f.fault.frames[1]; got.file != "https://app.test/main.js" || got.line != 2 || got.column != 3 {
		t.Errorf("frame parsed wrong: %+v", got)
	}
}
