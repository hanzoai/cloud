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
// channel, and returns the channel + a cleanup. Mirrors forward_test's SinkEvent probe.
func collectErrors(t *testing.T, wantOrg string) (<-chan []ErrorEvent, func()) {
	t.Helper()
	got := make(chan []ErrorEvent, 1)
	SetErrorSink(func(org string, errs []ErrorEvent) {
		if org == wantOrg {
			got <- errs
		}
	})
	return got, func() { SetErrorSink(nil) }
}

// TestFanOutErrors_FoldedException verifies the primary /v1/event path: an event whose
// top-level error was folded into properties.$exception (a *Exception) is detected as an
// error and its exception fields + identity/context are carried to the error sink.
func TestFanOutErrors_FoldedException(t *testing.T) {
	got, done := collectErrors(t, "acme")
	defer done()

	handled := false
	// foldException runs in ingestBody before the write core; replicate it here so the
	// fan-out sees exactly what production hands it (Type=error, $exception=*Exception).
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
			t.Errorf("best-effort props not carried: release=%q trace=%q", e.Release, e.TraceID)
		}
		if e.Level != "error" {
			t.Errorf("level = %q, want error", e.Level)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error sink was not called")
	}
}

// TestFanOutErrors_NativeExceptionMap verifies the JSON-wire path: an event decoded from
// the wire carries $exception as a map[string]any (not the typed *Exception). exceptionOf
// must read type/message/stack/handled out of the map.
func TestFanOutErrors_NativeExceptionMap(t *testing.T) {
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

// TestFanOutErrors_TypedErrorNoException verifies a bare type:'error' event with NO
// exception is still routed (it groups on message/transaction downstream).
func TestFanOutErrors_TypedErrorNoException(t *testing.T) {
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

// TestFanOutErrors_NoErrorsNoDispatch verifies a batch with zero error events never
// dispatches to the error sink (the common case — a normal pageview/event batch).
func TestFanOutErrors_NoErrorsNoDispatch(t *testing.T) {
	fired := make(chan struct{}, 1)
	SetErrorSink(func(org string, errs []ErrorEvent) { fired <- struct{}{} })
	defer SetErrorSink(nil)

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

// TestFanOutErrors_NilSinkIsNoOp verifies fan-out is inert (no panic/goroutine) when no
// error sink is installed — the default when the o11y embed is off.
func TestFanOutErrors_NilSinkIsNoOp(t *testing.T) {
	SetErrorSink(nil)
	fanOut("acme", []CaptureEvent{{Type: "error", Event: "boom"}})
	// nothing to assert beyond "did not panic / block"
}

// TestIsErrorEvent covers the detector across every wire shape.
func TestIsErrorEvent(t *testing.T) {
	cases := []struct {
		name string
		e    CaptureEvent
		want bool
	}{
		{"canonical type error", CaptureEvent{Type: "error"}, true},
		{"uppercase type", CaptureEvent{Type: "ERROR"}, true},
		{"pre-fold error object", CaptureEvent{Error: &Exception{Message: "m"}}, true},
		{"native $exception prop", CaptureEvent{Properties: map[string]any{"$exception": map[string]any{"message": "m"}}}, true},
		{"pageview", CaptureEvent{Type: "pageview"}, false},
		{"plain event", CaptureEvent{Type: "event", Event: "click"}, false},
		{"empty", CaptureEvent{}, false},
	}
	for _, tc := range cases {
		if got := isErrorEvent(tc.e); got != tc.want {
			t.Errorf("%s: isErrorEvent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestExceptionOf covers extraction from each carrier: typed pre-fold, typed post-fold,
// and decoded map.
func TestExceptionOf(t *testing.T) {
	h := true
	// pre-fold typed
	if typ, msg, stack, handled := exceptionOf(CaptureEvent{Error: &Exception{Type: "E", Message: "m", Stack: "s", Handled: &h}}); typ != "E" || msg != "m" || stack != "s" || handled == nil || !*handled {
		t.Errorf("pre-fold typed: %q %q %q %v", typ, msg, stack, handled)
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
