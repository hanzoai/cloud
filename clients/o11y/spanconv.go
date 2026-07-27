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

// SDK spans -> collector pdata, directly.
//
// The write end of the in-process sink is dstraces.ConsumeTraces, which takes
// ptrace.Traces. Reaching it used to mean SDK spans -> otlptrace.Exporter ->
// OTLP proto -> proto.Marshal -> ptrace.UnmarshalTraces, so the path advertised
// as Cost-0 in fact serialized and deserialized every batch in memory, and
// dragged go.opentelemetry.io/proto/otlp and otlptrace into the binary to do it.
//
// This converts once, in place. No proto, no marshal, no OTLP.

package o11y

import (
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// spansToTraces builds one ptrace.Traces from a batch of finished SDK spans.
//
// Spans are grouped by resource and then by instrumentation scope, the same
// nesting pdata expects, so a batch from one service produces one ResourceSpans
// rather than one per span.
func spansToTraces(spans []sdktrace.ReadOnlySpan) ptrace.Traces {
	td := ptrace.NewTraces()
	if len(spans) == 0 {
		return td
	}

	// One ResourceSpans per distinct resource, one ScopeSpans per distinct scope
	// inside it — the nesting pdata expects, so a batch from one service yields
	// one ResourceSpans rather than one per span. Spans from a single provider
	// share a resource pointer, so identity comparison is both correct and cheap;
	// a batch is small enough that the linear scan beats hashing.
	type scopeKey struct{ name, version string }
	type bucket struct {
		res    *sdkresource.Resource
		rs     ptrace.ResourceSpans
		scopes map[scopeKey]ptrace.ScopeSpans
	}
	buckets := make([]*bucket, 0, 2)

	for _, s := range spans {
		res := s.Resource()
		var b *bucket
		for _, cand := range buckets {
			if cand.res == res || (cand.res != nil && res != nil && cand.res.Equal(res)) {
				b = cand
				break
			}
		}
		if b == nil {
			rs := td.ResourceSpans().AppendEmpty()
			if res != nil {
				putAttrs(rs.Resource().Attributes(), res.Attributes())
			}
			b = &bucket{res: res, rs: rs, scopes: map[scopeKey]ptrace.ScopeSpans{}}
			buckets = append(buckets, b)
		}

		sc := s.InstrumentationScope()
		key := scopeKey{sc.Name, sc.Version}
		ss, ok := b.scopes[key]
		if !ok {
			ss = b.rs.ScopeSpans().AppendEmpty()
			ss.Scope().SetName(sc.Name)
			ss.Scope().SetVersion(sc.Version)
			b.scopes[key] = ss
		}
		fillSpan(ss.Spans().AppendEmpty(), s)
	}
	return td
}

func fillSpan(dst ptrace.Span, s sdktrace.ReadOnlySpan) {
	sc := s.SpanContext()
	dst.SetTraceID(pcommon.TraceID(sc.TraceID()))
	dst.SetSpanID(pcommon.SpanID(sc.SpanID()))
	if p := s.Parent(); p.HasSpanID() {
		dst.SetParentSpanID(pcommon.SpanID(p.SpanID()))
	}
	dst.SetName(s.Name())
	dst.SetKind(spanKind(s.SpanKind()))
	dst.SetStartTimestamp(pcommon.NewTimestampFromTime(s.StartTime()))
	dst.SetEndTimestamp(pcommon.NewTimestampFromTime(s.EndTime()))
	dst.SetDroppedAttributesCount(uint32(s.DroppedAttributes()))
	dst.SetDroppedEventsCount(uint32(s.DroppedEvents()))
	dst.SetDroppedLinksCount(uint32(s.DroppedLinks()))
	putAttrs(dst.Attributes(), s.Attributes())

	switch s.Status().Code {
	case codes.Ok:
		dst.Status().SetCode(ptrace.StatusCodeOk)
	case codes.Error:
		dst.Status().SetCode(ptrace.StatusCodeError)
	default:
		dst.Status().SetCode(ptrace.StatusCodeUnset)
	}
	dst.Status().SetMessage(s.Status().Description)

	for _, e := range s.Events() {
		ev := dst.Events().AppendEmpty()
		ev.SetName(e.Name)
		ev.SetTimestamp(pcommon.NewTimestampFromTime(e.Time))
		ev.SetDroppedAttributesCount(uint32(e.DroppedAttributeCount))
		putAttrs(ev.Attributes(), e.Attributes)
	}
	for _, l := range s.Links() {
		ln := dst.Links().AppendEmpty()
		ln.SetTraceID(pcommon.TraceID(l.SpanContext.TraceID()))
		ln.SetSpanID(pcommon.SpanID(l.SpanContext.SpanID()))
		ln.SetDroppedAttributesCount(uint32(l.DroppedAttributeCount))
		putAttrs(ln.Attributes(), l.Attributes)
	}
}

func spanKind(k trace.SpanKind) ptrace.SpanKind {
	switch k {
	case trace.SpanKindInternal:
		return ptrace.SpanKindInternal
	case trace.SpanKindServer:
		return ptrace.SpanKindServer
	case trace.SpanKindClient:
		return ptrace.SpanKindClient
	case trace.SpanKindProducer:
		return ptrace.SpanKindProducer
	case trace.SpanKindConsumer:
		return ptrace.SpanKindConsumer
	default:
		return ptrace.SpanKindUnspecified
	}
}

func putAttrs(dst pcommon.Map, attrs []attribute.KeyValue) {
	dst.EnsureCapacity(len(attrs))
	for _, kv := range attrs {
		putValue(dst.PutEmpty(string(kv.Key)), kv.Value)
	}
}

func putValue(dst pcommon.Value, v attribute.Value) {
	switch v.Type() {
	case attribute.BOOL:
		dst.SetBool(v.AsBool())
	case attribute.INT64:
		dst.SetInt(v.AsInt64())
	case attribute.FLOAT64:
		dst.SetDouble(v.AsFloat64())
	case attribute.STRING:
		dst.SetStr(v.AsString())
	case attribute.BOOLSLICE:
		s := dst.SetEmptySlice()
		for _, b := range v.AsBoolSlice() {
			s.AppendEmpty().SetBool(b)
		}
	case attribute.INT64SLICE:
		s := dst.SetEmptySlice()
		for _, i := range v.AsInt64Slice() {
			s.AppendEmpty().SetInt(i)
		}
	case attribute.FLOAT64SLICE:
		s := dst.SetEmptySlice()
		for _, f := range v.AsFloat64Slice() {
			s.AppendEmpty().SetDouble(f)
		}
	case attribute.STRINGSLICE:
		s := dst.SetEmptySlice()
		for _, str := range v.AsStringSlice() {
			s.AppendEmpty().SetStr(str)
		}
	default:
		dst.SetStr(v.Emit())
	}
}
