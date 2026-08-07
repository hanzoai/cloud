package integrations

// A chat turn had no telemetry at all, and the reason is easy to miss: the
// webhook's request span ends when we answer the platform 200, and everything
// worth observing happens AFTER that, in a detached goroutine. So "someone asked
// @hanzo something in this thread" and "a run happened" were two facts with
// nothing in common.
//
// These pin the span that joins them, and the identity it has to carry.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordTurns rebinds the package tracer to an in-memory recorder, which is the
// only honest place to assert from: a span that is created and never exported is
// the failure this file exists to catch.
func recordTurns(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := bridgeTracer
	bridgeTracer = tp.Tracer("test")
	t.Cleanup(func() { bridgeTracer = prev; _ = tp.Shutdown(context.Background()) })
	return sr
}

func turnAttr(sp sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range sp.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

// TestTurnRecordsTheConversationItCameFrom: the span a turn emits names the
// tenant and the exact thread, so a human looking at a Slack conversation has a
// value to search telemetry by.
//
// It asserts on a turn whose run does NOT succeed (there is no agents plane
// here), which is deliberate: a failed turn is precisely when someone goes
// looking, and instrumentation that only records the happy path is absent when it
// is needed.
func TestTurnRecordsTheConversationItCameFrom(t *testing.T) {
	sr := recordTurns(t)
	bridgeReady()

	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{}}
	in := Inbound{
		Provider:   "slack",
		ExternalID: "T0FAKE",
		User:       "U0FAKE",
		Channel:    "C0FAKE",
		ThreadID:   "1699999999.000100",
		Text:       "what broke last night",
	}
	delivered := 0
	runBridgeTurn(s, "acme", in, func(context.Context, string, bool) error {
		delivered++
		return nil
	})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("a turn must emit exactly one span, got %d", len(spans))
	}
	sp := spans[0]
	if sp.Name() != "agent.turn slack" {
		t.Fatalf("span name = %q, want %q", sp.Name(), "agent.turn slack")
	}
	// The tenant, under the key the trace plane files rows by. Without it the
	// conversation is filed as the platform's telemetry, not the tenant's, and the
	// org that owns the workspace cannot read it.
	if got := turnAttr(sp, "hanzo.org"); got != "acme" {
		t.Fatalf("turn span must name its tenant, got %q", got)
	}
	// The thread — the way in. This is the whole point of the span.
	for _, want := range []struct{ key, value string }{
		{"hanzo.chat.provider", "slack"},
		{"hanzo.chat.channel", "C0FAKE"},
		{"hanzo.chat.thread", "1699999999.000100"},
	} {
		if got := turnAttr(sp, want.key); got != want.value {
			t.Fatalf("turn span %s = %q, want %q", want.key, got, want.value)
		}
	}
	// The turn still answered the person, which is what makes the failed-run case
	// worth recording rather than dropping.
	if delivered != 1 {
		t.Fatalf("the turn delivered %d replies, want 1", delivered)
	}
}

// TestUnthreadedTurnSaysNothingRatherThanEmpty: a DM legitimately has no thread,
// and absence is a different fact from an empty one. An attribute stored blank is
// a value every query returns and none can explain.
func TestUnthreadedTurnSaysNothingRatherThanEmpty(t *testing.T) {
	sr := recordTurns(t)
	bridgeReady()

	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{}}
	runBridgeTurn(s, "acme", Inbound{Provider: "slack", ExternalID: "T0", User: "U0", Channel: "D0", Text: "hi"},
		func(context.Context, string, bool) error { return nil })

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	for _, kv := range spans[0].Attributes() {
		if string(kv.Key) == "hanzo.chat.thread" {
			t.Fatalf("an unthreaded turn recorded a thread attribute (%q) — absent is the honest answer", kv.Value.Emit())
		}
	}
	// The channel is still there: a DM has one, and it is how the conversation is
	// found.
	if got := turnAttr(spans[0], "hanzo.chat.channel"); got != "D0" {
		t.Fatalf("channel = %q, want D0", got)
	}
}
