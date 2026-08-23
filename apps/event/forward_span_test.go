// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package event

import (
	"strings"
	"testing"
	"time"
)

// collectSpans installs a span sink that forwards the batch for one org onto a channel,
// and returns the channel + the sink's remover. Mirrors collectErrors in the
// neighbouring file.
func collectSpans(t *testing.T, wantOrg string) (<-chan []SpanEvent, func()) {
	t.Helper()
	got := make(chan []SpanEvent, 1)
	remove := AddSpanSink(func(org string, spans []SpanEvent) {
		if org == wantOrg {
			got <- spans
		}
	})
	return got, remove
}

// TestFanOutSpansIsThePlanesRoute pins the filter to routeOf — the projection carries
// exactly the facts the plane files under the span signal, so the LLM views and
// event.fact can never disagree about what a span is. In particular an event carrying a
// span BODY that the plane routes as an act does NOT reach the sink: the wire's `type`
// picks the route, and a body cannot promote itself past it.
func TestFanOutSpansIsThePlanesRoute(t *testing.T) {
	got, done := collectSpans(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{
		{Type: "span", Span: &SpanBody{ID: "s-1", Trace: "t-1"}},  // routed span
		{Type: "SPAN", Span: &SpanBody{ID: "s-2", Trace: "t-1"}},  // routed span: type folds lower
		{Type: "event", Event: "click", Span: &SpanBody{ID: "x"}}, // routed act: the plane's word wins
		{Type: "error", Event: "boom"},                            // routed error
		{Type: "pageview"},                                        // routed act
	})

	select {
	case spans := <-got:
		if len(spans) != 2 {
			t.Fatalf("want the 2 span-routed events, got %d: %+v", len(spans), spans)
		}
		if spans[0].SpanID != "s-1" || spans[1].SpanID != "s-2" {
			t.Errorf("wrong events carried: %+v", spans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("span sink was not called")
	}
}

// TestFanOutSpansEnvelopeFields verifies the first-class envelope fields win over both
// the span body and the legacy $ property spellings — the same order the fact row
// resolves its identity in.
func TestFanOutSpansEnvelopeFields(t *testing.T) {
	got, done := collectSpans(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{{
		Type: "span", MessageID: "m-1",
		TraceID: "t-envelope", SpanID: "s-envelope",
		Release: "v3", Environment: "prod", Service: "gateway", Site: "acme.ai",
		Product: "chat", DistinctID: "u1", SessionID: "sess-1",
		Span:       &SpanBody{ID: "s-body", Trace: "t-body", Parent: "p-1"},
		Properties: map[string]any{"$trace_id": "t-stale", "$span_id": "s-stale", "$release": "stale", "$environment": "stale"},
	}})

	select {
	case spans := <-got:
		s := spans[0]
		if s.TraceID != "t-envelope" || s.SpanID != "s-envelope" {
			t.Errorf("envelope ids must win over body and properties: %+v", s)
		}
		if s.Release != "v3" || s.Environment != "prod" {
			t.Errorf("envelope release/environment must win: %+v", s)
		}
		if s.Service != "gateway" || s.Site != "acme.ai" || s.Product != "chat" {
			t.Errorf("origin not carried: %+v", s)
		}
		if s.Parent != "p-1" {
			t.Errorf("parent comes from the body: %q", s.Parent)
		}
		if s.MessageID != "m-1" || s.DistinctID != "u1" || s.SessionID != "sess-1" {
			t.Errorf("identity not carried: %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("span sink was not called")
	}
}

// TestFanOutSpansBodyRefinesTheRoute verifies the body fills what the envelope omits and
// refines the kind — applySpan's own precedence, restated on the projection: the route's
// default kind (internal) stands unless the envelope names one, and the body wins over
// both. The $ spellings are the last resort for a client with no first-class field.
func TestFanOutSpansBodyRefinesTheRoute(t *testing.T) {
	got, done := collectSpans(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{
		{Type: "span", Kind: "SERVER", Span: &SpanBody{ID: "a", Trace: "t", Kind: "Client", Status: "ERROR", Duration: 42}},
		{Type: "span", Kind: "server", Span: &SpanBody{ID: "b", Trace: "t"}},
		{Type: "span", Span: &SpanBody{ID: "c", Trace: "t"}},
		{Type: "span", Properties: map[string]any{"$trace_id": "t-legacy", "$span_id": "s-legacy"}},
	})

	select {
	case spans := <-got:
		if len(spans) != 4 {
			t.Fatalf("want 4 spans, got %d", len(spans))
		}
		if spans[0].Kind != "client" || spans[0].Status != "error" || spans[0].Duration != 42 {
			t.Errorf("body must refine kind/status/duration, lowercased: %+v", spans[0])
		}
		if spans[1].Kind != "server" {
			t.Errorf("envelope kind stands when the body names none: %q", spans[1].Kind)
		}
		if spans[2].Kind != kindInternal {
			t.Errorf("the route's default kind stands when nobody names one: %q", spans[2].Kind)
		}
		if spans[3].TraceID != "t-legacy" || spans[3].SpanID != "s-legacy" {
			t.Errorf("legacy $ spellings are the last resort: %+v", spans[3])
		}
		// Every span is named, so the projection can never write an unnamed row.
		for i, s := range spans {
			if s.Name != nameSpan {
				t.Errorf("span %d name = %q, want the route's default %q", i, s.Name, nameSpan)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("span sink was not called")
	}
}

// TestFanOutSpansStoresTheScrubbedProperties pins the rule that separates this slice from
// the destinations slice: a span sink WRITES A ROW, so it receives the same scrubMap copy
// the write core stores — the credential key is dropped and the token-shaped value is
// redacted before it can reach a stored attribute. The gen_ai marker survives untouched.
func TestFanOutSpansStoresTheScrubbedProperties(t *testing.T) {
	got, done := collectSpans(t, "acme")
	defer done()

	fanOut("acme", []CaptureEvent{{
		Type: "span", Span: &SpanBody{ID: "s", Trace: "t"},
		Properties: map[string]any{
			"gen_ai.system":             "openai",
			"gen_ai.request.model":      "zen-1",
			"gen_ai.usage.input_tokens": 128,
			"authorization":             "Bearer abcdefghijklmnop",
			"gen_ai.prompt":             "mail me at user@acme.ai",
		},
	}})

	select {
	case spans := <-got:
		p := spans[0].Properties
		if p["gen_ai.system"] != "openai" || p["gen_ai.request.model"] != "zen-1" {
			t.Errorf("the gen_ai marker and model must survive the scrub: %+v", p)
		}
		if _, ok := p["authorization"]; ok {
			t.Errorf("credential-shaped key must be dropped before storage: %+v", p)
		}
		if v, _ := p["gen_ai.prompt"].(string); strings.Contains(v, "user@acme.ai") {
			t.Errorf("email must be redacted before storage: %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("span sink was not called")
	}
}

// TestFanOutSpansNoSpansNoDispatch verifies a batch with zero span events never
// dispatches to the span sink (the common case — a normal pageview/event batch).
func TestFanOutSpansNoSpansNoDispatch(t *testing.T) {
	fired := make(chan struct{}, 1)
	remove := AddSpanSink(func(org string, spans []SpanEvent) { fired <- struct{}{} })
	defer remove()

	fanOut("acme", []CaptureEvent{
		{Type: "pageview"},
		{Type: "event", Event: "order_completed", Revenue: 10},
		{Type: "error", Event: "boom"},
	})

	select {
	case <-fired:
		t.Fatal("span sink fired for a batch with no span events")
	case <-time.After(200 * time.Millisecond):
		// expected: no dispatch
	}
}

// TestFanOutSpansNoSinksIsNoOp verifies fan-out is inert when no span sink is installed —
// the default when the o11y plane sink is off. A removed sink counts as absent, which is
// what makes ShutdownO11y's detach real.
func TestFanOutSpansNoSinksIsNoOp(t *testing.T) {
	remove := AddSpanSink(func(string, []SpanEvent) { t.Error("removed sink must not fire") })
	remove()
	fanOut("acme", []CaptureEvent{{Type: "span", Span: &SpanBody{ID: "s", Trace: "t"}}})
	time.Sleep(50 * time.Millisecond)
}

// TestSpanBodyOf covers both carriers: a span that stated a body, and a span-typed event
// that stated none (the zero body, which reads as "nothing further known").
func TestSpanBodyOf(t *testing.T) {
	if b := spanBodyOf(CaptureEvent{Span: &SpanBody{ID: "s", Duration: 7}}); b.ID != "s" || b.Duration != 7 {
		t.Errorf("stated body not returned: %+v", b)
	}
	if b := spanBodyOf(CaptureEvent{Type: "span"}); b != (SpanBody{}) {
		t.Errorf("absent body must read as the zero body: %+v", b)
	}
}
