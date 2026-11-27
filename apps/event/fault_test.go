// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package event

import (
	"encoding/json"
	"testing"
	"time"
)

// A published error fact reads back through DecodeFault with its grouping key,
// its place and its frames, so a consumer files exactly what the plane landed.
func TestDecodeFaultReadsAnErrorFact(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	data, err := json.Marshal(message{
		Signal: string(signalError), Org: "hanzo", ID: "f-1", Time: at,
		Product: "hanzo-ai", Env: "production", Release: "1.2.3", Path: "/pricing",
		Issue: "abc", Class: "TypeError", Message: "x is undefined", Origin: "app.js",
		Severity: severityError, Handled: true,
		Frames: []messageFrame{{Function: "render", File: "app.js", Line: 10, Column: 4, Own: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := DecodeFault(data)
	if !ok {
		t.Fatal("an error fact must decode")
	}
	if f.Org != "hanzo" || f.Issue != "abc" || f.Class != "TypeError" || f.Origin != "app.js" || f.Level != "error" {
		t.Fatalf("fault = %+v", f)
	}
	if !f.Time.Equal(at) || f.Product != "hanzo-ai" || f.Env != "production" || f.Release != "1.2.3" || !f.Handled {
		t.Fatalf("fault = %+v", f)
	}
	if len(f.Frames) != 1 || f.Frames[0] != (FaultFrame{Function: "render", File: "app.js", Line: 10, Column: 4, Own: true}) {
		t.Fatalf("frames = %+v", f.Frames)
	}
}

// The subject is shared with subscriber envelopes, and a fact of another signal or
// one naming no tenant is not the consumer's to file.
func TestDecodeFaultRefusesWhatIsNotAnErrorFact(t *testing.T) {
	for name, body := range map[string]any{
		"an envelope":   map[string]any{"org": "hanzo", "name": "$error"},
		"an act fact":   message{Signal: string(signalAct), Org: "hanzo"},
		"no tenant":     message{Signal: string(signalError)},
		"not even JSON": "{",
	} {
		data, _ := json.Marshal(body)
		if s, ok := body.(string); ok {
			data = []byte(s)
		}
		if _, ok := DecodeFault(data); ok {
			t.Errorf("%s decoded as a fault", name)
		}
	}
}

func TestErrorSubject(t *testing.T) {
	if ErrorSubject != "event.error" {
		t.Fatalf("ErrorSubject = %q", ErrorSubject)
	}
}

// A PEER STATES ITS OWN FINGERPRINT, AND A BROWSER CANNOT. The plane handler sets
// the key a peer gave its failure, so the fact is grouped by it; the same error
// stated without one is grouped by its message, as ever; and a value that is not a
// fingerprint is ignored rather than stored as a key.
//
// MUTATION: drop the `stated` read in applyFault and the first case groups by shape.
func TestAStatedFingerprintGroupsTheFact(t *testing.T) {
	ex := &Exception{Type: "internal", Message: "row 41 is gone"}
	stated := CaptureEvent{Type: "error", Error: ex, Properties: map[string]any{"fault.class": "server"}, stated: statedFingerprint("0123456789abcdef")}
	f, ok := normalize("hanzo", time.Now(), stated)
	if !ok || f.issue != "0123456789abcdef" {
		t.Fatalf("issue = %q (routable %v), want the stated key", f.issue, ok)
	}
	if f.attributes["fault.class"] != "server" {
		t.Fatalf("the fault's attributes did not reach the fact: %v", f.attributes)
	}
	unstated, _ := normalize("hanzo", time.Now(), CaptureEvent{Type: "error", Error: ex})
	if unstated.issue == "" || unstated.issue == "0123456789abcdef" {
		t.Fatalf("an unstated fault groups by its message, got %q", unstated.issue)
	}
	for _, bad := range []string{"", "short", "0123456789ABCDEF", "0123456789abcdeg", "0123456789abcdef0"} {
		if got := statedFingerprint(bad); got != "" {
			t.Errorf("statedFingerprint(%q) = %q, want none", bad, got)
		}
	}
}

// A FAULT A PEER STATES READS BACK, WHOLE, FROM THE STREAM: the fact a peer's failure
// becomes, rendered as the message the bus carries and decoded as the aggregator
// decodes it, keeps its stated key, its class, its code and its place.
func TestAPeersFaultReadsBackFromTheStream(t *testing.T) {
	f, ok := normalize("hanzo", time.Now(), CaptureEvent{
		Type: "error", Event: "server_error", Product: "cloud", Service: "agents", Path: "/v1/agent/sessions",
		Properties: map[string]any{"fault.class": "server", "fault.code": "internal", "fault.op": "GET /v1/agent/sessions", "release": "r1", "fault.request": "req-1"},
		Error:      &Exception{Type: "internal", Message: "row 41 is gone"},
		stated:     statedFingerprint("0123456789abcdef"),
	})
	if !ok {
		t.Fatal("not routable")
	}
	data, err := json.Marshal(wire(f))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := DecodeFault(data)
	if !ok {
		t.Fatal("the stream's message is not a fault")
	}
	if got.Issue != "0123456789abcdef" || got.Org != "hanzo" || got.Product != "cloud" || got.Service != "agents" || got.Path != "/v1/agent/sessions" || got.Message != "row 41 is gone" {
		t.Fatalf("fault = %+v", got)
	}
	for k, want := range map[string]string{"fault.class": "server", "fault.code": "internal", "fault.op": "GET /v1/agent/sessions", "release": "r1", "fault.request": "req-1"} {
		if got.Attributes[k] != want {
			t.Errorf("attribute %s = %q, want %q", k, got.Attributes[k], want)
		}
	}
}
