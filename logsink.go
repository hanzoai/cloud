// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// The LOG leg of the telemetry plane — the third signal, on the same road as
// the other two.
//
// Every process in this estate already writes structured JSON: one logger
// (luxfi/log), one install point (BuildDeps), one shape per line — level,
// module, method, path, trace, span, status, duration_ms, message. All of it
// went to a file descriptor and stopped there. Spans reached event.span and
// measurements reached the store, so a request could be found two ways out of
// three; the third — the line that says what happened, in words, carrying the
// same trace id — was reachable only by someone holding a shell on the pod that
// produced it.
//
// This is the missing hop, and it is a WRITER rather than a call: the process
// logger's output becomes stderr AND this sink (luxlog.MultiLevelWriter), so
// every package logging through the process default reaches the plane with no
// edit of its own. The lines already print `trace` and `span`, so a line and
// its span are joinable the moment they land.
//
// TWO PARTS, INSTALLED AT DIFFERENT TIMES, AND THAT SPLIT IS THE DESIGN.
//
// The WRITER installs in BuildDeps, beside the logger it wraps, because a
// default set late is a default that did not apply to whatever logged before it.
// The TRANSPORT installs in InstallTelemetry, beside the span exporter, because
// that is where this process's telemetry identity — its service name, its
// resource — is resolved, and two answers to "what service is this" is the drift
// that makes a line unjoinable to the span beside it. Between the two the sink
// HOLDS what it parses and flushes on attach, so the boot window — the most
// valuable lines a process ever writes — is carried rather than dropped.
//
// A WRITE NEVER FAILS AND NEVER BLOCKS. Write always reports the full length and
// a nil error: luxlog's multi-writer turns a short write into io.ErrShortWrite
// for the caller, and a telemetry leg that can fail a log call is a telemetry leg
// that can take the process down. Emit hands the record to the SDK's batch
// processor, which enqueues and returns; the dial, the encode and the send all
// happen on that processor's own goroutine.
//
// THE SINK'S OWN LOGGER GOES STRAIGHT TO STDERR. It has to: a sink reporting its
// trouble through the default logger writes a line that arrives back at itself,
// and each arrival is another line. One line per failure is a diagnostic; a line
// that reproduces is an outage.
package cloud

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
)

// planeLogEndpoint is the plane ingest's ZAP LOG wire on the pod's loopback —
// the address apps/o11y/planesink.go binds (planeLogListen, 0.0.0.0:4318).
//
// It is NAMED BY THE CALLER and never defaulted to, which is why installLogSink
// takes an address rather than reaching for one: luxfi/log's ZAP exporter defaults to
// 127.0.0.1:4317, which is the SPAN wire — a log batch sent there meets a
// receiver that decodes span envelopes, and the whole leg reads as configured
// while delivering nothing. Two ports because two receivers.
const planeLogEndpoint = "127.0.0.1:4318"

// holdMax bounds what the sink keeps while it has no transport. The window is
// BuildDeps to InstallTelemetry — stores opening, subsystems mounting — which is
// hundreds of lines, not thousands. Past the bound the NEWEST are dropped and
// counted, because in a boot window the first lines are the ones that say why
// the rest happened.
const holdMax = 2048

// planeLog is this process's log leg. One per process, like the logger it wraps:
// the writer installs once (BuildDeps), the transport attaches once
// (InstallTelemetry).
var planeLog = &logSink{}

// logSink parses luxlog's JSON lines and emits them as OTel log records.
//
// It is an io.Writer because that is the seam luxfi/log already has. The
// alternative — a hook, or a second call at every log site — would be a second
// way to log, and there are 127 packages on the first one.
type logSink struct {
	mu     sync.Mutex
	out    otellog.Logger // nil until attach
	held   []line
	lost   int
	closed bool
}

// line is a parsed record with the context its trace and span ids live in. The
// SDK reads those two off the CONTEXT and never off the record, so they travel
// together or they do not travel.
type line struct {
	ctx context.Context
	rec otellog.Record
}

// Write parses one log line and carries it to the plane. It reports the full
// length and no error unconditionally; see the file header.
func (s *logSink) Write(p []byte) (int, error) {
	if l, ok := decode(p); ok {
		s.emit(l)
	}
	return len(p), nil
}

// emit sends the line if a transport is attached, holds it while one is still
// coming, and drops it once the sink is closed.
func (s *logSink) emit(l line) {
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
	case s.out != nil:
		out := s.out
		s.mu.Unlock()
		out.Emit(l.ctx, l.rec)
	case len(s.held) < holdMax:
		s.held = append(s.held, l)
		s.mu.Unlock()
	default:
		s.lost++
		s.mu.Unlock()
	}
}

// attach installs the transport and flushes the boot window through it.
func (s *logSink) attach(out otellog.Logger) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	held, lost := s.held, s.lost
	s.out, s.held, s.lost = out, nil, 0
	s.mu.Unlock()

	for _, l := range held {
		out.Emit(l.ctx, l.rec)
	}
	if lost > 0 {
		sinkLog().Warn("log lines dropped before the plane transport attached", "lines", lost)
	}
}

// close stops the sink: nothing further is held, emitted or reported. Called
// when the deployment has no plane to send to, and again at shutdown, so a
// process with no destination never carries a boot window it cannot spend.
func (s *logSink) close() {
	s.mu.Lock()
	s.closed, s.out, s.held = true, nil, nil
	s.mu.Unlock()
}

// sinkLog is the sink's own voice, on stderr and only stderr. See the file
// header: routing it through the default logger is the amplification loop.
func sinkLog() luxlog.Logger {
	return luxlog.New("cloud").Output(os.Stderr).New("subsystem", "logsink")
}

// decode turns one luxlog JSON line into a record and its context. A line that
// is not this process's own JSON — a subprocess's raw stderr shares the
// descriptor — is not this leg's to carry, and reporting each one would be the
// amplification loop wearing a different hat.
func decode(p []byte) (line, bool) {
	var l line
	text := strings.TrimSpace(string(p))
	if !strings.HasPrefix(text, "{") {
		return l, false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return l, false
	}

	at := time.Now()
	if s, ok := fields[luxlog.TimestampFieldName].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			at = t
		}
	}
	l.rec.SetTimestamp(at)
	l.rec.SetObservedTimestamp(time.Now())
	if s, ok := fields[luxlog.MessageFieldName].(string); ok {
		l.rec.SetBody(otellog.StringValue(s))
	}
	if s, ok := fields[luxlog.LevelFieldName].(string); ok {
		l.rec.SetSeverityText(s)
		l.rec.SetSeverity(severity(s))
	}

	// trace and span leave the attributes and become the record's own ids: the
	// plane writes them to dedicated columns (apps/o11y/planesink.go, logRowsOf)
	// and keeping a second copy in the attribute map is the same fact twice, on
	// every row, forever.
	traceID, _ := fields[traceField].(string)
	spanID, _ := fields[spanField].(string)
	l.ctx = spanned(traceID, spanID)

	attrs := make([]otellog.KeyValue, 0, len(fields))
	for k, v := range fields {
		switch k {
		case luxlog.TimestampFieldName, luxlog.MessageFieldName, luxlog.LevelFieldName, traceField, spanField:
			continue
		}
		attrs = append(attrs, otellog.KeyValue{Key: k, Value: value(v)})
	}
	l.rec.AddAttributes(attrs...)
	return l, true
}

// The two correlation fields the framework prints on every request line
// (zip/telemetry.go). They are the join between a line and its span, which is
// the reason this leg is worth building at all.
const (
	traceField = "trace"
	spanField  = "span"
)

// spanned rebuilds the context the SDK reads a record's ids out of. Emit takes
// them from the context, never from the record, so a line that PRINTS a trace id
// lands with none unless it is put back here.
func spanned(traceID, spanID string) context.Context {
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return context.Background()
	}
	sid, _ := trace.SpanIDFromHex(spanID)
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled,
	}))
}

// severity maps luxlog's level word onto the OTel scale, which is what every
// level filter in the console compares against — a record carrying only the word
// is unfilterable there.
func severity(level string) otellog.Severity {
	switch level {
	case luxlog.LevelTraceValue:
		return otellog.SeverityTrace
	case luxlog.LevelDebugValue:
		return otellog.SeverityDebug
	case luxlog.LevelInfoValue:
		return otellog.SeverityInfo
	case luxlog.LevelWarnValue:
		return otellog.SeverityWarn
	case luxlog.LevelErrorValue:
		return otellog.SeverityError
	case luxlog.LevelFatalValue:
		return otellog.SeverityFatal
	case luxlog.LevelPanicValue:
		return otellog.SeverityFatal2
	default:
		return otellog.SeverityUndefined
	}
}

// value renders one JSON field as a typed OTel value. Numbers stay numbers —
// status and duration_ms are what anyone aggregates on, and a "503" string
// cannot be compared to 500 by any query the console can write.
func value(v any) otellog.Value {
	switch t := v.(type) {
	case string:
		return otellog.StringValue(t)
	case bool:
		return otellog.BoolValue(t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return otellog.Int64Value(i)
		}
		if f, err := t.Float64(); err == nil {
			return otellog.Float64Value(f)
		}
		return otellog.StringValue(t.String())
	case nil:
		return otellog.StringValue("")
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return otellog.StringValue("")
		}
		return otellog.StringValue(string(b))
	}
}

// installLogSink builds this process's log transport and attaches it, returning
// the flush-and-stop. Called ONLY from InstallTelemetry, which is where the
// service name and resource a batch is filed under are resolved.
//
// The batch's resource IS the process resource — the same value the spans carry,
// so a line and a span agree about what produced them, and service.name reaches
// the plane's reader where it looks for it first (apps/o11y/planesink.go,
// planeService).
//
// service is the TRANSPORT'S IDENTITY, and it carries this process's pid because
// ZAP admits one connection per node id while this binary re-execs itself as
// ~110 sibling processes that all read the same service name. The losers of that
// race drop their batches silently. Same collision, same fix, as the span wire
// above it: one string names the connection, another names the subject, and they
// were only ever equal by accident.
func installLogSink(log luxlog.Logger, res *resource.Resource, service, endpoint string) func(context.Context) {
	if PlaneDSN() == "" {
		log.Info("logs stay on stderr: this deployment runs no telemetry plane to carry them",
			"hint", "O11Y_DATASTORE_DSN is the same fact that binds the plane's log ear")
		planeLog.close()
		return func(context.Context) {}
	}

	attrs := make(map[string]string, len(res.Attributes()))
	for _, kv := range res.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	exp, err := luxlog.NewZAPExporter(luxlog.ZAPExporterConfig{
		Endpoint: endpoint,
		AppName:  service + "-" + strconv.Itoa(os.Getpid()),
		Resource: attrs,
	})
	if err != nil {
		log.Warn("log wire unavailable; logs stay on stderr", "endpoint", endpoint, "err", err)
		planeLog.close()
		return func(context.Context) {}
	}

	// meterName is this package's OTel scope — one name for every signal this
	// process emits, so a reader that groups by scope sees one producer.
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	planeLog.attach(lp.Logger(meterName))
	log.Info("log wire installed", "endpoint", endpoint, "service", service)

	return func(ctx context.Context) {
		planeLog.close()
		if err := lp.Shutdown(ctx); err != nil {
			sinkLog().Warn("log provider shutdown", "err", err)
		}
	}
}
