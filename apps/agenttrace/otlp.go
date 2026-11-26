// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"sync"
)

// OTLPTraceRequest models the OpenTelemetry Protocol (OTLP/JSON)
// ExportTraceServiceRequest schema (opentelemetry/proto/trace/v1/trace.proto).
type OTLPTraceRequest struct {
	ResourceSpans []OTLPResourceSpans `json:"resourceSpans"`
}

// OTLPResourceSpans groups spans sharing an OTLP Resource.
type OTLPResourceSpans struct {
	Resource   OTLPResource     `json:"resource"`
	ScopeSpans []OTLPScopeSpans `json:"scopeSpans"`
}

// OTLPResource carries resource-level attributes (service.name, service.version).
type OTLPResource struct {
	Attributes []OTLPKeyValue `json:"attributes"`
}

// OTLPScopeSpans groups spans emitted by one InstrumentationScope.
type OTLPScopeSpans struct {
	Scope OTLPScope  `json:"scope"`
	Spans []OTLPSpan `json:"spans"`
}

// OTLPScope identifies the instrumentation library.
type OTLPScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// OTLPSpan represents a single OTLP/JSON span record.
type OTLPSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []OTLPKeyValue `json:"attributes"`
	Status            OTLPStatus     `json:"status"`
}

// OTLPStatus represents an OTLP/JSON span status.
type OTLPStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

// OTLPKeyValue represents an OTLP/JSON attribute entry.
type OTLPKeyValue struct {
	Key   string       `json:"key"`
	Value OTLPAnyValue `json:"value"`
}

// OTLPAnyValue follows OTLP/JSON AnyValue encoding (intValue is stringified int64).
type OTLPAnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

var otlpBufPool = sync.Pool{
	New: func() any {
		b := bytes.NewBuffer(make([]byte, 0, 16*1024))
		return b
	},
}

// EncodeOTLPJSON encodes a slice of spans into a standard OTLP/JSON
// ExportTraceServiceRequest payload using a pooled streaming byte builder to
// avoid reflection and map allocations on the export path.
func EncodeOTLPJSON(spans []Span, serviceName string) []byte {
	if serviceName == "" {
		serviceName = ServiceName
	}
	buf := otlpBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer otlpBufPool.Put(buf)

	appendOTLPJSON(buf, spans, serviceName)
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

// AppendOTLPJSON appends a standard OTLP/JSON ExportTraceServiceRequest into dst.
func AppendOTLPJSON(dst *bytes.Buffer, spans []Span, serviceName string) {
	if serviceName == "" {
		serviceName = ServiceName
	}
	appendOTLPJSON(dst, spans, serviceName)
}

func appendOTLPJSON(b *bytes.Buffer, spans []Span, serviceName string) {
	b.WriteString(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":`)
	writeJSONString(b, serviceName)
	b.WriteString(`}},{"key":"service.version","value":{"stringValue":`)
	writeJSONString(b, ServiceVersion)
	b.WriteString(`}}]},"scopeSpans":[{"scope":{"name":`)
	writeJSONString(b, ScopeName)
	b.WriteString(`,"version":`)
	writeJSONString(b, ScopeVersion)
	b.WriteString(`},"spans":[`)

	var hex32 [32]byte
	var hex16 [16]byte
	var numBuf [32]byte

	for i := range spans {
		if i > 0 {
			b.WriteByte(',')
		}
		sp := &spans[i]
		b.WriteString(`{"traceId":"`)
		hex.Encode(hex32[:], sp.TraceID[:])
		b.Write(hex32[:])
		b.WriteString(`","spanId":"`)
		hex.Encode(hex16[:], sp.SpanID[:])
		b.Write(hex16[:])
		b.WriteByte('"')
		if !sp.ParentSpanID.IsZero() {
			b.WriteString(`,"parentSpanId":"`)
			hex.Encode(hex16[:], sp.ParentSpanID[:])
			b.Write(hex16[:])
			b.WriteByte('"')
		}
		b.WriteString(`,"name":`)
		writeJSONString(b, sp.Name)
		b.WriteString(`,"kind":`)
		b.Write(strconv.AppendInt(numBuf[:0], int64(sp.Kind), 10))
		b.WriteString(`,"startTimeUnixNano":"`)
		b.Write(strconv.AppendInt(numBuf[:0], sp.StartTimeNano, 10))
		b.WriteString(`","endTimeUnixNano":"`)
		b.Write(strconv.AppendInt(numBuf[:0], sp.EndTimeNano, 10))
		b.WriteString(`","attributes":[`)

		for j := range sp.Attributes {
			if j > 0 {
				b.WriteByte(',')
			}
			a := &sp.Attributes[j]
			b.WriteString(`{"key":`)
			writeJSONString(b, a.Key)
			b.WriteString(`,"value":{`)
			switch a.Kind {
			case AttrString:
				b.WriteString(`"stringValue":`)
				writeJSONString(b, a.Str)
			case AttrInt64:
				b.WriteString(`"intValue":"`)
				b.Write(strconv.AppendInt(numBuf[:0], a.Int, 10))
				b.WriteByte('"')
			case AttrFloat64:
				b.WriteString(`"doubleValue":`)
				f := a.Float
				if math.IsNaN(f) || math.IsInf(f, 0) {
					f = 0
				}
				b.Write(strconv.AppendFloat(numBuf[:0], f, 'g', -1, 64))
			case AttrBool:
				b.WriteString(`"boolValue":`)
				if a.Bool {
					b.WriteString("true")
				} else {
					b.WriteString("false")
				}
			default:
				b.WriteString(`"stringValue":""`)
			}
			b.WriteString(`}}`)
		}
		b.WriteString(`],"status":{"code":`)
		b.Write(strconv.AppendInt(numBuf[:0], int64(sp.Status), 10))
		if sp.StatusMessage != "" {
			b.WriteString(`,"message":`)
			writeJSONString(b, sp.StatusMessage)
		}
		b.WriteString(`}}`)
	}
	b.WriteString(`]}]}]}`)
}

// DecodeOTLPJSON parses and validates an OTLP/JSON payload into OTLPTraceRequest.
func DecodeOTLPJSON(raw []byte) (OTLPTraceRequest, error) {
	var req OTLPTraceRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return OTLPTraceRequest{}, err
	}
	return req, nil
}

// writeJSONString appends a valid RFC 8259 JSON-escaped string literal to b.
func writeJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
			continue
		}
		if start < i {
			b.WriteString(s[start:i])
		}
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '<':
			b.WriteString(`\u003c`)
		case '>':
			b.WriteString(`\u003e`)
		case '&':
			b.WriteString(`\u0026`)
		default:
			b.WriteString(`\u00`)
			const hexDigits = "0123456789abcdef"
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
		start = i + 1
	}
	if start < len(s) {
		b.WriteString(s[start:])
	}
	b.WriteByte('"')
}
