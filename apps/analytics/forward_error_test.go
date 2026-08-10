// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package analytics

import (
	"testing"
	"time"
)

// collectErrors installs an error sink that forwards the batch for one org onto a
// channel, and returns the channel + the sink's remover. Mirrors forward_test's
// SinkEvent probe.
func collectErrors(t *testing.T, wantOrg string) (<-chan []ErrorEvent, func()) {
	t.Helper()
	got := make(chan []ErrorEvent, 1)
	remove := AddErrorSink(func(org string, errs []ErrorEvent) {
		if org == wantOrg {
			got <- errs
		}
	})
	return got, remove
}

// TestFanOutErrorsFoldedException verifies the primary /v1/event path: an event whose
// top-level error was folded (foldException — Type defaulted to "error", the scrubbed
// exception copied into properties.$exception) is routed to the error sink with its
// exception fields + identity/context carried.
func TestFanOutErrorsFoldedException(t *testing.T) {
	got, done := collectErrors(t, "acme")
	defer done()

	handled := false
	// foldException runs in ingestDecoded before the write core; replicate it here so
	// the fan-out sees exactly what production hands it.
	folded := foldException(CaptureEvent{
		Error:      &Exception{Type: "TypeError", Message: "x is not a function", Stack: "at f (app.js:1:1)", Handled: &handled},
		DistinctID: "u1", SessionID: "s1", Path: "/checkout", URL: "https://acme.ai/checkout",
		Product: "app", Library: "@hanzo/event",
		Properties: map[string]any{"$release": "v2", "$trace_id": "abc", "keep": "me"},
	})

	fanOut("acme", []CaptureEvent{
		folded,
		{Type: "pageview"},              // not an error — must be skipped
		{Type: "event", Event: "click"}, // not an error — must be skipped
	})

	select {
	case errs := <-got:
		if len(errs) != 1 {
			t.Fatalf("want 1 error event (pageview+click skipped), got %d", len(errs))
		}
		e := errs[0]
		if e.ExceptionType != "TypeError" || e.Message != "x is not a function" {
			t.Errorf("exception not carried: %+v", e)
		}
		if e.Stack != "at f (app.js:1:1)" {
			t.Errorf("stack not carried: %q", e.Stack)
		}
		if e.Handled == nil || *e.Handled != false {
			t.Errorf("handled flag not carried: %v", e.Handled)
		}
		if e.DistinctID != "u1" || e.SessionID != "s1" {
			t.Errorf("identity not carried: %+v", e)
		}
		if e.Transaction != "/checkout" || e.Path != "/checkout" || e.URL != "https://acme.ai/checkout" {
			t.Errorf("route not carried: %+v", e)
		}
		if e.Product != "app" || e.Library != "@hanzo/event" {
			t.Errorf("surface not carried: %+v", e)
		}
		if e.Release != "v2" || e.TraceID != "abc" {
			t.Errorf("property fallbacks not carried: release=%q trace=%q", e.Release, e.TraceID)
		}
		if e.Level != "error" {
			t.Errorf("level = %q, want error", e.Level)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error sink was not called")
	}
}

// TestFanOutErrorsEnvelopeFields verifies the first-class envelope qualifiers win over
// the property spellings: a wire that states release/environment/service/site/trace on
// the envelope hands exactly those to the sink.
func TestFanOutErrorsEnvelopeFields(t *testing.T) {
	got, done := collectErrors(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{{
		Type: "error", Error: &Exception{Message: "boom"},
		Release: "v3", Environment: "prod", Service: "gateway", Site: "acme.ai",
		TraceID: "t-1", SpanID: "s-1",
		Properties: map[string]any{"$release": "stale", "$trace_id": "stale"},
	}})

	select {
	case errs := <-got:
		e := errs[0]
		if e.Release != "v3" || e.Environment != "prod" {
			t.Errorf("envelope release/environment must win: %+v", e)
		}
		if e.Service != "gateway" || e.Site != "acme.ai" {
			t.Errorf("service/site not carried: %+v", e)
		}
		if e.TraceID != "t-1" || e.SpanID != "s-1" {
			t.Errorf("envelope trace linkage must win: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error sink was not called")
	}
}

// TestFanOutErrorsNativeExceptionMap verifies the JSON-wire path: an error-typed event
// carrying $exception as a decoded map (not the typed *Exception). exceptionOf must
// read type/message/stack/handled out of the map.
func TestFanOutErrorsNativeExceptionMap(t *testing.T) {
	got, done := collectErrors(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{{
		Type: "error", Event: "$error",
		Properties: map[string]any{
			"$exception": map[string]any{
				"type": "RangeError", "message": "out of range", "stack": "at g()", "handled": true,
			},
		},
	}})

	select {
	case errs := <-got:
		if len(errs) != 1 {
			t.Fatalf("want 1, got %d", len(errs))
		}
		e := errs[0]
		if e.ExceptionType != "RangeError" || e.Message != "out of range" || e.Stack != "at g()" {
			t.Errorf("map exception not read: %+v", e)
		}
		if e.Handled == nil || *e.Handled != true {
			t.Errorf("handled from map not read: %v", e.Handled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error sink was not called")
	}
}

// TestFanOutErrorsTypedErrorNoException verifies a bare type:'error' event with NO
// exception is still carried (the consumer groups it on message/transaction).
func TestFanOutErrorsTypedErrorNoException(t *testing.T) {
	got, done := collectErrors(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{{Type: "error", Event: "boom", Path: "/x"}})

	select {
	case errs := <-got:
		if len(errs) != 1 {
			t.Fatalf("want 1, got %d", len(errs))
		}
		if errs[0].ExceptionType != "" || errs[0].Message != "" {
			t.Errorf("bare error should carry no exception: %+v", errs[0])
		}
		if errs[0].Transaction != "/x" {
			t.Errorf("transaction = %q, want /x", errs[0].Transaction)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error sink was not called for a bare error-typed event")
	}
}

// TestFanOutErrorsIsThePlanesRoute pins the filter to routeOf — the projection carries
// exactly the facts the plane files under the error signal, so sentry.hanzo.ai and
// event.fact can never disagree about what an error is. In particular a $exception
// property on an event the plane routes as an act does NOT reach the sink.
func TestFanOutErrorsIsThePlanesRoute(t *testing.T) {
	got, done := collectErrors(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{
		{Type: "ERROR", Event: "case-folded"},    // routed error: type folds lower
		{Error: &Exception{Message: "typeless"}}, // routed error: typeless + exception
		{Type: "event", Event: "decorated", Properties: map[string]any{ // routed act: the plane's word wins
			"$exception": map[string]any{"message": "not an error fact"},
		}},
	})

	select {
	case errs := <-got:
		if len(errs) != 2 {
			t.Fatalf("want the 2 error-routed events, got %d: %+v", len(errs), errs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error sink was not called")
	}
}

// TestFanOutErrorsNoErrorsNoDispatch verifies a batch with zero error events never
// dispatches to the error sink (the common case — a normal pageview/event batch).
func TestFanOutErrorsNoErrorsNoDispatch(t *testing.T) {
	fired := make(chan struct{}, 1)
	remove := AddErrorSink(func(org string, errs []ErrorEvent) { fired <- struct{}{} })
	defer remove()

	fanOut("acme", []CaptureEvent{
		{Type: "pageview"},
		{Type: "event", Event: "order_completed", Revenue: 10},
	})

	select {
	case <-fired:
		t.Fatal("error sink fired for a batch with no error events")
	case <-time.After(200 * time.Millisecond):
		// expected: no dispatch
	}
}

// TestFanOutErrorsNoSinksIsNoOp verifies fan-out is inert when no error sink is
// installed — the default when the o11y embed is off. A removed sink counts as absent.
func TestFanOutErrorsNoSinksIsNoOp(t *testing.T) {
	remove := AddErrorSink(func(string, []ErrorEvent) { t.Error("removed sink must not fire") })
	remove()
	fanOut("acme", []CaptureEvent{{Type: "error", Event: "boom"}})
	time.Sleep(50 * time.Millisecond)
}

// TestExceptionOf covers extraction from each carrier: typed Error, typed post-fold
// $exception, and decoded map.
func TestExceptionOf(t *testing.T) {
	h := true
	// typed Error field
	if typ, msg, stack, handled := exceptionOf(CaptureEvent{Error: &Exception{Type: "E", Message: "m", Stack: "s", Handled: &h}}); typ != "E" || msg != "m" || stack != "s" || handled == nil || !*handled {
		t.Errorf("typed Error: %q %q %q %v", typ, msg, stack, handled)
	}
	// post-fold typed (*Exception in properties)
	if typ, msg, _, _ := exceptionOf(CaptureEvent{Properties: map[string]any{"$exception": &Exception{Type: "E2", Message: "m2"}}}); typ != "E2" || msg != "m2" {
		t.Errorf("post-fold typed: %q %q", typ, msg)
	}
	// decoded map
	if typ, msg, stack, _ := exceptionOf(CaptureEvent{Properties: map[string]any{"$exception": map[string]any{"type": "E3", "message": "m3", "stack": "s3"}}}); typ != "E3" || msg != "m3" || stack != "s3" {
		t.Errorf("decoded map: %q %q %q", typ, msg, stack)
	}
	// none
	if typ, msg, _, _ := exceptionOf(CaptureEvent{Type: "error"}); typ != "" || msg != "" {
		t.Errorf("no exception should be empty: %q %q", typ, msg)
	}
}
