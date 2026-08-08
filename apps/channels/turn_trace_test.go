package channels

// A chat turn had no telemetry at all, and the reason is easy to miss: the
// adapter's request span ends when it answers the platform 200, and everything
// worth observing happens AFTER that, in a detached goroutine in THIS process.
// So "someone asked @hanzo something in this thread" and "a run happened" were
// two facts with nothing in common.
//
// These pin the span that joins them, and the identity it has to carry.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
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
	prev := turnTracer
	turnTracer = tp.Tracer("test")
	t.Cleanup(func() { turnTracer = prev; _ = tp.Shutdown(context.Background()) })
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

// spyTransport counts replies without reaching a platform.
func spyTransport(n *int) transport {
	return transport{
		id:        "slack",
		normalize: func(plane.ChannelsIngestIn) (Message, bool) { return Message{}, false },
		send: func(context.Context, *cloud.Service[state], string, Message) (Delivery, error) {
			*n++
			return Delivery{}, nil
		},
	}
}

// TestTurnRecordsTheConversationItCameFrom: the span a turn emits names the
// tenant and the exact thread, so a human looking at a Slack conversation has a
// value to search telemetry by.
//
// It asserts on a turn that CANNOT run (there is no integrations or agents plane
// here), which is deliberate: a failed turn is precisely when someone goes
// looking, and instrumentation that only records the happy path is absent when it
// is needed.
func TestTurnRecordsTheConversationItCameFrom(t *testing.T) {
	sr := recordTurns(t)

	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{}}
	sent := 0
	turn(s, spyTransport(&sent), "acme", Message{
		Channel: "slack",
		Account: "T0FAKE",
		Room:    Room{ID: "C0FAKE", Kind: RoomThread},
		ReplyTo: "1699999999.000100",
		Sender:  Sender{ExternalID: "U0FAKE", Org: "acme"},
		Text:    "what broke last night",
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
	// And it said NOTHING, which is the other half of the contract. The identity
	// door is unreachable here, and that is our failure rather than the asker's —
	// posting an apology into the room on every message while a socket is down is
	// worse than silence. The span is what remains, which is exactly the case
	// someone goes looking for.
	if sent != 0 {
		t.Fatalf("an unreachable identity door must not answer the room, sent %d", sent)
	}
}
