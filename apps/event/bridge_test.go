// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// pageFact is one project-key pageview as it rides the bus.
func pageFact() message {
	return message{
		Signal:   string(signalAct),
		Org:      "acme",
		Time:     time.Date(2026, 9, 24, 3, 0, 0, 123000000, time.UTC),
		ID:       "0198b2c4-7e21-7c3a-9f00-5a1b2c3d4e5f",
		Name:     "page_viewed",
		Kind:     kindPage,
		Product:  "shop",
		URL:      "https://shop.example/pricing?plan=pro",
		Path:     "/pricing",
		Distinct: "user-7",
		Session:  "sess-1",
		Groups:   map[string]string{"organization": "acme"},
		Attributes: map[string]string{
			"$source":  "posthog",
			"library":  "web",
			"referrer": "https://news.example/",
			"$set":     `{"plan":"pro"}`,
			"note":     "[beta]",
		},
		Key: siteKey,
	}
}

func headers(rec *kgo.Record) map[string]string {
	out := make(map[string]string, len(rec.Headers))
	for _, h := range rec.Headers {
		out[h.Key] = string(h.Value)
	}
	return out
}

// TestInsightsRecordIsWhatCaptureProduces pins the whole message the plugin-server
// parses: topic, key, headers, the envelope, and the event inside it as a string.
func TestInsightsRecordIsWhatCaptureProduces(t *testing.T) {
	now := time.Date(2026, 9, 24, 3, 0, 1, 0, time.UTC)
	rec, err := insightsRecord(pageFact(), now)
	if err != nil || rec == nil {
		t.Fatalf("insightsRecord = %v, %v", rec, err)
	}
	if rec.Topic != "events_plugin_ingestion" {
		t.Fatalf("topic = %q", rec.Topic)
	}
	if got := string(rec.Key); got != siteKey+":user-7" {
		t.Fatalf("key = %q, want token:distinct_id", got)
	}
	h := headers(rec)
	for k, want := range map[string]string{
		"token":       siteKey,
		"distinct_id": "user-7",
		"session_id":  "sess-1",
		"event":       "$pageview",
		"uuid":        "0198b2c4-7e21-7c3a-9f00-5a1b2c3d4e5f",
		"timestamp":   "1790218800123",
		"now":         "2026-09-24T03:00:01Z",
	} {
		if h[k] != want {
			t.Errorf("header %s = %q, want %q", k, h[k], want)
		}
	}

	var env map[string]any
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatalf("value: %v", err)
	}
	for k, want := range map[string]string{
		"uuid":        "0198b2c4-7e21-7c3a-9f00-5a1b2c3d4e5f",
		"distinct_id": "user-7",
		"token":       siteKey,
		"event":       "$pageview",
		"timestamp":   "2026-09-24T03:00:00.123Z",
		"now":         "2026-09-24T03:00:01Z",
		"ip":          "",
	} {
		if env[k] != want {
			t.Errorf("envelope %s = %v, want %q", k, env[k], want)
		}
	}
	data, ok := env["data"].(string)
	if !ok {
		t.Fatalf("data is %T, want a JSON string", env["data"])
	}
	var ev struct {
		UUID       string         `json:"uuid"`
		Event      string         `json:"event"`
		DistinctID string         `json:"distinct_id"`
		Timestamp  string         `json:"timestamp"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatalf("data: %v", err)
	}
	if ev.Event != "$pageview" || ev.DistinctID != "user-7" || ev.UUID != env["uuid"] {
		t.Fatalf("event = %+v", ev)
	}
	p := ev.Properties
	for k, want := range map[string]any{
		"$current_url":         "https://shop.example/pricing?plan=pro",
		"$host":                "shop.example",
		"$pathname":            "/pricing",
		"$session_id":          "sess-1",
		"product":              "shop",
		"$lib":                 "web",
		"$referrer":            "https://news.example/",
		"$event_name":          "page_viewed",
		"$source":              "posthog",
		"note":                 "[beta]",
		"$groups.organization": "acme",
	} {
		got := p[k]
		if k == "$groups.organization" {
			g, _ := p["$groups"].(map[string]any)
			got = g["organization"]
		}
		if got != want {
			t.Errorf("property %s = %v, want %v", k, got, want)
		}
	}
	if set, _ := p["$set"].(map[string]any); set["plan"] != "pro" {
		t.Errorf("$set = %#v, want the object persons processing reads", p["$set"])
	}
	for _, gone := range []string{"library", "referrer"} {
		if _, ok := p[gone]; ok {
			t.Errorf("%s survived its rename", gone)
		}
	}
}

// TestOnlyAProjectKeyIsFiled: the token is the key that admitted the event, so a
// fact no project key admitted, or one that names nobody, is not bridged.
func TestOnlyAProjectKeyIsFiled(t *testing.T) {
	for name, edit := range map[string]func(*message){
		"no key":      func(m *message) { m.Key = "" },
		"no identity": func(m *message) { m.Distinct, m.Anonymous, m.Person = "", "", "" },
	} {
		m := pageFact()
		edit(&m)
		if rec, err := insightsRecord(m, time.Now()); rec != nil || err != nil {
			t.Errorf("%s: record = %v, %v; want nothing", name, rec, err)
		}
	}
	m := pageFact()
	m.Distinct = ""
	m.Anonymous = "anon-3"
	rec, _ := insightsRecord(m, time.Now())
	if rec == nil || headers(rec)["distinct_id"] != "anon-3" {
		t.Fatal("an anonymous visitor is still someone")
	}
}

// TestIdentifyNamesTheAnonymousVisitor: an identify maps onto $identify and carries
// the pre-login id, which is what merges the two persons.
func TestIdentifyNamesTheAnonymousVisitor(t *testing.T) {
	m := pageFact()
	m.Kind, m.Name, m.Anonymous = kindIdentify, "user_identified", "anon-3"
	rec, err := insightsRecord(m, time.Now())
	if err != nil || rec == nil {
		t.Fatalf("insightsRecord = %v, %v", rec, err)
	}
	var env struct {
		Data string `json:"data"`
	}
	_ = json.Unmarshal(rec.Value, &env)
	var ev struct {
		Event      string         `json:"event"`
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal([]byte(env.Data), &ev)
	if ev.Event != "$identify" || ev.Properties["$anon_distinct_id"] != "anon-3" {
		t.Fatalf("identify = %s %v", ev.Event, ev.Properties["$anon_distinct_id"])
	}
	m.Kind, m.Name = kindTrack, "signup_completed"
	rec, _ = insightsRecord(m, time.Now())
	if headers(rec)["event"] != "signup_completed" {
		t.Fatal("a caller's own name must pass through")
	}
}

// TestEventUUIDIsStable: the plugin-server takes only a dashed UUID, and a
// redelivered fact must keep the one it had.
func TestEventUUIDIsStable(t *testing.T) {
	hex := "0198b2c47e217c3a9f005a1b2c3d4e5f"
	if got := eventUUID("acme", hex); got != "0198b2c4-7e21-7c3a-9f00-5a1b2c3d4e5f" {
		t.Fatalf("a hex id = %q, want it dashed", got)
	}
	a, b := eventUUID("acme", "msg-42"), eventUUID("acme", "msg-42")
	if a != b {
		t.Fatal("a derived uuid must not change between deliveries")
	}
	if _, err := uuid.Parse(a); err != nil {
		t.Fatalf("derived %q is not a uuid", a)
	}
	if a == eventUUID("zeta", "msg-42") {
		t.Fatal("two orgs' ids must not collide")
	}
}

// TestProjectKeyRidesTheFactToTheBridge drives the real ingest path: a project key
// reaches the published fact and becomes the token; an IAM key never does.
func TestProjectKeyRidesTheFactToTheBridge(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}})
	orig := resolveKeyOrg
	resolveKeyOrg = func(_ context.Context, key string) (string, bool) { return "acme", key == "pk-iamorgkey" }
	t.Cleanup(func() { resolveKeyOrg = orig })
	w := fakeWarehouse(t)
	app := mountApp(t)

	posthog := fmt.Sprintf(`{"api_key":%q,"event":"$pageview","distinct_id":"user-7","properties":{"consent":"analytics","$current_url":"https://shop.example/"}}`, siteKey)
	if code := postKeyed(t, app, "/v1/event", "", posthog, nil); code != http.StatusOK {
		t.Fatalf("posthog wire = %d", code)
	}
	if code := postKeyed(t, app, "/v1/event", "", `{"type":"track","event":"x","distinctId":"u","properties":{"consent":"analytics"}}`,
		map[string]string{"Authorization": "Bearer pk-iamorgkey"}); code != http.StatusOK {
		t.Fatalf("iam key = %d", code)
	}
	if len(w.facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(w.facts))
	}
	rec, err := insightsRecord(wire(w.facts[0]), time.Now())
	if err != nil || rec == nil || headers(rec)["token"] != siteKey || headers(rec)["event"] != "$pageview" {
		t.Fatalf("project-key fact = %v, %v", rec, err)
	}
	if w.facts[1].key != "" {
		t.Fatal("an IAM key must never ride the bus")
	}
	if rec, _ := insightsRecord(wire(w.facts[1]), time.Now()); rec != nil {
		t.Fatal("an org-level credential names no project and is not bridged")
	}
}

// TestBridgeFailsSoft: nothing the bridge meets can stop landing. A fact it cannot
// file is acked, a broker refusal is acked and dropped, and a broker that is down
// sends the fact back to the stream.
func TestBridgeFailsSoft(t *testing.T) {
	var produced []*kgo.Record
	var answer error
	orig := produce
	produce = func(_ context.Context, rec *kgo.Record) error {
		produced = append(produced, rec)
		return answer
	}
	t.Cleanup(func() { produce = orig })

	s := &drain{log: luxlog.New("test")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // bridgeRetry's pause returns at once
	body := func(m message) *infra.StreamMessage {
		b, _ := json.Marshal(m)
		return &infra.StreamMessage{Subject: signalAct.subject(), Data: b}
	}

	if err := s.bridge(ctx, &infra.StreamMessage{Data: []byte(`{"org":"acme","name":"x"}`)}); err != nil {
		t.Fatalf("an envelope is not a fact: %v", err)
	}
	unkeyed := pageFact()
	unkeyed.Key = ""
	if err := s.bridge(ctx, body(unkeyed)); err != nil || len(produced) != 0 {
		t.Fatalf("an unkeyed fact: err %v, produced %d", err, len(produced))
	}
	if err := s.bridge(ctx, body(pageFact())); err != nil || len(produced) != 1 {
		t.Fatalf("a keyed fact: err %v, produced %d", err, len(produced))
	}
	answer = fmt.Errorf("produce: %w", kerr.MessageTooLarge)
	if err := s.bridge(ctx, body(pageFact())); err != nil {
		t.Fatalf("a refusal no retry changes must be acked: %v", err)
	}
	answer = errors.New("dial tcp kafka:9092: connection refused")
	if err := s.bridge(ctx, body(pageFact())); err == nil {
		t.Fatal("a broker that is down must send the fact back to the stream")
	}
}

// inner decodes the event a record carries.
func inner(t *testing.T, rec *kgo.Record) (string, string, map[string]any) {
	t.Helper()
	var env struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatalf("value: %v", err)
	}
	var ev struct {
		UUID       string         `json:"uuid"`
		Event      string         `json:"event"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(env.Data), &ev); err != nil {
		t.Fatalf("data: %v", err)
	}
	return ev.UUID, ev.Event, ev.Properties
}

// TestSignInJoinsTheVisitorToTheAccount: a sign-in that carries the visitor's
// anonymous id beside the account's is filed after an $identify joining them, so
// the steps before sign-in and after it are one person.
func TestSignInJoinsTheVisitorToTheAccount(t *testing.T) {
	m := pageFact()
	m.Kind, m.Name, m.Anonymous, m.Distinct = kindTrack, "login_completed", "anon-3", "user-7"
	recs, err := insightsRecords(m, time.Now())
	if err != nil || len(recs) != 2 {
		t.Fatalf("insightsRecords = %d records, %v; want an $identify and the event", len(recs), err)
	}
	joinID, name, props := inner(t, recs[0])
	if name != "$identify" || props["$anon_distinct_id"] != "anon-3" || headers(recs[0])["distinct_id"] != "user-7" {
		t.Fatalf("first record = %s anon %v distinct %s", name, props["$anon_distinct_id"], headers(recs[0])["distinct_id"])
	}
	eventID, name, _ := inner(t, recs[1])
	if name != "login_completed" {
		t.Fatalf("second record = %s, want the sign-in itself", name)
	}
	if joinID == eventID {
		t.Fatal("the $identify and the event must not share a uuid")
	}
	again, _ := insightsRecords(m, time.Now())
	if id, _, _ := inner(t, again[0]); id != joinID {
		t.Fatal("a redelivered sign-in must produce the same $identify uuid")
	}

	for _, same := range []message{
		func() message { c := m; c.Anonymous = c.Distinct; return c }(),
		func() message { c := m; c.Anonymous = ""; return c }(),
		func() message { c := m; c.Name = "signup_submitted"; return c }(),
	} {
		if recs, _ := insightsRecords(same, time.Now()); len(recs) != 1 {
			t.Fatalf("%s anon %q distinct %q: %d records, want the event alone", same.Name, same.Anonymous, same.Distinct, len(recs))
		}
	}
}

// TestVisitorPropertiesUseInsightsNames: what ingest stamps about the visitor is
// read by insights' lenses under their own names.
func TestVisitorPropertiesUseInsightsNames(t *testing.T) {
	m := pageFact()
	m.Attributes["user_agent"] = "Mozilla/5.0"
	m.Attributes["country"] = "US"
	m.Attributes["language"] = "en-US"
	m.Attributes["device"] = "mobile"
	rec, err := insightsRecord(m, time.Now())
	if err != nil || rec == nil {
		t.Fatalf("insightsRecord = %v, %v", rec, err)
	}
	_, _, props := inner(t, rec)
	for k, want := range map[string]string{
		"$raw_user_agent":     "Mozilla/5.0",
		"$geoip_country_code": "US",
		"$browser_language":   "en-US",
		"$device_type":        "mobile",
	} {
		if props[k] != want {
			t.Errorf("%s = %v, want %q", k, props[k], want)
		}
	}
	for _, k := range []string{"user_agent", "country", "language", "device"} {
		if _, ok := props[k]; ok {
			t.Errorf("%s is carried under its plane name too", k)
		}
	}
}

// The visitor's address rides to insights so it can place the visit; it is never a property.
func TestInsightsRecordCarriesTheVisitorAddress(t *testing.T) {
	m := pageFact()
	m.IP = "203.0.113.7"
	rec, err := insightsRecord(m, time.Now())
	if err != nil || rec == nil {
		t.Fatalf("insightsRecord = %v, %v", rec, err)
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatal(err)
	}
	if env["ip"] != "203.0.113.7" {
		t.Errorf("envelope ip = %v", env["ip"])
	}
	if strings.Contains(env["data"].(string), "203.0.113.7") {
		t.Error("the address is not a property")
	}
}

func TestBridgeSplitsScreenAndMapsTestFlags(t *testing.T) {
	m := pageFact()
	m.Attributes["screen"] = "1920x1080"
	m.Attributes["internal"] = "true"
	m.Attributes["test"] = "true"
	p := properties(m, "user-7")
	if p["$screen_width"] != 1920 || p["$screen_height"] != 1080 {
		t.Errorf("screen = %v x %v", p["$screen_width"], p["$screen_height"])
	}
	if _, ok := p["screen"]; ok {
		t.Error("screen passed through unrenamed")
	}
	if p["$internal"] != "true" || p["is_test"] != "true" {
		t.Errorf("flags = %v %v", p["$internal"], p["is_test"])
	}
	if _, ok := p["internal"]; ok {
		t.Error("internal passed through unrenamed")
	}
}

func TestScreenOfNormalizes(t *testing.T) {
	for in, want := range map[string]string{"1920x1080": "1920x1080", " 390 × 844 ": "390x844", "1X2": "1x2"} {
		if got, ok := screenOf(in); !ok || got != want {
			t.Errorf("screenOf(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "wide", "1920", "axb"} {
		if _, ok := screenOf(bad); ok {
			t.Errorf("screenOf(%q) accepted", bad)
		}
	}
	if !testTraffic(map[string]any{"test": true}) || testTraffic(map[string]any{"test": "false"}) {
		t.Error("testTraffic misreads the flag")
	}
}

// errorFact is one project-key error fact as it rides event.error on the bus.
func errorFact() message {
	return message{
		Signal:   string(signalError),
		Org:      "acme",
		Time:     time.Date(2026, 9, 24, 3, 5, 0, 456000000, time.UTC),
		ID:       "err_0198b2c4",
		Name:     nameError,
		Message:  "Cannot read properties of null (reading 'submit')",
		Severity: severityFatal,
		Product:  "site",
		Env:      "production",
		Service:  "web",
		Release:  "2026.11.21-abc123",
		URL:      "https://hanzo.ai/pricing?plan=pro",
		Path:     "/pricing",
		Distinct: "user-7",
		Session:  "sess-9",
		Trace:    "trace-1",
		Span:     "span-2",
		Issue:    "deadbeef12345678",
		Class:    "TypeError",
		Origin:   "hanzo.ai",
		Handled:  false,
		Frames: []messageFrame{
			{Function: "inpage", File: "chrome-extension://abc/inpage.js", Line: 1, Column: 2, Own: false},
			{Function: "onSubmit", File: "https://hanzo.ai/_next/static/chunks/checkout.js", Line: 42, Column: 17, Own: true},
		},
		Attributes: map[string]string{
			"library":        "@hanzo/event",
			"error_source":   "window_error",
			"component_path": "PricingPage > CheckoutForm > SubmitButton",
			"control_id":     "btn-checkout-submit",
			"route_template": "/pricing",
			"hostname":       "hanzo.ai",
			"breadcrumbs":    `[{"t":"click","target":"btn-checkout-submit"}]`,
		},
		Key: siteKey,
	}
}

// TestErrorFactRendersAsInsightsException pins the complete $exception contract
// produced for signalError facts onto events_plugin_ingestion.
func TestErrorFactRendersAsInsightsException(t *testing.T) {
	now := time.Date(2026, 9, 24, 3, 5, 1, 0, time.UTC)
	rec, err := insightsRecord(errorFact(), now)
	if err != nil || rec == nil {
		t.Fatalf("insightsRecord = %v, %v", rec, err)
	}
	if rec.Topic != bridgeTopic {
		t.Fatalf("topic = %q, want %q", rec.Topic, bridgeTopic)
	}
	if got := string(rec.Key); got != siteKey+":user-7" {
		t.Fatalf("key = %q, want token:distinct_id", got)
	}
	h := headers(rec)
	if h["event"] != "$exception" || h["token"] != siteKey || h["distinct_id"] != "user-7" || h["session_id"] != "sess-9" {
		t.Fatalf("headers = %v", h)
	}

	var env map[string]any
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env["event"] != "$exception" || env["token"] != siteKey || env["distinct_id"] != "user-7" {
		t.Fatalf("envelope = %v", env)
	}

	_, evName, p := inner(t, rec)
	if evName != "$exception" {
		t.Fatalf("inner event = %q, want $exception", evName)
	}
	for k, want := range map[string]any{
		"$exception_fingerprint": "deadbeef12345678",
		"fingerprint":            "deadbeef12345678",
		"$exception_type":        "TypeError",
		"error_class":            "TypeError",
		"$exception_message":     "Cannot read properties of null (reading 'submit')",
		"message":                "Cannot read properties of null (reading 'submit')",
		"$exception_level":       "fatal",
		"$exception_handled":     false,
		"error_source":           "window_error",
		"file":                   "https://hanzo.ai/_next/static/chunks/checkout.js",
		"function":               "onSubmit",
		"line":                   float64(42),
		"column":                 float64(17),
		"component_path":         "PricingPage > CheckoutForm > SubmitButton",
		"control_id":             "btn-checkout-submit",
		"route_template":         "/pricing",
		"hostname":               "hanzo.ai",
		"release":                "2026.11.21-abc123",
		"$release":               "2026.11.21-abc123",
		"$session_id":            "sess-9",
		"$insert_id":             "err_0198b2c4",
		"$lib":                   "@hanzo/event",
		"$current_url":           "https://hanzo.ai/pricing?plan=pro",
		"$pathname":              "/pricing",
		"environment":            "production",
		"service":                "web",
		"trace_id":               "trace-1",
		"span_id":                "span-2",
	} {
		if p[k] != want {
			t.Errorf("property %s = %#v, want %#v", k, p[k], want)
		}
	}

	if types, ok := p["$exception_types"].([]any); !ok || len(types) != 1 || types[0] != "TypeError" {
		t.Errorf("$exception_types = %#v", p["$exception_types"])
	}
	if vals, ok := p["$exception_values"].([]any); !ok || len(vals) != 1 || vals[0] != "Cannot read properties of null (reading 'submit')" {
		t.Errorf("$exception_values = %#v", p["$exception_values"])
	}
	crumbs, ok := p["breadcrumbs"].([]any)
	if !ok || len(crumbs) != 1 {
		t.Fatalf("breadcrumbs = %#v, want decoded JSON array", p["breadcrumbs"])
	}
	list, ok := p["$exception_list"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("$exception_list = %#v", p["$exception_list"])
	}
	entry := list[0].(map[string]any)
	if entry["type"] != "TypeError" || entry["value"] != "Cannot read properties of null (reading 'submit')" {
		t.Errorf("entry = %#v", entry)
	}
	mech := entry["mechanism"].(map[string]any)
	if mech["type"] != "window_error" || mech["handled"] != false || mech["synthetic"] != false {
		t.Errorf("mechanism = %#v", mech)
	}
	st := entry["stacktrace"].(map[string]any)
	frames := st["frames"].([]any)
	if st["type"] != "raw" || len(frames) != 2 {
		t.Fatalf("stacktrace = %#v", st)
	}
	f0 := frames[0].(map[string]any)
	f1 := frames[1].(map[string]any)
	if f0["in_app"] != false || f1["in_app"] != true || f1["function"] != "onSubmit" || f1["lineno"] != float64(42) || f1["colno"] != float64(17) {
		t.Errorf("frames = %#v", frames)
	}
}

// TestErrorFactEdgeCases verifies defaults, fallbacks, handled flags, El annotation
// extraction, attribute-only frames, and dropping unkeyed / identity-less faults.
func TestErrorFactEdgeCases(t *testing.T) {
	// 1. Minimal error fact with defaults, handled=true, El annotation, attribute file/line/col, session fallback
	m := message{
		Signal:  string(signalError),
		Org:     "acme",
		Time:    time.Date(2026, 9, 24, 4, 0, 0, 0, time.UTC),
		ID:      "err-min",
		URL:     "https://hanzo.app/chat",
		Path:    "/chat",
		Session: "sess-only",
		Handled: true,
		El: &messageEl{
			Testid: "chat-send",
			Path:   []string{"App", "ChatPanel", "Composer"},
		},
		Attributes: map[string]string{
			"file":     "https://hanzo.app/app.js",
			"line":     "12",
			"column":   "5",
			"function": "send",
		},
		Key: siteKey,
	}
	rec, err := insightsRecord(m, time.Now())
	if err != nil || rec == nil {
		t.Fatalf("minimal error record = %v, %v", rec, err)
	}
	if headers(rec)["distinct_id"] != "sess-only" {
		t.Errorf("distinct_id = %q, want session fallback", headers(rec)["distinct_id"])
	}
	_, _, p := inner(t, rec)
	if p["$exception_type"] != "Error" || p["$exception_level"] != "error" || p["$exception_handled"] != true {
		t.Errorf("defaults = type:%v level:%v handled:%v", p["$exception_type"], p["$exception_level"], p["$exception_handled"])
	}
	if _, hasVals := p["$exception_values"]; hasVals {
		t.Errorf("empty message should omit $exception_values, got %v", p["$exception_values"])
	}
	if p["component_path"] != "App > ChatPanel > Composer" || p["control_id"] != "chat-send" {
		t.Errorf("el fallback = component_path:%v control_id:%v", p["component_path"], p["control_id"])
	}
	if p["hostname"] != "hanzo.app" || p["route_template"] != "/chat" || p["error_source"] != "window_error" {
		t.Errorf("location defaults = host:%v route:%v source:%v", p["hostname"], p["route_template"], p["error_source"])
	}
	if p["line"] != float64(12) || p["column"] != float64(5) {
		t.Errorf("attribute frame line/col = %v:%v", p["line"], p["column"])
	}

	// 2. No project key or no identity/session at all -> dropped
	noKey := errorFact()
	noKey.Key = ""
	if r, err := insightsRecord(noKey, time.Now()); r != nil || err != nil {
		t.Errorf("noKey = %v, %v; want nil", r, err)
	}
	noWho := errorFact()
	noWho.Distinct, noWho.Anonymous, noWho.Person, noWho.Session = "", "", "", ""
	if r, err := insightsRecord(noWho, time.Now()); r != nil || err != nil {
		t.Errorf("noWho = %v, %v; want nil", r, err)
	}
}

// TestErrorIngestToBridgeEndToEnd drives POST /v1/event with type=error through
// normalize -> wire -> insightsRecord and verifies scrubbed $exception output.
func TestErrorIngestToBridgeEndToEnd(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "site"}})
	w := fakeWarehouse(t)
	app := mountApp(t)

	payload := `{"batch":[{"messageId":"err_e2e_1","type":"error","distinctId":"user-9","sessionId":"sess-9","url":"https://hanzo.ai/checkout","path":"/checkout","release":"v1.2.3","level":"error","error":{"type":"ReferenceError","message":"token sk-secret1234567890123456 failed","stack":"ReferenceError: failed\n    at pay (https://hanzo.ai/checkout.js:88:9)","handled":false},"properties":{"error_source":"unhandled_rejection","route_template":"/checkout","component_path":"Checkout > PayButton","control_id":"pay-now","breadcrumbs":[{"t":"click","target":"pay-now"}]}}]}`
	if code := postKeyed(t, app, "/v1/event?ingest_key="+siteKey, "", payload, nil); code != http.StatusOK {
		t.Fatalf("ingest error = %d", code)
	}
	if len(w.facts) != 1 || w.facts[0].signal != signalError {
		t.Fatalf("facts = %+v, want 1 signalError fact", w.facts)
	}
	rec, err := insightsRecord(wire(w.facts[0]), time.Now())
	if err != nil || rec == nil {
		t.Fatalf("insightsRecord = %v, %v", rec, err)
	}
	_, evName, p := inner(t, rec)
	if evName != "$exception" || headers(rec)["event"] != "$exception" {
		t.Fatalf("event = %q header = %q", evName, headers(rec)["event"])
	}
	if p["$exception_type"] != "ReferenceError" || p["$exception_fingerprint"] == "" || p["$exception_fingerprint"] == nil {
		t.Fatalf("type/fp = %v / %v", p["$exception_type"], p["$exception_fingerprint"])
	}
	if msg, _ := p["$exception_message"].(string); strings.Contains(msg, "sk-secret1234567890123456") {
		t.Fatalf("secret leaked into $exception_message: %q", msg)
	}
	if p["file"] != "https://hanzo.ai/checkout.js" || p["function"] != "pay" || p["line"] != float64(88) || p["column"] != float64(9) {
		t.Errorf("frame = %v:%v:%v in %v", p["file"], p["line"], p["column"], p["function"])
	}
}

// TestErrorBridgeConsumesSeparateJetStreamDurable verifies against a real embedded
// JetStream bus that event-insights (event.act) and event-insights-errors
// (event.error) bind independent durables, bridge both signals, and ignore
// subscriber EventEnvelopes on event.error.
func TestErrorBridgeConsumesSeparateJetStreamDurable(t *testing.T) {
	if bridgeDurable != "event-insights" || bridgeErrorDurable != "event-insights-errors" || bridgeDurable == bridgeErrorDurable {
		t.Fatalf("durables = %q / %q", bridgeDurable, bridgeErrorDurable)
	}
	_, cl := planeOn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := EnsureEventStream(ctx, cl); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	var mu sync.Mutex
	var produced []*kgo.Record
	orig := produce
	produce = func(_ context.Context, rec *kgo.Record) error {
		mu.Lock()
		produced = append(produced, rec)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { produce = orig })

	if _, err := cl.PublishJSONToStream(ctx, signalAct.subject(), pageFact()); err != nil {
		t.Fatalf("publish act: %v", err)
	}
	if _, err := cl.PublishJSONToStream(ctx, signalError.subject(), errorFact()); err != nil {
		t.Fatalf("publish error: %v", err)
	}
	// Subscriber EventEnvelope sharing event.error (no "signal" field) must be ignored.
	if _, err := cl.PublishJSONToStream(ctx, signalError.subject(), EventEnvelope{
		Org: "acme", ID: "env-1", Name: "$error", Time: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("publish envelope: %v", err)
	}

	s := &drain{log: luxlog.New("test")}
	go s.relay(ctx, cl)
	go s.relayErrors(ctx, cl)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(produced)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Verify both separate durables exist with their own FilterSubjects.
	js := cl.JetStream()
	actCons, err := js.Consumer(ctx, EventStream, bridgeDurable)
	if err != nil {
		t.Fatalf("act durable missing: %v", err)
	}
	if info, err := actCons.Info(ctx); err != nil || info.Config.FilterSubject != signalAct.subject() {
		t.Fatalf("act durable filter = %v, err = %v", info.Config.FilterSubject, err)
	}
	errCons, err := js.Consumer(ctx, EventStream, bridgeErrorDurable)
	if err != nil {
		t.Fatalf("error durable missing: %v", err)
	}
	if info, err := errCons.Info(ctx); err != nil || info.Config.FilterSubject != signalError.subject() {
		t.Fatalf("error durable filter = %v, err = %v", info.Config.FilterSubject, err)
	}

	// Give any stray envelope processing a brief moment before asserting exact count.
	time.Sleep(100 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(produced) != 2 {
		t.Fatalf("produced %d records, want 2 ($pageview + $exception)", len(produced))
	}
	seen := map[string]bool{}
	for _, rec := range produced {
		seen[headers(rec)["event"]] = true
	}
	if !seen["$pageview"] || !seen["$exception"] {
		t.Fatalf("events bridged = %v, want $pageview and $exception", seen)
	}
}
