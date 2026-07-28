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

// The embedded collector's ingest door, in ZAP.
//
// Cloud runs a real otelcol pipeline; this is the receiver that feeds it. It
// replaces otlpreceiver, which mattered for a reason beyond dependency hygiene:
// that receiver bound OTLP gRPC to 0.0.0.0:4317, and 4317 is the canonical ZAP
// port every Hanzo service now sends spans to. An OTLP listener on that port
// receives ZAP frames it cannot parse.
//
// The wire is the one luxfi/trace emits and o11y/pkg/zapreceiver decodes: a JSON
// SpanBatch inside a ZAP envelope. No protobuf, no OTLP, no gRPC.

package o11y

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"

	zaplogreceiver "github.com/hanzoai/o11y/pkg/zaplogreceiver"
	zapreceiver "github.com/hanzoai/o11y/pkg/zapreceiver"
)

// zapReceiverConfig is the config block the pipeline YAML supplies.
type zapReceiverConfig struct {
	// Endpoint is host:port to bind. A scheme has no meaning on this wire.
	Endpoint string `mapstructure:"endpoint"`
}

func newZapReceiverFactory() receiver.Factory {
	return receiver.NewFactory(
		component.MustNewType("zap"),
		func() component.Config { return &zapReceiverConfig{Endpoint: "0.0.0.0:4317"} },
		receiver.WithTraces(createZapTracesReceiver, component.StabilityLevelBeta),
		receiver.WithLogs(createZapLogsReceiver, component.StabilityLevelBeta),
	)
}

func createZapTracesReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	next consumer.Traces,
) (receiver.Traces, error) {
	c, ok := cfg.(*zapReceiverConfig)
	if !ok {
		return nil, fmt.Errorf("zap receiver: unexpected config type %T", cfg)
	}
	return &zapTracesReceiver{cfg: c, next: next, set: set}, nil
}

type zapTracesReceiver struct {
	cfg  *zapReceiverConfig
	next consumer.Traces
	set  receiver.Settings
	rcv  *zapreceiver.Receiver
}

func (r *zapTracesReceiver) Start(_ context.Context, _ component.Host) error {
	rcv, err := zapreceiver.New(zapreceiver.Config{
		Listen: r.cfg.Endpoint,
		NodeID: "cloud-zap-ingest",
		OnBatch: func(ctx context.Context, b *zapreceiver.SpanBatch) error {
			return r.next.ConsumeTraces(ctx, batchToTraces(b))
		},
	})
	if err != nil {
		return fmt.Errorf("zap receiver: start on %s: %w", r.cfg.Endpoint, err)
	}
	r.rcv = rcv
	return nil
}

func (r *zapTracesReceiver) Shutdown(context.Context) error {
	if r.rcv != nil {
		r.rcv.Stop()
	}
	return nil
}

// batchToTraces converts the wire shape into pdata.
//
// One batch is one service, so it yields one ResourceSpans. Scope is not on the
// wire — the SpanBatch carries app name and resource attributes, not
// instrumentation scope — so spans land in a single unnamed ScopeSpans rather
// than inventing a scope that the emitter never sent.
func batchToTraces(b *zapreceiver.SpanBatch) ptrace.Traces {
	td := ptrace.NewTraces()
	if b == nil || len(b.Spans) == 0 {
		return td
	}
	rs := td.ResourceSpans().AppendEmpty()
	attrs := rs.Resource().Attributes()
	if b.AppName != "" {
		attrs.PutStr("service.name", b.AppName)
	}
	if b.Version != "" {
		attrs.PutStr("service.version", b.Version)
	}
	for k, v := range b.Resource {
		attrs.PutStr(k, v)
	}

	ss := rs.ScopeSpans().AppendEmpty()
	for _, s := range b.Spans {
		dst := ss.Spans().AppendEmpty()
		if id, err := hex.DecodeString(s.TraceID); err == nil && len(id) == 16 {
			dst.SetTraceID(pcommon.TraceID(id))
		}
		if id, err := hex.DecodeString(s.SpanID); err == nil && len(id) == 8 {
			dst.SetSpanID(pcommon.SpanID(id))
		}
		if s.ParentSpanID != "" {
			if id, err := hex.DecodeString(s.ParentSpanID); err == nil && len(id) == 8 {
				dst.SetParentSpanID(pcommon.SpanID(id))
			}
		}
		dst.SetName(s.Name)
		dst.SetKind(kindFromString(s.Kind))
		dst.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, s.StartUnixNs)))
		dst.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, s.EndUnixNs)))
		putAnyAttrs(dst.Attributes(), s.Attributes)

		switch s.StatusCode {
		case "ok", "Ok", "OK":
			dst.Status().SetCode(ptrace.StatusCodeOk)
		case "error", "Error", "ERROR":
			dst.Status().SetCode(ptrace.StatusCodeError)
		default:
			dst.Status().SetCode(ptrace.StatusCodeUnset)
		}
		dst.Status().SetMessage(s.StatusMsg)

		for _, e := range s.Events {
			ev := dst.Events().AppendEmpty()
			ev.SetName(e.Name)
			ev.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, e.TimeUnixNs)))
			putAnyAttrs(ev.Attributes(), e.Attributes)
		}
	}
	return td
}

func kindFromString(k string) ptrace.SpanKind {
	switch k {
	case "server", "SPAN_KIND_SERVER":
		return ptrace.SpanKindServer
	case "client", "SPAN_KIND_CLIENT":
		return ptrace.SpanKindClient
	case "producer", "SPAN_KIND_PRODUCER":
		return ptrace.SpanKindProducer
	case "consumer", "SPAN_KIND_CONSUMER":
		return ptrace.SpanKindConsumer
	case "internal", "SPAN_KIND_INTERNAL":
		return ptrace.SpanKindInternal
	default:
		return ptrace.SpanKindUnspecified
	}
}

// putAnyAttrs maps the JSON-decoded attribute map. JSON has one number type, so
// an integer arrives as float64; it is written as an int when it is one, which
// keeps span attributes like http.status_code queryable as numbers rather than
// as 502.0.
func putAnyAttrs(dst pcommon.Map, attrs map[string]any) {
	if len(attrs) == 0 {
		return
	}
	dst.EnsureCapacity(len(attrs))
	for k, v := range attrs {
		switch t := v.(type) {
		case string:
			dst.PutStr(k, t)
		case bool:
			dst.PutBool(k, t)
		case float64:
			if t == float64(int64(t)) {
				dst.PutInt(k, int64(t))
			} else {
				dst.PutDouble(k, t)
			}
		case nil:
			dst.PutEmpty(k)
		default:
			dst.PutStr(k, fmt.Sprint(t))
		}
	}
}

func createZapLogsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	next consumer.Logs,
) (receiver.Logs, error) {
	c, ok := cfg.(*zapReceiverConfig)
	if !ok {
		return nil, fmt.Errorf("zap receiver: unexpected config type %T", cfg)
	}
	return &zapLogsReceiver{cfg: c, next: next, set: set}, nil
}

type zapLogsReceiver struct {
	cfg  *zapReceiverConfig
	next consumer.Logs
	set  receiver.Settings
	rcv  *zaplogreceiver.Receiver
}

func (r *zapLogsReceiver) Start(_ context.Context, _ component.Host) error {
	rcv, err := zaplogreceiver.New(zaplogreceiver.Config{
		Listen: r.cfg.Endpoint,
		NodeID: "cloud-zap-log-ingest",
		OnBatch: func(ctx context.Context, b *zaplogreceiver.LogBatch) error {
			return r.next.ConsumeLogs(ctx, logBatchToLogs(b))
		},
	})
	if err != nil {
		return fmt.Errorf("zap log receiver: start on %s: %w", r.cfg.Endpoint, err)
	}
	r.rcv = rcv
	return nil
}

func (r *zapLogsReceiver) Shutdown(context.Context) error {
	if r.rcv != nil {
		r.rcv.Stop()
	}
	return nil
}

// logBatchToLogs converts the wire shape into pdata.
//
// Severity arrives as the OTel numeric level. The text is carried separately
// because an emitter may use its own vocabulary ("WARN" vs "Warning"), and
// re-deriving it from the number would silently rewrite what the service said.
func logBatchToLogs(b *zaplogreceiver.LogBatch) plog.Logs {
	ld := plog.NewLogs()
	if b == nil || len(b.Records) == 0 {
		return ld
	}
	rl := ld.ResourceLogs().AppendEmpty()
	attrs := rl.Resource().Attributes()
	if b.AppName != "" {
		attrs.PutStr("service.name", b.AppName)
	}
	if b.Version != "" {
		attrs.PutStr("service.version", b.Version)
	}
	for k, v := range b.Resource {
		attrs.PutStr(k, v)
	}

	sl := rl.ScopeLogs().AppendEmpty()
	for _, r := range b.Records {
		dst := sl.LogRecords().AppendEmpty()
		dst.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, r.TimeUnixNs)))
		if r.ObservedTimeUnixNs != 0 {
			dst.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, r.ObservedTimeUnixNs)))
		}
		dst.SetSeverityNumber(plog.SeverityNumber(r.Severity))
		dst.SetSeverityText(r.SeverityText)
		dst.Body().SetStr(r.Body)
		dst.SetEventName(r.EventName)
		if id, err := hex.DecodeString(r.TraceID); err == nil && len(id) == 16 {
			dst.SetTraceID(pcommon.TraceID(id))
		}
		if id, err := hex.DecodeString(r.SpanID); err == nil && len(id) == 8 {
			dst.SetSpanID(pcommon.SpanID(id))
		}
		putAnyAttrs(dst.Attributes(), r.Attributes)
	}
	return ld
}
