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

// bridge.go — every product event, onto insights' own ingestion topic.
//
// /v1/event is the ONE ingest; insights runs no capture tier of its own. What its
// pipeline needs is the message its capture would have produced, on the topic its
// plugin-server consumes. So these consumers render each act fact and each error
// fact (as $exception) as exactly that message and produce it to
// events_plugin_ingestion on the embedded broker (apps/kafka). The plugin-server
// does the rest as it always has: resolves the project by token, processes persons,
// and emits datastore_events_json, which the events table behind the Event Explorer
// and Error Tracking reads. The Live tab reads the raw topic this file writes.
//
// A CONSUMER, NOT A STEP. It binds its own durable on the EVENT stream beside the
// warehouse's (warehouse.go), so insights being slow, down or absent never touches
// ingest or landing. A produce that fails is retried from the stream after a pause;
// one the broker refuses for good is dropped and logged. The durable redelivers
// without limit, so it never raises the MAX_DELIVERIES advisory the warehouse
// counts as a lost fact.
//
// THE TOKEN IS THE PROJECT KEY. An insights project's api_token is the pk- key of
// the cloud project it mirrors, so the key that admitted an event is the token it
// is filed under. A fact no project key admitted (a bearer, an IAM key, a team
// token) is not bridged: it names an org and no project, and picking one would
// file one site's traffic under another's.

package event

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/commerce/infra"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// bridgeTopic is the plugin-server's ingestion topic (KAFKA_EVENTS_PLUGIN_INGESTION
// in insights' nodejs/src/common/config/kafka-topics.ts).
const bridgeTopic = "events_plugin_ingestion"

// bridgeDurable names the act consumer on the EVENT stream.
const bridgeDurable = subjectRoot + "-insights"

// bridgeErrorDurable names the error consumer on the EVENT stream. It is a
// separate durable from bridgeDurable so the existing event-insights durable's
// FilterSubject is never mutated in JetStream.
const bridgeErrorDurable = subjectRoot + "-insights-errors"

// bridgeRetry is the pause before a failed produce goes back to the stream. The
// handler holds its message for it, so a broker that is down costs one attempt
// per pause rather than a hot loop.
const bridgeRetry = 2 * time.Second

// eventSpace is the namespace a fact id that is not already a UUID is hashed in.
// The plugin-server refuses anything but a dashed UUID, and the hash is
// deterministic so a redelivered fact keeps its uuid and collapses downstream.
var eventSpace = uuid.MustParse("5b0a8f5e-5c7e-4b8e-9d3a-2f6a1c0e7d41")

// reservedName maps an act kind onto the name insights' lenses key on. A caller's
// own name wins everywhere else.
var reservedName = map[string]string{
	kindPage:     "$pageview",
	kindIdentify: "$identify",
	kindGroup:    "$groupidentify",
}

// renamed maps plane attributes onto the property names insights reads. The
// visitor's agent, country, language and device class are stamped once at ingest
// under the plane's names; insights' lenses read them under these.
var renamed = map[string]string{
	"referrer":        "$referrer",
	"referrer_domain": "$referring_domain",
	"library":         "$lib",
	"library_version": "$lib_version",
	"user_agent":      "$raw_user_agent",
	"country":         "$geoip_country_code",
	"language":        "$browser_language",
	"browser":         "$browser",
	"os":              "$os",
	"device":          "$device_type",
	"internal":        "$internal",
	"bot":             "$bot",
	"test":            "is_test",
}

// signIn names the facts that join a visitor's pre-sign-in id to the account:
// the one moment a browser holds both. Insights merges two ids into one person
// only on an $identify, so each of these that carries both ids is filed after an
// $identify of its own.
var signIn = map[string]bool{
	"login_completed":  true,
	"signup_completed": true,
}

// captured is the message insights' capture produces: the envelope, with the
// event itself as a JSON string in data. The encoding is double because that is
// what the plugin-server parses.
type captured struct {
	UUID       string `json:"uuid"`
	DistinctID string `json:"distinct_id"`
	IP         string `json:"ip"`
	Data       string `json:"data"`
	Now        string `json:"now"`
	Token      string `json:"token"`
	Event      string `json:"event"`
	Timestamp  string `json:"timestamp"`
}

// capturedData is the event carried in captured.Data.
type capturedData struct {
	UUID       string         `json:"uuid"`
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  string         `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

// exceptionEntry matches Insights / Cymbal's $exception_list item contract.
type exceptionEntry struct {
	Type       string               `json:"type"`
	Value      string               `json:"value"`
	Mechanism  exceptionMechanism   `json:"mechanism"`
	Stacktrace *exceptionStacktrace `json:"stacktrace,omitempty"`
}

type exceptionMechanism struct {
	Type      string `json:"type"`
	Handled   bool   `json:"handled"`
	Synthetic bool   `json:"synthetic"`
}

type exceptionStacktrace struct {
	Type   string           `json:"type"`
	Frames []exceptionFrame `json:"frames"`
}

type exceptionFrame struct {
	Filename string `json:"filename,omitempty"`
	Function string `json:"function,omitempty"`
	Lineno   uint32 `json:"lineno,omitempty"`
	Colno    uint32 `json:"colno,omitempty"`
	InApp    bool   `json:"in_app"`
}

// insightsRecords renders ONE act or error fact as the records insights files for
// it: the event, preceded by an $identify when an act fact is a sign-in that carries
// both the visitor's anonymous id and the account's distinct id. The $identify's id
// is derived from the fact's, so a redelivered fact collapses downstream like any
// other.
func insightsRecords(m message, now time.Time) ([]*kgo.Record, error) {
	rec, err := insightsRecord(m, now)
	if err != nil || rec == nil {
		return nil, err
	}
	if m.Signal == string(signalError) || !signIn[m.Name] || m.Anonymous == "" || m.Distinct == "" || m.Anonymous == m.Distinct {
		return []*kgo.Record{rec}, nil
	}
	id := m
	id.ID = m.ID + "/identify"
	id.Kind = kindIdentify
	id.Name = "$identify"
	join, err := insightsRecord(id, now)
	if err != nil || join == nil {
		return []*kgo.Record{rec}, err
	}
	return []*kgo.Record{join, rec}, nil
}

// insightsRecord renders ONE act or error fact as the record insights' capture
// would have produced for it. It returns nil when the fact cannot be filed: no
// project key admitted it, or it names nobody.
//
// Pure — the clock is a parameter — so the whole contract is asserted in a test.
func insightsRecord(m message, now time.Time) (*kgo.Record, error) {
	isError := m.Signal == string(signalError)
	distinct := cmp.Or(m.Distinct, m.Anonymous, m.Person)
	if isError && distinct == "" {
		distinct = m.Session
	}
	if m.Key == "" || distinct == "" {
		return nil, nil
	}
	id := eventUUID(m.Org, m.ID)
	name := cmp.Or(reservedName[m.Kind], m.Name)
	if isError {
		name = "$exception"
	}
	stamp := m.Time.UTC().Format(time.RFC3339Nano)
	at := now.UTC().Format(time.RFC3339Nano)

	props := properties(m, distinct)
	if isError {
		applyErrorProperties(props, m)
	}
	if m.Name != "" && name != m.Name {
		props["$event_name"] = m.Name
	}
	data, err := json.Marshal(capturedData{
		UUID: id, Event: name, DistinctID: distinct, Timestamp: stamp, Properties: props,
	})
	if err != nil {
		return nil, err
	}
	value, err := json.Marshal(captured{
		UUID: id, DistinctID: distinct, IP: m.IP, Data: string(data), Now: at,
		Token: m.Key, Event: name, Timestamp: stamp,
	})
	if err != nil {
		return nil, err
	}
	headers := []kgo.RecordHeader{
		{Key: "token", Value: []byte(m.Key)},
		{Key: "distinct_id", Value: []byte(distinct)},
		{Key: "timestamp", Value: []byte(strconv.FormatInt(m.Time.UnixMilli(), 10))},
		{Key: "event", Value: []byte(name)},
		{Key: "uuid", Value: []byte(id)},
		{Key: "now", Value: []byte(at)},
	}
	if m.Session != "" {
		headers = append(headers, kgo.RecordHeader{Key: "session_id", Value: []byte(m.Session)})
	}
	return &kgo.Record{
		Topic: bridgeTopic,
		// token:distinct_id, capture's own key, so one person's events stay
		// ordered on one partition.
		Key:     []byte(m.Key + ":" + distinct),
		Value:   value,
		Headers: headers,
	}, nil
}

// eventUUID is the fact id when it is a UUID, else a UUID derived from it.
func eventUUID(org, id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return u.String()
	}
	return uuid.NewSHA1(eventSpace, []byte(org+"/"+id)).String()
}

// properties rebuilds the event's properties from the fact: the attributes, under
// the names insights reads, then the columns, which win.
func properties(m message, distinct string) map[string]any {
	props := make(map[string]any, len(m.Attributes)+8)
	for k, v := range m.Attributes {
		if _, ok := renamed[k]; !ok {
			props[k] = attributeValue(v)
		}
	}
	for from, to := range renamed {
		if v, ok := m.Attributes[from]; ok {
			props[to] = v
		}
	}
	set := func(k, v string) {
		if v != "" {
			props[k] = v
		}
	}
	// screen is stamped WxH at ingest; insights reads the two sides apart.
	if w, h, ok := splitScreen(m.Attributes["screen"]); ok {
		props["$screen_width"], props["$screen_height"] = w, h
	}
	delete(props, "screen")
	set("$current_url", m.URL)
	set("$host", hostOf(m.URL))
	set("$pathname", m.Path)
	set("$session_id", m.Session)
	set("product", m.Product)
	if len(m.Groups) > 0 {
		props["$groups"] = m.Groups
	}
	if m.Kind == kindIdentify && m.Anonymous != "" && m.Anonymous != distinct {
		props["$anon_distinct_id"] = m.Anonymous
	}
	heatmap(m.Name, props, m.URL, m.Path)
	return props
}

// applyErrorProperties enriches a $exception event's property bag with Insights
// Error Tracking and AST Journeys fields from a signalError fact.
func applyErrorProperties(props map[string]any, m message) {
	set := func(k, v string) {
		if v != "" {
			props[k] = v
		}
	}

	errClass := cmp.Or(m.Class, m.Attributes["$exception_type"], m.Attributes["error_class"], "Error")
	errMsg := cmp.Or(m.Message, m.Attributes["$exception_message"], m.Attributes["message"])
	fp := cmp.Or(m.Issue, m.Attributes["$exception_fingerprint"], m.Attributes["fingerprint"])
	level := cmp.Or(severityText(m.Severity), m.Attributes["$exception_level"], "error")
	errSource := cmp.Or(m.Attributes["error_source"], m.Kind, defaultErrorSource(m))

	set("$exception_fingerprint", fp)
	if fp != "" {
		if _, ok := props["fingerprint"]; !ok {
			props["fingerprint"] = fp
		}
	}
	props["$exception_type"] = errClass
	props["error_class"] = errClass
	props["$exception_types"] = []string{errClass}
	if errMsg != "" {
		props["$exception_message"] = errMsg
		props["$exception_values"] = []string{errMsg}
		if _, ok := props["message"]; !ok {
			props["message"] = errMsg
		}
	}
	props["$exception_level"] = level
	props["$exception_handled"] = m.Handled
	set("error_source", errSource)

	entry := exceptionEntry{
		Type:  errClass,
		Value: errMsg,
		Mechanism: exceptionMechanism{
			Type:      cmp.Or(m.Attributes["error_source"], "generic"),
			Handled:   m.Handled,
			Synthetic: false,
		},
	}

	if len(m.Frames) > 0 {
		frames := make([]exceptionFrame, 0, len(m.Frames))
		for _, fr := range m.Frames {
			frames = append(frames, exceptionFrame{
				Filename: fr.File,
				Function: fr.Function,
				Lineno:   fr.Line,
				Colno:    fr.Column,
				InApp:    fr.Own,
			})
		}
		entry.Stacktrace = &exceptionStacktrace{Type: "raw", Frames: frames}

		if top, ok := topMessageFrame(m.Frames); ok {
			set("file", top.File)
			set("function", top.Function)
			if top.Line > 0 {
				props["line"] = top.Line
			}
			if top.Column > 0 {
				props["column"] = top.Column
			}
		}
	} else if file := m.Attributes["file"]; file != "" {
		ln, _ := strconv.ParseUint(m.Attributes["line"], 10, 32)
		col, _ := strconv.ParseUint(m.Attributes["column"], 10, 32)
		fn := m.Attributes["function"]
		entry.Stacktrace = &exceptionStacktrace{
			Type: "raw",
			Frames: []exceptionFrame{{
				Filename: file,
				Function: fn,
				Lineno:   uint32(ln),
				Colno:    uint32(col),
				InApp:    ownFile(file),
			}},
		}
		if ln > 0 {
			props["line"] = uint32(ln)
		}
		if col > 0 {
			props["column"] = uint32(col)
		}
	}
	props["$exception_list"] = []exceptionEntry{entry}

	set("hostname", cmp.Or(m.Attributes["hostname"], hostOf(m.URL), m.Origin))
	set("route_template", cmp.Or(m.Attributes["route_template"], m.Path))
	if _, ok := props["component_path"]; !ok && m.El != nil {
		if len(m.El.Path) > 0 {
			set("component_path", strings.Join(m.El.Path, " > "))
		} else {
			set("component_path", m.El.Component)
		}
	}
	if _, ok := props["control_id"]; !ok && m.El != nil {
		set("control_id", cmp.Or(m.El.Testid, m.El.Name, m.El.Label))
	}

	rel := cmp.Or(m.Release, m.Attributes["$release"], m.Attributes["release"])
	set("release", rel)
	set("$release", rel)

	if _, ok := props["$session_id"]; !ok {
		set("$session_id", m.Attributes["session_id"])
	}
	if _, ok := props["$insert_id"]; !ok {
		set("$insert_id", m.ID)
	}
	set("environment", m.Env)
	set("service", m.Service)
	set("trace_id", m.Trace)
	set("span_id", m.Span)
}

func defaultErrorSource(m message) string {
	if m.URL != "" {
		return "window_error"
	}
	if m.Service != "" {
		return "service_error"
	}
	return "runtime_error"
}

// topMessageFrame prefers the first first-party frame, else the first frame,
// matching firstOwn in fact.go.
func topMessageFrame(frames []messageFrame) (messageFrame, bool) {
	for _, fr := range frames {
		if fr.Own {
			return fr, true
		}
	}
	if len(frames) > 0 {
		return frames[0], true
	}
	return messageFrame{}, false
}

// attributeValue undoes asStr for a composite: the plane stores every property as a
// string, so an object or array arrives as its JSON text. $set, $set_once and
// $groups have to reach persons processing as objects again.
func attributeValue(v string) any {
	if s := strings.TrimSpace(v); s != "" && (s[0] == '{' || s[0] == '[') && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return v
}

// bridge files ONE message. A nil return acks it; an error sends it back to the
// stream after bridgeRetry.
func (s *drain) bridge(ctx context.Context, sm *infra.StreamMessage) error {
	m, err := decodeMessage(sm.Data)
	if err != nil {
		// Not a fact, or not one that decodes — the warehouse's durable counts that.
		return nil
	}
	recs, err := insightsRecords(m, time.Now())
	if err != nil {
		s.log.Error("insights bridge: unrenderable fact — dropped", "id", m.ID, "err", err)
		return nil
	}
	for _, rec := range recs {
		if err := produce(ctx, rec); err != nil {
			if rejected(err) {
				s.log.Error("insights bridge: broker refused the event — dropped", "org", m.Org, "id", m.ID, "err", err)
				continue
			}
			s.log.Warn("insights bridge: produce failed — will retry", "org", m.Org, "id", m.ID, "err", err)
			sleep(ctx, bridgeRetry)
			return err
		}
	}
	return nil
}

// rejected reports a broker answer that no retry changes: a non-retriable Kafka
// error, such as a record too large. Timeouts and dial errors are not Kafka
// errors, so they retry.
func rejected(err error) bool {
	var ke *kerr.Error
	return errors.As(err, &ke) && !ke.Retriable
}

// relay binds the bridge's act durable on cl and consumes until ctx ends, rebinding
// after a pause when the bind or the consume fails. It never reports to the
// warehouse's consumers: the bridge failing must not stop a row from landing.
func (s *drain) relay(ctx context.Context, cl *infra.PubSubClient) {
	s.relaySubject(ctx, cl, bridgeDurable, signalAct.subject())
}

// relayErrors binds the bridge's error durable on cl and consumes event.error
// facts onto insights' events_plugin_ingestion topic as $exception events.
func (s *drain) relayErrors(ctx context.Context, cl *infra.PubSubClient) {
	s.relaySubject(ctx, cl, bridgeErrorDurable, signalError.subject())
}

func (s *drain) relaySubject(ctx context.Context, cl *infra.PubSubClient, durable, subject string) {
	for ctx.Err() == nil {
		err := s.bindBridgeFor(ctx, cl, durable, subject)
		if ctx.Err() != nil {
			return
		}
		s.log.Warn("insights bridge: consume ended — rebinding", "durable", durable, "subject", subject, "err", err)
		sleep(ctx, 5*time.Second)
	}
}

func (s *drain) bindBridge(ctx context.Context, cl *infra.PubSubClient) error {
	return s.bindBridgeFor(ctx, cl, bridgeDurable, signalAct.subject())
}

func (s *drain) bindErrorBridge(ctx context.Context, cl *infra.PubSubClient) error {
	return s.bindBridgeFor(ctx, cl, bridgeErrorDurable, signalError.subject())
}

func (s *drain) bindBridgeFor(ctx context.Context, cl *infra.PubSubClient, durable, subject string) error {
	if _, err := cl.CreateConsumer(ctx, EventStream, &infra.ConsumerConfig{
		Name:          durable,
		Durable:       durable,
		Description:   "bridge " + subject + " to insights' " + bridgeTopic,
		FilterSubject: subject,
		DeliverPolicy: infra.DeliverAll,
		AckPolicy:     infra.AckExplicit,
		AckWait:       ackWait,
	}); err != nil {
		return err
	}
	return cl.ConsumeMessages(ctx, EventStream, durable, func(sm *infra.StreamMessage) error {
		return s.bridge(ctx, sm)
	})
}

// splitScreen reads the WxH the ingest stamped as a width and a height.
func splitScreen(v string) (w, h int, ok bool) {
	a, b, found := strings.Cut(v, "x")
	if !found {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	return w, h, err1 == nil && err2 == nil && w > 0 && h > 0
}
