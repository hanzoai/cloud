// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import "time"

// AttrKind discriminates the value stored in an Attr without interface boxing.
type AttrKind uint8

const (
	AttrString AttrKind = iota + 1
	AttrInt64
	AttrFloat64
	AttrBool
)

// Attr is a zero-interface typed span attribute to minimize hot-path heap allocations.
type Attr struct {
	Key   string   `json:"key"`
	Kind  AttrKind `json:"kind"`
	Str   string   `json:"str,omitempty"`
	Int   int64    `json:"int,omitempty"`
	Float float64  `json:"float,omitempty"`
	Bool  bool     `json:"bool,omitempty"`
}

// StringAttr constructs a string Attr.
func StringAttr(key, val string) Attr {
	return Attr{Key: key, Kind: AttrString, Str: val}
}

// Int64Attr constructs an int64 Attr.
func Int64Attr(key string, val int64) Attr {
	return Attr{Key: key, Kind: AttrInt64, Int: val}
}

// Float64Attr constructs a float64 Attr.
func Float64Attr(key string, val float64) Attr {
	return Attr{Key: key, Kind: AttrFloat64, Float: val}
}

// BoolAttr constructs a bool Attr.
func BoolAttr(key string, val bool) Attr {
	return Attr{Key: key, Kind: AttrBool, Bool: val}
}

// SpanKind follows the OTLP Span.SpanKind enumeration.
type SpanKind uint8

const (
	SpanKindUnspecified SpanKind = 0
	SpanKindInternal    SpanKind = 1
	SpanKindServer      SpanKind = 2
	SpanKindClient      SpanKind = 3
	SpanKindProducer    SpanKind = 4
	SpanKindConsumer    SpanKind = 5
)

// StatusCode follows the OTLP Status.StatusCode enumeration.
type StatusCode uint8

const (
	StatusUnset StatusCode = 0
	StatusOK    StatusCode = 1
	StatusError StatusCode = 2
)

// Span represents one immutable completed span emitted under a unified task_id
// and W3C Trace Context.
type Span struct {
	TaskID        string     `json:"taskId"`
	OrgID         string     `json:"orgId"`
	TenantID      string     `json:"tenantId"`
	Subsystem     Subsystem  `json:"subsystem"`
	TraceID       TraceID    `json:"traceId"`
	SpanID        SpanID     `json:"spanId"`
	ParentSpanID  SpanID     `json:"parentSpanId,omitempty"`
	Name          string     `json:"name"`
	Kind          SpanKind   `json:"kind"`
	StartTimeNano int64      `json:"startTimeUnixNano"`
	EndTimeNano   int64      `json:"endTimeUnixNano"`
	Status        StatusCode `json:"status"`
	StatusMessage string     `json:"statusMessage,omitempty"`
	Attributes    []Attr     `json:"attributes"`
	RedactedCount int        `json:"redactedCount,omitempty"`
}

// Duration returns the elapsed wall-clock duration of s.
func (s *Span) Duration() time.Duration {
	if s.EndTimeNano <= s.StartTimeNano {
		return 0
	}
	return time.Duration(s.EndTimeNano - s.StartTimeNano)
}

// LookupAttr returns the last attribute matching key, if present.
func (s *Span) LookupAttr(key string) (Attr, bool) {
	for i := len(s.Attributes) - 1; i >= 0; i-- {
		if s.Attributes[i].Key == key {
			return s.Attributes[i], true
		}
	}
	return Attr{}, false
}

// IntAttr returns the int64 attribute value for key, or (0, false).
func (s *Span) IntAttr(key string) (int64, bool) {
	a, ok := s.LookupAttr(key)
	if !ok || a.Kind != AttrInt64 {
		return 0, false
	}
	return a.Int, true
}

// FloatAttr returns the float64 attribute value for key, or (0, false).
func (s *Span) FloatAttr(key string) (float64, bool) {
	a, ok := s.LookupAttr(key)
	if !ok || a.Kind != AttrFloat64 {
		return 0, false
	}
	return a.Float, true
}

// StrAttr returns the string attribute value for key, or ("", false).
func (s *Span) StrAttr(key string) (string, bool) {
	a, ok := s.LookupAttr(key)
	if !ok || a.Kind != AttrString {
		return "", false
	}
	return a.Str, true
}

// Clone returns a deep copy of s so callers cannot mutate stored spans.
func (s Span) Clone() Span {
	if len(s.Attributes) > 0 {
		attrs := make([]Attr, len(s.Attributes))
		copy(attrs, s.Attributes)
		s.Attributes = attrs
	}
	return s
}

// ActiveSpan is a lightweight handle returned by Tracer.Start* methods and
// finalized via End (or EndWithError). It is owned by a single goroutine.
type ActiveSpan struct {
	tracer *Tracer
	span   Span
	ended  bool
}

// Context returns the TaskContext representing this active span, suitable for
// propagating to child subsystem operations via WithTaskContext or InjectHeaders.
func (a *ActiveSpan) Context() TaskContext {
	return TaskContext{
		TaskID:    a.span.TaskID,
		OrgID:     a.span.OrgID,
		TenantID:  a.span.TenantID,
		TraceID:   a.span.TraceID,
		SpanID:    a.span.SpanID,
		Flags:     TraceFlagSampled,
		Subsystem: a.span.Subsystem,
	}
}

// SetAttr appends a DLP-sanitized attribute to the active span before End().
func (a *ActiveSpan) SetAttr(attr Attr) {
	if a == nil || a.ended {
		return
	}
	mode := CaptureRedact
	if a.tracer != nil {
		mode = a.tracer.captureMode
	}
	clean, red := SanitizeAttr(attr, mode)
	if red {
		a.span.RedactedCount++
	}
	a.span.Attributes = append(a.span.Attributes, clean)
}

// End finalizes the span with StatusOK (if unset) and enqueues it into the
// tracer's bounded ring buffer. Calling End more than once is a safe no-op.
func (a *ActiveSpan) End() {
	a.endAt(0, StatusOK, "")
}

// EndAt finalizes the span with an explicit nanosecond timestamp (useful for
// deterministic replays and benchmarks).
func (a *ActiveSpan) EndAt(endNano int64) {
	a.endAt(endNano, StatusOK, "")
}

// EndWithError finalizes the span with StatusError and a DLP-scrubbed message.
func (a *ActiveSpan) EndWithError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
		if a.tracer == nil || a.tracer.captureMode != CaptureRaw {
			if cleaned, red := ScrubSecrets(msg); red {
				msg = cleaned
				a.span.RedactedCount++
			}
		}
	}
	a.endAt(0, StatusError, msg)
}

func (a *ActiveSpan) endAt(endNano int64, status StatusCode, msg string) {
	if a == nil || a.ended {
		return
	}
	a.ended = true
	if endNano <= 0 {
		endNano = time.Now().UnixNano()
	}
	if endNano < a.span.StartTimeNano {
		endNano = a.span.StartTimeNano
	}
	a.span.EndTimeNano = endNano
	if a.span.Status == StatusUnset {
		a.span.Status = status
	}
	if msg != "" {
		a.span.StatusMessage = msg
	}
	if a.tracer != nil {
		a.tracer.enqueue(a.span)
	}
}
