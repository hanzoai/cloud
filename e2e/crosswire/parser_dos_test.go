package crosswire

import (
	"encoding/binary"
	"runtime"
	"testing"

	amqpwire "github.com/hanzoai/amqp/wire"
)

// The field-table parser (github.com/hanzoai/amqp/wire) recurses on nested
// tables ('F') and arrays ('A') with NO explicit depth guard: field() -> the
// 'A' case -> field(), and Table() -> field() -> Table(). A hostile client can
// hand the broker an arbitrarily deep nesting inside any table it publishes —
// the content-header Headers table on every basic.publish, or a method's
// arguments table — and each level is one more Go stack frame. This is the
// classic recursion-DoS shape, so the question the blue author raised was not
// "does the parser recurse" (it does) but "is it EXPLOITABLE at the frame-max
// the broker actually runs with." This probe answers it by construction rather
// than by assertion, against the REAL parser.
//
// The bound is the frame-max. wire.Reader refuses a frame whose payload exceeds
// the negotiated frame-max, and the broker's default (and its clamp ceiling) is
// 128 KiB (protocol.DefaultFrameMax). A table therefore rides in at most 128 KiB
// of octets, and the CHEAPEST recursion vector is a nested array: 'A' + a 4-byte
// length is 5 octets per level, so the deepest nesting a single default-frame
// can carry is ~128 KiB / 5 ~= 26 000 levels. That is the worst case, and it is
// what this test parses.

const defaultFrameMax = 128 << 10 // protocol.DefaultFrameMax

// nestedArrayField builds `depth` levels of nested AMQP field-table arrays,
// each holding exactly the next as its single element. 5 octets per level:
// the type byte 'A' plus a 4-byte big-endian body length. Built in one forward
// pass (O(n)), so the builder itself is not the cost being measured.
func nestedArrayField(depth int) []byte {
	buf := make([]byte, 5*depth)
	for i := 0; i < depth; i++ {
		off := 5 * i
		buf[off] = 'A'
		// bytes remaining after this level's own 5-octet header
		binary.BigEndian.PutUint32(buf[off+1:off+5], uint32(5*(depth-1-i)))
	}
	return buf
}

// headersProps wraps one field value as a basic-class content-header property
// section with the Headers table present (presence flag 0x2000) — the exact
// bytes wire.DecodeProps parses on every published message that carries headers.
func headersProps(field []byte) []byte {
	body := append([]byte{0x00}, field...) // one entry: empty short-string key + field
	out := []byte{0x20, 0x00}              // flags: flagHeaders, no more-flags bit
	out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
	return append(out, body...)
}

// realizedArrayDepth walks the parsed Headers to confirm the nesting actually
// materialised (so a lazy/short parse cannot masquerade as success).
func realizedArrayDepth(p amqpwire.Props) int {
	var v any = p.Headers
	depth := 0
	for {
		switch t := v.(type) {
		case amqpwire.Table:
			for _, e := range t { // the single entry
				v = e
			}
			if len(t) == 0 {
				return depth
			}
		case []any:
			depth++
			if len(t) == 0 {
				return depth
			}
			v = t[0]
		default:
			return depth
		}
	}
}

// TestFieldTableRecursionIsBoundedByFrameMax proves the recursion is NOT a crash
// vector at the broker's default frame-max: the deepest nesting a 128 KiB frame
// can carry parses cleanly, in a few MB of transient heap, and returns. The
// parser has no depth guard, but the frame-max cap keeps depth ~4 orders of
// magnitude below Go's 1 GB goroutine-stack ceiling — so a single frame cannot
// overflow the stack, and the finding is "bounded, not exploitable at the
// default (or any near-default) frame-max."
func TestFieldTableRecursionIsBoundedByFrameMax(t *testing.T) {
	// Deepest nesting that still fits a single default-frame payload.
	depth := (defaultFrameMax - 8) / 5 // leave room for flags + table size + key
	field := nestedArrayField(depth)
	props := headersProps(field)
	if len(props) > defaultFrameMax {
		t.Fatalf("probe built %d octets, over the %d frame-max it is meant to model", len(props), defaultFrameMax)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p, err := amqpwire.DecodeProps(props) // the real parser, no recover(): a stack overflow here is fatal
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("the real parser rejected a %d-deep table at frame-max: %v", depth, err)
	}
	got := realizedArrayDepth(p)
	if got != depth {
		t.Fatalf("realized depth %d, want %d — the parse did not fully nest", got, depth)
	}
	t.Logf("MEASURED: a %d-deep field-table array (%d octets, the deepest a %d-octet frame can carry) parsed with no crash, "+
		"realized depth %d, ~%d KiB transient heap. The parser has NO depth guard, but wire.Reader caps a frame at frame-max, "+
		"so depth is bounded to ~frameMax/5 and stays far below Go's 1 GB stack ceiling. NOT exploitable at the default frame-max.",
		depth, len(props), defaultFrameMax, got, (int64(after.HeapAlloc)-int64(before.HeapAlloc))/1024)
}

// TestFieldTableRecursionSurvivesBeyondFrameMax records the latent edge: even a
// depth an operator could only reach by RAISING frame-max into the MB range
// (200k levels ~ a 1 MB frame) still parses without overflowing the stack, so
// the recursion is not a practical crash vector at any realistic frame-max. It
// documents the residual — the guard belongs in the parser, not in cloud — while
// pinning that today's bound is comfortable.
func TestFieldTableRecursionSurvivesBeyondFrameMax(t *testing.T) {
	const depth = 200_000 // ~1 MB frame: only reachable if FrameMax is raised ~8x
	p, err := amqpwire.DecodeProps(headersProps(nestedArrayField(depth)))
	if err != nil {
		t.Fatalf("parser rejected a %d-deep table: %v", depth, err)
	}
	if got := realizedArrayDepth(p); got != depth {
		t.Fatalf("realized depth %d, want %d", got, depth)
	}
	t.Logf("MEASURED: %d-deep (10x the default-frame ceiling) still parsed without stack overflow; Go's growable "+
		"goroutine stack absorbs it. A depth guard in github.com/hanzoai/amqp/wire would close the class permanently.", depth)
}
