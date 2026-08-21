package event

// publish_test.go — the accepted-batch publish (bus.go). The subject grammar is a
// PUBLISHED contract (orgs subscribe to it), and the envelope's tenant key is the one
// field every consumer on the plane reads, so both are pinned here rather than left to
// whatever the marshaller happens to emit.

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSubjectForFoldsNamesToTokens pins the subject grammar: canonical names land on
// their token, arbitrary names fold safely, and nothing can mint a wildcard or an
// unbounded token.
func TestSubjectForFoldsNamesToTokens(t *testing.T) {
	cases := map[string]string{
		"$pageview":        "event.pageview",
		"$error":           "event.error",
		"$identify":        "event.identify",
		"signup_completed": "event.signup_completed",
		"order_completed":  "event.order_completed",
		"Custom Thing!":    "event.custom_thing",
		"a>b*c":            "event.a_b_c",
		"$":                "event.custom",
		"":                 "event.custom",
		"...":              "event.custom",
	}
	for name, want := range cases {
		if got := subjectFor(name); got != want {
			t.Errorf("subjectFor(%q) = %q, want %q", name, got, want)
		}
	}
	long := subjectFor("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if len(long) > len(plane)+1+maxSubjectToken {
		t.Errorf("subjectFor must bound token length, got %d bytes", len(long))
	}
}

// TestSubjectForStaysOnThePlane proves no caller-chosen name can escape the subject
// space the stream binds — a token that produced a leading dot, a wildcard, or a second
// root would either miss the stream or subscribe every org to it.
func TestSubjectForStaysOnThePlane(t *testing.T) {
	for _, name := range []string{"$pageview", ">", "*", ".", "a.b.c", "  ", "$$$>", "ÜBER"} {
		got := subjectFor(name)
		if want := plane + "."; len(got) <= len(want) || got[:len(want)] != want {
			t.Errorf("subjectFor(%q) = %q — every subject must be rooted at %q", name, got, want)
		}
		tok := got[len(plane)+1:]
		for _, r := range tok {
			ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
			if !ok {
				t.Errorf("subjectFor(%q) = %q — token carries %q, which is not a literal subject token", name, got, r)
			}
		}
	}
}

// TestEventEnvelopeNamesTheTenantOnce is the invariant the delivery engine depends on:
// the envelope's tenant field is spelled EventOrgKey, and so is the warehouse message's.
// A publisher that spelled it otherwise would resolve to no org and deliver to nobody —
// silently — so the two are compared here against the one constant.
func TestEventEnvelopeNamesTheTenantOnce(t *testing.T) {
	for _, tc := range []struct {
		what string
		val  any
	}{
		{"EventEnvelope", EventEnvelope{Org: "acme"}},
		{"message", message{Signal: "event", Org: "acme"}},
	} {
		raw, err := json.Marshal(tc.val)
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		got, ok := m[EventOrgKey]
		if !ok {
			t.Fatalf("%s does not carry the tenant under %q: %s", tc.what, EventOrgKey, raw)
		}
		var org string
		if err := json.Unmarshal(got, &org); err != nil || org != "acme" {
			t.Fatalf("%s: %s = %s, want \"acme\"", tc.what, EventOrgKey, got)
		}
	}
}

// TestEventStreamIdentity pins the plane's exported identity — the values a consumer in
// another package binds to.
func TestEventStreamIdentity(t *testing.T) {
	if EventStream != "EVENT" {
		t.Errorf("EventStream = %q, want EVENT", EventStream)
	}
	if len(EventSubjects) != 1 || EventSubjects[0] != "event.>" {
		t.Errorf("EventSubjects = %v, want [event.>]", EventSubjects)
	}
	if EventOrgKey != "org" {
		t.Errorf("EventOrgKey = %q, want org", EventOrgKey)
	}
	// Every fact-plane signal must fall inside the ONE wildcard the stream binds.
	for _, s := range []signal{signalAct, signalError, signalLog, signalSpan, signalSample} {
		if got := s.subject(); got[:len(plane)+1] != plane+"." {
			t.Errorf("signal %q publishes to %q, outside %v", s, got, EventSubjects)
		}
	}
}

// TestPublishEventsNoBusIsNoOp: publishing with no reachable bus returns promptly and
// never panics — the batch is already durable in the warehouse, so the plane is
// best-effort here by design.
func TestPublishEventsNoBusIsNoOp(t *testing.T) {
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1") // connection refused, fast
	conn.close()
	defer conn.close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		PublishEvents("acme", []SinkEvent{{MessageID: "m", Name: "$pageview", Time: time.Now()}})
	}()
	select {
	case <-done:
	case <-time.After(publishTimeout + 5*time.Second):
		t.Fatal("PublishEvents blocked with no reachable bus")
	}
}

// TestPublishEventsRefusesAnOrglessBatch: no org means no tenant on the envelope, which
// downstream would mean "delivered to nobody". It never reaches the wire.
func TestPublishEventsRefusesAnOrglessBatch(t *testing.T) {
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	conn.close()
	defer conn.close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		PublishEvents("", []SinkEvent{{MessageID: "m", Name: "$pageview"}})
		PublishEvents("acme", nil)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an org-less or empty batch must return without touching the bus")
	}
}
