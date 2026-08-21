// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// spansink_test.go — the pure projection spansink.go promises is unit-tested, on the
// terms errorsink_test.go set for the Sentry lens: no datastore, no socket, no runtime.
// spanRow and genAIAttributes are total on their input, so what a projected span WOULD
// carry is provable here, and the one contract that cannot be proven from this side —
// that the reader binds the tenant SLUG — is pinned against the errorsink UUID formula
// so the two lenses can never quietly converge on one wrong answer.

package o11y

import (
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/o11y/pkg/types/llmobstypes"
)

// llmSpan is a fully-stated LLM span as the analytics fan-out hands it over.
func llmSpan() event.SpanEvent {
	return event.SpanEvent{
		MessageID:   "m-1",
		Time:        time.Unix(1700000000, 0).UTC(),
		Name:        "chat zen-1",
		Kind:        "client",
		Status:      "error",
		Duration:    1500,
		TraceID:     "t-1",
		SpanID:      "s-1",
		Parent:      "p-1",
		Service:     "gateway",
		Product:     "chat",
		Site:        "acme.ai",
		Release:     "v3",
		Environment: "prod",
		DistinctID:  "u-1",
		SessionID:   "sess-1",
		Properties: map[string]any{
			llmobstypes.GenAISystem:      "openai",
			"gen_ai.request.model":       "zen-1",
			"gen_ai.usage.input_tokens":  float64(128),
			"gen_ai.usage.output_tokens": float64(256),
		},
	}
}

// TestSpanRowMapsOntoPlaneColumns proves the ONE translate: an event.SpanEvent maps
// onto an event.span row in planeSpanColumns order, with every value in the column the
// LLM views read it from. A positional Insert misaligns silently, so the width is
// asserted with the values.
func TestSpanRowMapsOntoPlaneColumns(t *testing.T) {
	row, ok := spanRow("acme", llmSpan())
	if !ok {
		t.Fatal("a gen_ai span must project")
	}
	if len(row) != len(planeSpanColumns) {
		t.Fatalf("row is %d wide, want %d (%v)", len(row), len(planeSpanColumns), planeSpanColumns)
	}
	if got := spanCol(t, row, "org"); got != "acme" {
		t.Errorf("org = %v", got)
	}
	if got := spanCol(t, row, "time"); got != time.Unix(1700000000, 0).UTC() {
		t.Errorf("time = %v", got)
	}
	// id IS the span id — event.span's ReplacingMergeTree identity is (org, trace_id,
	// time, id), so a re-sent span collapses onto its own row rather than duplicating.
	if got := spanCol(t, row, "id"); got != "s-1" {
		t.Errorf("id = %v, want the span id", got)
	}
	if got := spanCol(t, row, "span_id"); got != "s-1" {
		t.Errorf("span_id = %v", got)
	}
	if got := spanCol(t, row, "trace_id"); got != "t-1" {
		t.Errorf("trace_id = %v", got)
	}
	if got := spanCol(t, row, "parent"); got != "p-1" {
		t.Errorf("parent = %v", got)
	}
	if got := spanCol(t, row, "name"); got != "chat zen-1" {
		t.Errorf("name = %v", got)
	}
	if got := spanCol(t, row, "kind"); got != "client" {
		t.Errorf("kind = %v", got)
	}
	if got := spanCol(t, row, "status"); got != "error" {
		t.Errorf("status = %v", got)
	}
	if got := spanCol(t, row, "duration"); got != uint64(1500) {
		t.Errorf("duration = %v", got)
	}
	// The emitting workload, then the surface — the precedence the error lens resolves
	// service_name with, and the column the views read as service.name.
	if got := spanCol(t, row, "service"); got != "gateway" {
		t.Errorf("service = %v, want the wire's own service", got)
	}
	attrs, _ := spanCol(t, row, "attributes").(map[string]string)
	if attrs["gen_ai.request.model"] != "zen-1" {
		t.Errorf("the gen_ai attributes must travel verbatim: %+v", attrs)
	}
	// A JSON whole number reads back as an integer, never "128.0", because the views
	// parse these two keys as floats out of a string map.
	if attrs["gen_ai.usage.input_tokens"] != "128" || attrs["gen_ai.usage.output_tokens"] != "256" {
		t.Errorf("token counts must render as whole numbers: %+v", attrs)
	}
	if attrs["service.version"] != "v3" || attrs["deployment.environment"] != "prod" || attrs["site"] != "acme.ai" {
		t.Errorf("the qualifiers with no column must ride in attributes: %+v", attrs)
	}
	if attrs[llmobstypes.SessionID] != "sess-1" || attrs[llmobstypes.UserID] != "u-1" {
		t.Errorf("the sessions/users views group on these two keys: %+v", attrs)
	}
}

// TestSpanRowSkipsNonLLMSpan pins the admission rule to the reader's own marker: a span
// with no gen_ai.system is invisible to every LLM view by construction, so projecting it
// would write a row no read can ever return.
func TestSpanRowSkipsNonLLMSpan(t *testing.T) {
	plain := llmSpan()
	plain.Properties = map[string]any{"http.request.method": "GET"}
	if row, ok := spanRow("acme", plain); ok {
		t.Errorf("a span without the gen_ai marker must not project: %+v", row)
	}
	// A marker present but empty is the same "no provider named" and must not admit the
	// span — the empty value is indistinguishable from an absent key on the read side.
	blank := llmSpan()
	blank.Properties = map[string]any{llmobstypes.GenAISystem: ""}
	if _, ok := spanRow("acme", blank); ok {
		t.Error("an empty gen_ai.system must not project")
	}
	// No properties at all — the common non-LLM span.
	bare := llmSpan()
	bare.Properties = nil
	if _, ok := spanRow("acme", bare); ok {
		t.Error("a span with no attributes must not project")
	}
}

// TestSpanRowIdentityFallbacks covers the two derivations that keep distinct spans
// distinct: a span that named no id takes the message id (else every unnamed span in an
// org shares one identity and the ReplacingMergeTree collapses them), and a span that
// named no trace is its own single-span trace (else every orphan folds into one bogus
// trace the traces view then reports).
func TestSpanRowIdentityFallbacks(t *testing.T) {
	s := llmSpan()
	s.SpanID, s.TraceID = "", ""
	row, ok := spanRow("acme", s)
	if !ok {
		t.Fatal("must still project")
	}
	if got := spanCol(t, row, "id"); got != "m-1" {
		t.Errorf("id = %v, want the message id", got)
	}
	if got := spanCol(t, row, "span_id"); got != "m-1" {
		t.Errorf("span_id = %v, want the message id", got)
	}
	if got := spanCol(t, row, "trace_id"); got != "m-1" {
		t.Errorf("trace_id = %v, want its own span id", got)
	}

	// Two id-less spans in one org must stay two rows.
	other := llmSpan()
	other.SpanID, other.TraceID, other.MessageID = "", "", "m-2"
	row2, _ := spanRow("acme", other)
	if spanCol(t, row, "id") == spanCol(t, row2, "id") {
		t.Error("distinct spans must not share an identity")
	}
}

// TestSpanTenantIsTheSlugNotTheUUID pins the tenant boundary of every LLM view. The span
// views have no org column: the reader binds `gen_ai.hanzo.org_id = <validated X-Org-Id
// slug>`, so this lens stamps the analytics tenant slug verbatim. It deliberately does
// NOT derive the o11y org UUID the Sentry lens needs (deriveOrgUUID, pinned by
// TestDeriveOrgUUID) — that value is correct for the Sentry module's Ingest and is a row
// every LLM view returns zero of.
func TestSpanTenantIsTheSlugNotTheUUID(t *testing.T) {
	attrs, ok := genAIAttributes("acme", llmSpan())
	if !ok {
		t.Fatal("a gen_ai span must project")
	}
	if attrs[llmobstypes.GenAIHanzoOrgID] != "acme" {
		t.Fatalf("tenant = %q, want the slug the reader binds", attrs[llmobstypes.GenAIHanzoOrgID])
	}
	if attrs[llmobstypes.GenAIHanzoOrgID] == deriveOrgUUID("acme").String() {
		t.Error("the span views bind the slug; a derived UUID here reads as no rows")
	}

	// The tenant is stamped LAST and unconditionally: a client-supplied value would let
	// one org write rows another org reads, so the server's org always overwrites it.
	spoof := llmSpan()
	spoof.Properties[llmobstypes.GenAIHanzoOrgID] = "victim"
	attrs, _ = genAIAttributes("acme", spoof)
	if attrs[llmobstypes.GenAIHanzoOrgID] != "acme" {
		t.Errorf("a wire-supplied tenant must be overwritten: %q", attrs[llmobstypes.GenAIHanzoOrgID])
	}
}

// TestGenAIAttributesFillOnlyWhatSpanOmits: session.id and user.id are the read side's
// canonical vocabulary, so the SPAN'S OWN statement is authoritative and the envelope's
// identity only fills what the span left unsaid.
func TestGenAIAttributesFillOnlyWhatSpanOmits(t *testing.T) {
	s := llmSpan()
	s.Properties[llmobstypes.SessionID] = "span-said-this"
	s.Properties[llmobstypes.UserID] = "span-said-that"
	attrs, _ := genAIAttributes("acme", s)
	if attrs[llmobstypes.SessionID] != "span-said-this" || attrs[llmobstypes.UserID] != "span-said-that" {
		t.Errorf("the span's own session/user must stand: %+v", attrs)
	}

	// …and the envelope fills them when the span named none.
	bare := llmSpan()
	attrs, _ = genAIAttributes("acme", bare)
	if attrs[llmobstypes.SessionID] != "sess-1" || attrs[llmobstypes.UserID] != "u-1" {
		t.Errorf("the envelope must fill an unsaid session/user: %+v", attrs)
	}
}

// TestSpanRowServiceFallsBackToTheSurface: a browser span names no service, so the
// emitting surface identifies it rather than leaving the column — and an empty service
// is the column every fleet reader filters on.
func TestSpanRowServiceFallsBackToTheSurface(t *testing.T) {
	s := llmSpan()
	s.Service = ""
	row, _ := spanRow("acme", s)
	if got := spanCol(t, row, "service"); got != "chat" {
		t.Errorf("service = %v, want the emitting surface", got)
	}
}

// TestLLMLensEnabled covers the default-ON flag semantics, the same posture as the
// Sentry error lens next door.
func TestLLMLensEnabled(t *testing.T) {
	t.Setenv(llmLensEnv, "")
	if !llmLensEnabled() {
		t.Error("default must be ON")
	}
	for _, off := range []string{"0", "false", "no", "off", "OFF"} {
		t.Setenv(llmLensEnv, off)
		if llmLensEnabled() {
			t.Errorf("%q must disable the lens", off)
		}
	}
	t.Setenv(llmLensEnv, "1")
	if !llmLensEnabled() {
		t.Error("1 must enable the lens")
	}
}

// TestInstallSpanSinkNeedsThePlaneSink: without a plane sink there is nothing to write
// through, so NO sink is installed (spans stay on the event plane — honest degradation);
// with one, the sink installs and clearSpanSink detaches it. The detach's EFFECT — that a
// removed sink never fires again — is proven where the fan-out lives
// (event.TestFanOutSpansNoSinksIsNoOp).
func TestInstallSpanSinkNeedsThePlaneSink(t *testing.T) {
	log := luxlog.New("test")
	prev := embeddedPlaneSink
	t.Cleanup(func() { embeddedPlaneSink = prev; clearSpanSink() })

	embeddedPlaneSink = nil
	clearSpanSink()
	installSpanSink(log)
	if removeSpanSink != nil {
		t.Fatal("no plane sink must install no span sink")
	}

	embeddedPlaneSink = &planeSink{}
	installSpanSink(log)
	if removeSpanSink == nil {
		t.Fatal("a live plane sink must install the span sink")
	}
	clearSpanSink()
	if removeSpanSink != nil {
		t.Fatal("clearSpanSink must detach")
	}
}

// TestInstallSpanSinkRespectsTheFlag: the lens off means no sink at all, not a sink that
// drops batches.
func TestInstallSpanSinkRespectsTheFlag(t *testing.T) {
	prev := embeddedPlaneSink
	t.Cleanup(func() { embeddedPlaneSink = prev; clearSpanSink() })

	t.Setenv(llmLensEnv, "off")
	embeddedPlaneSink = &planeSink{}
	clearSpanSink()
	installSpanSink(luxlog.New("test"))
	if removeSpanSink != nil {
		t.Fatal("a disabled lens must install no sink")
	}
}

// TestConsumeSpansWithoutPlaneSinkIsInert: the handler resolves the sink per batch, so a
// batch that lands after shutdown returns rather than writing through a closed
// connection. Fail-soft: it never returns an error to the ingest and never panics.
func TestConsumeSpansWithoutPlaneSinkIsInert(t *testing.T) {
	prev := embeddedPlaneSink
	t.Cleanup(func() { embeddedPlaneSink = prev })
	embeddedPlaneSink = nil
	consumeSpans(luxlog.New("test"), "acme", []event.SpanEvent{llmSpan()})
}

// TestConsumeSpansRefusesAnOrglessBatch: the tenant is the whole boundary of every LLM
// read, so a batch with no org writes nothing rather than landing rows no org owns.
func TestConsumeSpansRefusesAnOrglessBatch(t *testing.T) {
	prev := embeddedPlaneSink
	t.Cleanup(func() { embeddedPlaneSink = prev })
	// A sink whose datastore connection would panic if it were ever reached: the guard
	// must return before any write is attempted.
	embeddedPlaneSink = &planeSink{}
	consumeSpans(luxlog.New("test"), "", []event.SpanEvent{llmSpan()})
}

// TestClearSpanSinkIsSafe: clearing when never installed is a no-op (idempotent), which
// is what lets ShutdownO11y call it unconditionally.
func TestClearSpanSinkIsSafe(t *testing.T) {
	clearSpanSink()
	clearSpanSink()
}

// TestSpanSinkNamesTheOneWritePath guards the file's central claim: rows go through
// insertSpans, which owes event.span AND the event.trace partial the trace list resolves
// against. A projection that inserted event.span directly would be invisible to every
// trace read.
func TestSpanSinkNamesTheOneWritePath(t *testing.T) {
	if planeSpanTable != "event.span" || planeTraceTable != "event.trace" {
		t.Fatalf("the lens writes %s and its summary %s", planeSpanTable, planeTraceTable)
	}
	rows := [][]any{}
	for _, s := range []event.SpanEvent{llmSpan()} {
		row, ok := spanRow("acme", s)
		if !ok {
			t.Fatal("must project")
		}
		rows = append(rows, row)
	}
	partials := traceRowsOf(rows)
	if len(partials) != 1 {
		t.Fatalf("one trace in the batch must yield one partial, got %d", len(partials))
	}
	if partials[0][0] != "acme" || partials[0][1] != "t-1" {
		t.Errorf("the partial must be keyed (org, trace_id): %+v", partials[0])
	}
	if partials[0][4] != uint64(1) {
		t.Errorf("num_spans = %v, want 1", partials[0][4])
	}
}

// TestSpanRowStatusNarrowsToThePlanesVocabulary: event.span states error or ok, so an
// unset/absent status is a span that completed without declaring failure.
func TestSpanRowStatusNarrowsToThePlanesVocabulary(t *testing.T) {
	for status, want := range map[string]string{"error": "error", "ok": "ok", "unset": "ok", "": "ok"} {
		s := llmSpan()
		s.Status = status
		row, _ := spanRow("acme", s)
		if got := spanCol(t, row, "status"); got != want {
			t.Errorf("status %q projected as %v, want %q", status, got, want)
		}
	}
	// The same narrowing for kind: OTel's own default for a span that states none.
	s := llmSpan()
	s.Kind = "nonsense"
	row, _ := spanRow("acme", s)
	if got := spanCol(t, row, "kind"); got != "internal" {
		t.Errorf("kind = %v, want internal", got)
	}
}

// TestGenAIAttributesKeepTheReadersKeys is the drift alarm on the four attribute names
// this lens and the o11y read side must agree on. A bump that renames one on the read
// side silently stops matching what this writes, so the constants are asserted literally
// against the spellings the OTel GenAI semantic conventions publish.
func TestGenAIAttributesKeepTheReadersKeys(t *testing.T) {
	for name, want := range map[string]string{
		llmobstypes.GenAISystem:     "gen_ai.system",
		llmobstypes.GenAIHanzoOrgID: "gen_ai.hanzo.org_id",
		llmobstypes.SessionID:       "session.id",
		llmobstypes.UserID:          "user.id",
	} {
		if name != want {
			t.Errorf("attribute key drifted: %q, want %q", name, want)
		}
	}
	attrs, _ := genAIAttributes("acme", llmSpan())
	for k := range attrs {
		if strings.HasPrefix(k, "gen_ai.") && strings.Contains(k, " ") {
			t.Errorf("attribute key %q must travel verbatim", k)
		}
	}
}
