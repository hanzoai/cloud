package team

import (
	"encoding/json"
	"os"
	"testing"
)

// testSession wires a session onto a fresh per-workspace store + the real embedded
// model hierarchy. Ported from team-go/pkg/transactor (server → transServer).
func testSession() *session {
	dir, _ := os.MkdirTemp("", "team-test")
	return &session{
		server:    &transServer{hub: newHub()},
		store:     newStore(dir),
		hier:      buildHierarchy(modelJSON),
		account:   "2d4d67ab-30f1-474e-b81f-f60461852259",
		workspace: "e48f81fd-12be-4bcd-aecb-3eaa9a9b5b18",
	}
}

// TestHelloNegotiatesJSON is the load-bearing no-msgpack assertion: the server
// answers hello with binary=false, so the client serializes JSON for the rest of
// the session and msgpack is never used.
func TestHelloNegotiatesJSON(t *testing.T) {
	out := testSession().handle([]byte(`{"id":-1,"method":"hello","params":[]}`))
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r["result"] != "hello" {
		t.Fatalf("result = %v", r["result"])
	}
	if r["binary"] != false {
		t.Fatalf("binary = %v (msgpack must be off)", r["binary"])
	}
	if r["useCompression"] != false {
		t.Fatalf("useCompression = %v", r["useCompression"])
	}
	if r["lastHash"] != modelHash {
		t.Fatalf("lastHash = %v", r["lastHash"])
	}
	acc, ok := r["account"].(map[string]any)
	if !ok || acc["uuid"] != "2d4d67ab-30f1-474e-b81f-f60461852259" {
		t.Fatalf("account = %v", r["account"])
	}
}

// TestHelloServesModelVersion is the decouple guard: the front's version handshake
// reads serverVersion, which MUST be the MODEL version (modelVersion). Default
// is the front's 0.6.0.
func TestHelloServesModelVersion(t *testing.T) {
	out := testSession().handle([]byte(`{"id":-1,"method":"hello","params":[]}`))
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r["serverVersion"] != modelVersion() {
		t.Fatalf("serverVersion = %v, want modelVersion() = %v", r["serverVersion"], modelVersion())
	}
	if r["serverVersion"] != defaultModelVersion {
		t.Fatalf("serverVersion = %v, want default %v", r["serverVersion"], defaultModelVersion)
	}
	if r["serverVersion"] != "0.6.0" {
		t.Fatalf("serverVersion = %v, want pinned 0.6.0", r["serverVersion"])
	}
}

// TestLoadModelServesFullModel proves the entire embedded model bundle is returned
// in full so the client can build its hierarchy.
func TestLoadModelServesFullModel(t *testing.T) {
	out := testSession().handle([]byte(`{"id":1,"method":"loadModel","params":[0]}`))
	var r struct {
		ID     int64 `json:"id"`
		Result struct {
			Full         bool              `json:"full"`
			Hash         string            `json:"hash"`
			Transactions []json.RawMessage `json:"transactions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != 1 || !r.Result.Full {
		t.Fatalf("id=%d full=%v", r.ID, r.Result.Full)
	}
	var embedded []json.RawMessage
	if err := json.Unmarshal(modelJSON, &embedded); err != nil {
		t.Fatalf("embedded model.json is not a Tx array: %v", err)
	}
	if len(r.Result.Transactions) != len(embedded) {
		t.Fatalf("transactions = %d, want %d (full embedded model)", len(r.Result.Transactions), len(embedded))
	}
	if r.Result.Hash != modelHash {
		t.Fatalf("hash mismatch")
	}
}

func TestHandleMisc(t *testing.T) {
	s := testSession()
	if string(s.handle([]byte("ping"))) != "pong!" {
		t.Fatal("ping must answer pong!")
	}
	// findAll over an empty workspace → an empty TotalArray (the wire shape the
	// client's rpc reviver turns back into a FindResult).
	var fa struct {
		Result struct {
			DataType string            `json:"dataType"`
			Total    int               `json:"total"`
			Value    []json.RawMessage `json:"value"`
		} `json:"result"`
	}
	json.Unmarshal(s.handle([]byte(`{"id":2,"method":"findAll","params":["core:class:Doc",{}]}`)), &fa)
	if fa.Result.DataType != "TotalArray" || fa.Result.Value == nil || len(fa.Result.Value) != 0 {
		t.Fatalf("findAll result = %+v, want empty TotalArray", fa.Result)
	}
	// domainRequest(communication) → a well-formed DomainResult so .value is never null.
	var dr struct {
		Result struct {
			Domain string `json:"domain"`
		} `json:"result"`
	}
	json.Unmarshal(s.handle([]byte(`{"id":3,"method":"domainRequest","params":["communication",{"findLabels":{}}]}`)), &dr)
	if dr.Result.Domain != "communication" {
		t.Fatalf("domainRequest domain = %q", dr.Result.Domain)
	}
	// tx → object ack, correlated id
	var tx struct {
		ID     int64          `json:"id"`
		Result map[string]any `json:"result"`
	}
	json.Unmarshal(s.handle([]byte(`{"id":9,"method":"tx","params":[{}]}`)), &tx)
	if tx.ID != 9 || tx.Result == nil {
		t.Fatalf("tx reply = %+v", tx)
	}
}

// TestEnvelopeWrapsRPC is the end-to-end transport check: a request wrapped in a
// ZAP envelope decodes, dispatches, and the reply re-wraps with the same id.
func TestEnvelopeWrapsRPC(t *testing.T) {
	req := Encode(Envelope{ID: 42, Kind: KindRequest, Payload: []byte(`{"id":42,"method":"hello","params":[]}`)})
	env, err := Decode(req)
	if err != nil {
		t.Fatal(err)
	}
	reply := testSession().handle(env.Payload)
	out := Encode(Envelope{ID: env.ID, Kind: KindResponse, Payload: reply})
	back, err := Decode(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != 42 || back.Kind != KindResponse {
		t.Fatalf("reply envelope id=%d kind=%d", back.ID, back.Kind)
	}
	var r map[string]any
	if err := json.Unmarshal(back.Payload, &r); err != nil || r["result"] != "hello" {
		t.Fatalf("reply payload = %s", back.Payload)
	}
}

// TestOriginAllowed is the WS-upgrade Origin allow-list table: absent Origin
// (non-browser) and the team/hanzo surfaces are admitted; everything else —
// including lookalike registered domains — is refused before the upgrade.
func TestOriginAllowed(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"", "hanzo.team", true},                          // non-browser client
		{"https://hanzo.team", "hanzo.team", true},        // same host
		{"https://hanzo.team", "api.hanzo.ai", true},      // team surface
		{"https://team.hanzo.ai", "hanzo.team", true},     // team surface
		{"https://api.hanzo.team", "hanzo.team", true},    // team surface
		{"https://console.hanzo.ai", "hanzo.team", true},  // *.hanzo.ai
		{"https://hanzo.ai", "hanzo.team", true},          // apex
		{"http://localhost:8087", "localhost:8000", true}, // local dev
		{"https://evil.example", "hanzo.team", false},     // foreign origin
		{"https://evilhanzo.ai", "hanzo.team", false},     // suffix lookalike
		{"https://hanzo.ai.evil.example", "hanzo.team", false},
		{"null", "hanzo.team", false},   // opaque origin
		{"://bad", "hanzo.team", false}, // unparseable
	}
	for _, c := range cases {
		if got := originAllowed(c.origin, c.host); got != c.want {
			t.Errorf("originAllowed(%q, %q) = %v, want %v", c.origin, c.host, got, c.want)
		}
	}
}
