package plane

import (
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/zap-proto/zip"
)

// The plane's wire is generated, and these are the three things that keeps it
// honest: the bytes did not move, the offsets are the ONE derivation's, and the
// type says so at compile time.
//
// The wire matters here more than anywhere else in the fleet. A rolling deploy
// has one pod encoding by reflection and one by generated code, and they must
// read one message — so a codec that is merely self-consistent is a codec that
// works in every test and answers nothing in production.

// wired is every type that has declared its wire. It is spelled out rather than
// discovered, because the point of each assertion below is to hold a SPECIFIC
// contract, and a list built from the same generated file it is checking would
// shrink quietly the day the generator stopped emitting one.
//
// The values are typed nils, which is deliberate: the compile-time assertion is
// what this list is for, and a nil is also the input the round trip below has to
// survive — a handler returning a nil *Out reaches the encoder as one.
var wired = []zip.Wire{
	(*StartIn)(nil),
	(*Started)(nil),
}

// TestTheGeneratedCodecKeepsTheWire is the one that would stop a bad deploy.
//
// The goldens were produced by the REFLECTIVE encoder, before it learned to
// prefer a type's own methods — so they are what every peer on the old build
// writes and expects to read. They are hex rather than a round trip on purpose:
// a round trip only proves the codec agrees with itself.
func TestTheGeneratedCodecKeepsTheWire(t *testing.T) {
	for _, c := range []struct {
		name string
		v    zip.Wire
		want string
	}{
		{"StartIn", &StartIn{App: "commerce"},
			"5a4150000100000010000000200000000800000008000000636f6d6d65726365"},
		{"Started", &Started{Addr: "/run/hanzo/commerce.sock", Known: true},
			"5a415000010000001000000038000000100000001800000001000000000000002f72756e2f68616e7a6f2f636f6d6d657263652e736f636b"},
		{"Started zero", &Started{},
			"5a41500001000000100000002000000000000000000000000000000000000000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.v.MarshalZAP()
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != c.want {
				t.Fatalf("the wire moved:\n got %s\nwant %s", hex.EncodeToString(got), c.want)
			}
		})
	}
}

// TestTheConstantsAreTheOneDerivation closes the loop the generator opens.
//
// The generator reads the `zap:` tag a human wrote and emits it as a constant;
// this asserts that constant is what zip actually encodes at. So the tag in the
// declaration, the offset in the generated code, the ordinal a .zap schema states
// and the byte the encoder writes are one fact, checked rather than agreed.
func TestTheConstantsAreTheOneDerivation(t *testing.T) {
	for _, c := range []struct {
		v    any
		want map[string]int
		size int
	}{
		{StartIn{}, map[string]int{"App": startInAppOff}, startInSize},
		{Started{}, map[string]int{"Addr": startedAddrOff, "Known": startedKnownOff}, startedSize},
	} {
		typ := reflect.TypeOf(c.v)
		t.Run(typ.Name(), func(t *testing.T) {
			shape, err := zip.LayoutOf(typ)
			if err != nil {
				t.Fatal(err)
			}
			if shape.Size != c.size {
				t.Errorf("size: generated %d, derived %d", c.size, shape.Size)
			}
			if len(shape.Slots) != len(c.want) {
				t.Fatalf("slots: derived %d, generated %d", len(shape.Slots), len(c.want))
			}
			for _, s := range shape.Slots {
				if got, ok := c.want[s.Name]; !ok || got != s.Offset {
					t.Errorf("%s: generated offset %d, derived %d", s.Name, got, s.Offset)
				}
			}
		})
	}
}

// TestATaggedTypeOwnsItsWire is the compile-time half: delete the generated file,
// or drop a tag so the generator stops emitting for a type, and this stops
// building rather than quietly returning to the reflective path.
func TestATaggedTypeOwnsItsWire(t *testing.T) {
	for _, w := range wired {
		typ := reflect.TypeOf(w).Elem()
		// A nil answers as an absent body rather than panicking, which is what the
		// reflective encoder does and what op.invoke hands it for a void reply.
		if raw, err := w.MarshalZAP(); err != nil || raw != nil {
			t.Errorf("%s: a nil encoded as %v, %v; want nil, nil", typ, raw, err)
		}
		v := reflect.New(typ).Interface().(zip.Wire)
		raw, err := v.MarshalZAP()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if err := reflect.New(typ).Interface().(zip.Wire).UnmarshalZAP(raw); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
}

// TestAReplyDetachesItself is why plane.Ask no longer walks a generated reply by
// reflection: the codec clones as it reads, so a decoded string does not point
// into the connection's read buffer.
//
// The check is aliasing, not equality — a view and a copy carry the same
// characters, and only one of them changes when the buffer is reused.
func TestAReplyDetachesItself(t *testing.T) {
	raw, err := (&Started{Addr: "/run/hanzo/commerce.sock", Known: true}).MarshalZAP()
	if err != nil {
		t.Fatal(err)
	}
	var out Started
	if err := out.UnmarshalZAP(raw); err != nil {
		t.Fatal(err)
	}
	// The comparison is against a LITERAL, not against a second variable holding
	// out.Addr. Assigning a string copies its HEADER, so `want := out.Addr` would
	// alias the same bytes and both sides would move together — an assertion that
	// passes whether or not the codec copies. That is the bug this test had.
	const want = "/run/hanzo/commerce.sock"
	if out.Addr != want {
		t.Fatalf("decoded %q, want %q", out.Addr, want)
	}
	// What the transport does between calls: the frame is pooled and overwritten.
	for i := range raw {
		raw[i] = 'x'
	}
	if out.Addr != want {
		t.Fatalf("the reply pointed into the frame: it became %q", out.Addr)
	}
}

func BenchmarkStartedMarshal(b *testing.B) {
	v := &Started{Addr: "/run/hanzo/commerce.sock", Known: true}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := v.MarshalZAP(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStartedUnmarshal(b *testing.B) {
	raw, err := (&Started{Addr: "/run/hanzo/commerce.sock", Known: true}).MarshalZAP()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var out Started
		if err := out.UnmarshalZAP(raw); err != nil {
			b.Fatal(err)
		}
	}
}
