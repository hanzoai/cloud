// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	randv2 "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// HeaderTraceparent is the canonical W3C Trace Context traceparent header.
	HeaderTraceparent = "Traceparent"
	// HeaderTracestate is the canonical W3C Trace Context tracestate header.
	HeaderTracestate = "Tracestate"
	// HeaderTaskID carries the explicit Hanzo unified task identifier across hops.
	HeaderTaskID = "X-Hanzo-Task-Id"

	// TraceFlagSampled is the W3C trace-flags sampled bit (0x01).
	TraceFlagSampled byte = 0x01

	maxTracestateLen = 512
	maxIDLen         = 128
)

// Context validation errors.
var (
	ErrInvalidTraceparent = errors.New("agenttrace: invalid W3C traceparent header")
	ErrInvalidTracestate  = errors.New("agenttrace: invalid W3C tracestate header")
	ErrInvalidTaskID      = errors.New("agenttrace: invalid task_id")
	ErrInvalidOrgID       = errors.New("agenttrace: invalid org_id")
)

// TraceID is a 128-bit W3C trace identifier (16 bytes, 32 lowercase hex chars).
type TraceID [16]byte

// SpanID is a 64-bit W3C span identifier (8 bytes, 16 lowercase hex chars).
type SpanID [8]byte

// IsZero reports whether the TraceID is all zeros (invalid per W3C spec).
func (t TraceID) IsZero() bool {
	return t == (TraceID{})
}

// String formats TraceID as 32 lowercase hexadecimal characters.
func (t TraceID) String() string {
	var buf [32]byte
	hex.Encode(buf[:], t[:])
	return string(buf[:])
}

// IsZero reports whether the SpanID is all zeros (invalid per W3C spec).
func (s SpanID) IsZero() bool {
	return s == (SpanID{})
}

// String formats SpanID as 16 lowercase hexadecimal characters.
func (s SpanID) String() string {
	var buf [16]byte
	hex.Encode(buf[:], s[:])
	return string(buf[:])
}

// ParseTraceID parses a 32-character lowercase hex W3C TraceID.
func ParseTraceID(s string) (TraceID, error) {
	var id TraceID
	if len(s) != 32 || !isLowerHex(s) {
		return id, fmt.Errorf("%w: invalid trace_id", ErrInvalidTraceparent)
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return TraceID{}, fmt.Errorf("%w: %v", ErrInvalidTraceparent, err)
	}
	if id.IsZero() {
		return TraceID{}, fmt.Errorf("%w: all-zero trace_id", ErrInvalidTraceparent)
	}
	return id, nil
}

// ParseSpanID parses a 16-character lowercase hex W3C SpanID.
func ParseSpanID(s string) (SpanID, error) {
	var id SpanID
	if len(s) != 16 || !isLowerHex(s) {
		return id, fmt.Errorf("%w: invalid span_id", ErrInvalidTraceparent)
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return SpanID{}, fmt.Errorf("%w: %v", ErrInvalidTraceparent, err)
	}
	if id.IsZero() {
		return SpanID{}, fmt.Errorf("%w: all-zero span_id", ErrInvalidTraceparent)
	}
	return id, nil
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// TaskContext carries the unified W3C Trace Context and Hanzo execution identity
// across all six subsystems under one task_id.
type TaskContext struct {
	TaskID    string
	OrgID     string
	TenantID  string
	TraceID   TraceID
	SpanID    SpanID
	Flags     byte
	Subsystem Subsystem
}

// Valid reports whether tc carries a valid non-zero W3C trace/span pair and
// non-empty task_id and org_id.
func (tc TaskContext) Valid() bool {
	return tc.TaskID != "" && tc.OrgID != "" && !tc.TraceID.IsZero() && !tc.SpanID.IsZero()
}

// Sampled reports whether the W3C sampled flag is set.
func (tc TaskContext) Sampled() bool {
	return (tc.Flags & TraceFlagSampled) != 0
}

// Traceparent encodes tc into a W3C Trace Context Level 1 traceparent string:
// "00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>".
func (tc TaskContext) Traceparent() string {
	var buf [55]byte
	buf[0] = '0'
	buf[1] = '0'
	buf[2] = '-'
	hex.Encode(buf[3:35], tc.TraceID[:])
	buf[35] = '-'
	hex.Encode(buf[36:52], tc.SpanID[:])
	buf[52] = '-'
	hex.Encode(buf[53:55], []byte{tc.Flags})
	return string(buf[:])
}

// Tracestate encodes Hanzo correlation metadata into a W3C tracestate vendor entry:
// "hanzo=tid:<task_id>;org:<org_id>;ten:<tenant_id>;sub:<subsystem>".
func (tc TaskContext) Tracestate() string {
	var b strings.Builder
	b.Grow(96)
	b.WriteString("hanzo=tid:")
	b.WriteString(sanitizeTracestateVal(tc.TaskID))
	if tc.OrgID != "" {
		b.WriteString(";org:")
		b.WriteString(sanitizeTracestateVal(tc.OrgID))
	}
	if tc.TenantID != "" && tc.TenantID != tc.OrgID {
		b.WriteString(";ten:")
		b.WriteString(sanitizeTracestateVal(tc.TenantID))
	}
	if tc.Subsystem != "" {
		b.WriteString(";sub:")
		b.WriteString(sanitizeTracestateVal(string(tc.Subsystem)))
	}
	return b.String()
}

func sanitizeTracestateVal(v string) string {
	if v == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v) && b.Len() < maxIDLen; i++ {
		c := v[i]
		// Allow safe printable ASCII excluding W3C tracestate delimiters ',', '=', ';', and control chars.
		if c >= 0x21 && c <= 0x7e && c != ',' && c != '=' && c != ';' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ParseTraceparent parses a W3C traceparent header strictly per W3C Trace Context v1.
func ParseTraceparent(header string) (TraceID, SpanID, byte, error) {
	header = strings.TrimSpace(header)
	// Standard v00 length is exactly 55 bytes: 2 + 1 + 32 + 1 + 16 + 1 + 2.
	if len(header) != 55 || header[2] != '-' || header[35] != '-' || header[52] != '-' {
		return TraceID{}, SpanID{}, 0, ErrInvalidTraceparent
	}
	ver := header[0:2]
	if !isLowerHex(ver) || ver == "ff" || ver != "00" {
		return TraceID{}, SpanID{}, 0, fmt.Errorf("%w: unsupported version %q", ErrInvalidTraceparent, ver)
	}
	tid, err := ParseTraceID(header[3:35])
	if err != nil {
		return TraceID{}, SpanID{}, 0, err
	}
	sid, err := ParseSpanID(header[36:52])
	if err != nil {
		return TraceID{}, SpanID{}, 0, err
	}
	flagHex := header[53:55]
	if !isLowerHex(flagHex) {
		return TraceID{}, SpanID{}, 0, fmt.Errorf("%w: invalid flags", ErrInvalidTraceparent)
	}
	var fb [1]byte
	if _, err := hex.Decode(fb[:], []byte(flagHex)); err != nil {
		return TraceID{}, SpanID{}, 0, ErrInvalidTraceparent
	}
	return tid, sid, fb[0], nil
}

// ParseTracestate extracts Hanzo correlation fields from a W3C tracestate header.
func ParseTracestate(header string) (taskID, orgID, tenantID string, sub Subsystem, err error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", "", "", "", nil
	}
	if len(header) > maxTracestateLen {
		return "", "", "", "", ErrInvalidTracestate
	}
	for i := 0; i < len(header); i++ {
		if header[i] < 0x20 || header[i] == 0x7f {
			return "", "", "", "", ErrInvalidTracestate
		}
	}
	for _, member := range strings.Split(header, ",") {
		member = strings.TrimSpace(member)
		if !strings.HasPrefix(member, "hanzo=") {
			continue
		}
		val := strings.TrimPrefix(member, "hanzo=")
		for _, kv := range strings.Split(val, ";") {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			switch k {
			case "tid":
				taskID = v
			case "org":
				orgID = v
			case "ten":
				tenantID = v
			case "sub":
				if s := Subsystem(v); s.Valid() {
					sub = s
				}
			}
		}
		break
	}
	return taskID, orgID, tenantID, sub, nil
}

// InjectHeaders writes W3C traceparent, tracestate, and X-Hanzo-Task-Id headers
// into h for outbound cross-subsystem propagation.
func InjectHeaders(h http.Header, tc TaskContext) {
	if h == nil || tc.TraceID.IsZero() || tc.SpanID.IsZero() {
		return
	}
	h.Set(HeaderTraceparent, tc.Traceparent())
	if ts := tc.Tracestate(); ts != "" {
		h.Set(HeaderTracestate, ts)
	}
	if tc.TaskID != "" {
		h.Set(HeaderTaskID, sanitizeTracestateVal(tc.TaskID))
	}
}

// ExtractHeaders parses W3C traceparent/tracestate and X-Hanzo-Task-Id from h.
// Note: authenticated org_id MUST still be enforced by the caller's identity
// boundary; any org in tracestate is treated as correlation metadata only.
func ExtractHeaders(h http.Header) (TaskContext, error) {
	if h == nil {
		return TaskContext{}, ErrInvalidTraceparent
	}
	tp := h.Get(HeaderTraceparent)
	tid, sid, flags, err := ParseTraceparent(tp)
	if err != nil {
		return TaskContext{}, err
	}
	taskID, orgID, tenantID, sub, err := ParseTracestate(h.Get(HeaderTracestate))
	if err != nil {
		return TaskContext{}, err
	}
	if hdrTask := strings.TrimSpace(h.Get(HeaderTaskID)); hdrTask != "" {
		if taskID == "" {
			taskID = sanitizeTracestateVal(hdrTask)
		} else if taskID != hdrTask {
			return TaskContext{}, fmt.Errorf("%w: tracestate task_id %q disagrees with header %q", ErrInvalidTaskID, taskID, hdrTask)
		}
	}
	if tenantID == "" {
		tenantID = orgID
	}
	return TaskContext{
		TaskID:    taskID,
		OrgID:     orgID,
		TenantID:  tenantID,
		TraceID:   tid,
		SpanID:    sid,
		Flags:     flags,
		Subsystem: sub,
	}, nil
}

type taskCtxKey struct{}

// WithTaskContext attaches tc to ctx so downstream subsystem calls automatically
// inherit task_id, org_id, tenant_id, trace_id, and parent span_id.
func WithTaskContext(ctx context.Context, tc TaskContext) context.Context {
	return context.WithValue(ctx, taskCtxKey{}, tc)
}

// TaskContextFrom extracts the active TaskContext from ctx.
func TaskContextFrom(ctx context.Context) (TaskContext, bool) {
	if ctx == nil {
		return TaskContext{}, false
	}
	tc, ok := ctx.Value(taskCtxKey{}).(TaskContext)
	return tc, ok && !tc.TraceID.IsZero() && !tc.SpanID.IsZero()
}

// ValidateIdentifier ensures task_id/org_id/tenant_id are non-empty, bounded,
// and free of control characters or header-injection bytes.
func ValidateIdentifier(s string) bool {
	if len(s) == 0 || len(s) > maxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || c == ',' || c == '=' || c == ';' {
			return false
		}
	}
	return true
}

// idGenerator generates non-zero 128-bit TraceIDs and 64-bit SpanIDs with minimal
// hot-path contention. When deterministicSeed != 0, IDs are reproducible for
// benchmarks and golden tests; otherwise each pooled ChaCha8 instance is seeded
// from crypto/rand.
type idGenerator struct {
	deterministic bool
	seq           atomic.Uint64
	salt          uint64
	pool          sync.Pool
}

func newIDGenerator(deterministicSeed uint64) *idGenerator {
	g := &idGenerator{
		deterministic: deterministicSeed != 0,
		salt:          deterministicSeed,
	}
	if !g.deterministic {
		g.pool.New = func() any {
			var seed [32]byte
			_, _ = cryptorand.Read(seed[:])
			return randv2.NewChaCha8(seed)
		}
	}
	return g
}

func (g *idGenerator) nextTraceID() TraceID {
	var tid TraceID
	if g.deterministic {
		n1 := splitMix64(g.salt ^ g.seq.Add(1))
		n2 := splitMix64((g.salt + 0x9e3779b97f4a7c15) ^ g.seq.Add(1))
		binary.BigEndian.PutUint64(tid[0:8], n1)
		binary.BigEndian.PutUint64(tid[8:16], n2)
		if tid.IsZero() {
			tid[15] = 1
		}
		return tid
	}
	rng := g.pool.Get().(*randv2.ChaCha8)
	binary.BigEndian.PutUint64(tid[0:8], rng.Uint64())
	binary.BigEndian.PutUint64(tid[8:16], rng.Uint64())
	g.pool.Put(rng)
	if tid.IsZero() {
		tid[15] = 1
	}
	return tid
}

func (g *idGenerator) nextSpanID() SpanID {
	var sid SpanID
	if g.deterministic {
		n := splitMix64((g.salt ^ 0xbf58476d1ce4e5b9) + g.seq.Add(1))
		if n == 0 {
			n = 1
		}
		binary.BigEndian.PutUint64(sid[:], n)
		return sid
	}
	rng := g.pool.Get().(*randv2.ChaCha8)
	n := rng.Uint64()
	g.pool.Put(rng)
	if n == 0 {
		n = 1
	}
	binary.BigEndian.PutUint64(sid[:], n)
	return sid
}

func splitMix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
