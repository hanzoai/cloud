// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

// replay_test.go — the session-replay door's proofs.
//
// The load-bearing one is TestSnapshotRecordIsTheWireContract. Every other consumer
// on this surface is in this repo and in this language, so a drift in what cloud
// writes goes red at compile time somewhere. The replay ingester is NEITHER: it is a
// separate process that reads these bytes, and nothing in this build links against
// it. A renamed header, a `data` that stopped being double-encoded, or a partition
// key that stopped being the session id would all compile, deploy, answer 200, and
// silently produce recordings nobody can play. That test IS the contract.

package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// ── the harness ─────────────────────────────────────────────────────────────

// fakeProducer substitutes the door's ONE seam onto the broker and records what it
// was handed, so a test reads back the record a request actually produced. Restored
// via t.Cleanup, the same discipline fakeWarehouse keeps.
//
// err is what the substituted produce returns, so the failure path is driven from
// the same helper as the happy one — a produce that fails must never become a 200,
// and that is only assertable by making one fail.
func fakeProducer(t *testing.T, err error) *[]*kgo.Record {
	t.Helper()
	var got []*kgo.Record
	orig := produce
	produce = func(_ context.Context, rec *kgo.Record) error {
		got = append(got, rec)
		return err
	}
	t.Cleanup(func() { produce = orig })
	return &got
}

// mapped configures the org→token shim for this test.
func mapped(t *testing.T, pairs string) {
	t.Helper()
	t.Setenv(replayTokensEnv, pairs)
}

// replayWire is a minimal well-formed snapshot batch for org "acme".
const replayWire = `{"sessionId":"sess-01","windowId":"win-01","distinctId":"person-7",` +
	`"events":[{"type":4,"timestamp":1785951969399,"data":{"href":"https://hanzo.ai/"}}]}`

// header reads one header off a produced record. Absent is "" and the tests
// distinguish that from an empty value by asserting presence separately.
func header(rec *kgo.Record, key string) (string, bool) {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// ── 1. the wire contract ────────────────────────────────────────────────────

// TestSnapshotRecordIsTheWireContract pins EVERY byte of the produced message that
// the out-of-repo consumer reads: the topic, the partition key, all seven headers,
// the envelope, and the fact that `data` is a JSON STRING which parses a second time
// into the inner document with the rrweb events verbatim.
//
// It drives the PURE builder, so the id and the clock are inputs rather than
// ambient — the assertion is exact rather than "looks about right".
func TestSnapshotRecordIsTheWireContract(t *testing.T) {
	// A quote-dense rrweb event, chosen so a `data` that was NOT double-encoded
	// would decode differently rather than coincidentally the same.
	item := `{"type":2,"timestamp":1785951969399,"data":{"node":{"tagName":"div","attributes":{"class":"a \"b\""}}}}`
	in := replayBody{
		SessionID:  "sess-01",
		WindowID:   "win-01",
		DistinctID: "person-7",
		Events:     []json.RawMessage{json.RawMessage(item)},
	}
	now := time.Date(2026, 8, 5, 17, 46, 9, 399_000_000, time.UTC)

	rec, err := snapshotRecord(in, "hi_token", "203.0.113.9", "0d1a5f4e-7c62-4a5b-9c31-2f0e6b8d4a11", now)
	if err != nil {
		t.Fatalf("snapshotRecord: %v", err)
	}

	if rec.Topic != "session_recording_snapshot_item_events" {
		t.Errorf("topic = %q, want session_recording_snapshot_item_events", rec.Topic)
	}
	// The partition key is the session id, RAW — it is what keeps every batch of one
	// recording on one partition and in order.
	if string(rec.Key) != "sess-01" {
		t.Errorf("partition key = %q, want the raw session id %q", rec.Key, "sess-01")
	}

	// Every header, by name and by value. The `token` one is load-bearing: the
	// consumer reads the team from the HEADER, so a message missing it is dropped
	// with no error at either end.
	for _, want := range []struct{ key, value string }{
		{"token", "hi_token"},
		{"distinct_id", "person-7"},
		{"session_id", "sess-01"},
		{"timestamp", strconv.FormatInt(now.UnixMilli(), 10)},
		{"event", "$snapshot_items"},
		{"uuid", "0d1a5f4e-7c62-4a5b-9c31-2f0e6b8d4a11"},
		{"now", "2026-08-05T17:46:09.399Z"},
	} {
		got, present := header(rec, want.key)
		if !present {
			t.Errorf("header %q is ABSENT — the consumer reads the tenant and the identity off these", want.key)
			continue
		}
		if got != want.value {
			t.Errorf("header %q = %q, want %q", want.key, got, want.value)
		}
	}
	if len(rec.Headers) != 7 {
		t.Errorf("produced %d headers, want exactly 7", len(rec.Headers))
	}

	// The OUTER envelope.
	var env snapshotEnvelope
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatalf("envelope json: %v (%s)", err, rec.Value)
	}
	for _, f := range []struct{ name, got, want string }{
		{"uuid", env.UUID, "0d1a5f4e-7c62-4a5b-9c31-2f0e6b8d4a11"},
		{"distinct_id", env.DistinctID, "person-7"},
		{"ip", env.IP, "203.0.113.9"},
		{"now", env.Now, "2026-08-05T17:46:09.399Z"},
		{"token", env.Token, "hi_token"},
		{"event", env.Event, "$snapshot_items"},
		{"timestamp", env.Timestamp, "2026-08-05T17:46:09.399Z"},
	} {
		if f.got != f.want {
			t.Errorf("envelope %s = %q, want %q", f.name, f.got, f.want)
		}
	}

	// `data` IS DOUBLE-ENCODED. Proving it takes two steps that cannot both pass
	// unless it really is a string carrying JSON: the raw field must be a JSON
	// string (not an object), and its contents must parse on their own.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Value, &raw); err != nil {
		t.Fatalf("envelope raw json: %v", err)
	}
	if b := raw["data"]; len(b) == 0 || b[0] != '"' {
		t.Fatalf("envelope data is not a JSON STRING (starts %q) — the consumer parses it a second "+
			"time, so an inlined object is a message it cannot read", string(b[:min(len(b), 12)]))
	}

	var doc snapshotData
	if err := json.Unmarshal([]byte(env.Data), &doc); err != nil {
		t.Fatalf("inner document json: %v (%s)", err, env.Data)
	}
	if doc.Event != "$snapshot_items" {
		t.Errorf("inner event = %q, want $snapshot_items", doc.Event)
	}
	p := doc.Properties
	for _, f := range []struct{ name, got, want string }{
		{"distinct_id", p.DistinctID, "person-7"},
		{"$session_id", p.SessionID, "sess-01"},
		{"$window_id", p.WindowID, "win-01"},
		{"$snapshot_source", p.SnapshotSource, "web"},
		{"$lib", p.Lib, "@hanzo/replay"},
	} {
		if f.got != f.want {
			t.Errorf("inner properties %s = %q, want %q", f.name, f.got, f.want)
		}
	}
	// The rrweb events survive VERBATIM — the ingester derives the whole summary
	// (click/keypress/mouse-activity counts, size) from exactly these bytes.
	if len(p.SnapshotItems) != 1 {
		t.Fatalf("$snapshot_items has %d entries, want 1", len(p.SnapshotItems))
	}
	if got := string(p.SnapshotItems[0]); got != item {
		t.Errorf("$snapshot_items[0] = %s\nwant (byte-identical) %s", got, item)
	}
}

// TestSnapshotItemsIsAFlatArray guards the shape the consumer walks: the events go
// in as a flat array of rrweb objects, never wrapped in a per-batch envelope of
// their own.
func TestSnapshotItemsIsAFlatArray(t *testing.T) {
	in := replayBody{SessionID: "s", Events: []json.RawMessage{
		json.RawMessage(`{"type":3,"timestamp":1,"data":{}}`),
		json.RawMessage(`{"type":4,"timestamp":2,"data":{}}`),
	}}
	rec, err := snapshotRecord(in, "hi_t", "", "id", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("snapshotRecord: %v", err)
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	var probe struct {
		Properties struct {
			Items []struct {
				Type int `json:"type"`
			} `json:"$snapshot_items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(env.Data), &probe); err != nil {
		t.Fatalf("inner: %v", err)
	}
	if len(probe.Properties.Items) != 2 ||
		probe.Properties.Items[0].Type != 3 || probe.Properties.Items[1].Type != 4 {
		t.Errorf("$snapshot_items = %v, want a FLAT array of the two rrweb events in order",
			probe.Properties.Items)
	}
}

// ── 2. sessionId validation ─────────────────────────────────────────────────

// TestValidSessionID is the grammar, exactly as the pipeline applies it. The id is
// the PARTITION KEY, so one this accepts and the far end refuses is a message
// produced into a partition and then dropped — reported to the caller as success.
func TestValidSessionID(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		want bool
	}{
		{"simple", "abc123", true},
		{"with dashes", "sess-01-abc", true},
		{"uuid shaped", "0d1a5f4e-7c62-4a5b-9c31-2f0e6b8d4a11", true},
		{"upper case", "ABC-123", true},
		{"single char", "a", true},
		{"exactly 70", strings.Repeat("a", 70), true},

		{"empty", "", false},
		{"71 chars", strings.Repeat("a", 71), false},
		{"underscore", "sess_01", false},
		{"dot", "sess.01", false},
		{"slash", "sess/01", false},
		{"space", "sess 01", false},
		{"newline", "sess\n01", false},
		{"null byte", "sess\x0001", false},
		{"non-ascii", "sessión", false},
		{"emoji", "sess-🎬", false},
		{"colon", "sess:01", false},
		{"plus", "sess+01", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validSessionID(tc.id); got != tc.want {
				t.Errorf("validSessionID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// TestSessionIDLengthIsBytesNotRunes is the subtle half of the bound. Ranging a Go
// string yields RUNES, so a rune-counted limit would admit an id of 70 multi-byte
// characters — 210 bytes at the far end, which applies the limit to bytes. The
// non-ASCII refusal makes it moot today and the assertion keeps it moot if the
// character rule is ever widened.
func TestSessionIDLengthIsBytesNotRunes(t *testing.T) {
	if validSessionID(strings.Repeat("é", 70)) {
		t.Error("a 70-rune / 140-byte id was accepted — the downstream bound is in BYTES")
	}
}

// TestReplayRefusesABadSessionID drives the same grammar through the DOOR, so the
// refusal is a 400 on the wire and not merely a false from a pure function.
func TestReplayRefusesABadSessionID(t *testing.T) {
	mapped(t, "acme=hi_token")
	got := fakeProducer(t, nil)
	app := mountApp(t)
	for _, id := range []string{"", "sess_01", "sess/01", strings.Repeat("a", 71), "sess 01"} {
		body := `{"sessionId":` + strconv.Quote(id) + `,"events":[{"type":4,"timestamp":1,"data":{}}]}`
		if code, resp := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", body); code != http.StatusBadRequest {
			t.Errorf("sessionId %q = %d (%s), want 400", id, code, resp)
		}
	}
	if len(*got) != 0 {
		t.Errorf("a refused sessionId still produced %d record(s) — a key the pipeline "+
			"rejects must never reach the topic", len(*got))
	}
}

// ── 3. the credential gate ──────────────────────────────────────────────────

// TestReplayRefusesTheAnonymousCaller: no credential of any kind, on a recognized
// brand host and on the API host, is 401 ingest_key_required — the SAME refusal
// every ingest door answers, because this door shares their resolver. Nothing is
// produced.
func TestReplayRefusesTheAnonymousCaller(t *testing.T) {
	mapped(t, "acme=hi_token")
	got := fakeProducer(t, nil)
	app := mountApp(t)
	for _, host := range []string{"hanzo.ai", "api.hanzo.ai"} {
		code, body := doHost(t, app, replayPath, "", "", host, replayWire)
		refusedAnon(t, "anonymous "+replayPath+" on host "+host, code, body)
	}
	if len(*got) != 0 {
		t.Errorf("an anonymous caller produced %d record(s) — a recording nobody can attribute "+
			"must not reach the topic", len(*got))
	}
}

// TestReplayFailsClosedOnUnresolvableCredential: a caller that PRESENTED a
// credential which does not resolve is 403 on every carrier — never downgraded, and
// never produced. This is the same fail-closed rule doors_test.go holds over the
// doors table, asserted here because this route is not in that table.
func TestReplayFailsClosedOnUnresolvableCredential(t *testing.T) {
	mapped(t, "acme=hi_token")
	got := fakeProducer(t, nil)
	app := mountApp(t)
	stubResolver(t, func(string) (string, bool) { return "", false })
	for _, hdr := range []map[string]string{
		{"x-api-key": "sk-nosuch"},
		{"Authorization": "Bearer pk-nosuch"},
		{"x-hanzo-ingest-key": "pk-nosuch"},
	} {
		if code := postKeyed(t, app, replayPath, "hanzo.ai", replayWire, hdr); code != http.StatusForbidden {
			t.Errorf("unresolvable credential %v = %d, want 403 (fail closed)", hdr, code)
		}
	}
	if len(*got) != 0 {
		t.Errorf("an unresolvable credential produced %d record(s)", len(*got))
	}
}

// TestReplayAdmitsAResolvedKey is the "the gate is not just a wall" half: a
// resolvable sk- and a resolvable pk- both reach the produce.
func TestReplayAdmitsAResolvedKey(t *testing.T) {
	mapped(t, "acme=hi_token")
	for _, hdr := range []map[string]string{
		{"x-api-key": "sk-good"},
		{"Authorization": "Bearer pk-good"},
		{"x-hanzo-ingest-key": "pk-good"},
	} {
		got := fakeProducer(t, nil)
		app := mountApp(t)
		stubResolver(t, func(string) (string, bool) { return "acme", true })
		if code := postKeyed(t, app, replayPath, "", replayWire, hdr); code != http.StatusOK {
			t.Errorf("resolvable %v = %d, want 200", hdr, code)
		}
		if len(*got) != 1 {
			t.Errorf("resolvable %v produced %d record(s), want 1", hdr, len(*got))
		}
	}
}

// TestReplayTakesTheKeyOffTheQueryCarrier is the sendBeacon case, and it is the
// reason this door cannot be a typed op: a recorder drains its buffer on page-unload
// through navigator.sendBeacon, which cannot set a header, so the key arrives as
// ?ingest_key= — a carrier a typed In never sees.
func TestReplayTakesTheKeyOffTheQueryCarrier(t *testing.T) {
	mapped(t, "acme=hi_token")
	got := fakeProducer(t, nil)
	app := mountApp(t)
	key := stubResolver(t, func(string) (string, bool) { return "acme", true })
	if code := postKeyed(t, app, replayPath+"?ingest_key=pk-beacon", "", replayWire, nil); code != http.StatusOK {
		t.Fatalf("?ingest_key= = %d, want 200", code)
	}
	if *key != "pk-beacon" {
		t.Errorf("resolver was handed %q, want the key off the query carrier", *key)
	}
	if len(*got) != 1 {
		t.Fatalf("produced %d record(s), want 1", len(*got))
	}
}

// TestReplayTenantIsTheCredentialsNeverTheBody: the token that goes on the wire is
// the one mapped for the org the CREDENTIAL resolved to. A body naming another org,
// another token or another tenant cannot move it.
func TestReplayTenantIsTheCredentialsNeverTheBody(t *testing.T) {
	mapped(t, "acme=hi_acme,victim=hi_victim")
	got := fakeProducer(t, nil)
	app := mountApp(t)
	body := `{"sessionId":"sess-01","distinctId":"d","org":"victim","token":"hi_victim",` +
		`"events":[{"type":4,"timestamp":1,"data":{}}]}`
	if code, resp := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", body); code != http.StatusOK {
		t.Fatalf("= %d (%s), want 200", code, resp)
	}
	if len(*got) != 1 {
		t.Fatalf("produced %d record(s), want 1", len(*got))
	}
	tok, _ := header((*got)[0], "token")
	if tok != "hi_acme" {
		t.Errorf("token header = %q, want hi_acme — the tenant is the CREDENTIAL's, and a body "+
			"naming another org must not reach another team's stream", tok)
	}
}

// ── 4. org → token, fail closed ─────────────────────────────────────────────

// TestParseReplayTokens is the shim's grammar, including the property that makes it
// safe to hold several orgs: one malformed pair must not un-map the others.
func TestParseReplayTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"one pair", "hanzo=hi_abc", map[string]string{"hanzo": "hi_abc"}},
		{"two pairs", "hanzo=hi_abc,acme=hi_def", map[string]string{"hanzo": "hi_abc", "acme": "hi_def"}},
		{"spaces trimmed", " hanzo = hi_abc , acme = hi_def ", map[string]string{"hanzo": "hi_abc", "acme": "hi_def"}},
		{"missing separator is skipped", "hanzo,acme=hi_def", map[string]string{"acme": "hi_def"}},
		{"empty org is skipped", "=hi_abc,acme=hi_def", map[string]string{"acme": "hi_def"}},
		{"empty token is skipped", "hanzo=,acme=hi_def", map[string]string{"acme": "hi_def"}},
		{"trailing comma", "hanzo=hi_abc,", map[string]string{"hanzo": "hi_abc"}},
		{"token may contain =", "hanzo=hi_a=b", map[string]string{"hanzo": "hi_a=b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseReplayTokens(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("parseReplayTokens(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			for org, token := range tc.want {
				if got[org] != token {
					t.Errorf("parseReplayTokens(%q)[%q] = %q, want %q", tc.raw, org, got[org], token)
				}
			}
		})
	}
}

// TestUnmappedOrgFailsClosed is the one that matters most about the shim: an org the
// pipeline is not configured for is REFUSED, and refused with a status, rather than
// produced with an empty token. The consumer resolves the team from that header and
// drops what it cannot resolve, silently — so a 200 here would be a recording
// discarded off-cluster with every status check green.
func TestUnmappedOrgFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, tokens string }{
		{"nothing configured at all", ""},
		{"another org configured", "other=hi_other"},
		{"this org present but blank", "acme="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped(t, tc.tokens)
			got := fakeProducer(t, nil)
			app := mountApp(t)
			code, body := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", replayWire)
			if code != http.StatusServiceUnavailable {
				t.Errorf("unmapped org = %d (%s), want 503", code, body)
			}
			var e struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(body, &e); err != nil || e.Code != "replay_not_configured" {
				t.Errorf("code = %q (%s), want replay_not_configured", e.Code, body)
			}
			if len(*got) != 0 {
				t.Errorf("an unmapped org produced %d record(s) — an empty token is dropped "+
					"silently downstream, so it must never be produced", len(*got))
			}
		})
	}
}

// TestMappedOrgProducesItsOwnToken: two orgs, two tokens, and each credential
// reaches only its own.
func TestMappedOrgProducesItsOwnToken(t *testing.T) {
	mapped(t, "acme=hi_acme,globex=hi_globex")
	for _, tc := range []struct{ org, want string }{
		{"acme", "hi_acme"},
		{"globex", "hi_globex"},
	} {
		got := fakeProducer(t, nil)
		app := mountApp(t)
		if code, body := doBody(t, app, http.MethodPost, replayPath, "user-dave", tc.org, replayWire); code != http.StatusOK {
			t.Fatalf("org %s = %d (%s), want 200", tc.org, code, body)
		}
		if len(*got) != 1 {
			t.Fatalf("org %s produced %d record(s), want 1", tc.org, len(*got))
		}
		if tok, _ := header((*got)[0], "token"); tok != tc.want {
			t.Errorf("org %s produced token %q, want %q", tc.org, tok, tc.want)
		}
	}
}

// ── 5. bounds, and the produce failure ──────────────────────────────────────

// TestReplayBoundsAreRefusedNotTruncated: over either bound the request is refused
// whole. A truncated recording would be unplayable and the receipt would be a lie.
func TestReplayBoundsAreRefusedNotTruncated(t *testing.T) {
	mapped(t, "acme=hi_token")

	t.Run("oversize body is 413", func(t *testing.T) {
		got := fakeProducer(t, nil)
		app := mountApp(t)
		// One event whose data pushes the body past the cap.
		big := `{"sessionId":"sess-01","events":[{"type":2,"timestamp":1,"data":{"blob":"` +
			strings.Repeat("x", maxReplayBytes) + `"}}]}`
		code, body := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", big)
		if code != http.StatusRequestEntityTooLarge {
			t.Errorf("oversize body = %d (%s), want 413 — the status is what tells a recorder to chunk", code, body)
		}
		if len(*got) != 0 {
			t.Errorf("an oversize body produced %d record(s)", len(*got))
		}
	})

	t.Run("too many events is 400", func(t *testing.T) {
		got := fakeProducer(t, nil)
		app := mountApp(t)
		items := make([]string, maxReplayEvents+1)
		for i := range items {
			items[i] = `{"type":3,"timestamp":1,"data":{}}`
		}
		body := `{"sessionId":"sess-01","events":[` + strings.Join(items, ",") + `]}`
		if code, resp := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", body); code != http.StatusBadRequest {
			t.Errorf("oversize batch = %d (%s), want 400", code, resp)
		}
		if len(*got) != 0 {
			t.Errorf("an oversize batch produced %d record(s)", len(*got))
		}
	})

	t.Run("empty batch is 400", func(t *testing.T) {
		app := mountApp(t)
		fakeProducer(t, nil)
		if code, resp := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme",
			`{"sessionId":"sess-01","events":[]}`); code != http.StatusBadRequest {
			t.Errorf("empty batch = %d (%s), want 400", code, resp)
		}
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		app := mountApp(t)
		fakeProducer(t, nil)
		if code, resp := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme",
			`{"sessionId":`); code != http.StatusBadRequest {
			t.Errorf("malformed body = %d (%s), want 400", code, resp)
		}
	})
}

// TestProduceFailureIsA503NeverA200 is the receipt's honesty, and it is the whole
// reason the produce is synchronous. A batch that was admitted and could not be made
// durable is the caller's business: a 200 over it would be exactly the silent loss
// that `answer` (event.go) exists to prevent, and a recorder would never retry.
func TestProduceFailureIsA503NeverA200(t *testing.T) {
	mapped(t, "acme=hi_token")
	fakeProducer(t, errors.New("broker unreachable"))
	app := mountApp(t)
	code, body := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", replayWire)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("failed produce = %d (%s), want 503", code, body)
	}
	if strings.Contains(string(body), `"accepted"`) {
		t.Errorf("a failed produce answered a receipt (%s) — a recording that was not made "+
			"durable must never be reported as accepted", body)
	}
}

// TestReplayAnswersTheSharedReceipt: the happy path answers the SAME CaptureResult
// every other door on this surface answers, and one batch is one accepted unit.
func TestReplayAnswersTheSharedReceipt(t *testing.T) {
	mapped(t, "acme=hi_token")
	fakeProducer(t, nil)
	app := mountApp(t)
	code, body := doBody(t, app, http.MethodPost, replayPath, "user-dave", "acme", replayWire)
	if code != http.StatusOK {
		t.Fatalf("= %d (%s), want 200", code, body)
	}
	var got CaptureResult
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("receipt json: %v (%s)", err, body)
	}
	if got.Accepted != 1 || got.Dropped != 0 {
		t.Errorf("receipt = %+v, want {Accepted:1 Dropped:0}", got)
	}
}

// ── 6. the seam defaults to the real thing ──────────────────────────────────

// TestProduceSeamDefaultsToTheRealThing pins what fakeProducer substitutes, for the
// same reason TestWritePathSeamsDefaultToTheRealThing pins the write path's: a
// `produce` rebound at its DECLARATION to a func returning nil would discard every
// recording while each caller still got its 200, and nothing else in this package
// would notice.
func TestProduceSeamDefaultsToTheRealThing(t *testing.T) {
	if !samePtr(produce, produceSnapshot) {
		t.Error("produce does not default to produceSnapshot — a substituted produce seam " +
			"discards recordings behind a 200 receipt")
	}
}
