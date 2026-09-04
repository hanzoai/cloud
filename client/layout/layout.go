// Package layout is where a plane type's ZAP layout is computed, once, at BUILD
// time.
//
// A field IS its offset on this wire, so "which offset does each field take, and
// how wide is the object" is the whole schema. That question has exactly one
// answer and it must be answered in exactly one place: the .zap schema generated
// from these types, the Go codec generated from them, and the reflective encoder
// they still fall back to have to agree byte for byte, or a rolling deploy has two
// pods reading one message differently.
//
// So this package is the answer, and everything downstream READS it:
//
//	the `zap:"N"` tag       the declaration a human sees and a reviewer checks
//	cloud.zap               emitted from these offsets as explicit @N ordinals
//	client/zap_gen.go        emitted from these offsets as Go constants
//
// It takes a [types.Struct] rather than a [reflect.Type] on purpose. go/types is
// the compile-time type system: it needs a loaded package to exist at all, so
// this package CANNOT be reached from a serving binary — the build-time boundary
// is structural rather than a convention someone has to keep.
//
// # The rule
//
// A field is aligned to its own width and takes that many bytes, which is the
// rule zap's own schema builder applies and the one the reflective encoder
// already ships. The object's size is rounded up to 8.
//
// Variable-length values — text, bytes, a list, a nested object — take an 8-byte
// slot holding an offset and a length; the value itself lives after the fixed
// area. A fixed-size array is the exception and the reason this package states
// alignment separately from width: bytes_fixed[32] is 32 bytes written INLINE and
// aligned to 1, so an id sits immediately after the u32 before it rather than at
// the next 32-byte boundary. That is what zapgen's own schema does
// (`BlockchainID id32 @4`), and it is the one shape the reflective encoder cannot
// express at all.
package layout

import (
	"fmt"
	"go/types"
	"strconv"
	"strings"
)

// Kind is what a field is on the wire. It is the .zap scalar set and nothing
// else: there is no map, no interface and no free-form value, because a field
// that cannot state its own width cannot have an offset.
type Kind uint8

const (
	Bool Kind = iota
	Int8
	Int16
	Int32
	Int64
	Uint8
	Uint16
	Uint32
	Uint64
	Float32
	Float64
	Text
	Bytes
	Fixed  // bytes_fixed[N] — inline, N wide
	Struct // a complete ZAP message carried as bytes
	List
)

// Field is one slot: where it sits, how wide it is, and what it holds.
type Field struct {
	Name   string // the Go field name
	Index  int    // its position in the Go struct, for a generator that walks fields
	Offset int    // where the slot begins
	Kind   Kind
	N      int        // Fixed: the array length. List: 0. Otherwise unused.
	Ptr    bool       // the Go field is a pointer to the encoded type
	Type   types.Type // the Go type, minus any pointer
	Elem   *Field     // List: how one element is encoded
	Tagged bool       // the field carried a zap: tag
	Stated int        // the offset that tag stated
}

// Width is how many bytes the field's slot takes.
func (f Field) Width() int { return width(f.Kind, f.N) }

// Layout is a whole message: its slots, in declaration order, and its size.
type Layout struct {
	Fields []Field
	Size   int
}

// Of derives the layout of a struct type.
//
// A field carrying a `zap:"N"` tag is CHECKED against the derived offset and the
// mismatch is refused, naming both — the tag is what a reviewer reads and it may
// not be able to lie. An untagged field is derived silently, which is what lets a
// type be converted one at a time.
func Of(s *types.Struct) (Layout, error) {
	var lay Layout
	off := 0
	for i := range s.NumFields() {
		v := s.Field(i)
		if !v.Exported() {
			// An unexported field cannot be read by any encoder, so it was never
			// part of the contract — and it must not take a slot either, or a peer
			// that cannot see it reads every later field at the wrong offset.
			continue
		}
		f, err := fieldOf(v.Type())
		if err != nil {
			return Layout{}, fmt.Errorf("%s: %w", v.Name(), err)
		}
		f.Name, f.Index = v.Name(), i
		w := f.Width()
		off = align(off, alignment(f.Kind))
		f.Offset = off
		off += w

		if tag, ok := lookup(s.Tag(i), "zap"); ok {
			stated, err := offsetIn(tag)
			if err != nil {
				return Layout{}, fmt.Errorf("%s: %w", v.Name(), err)
			}
			f.Tagged, f.Stated = true, stated
			if stated != f.Offset {
				return Layout{}, fmt.Errorf(
					"%s: zap tag says offset %d, the layout puts it at %d — "+
						"a field that moved is a wire change for every peer, so fix the tag "+
						"only if the move was intended", v.Name(), stated, f.Offset)
			}
		}
		lay.Fields = append(lay.Fields, f)
	}
	lay.Size = align(off, 8)
	return lay, nil
}

// Tagged reports whether every slot in the layout carries its tag. It is the
// generator's opt-in: a type says "my wire is declared, generate my codec" by
// tagging its fields, and one untagged field means the type is not ready.
func (l Layout) Tagged() bool {
	if len(l.Fields) == 0 {
		return false
	}
	for _, f := range l.Fields {
		if !f.Tagged {
			return false
		}
	}
	return true
}

func fieldOf(t types.Type) (Field, error) {
	f := Field{Type: t}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		f.Ptr = true
		t = p.Elem()
		f.Type = t
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		k, err := basic(u)
		if err != nil {
			return f, err
		}
		f.Kind = k
	case *types.Array:
		// bytes_fixed[N]. Only a byte array: an array of anything else has no
		// inline wire form, and a list is how a sequence crosses.
		e, ok := u.Elem().Underlying().(*types.Basic)
		if !ok || e.Kind() != types.Byte {
			return f, fmt.Errorf("an array of %s has no wire form; use a list", u.Elem())
		}
		f.Kind, f.N = Fixed, int(u.Len())
	case *types.Slice:
		if e, ok := u.Elem().Underlying().(*types.Basic); ok && e.Kind() == types.Byte {
			f.Kind = Bytes
			break
		}
		f.Kind = List
		ef, err := fieldOf(u.Elem())
		if err != nil {
			return f, err
		}
		if ef.Kind == List {
			return f, fmt.Errorf("a list of lists has no wire form; wrap the inner one in a struct")
		}
		f.Elem = &ef
	case *types.Struct:
		f.Kind = Struct
	default:
		return f, fmt.Errorf("a %s cannot cross the plane; give it a type that can", t.Underlying())
	}
	return f, nil
}

func basic(b *types.Basic) (Kind, error) {
	switch b.Kind() {
	case types.Bool:
		return Bool, nil
	case types.Int8:
		return Int8, nil
	case types.Int16:
		return Int16, nil
	case types.Int32:
		return Int32, nil
	case types.Int, types.Int64:
		return Int64, nil
	case types.Uint8:
		return Uint8, nil
	case types.Uint16:
		return Uint16, nil
	case types.Uint32:
		return Uint32, nil
	case types.Uint, types.Uint64:
		return Uint64, nil
	case types.Float32:
		return Float32, nil
	case types.Float64:
		return Float64, nil
	case types.String:
		return Text, nil
	}
	return 0, fmt.Errorf("a %s cannot cross the plane; give it a type that can", b)
}

// width is the fixed-area size of one slot. Everything variable — text, bytes, a
// list, a nested object — carries an offset and a length in 8 bytes.
func width(k Kind, n int) int {
	switch k {
	case Bool, Int8, Uint8:
		return 1
	case Int16, Uint16:
		return 2
	case Int32, Uint32, Float32:
		return 4
	case Fixed:
		return n
	}
	return 8
}

// alignment is where a slot may begin. It is the slot's own width, except for
// bytes_fixed: those bytes are written inline and align to 1, so a 32-byte id
// follows a u32 at offset 4 rather than at 32.
func alignment(k Kind) int {
	if k == Fixed {
		return 1
	}
	return width(k, 0)
}

func align(off, n int) int { return (off + n - 1) &^ (n - 1) }

// offsetIn reads the slot out of a `zap:` tag.
//
// The grammar is the .zap file's own: `@N` is the byte offset, which is what
// zapgen writes and what a reviewer compares against the schema. A tag may carry
// other parts (a wire type the Go type cannot say, like bytes_fixed[32]) and
// those are the schema's business, not this one's.
//
// `zap:"-"` is REFUSED rather than honoured. A schema that drops the field would
// still be describing a wire where the encoder gives it a slot, so the two would
// disagree about every field after it — and a field that silently does not cross
// is the exact failure this plane's codec exists to make impossible.
func offsetIn(tag string) (int, error) {
	for part := range strings.SplitSeq(tag, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "-":
			return 0, fmt.Errorf(`zap:"-" does not remove a field from this wire: ` +
				"the encoder still gives it a slot, so dropping it from the schema " +
				"moves every field after it. Delete the field, or let it cross")
		case strings.HasPrefix(part, "@"):
			n, err := strconv.Atoi(part[1:])
			if err != nil {
				return 0, fmt.Errorf("zap tag %q is not an offset", part)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf(`zap tag %q states no slot; write it as @N, the byte offset`, tag)
}

// lookup reads one key out of a struct tag.
//
// It is reflect.StructTag.Get's rule, spelled here rather than reached for,
// because go/types hands the tag over as the raw string and reaching for
// reflect.StructTag to read it would put the runtime type system back in a
// package whose whole point is that it cannot be reached from one.
func lookup(tag, key string) (string, bool) {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		k := tag[:i]
		tag = tag[i+1:]

		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		quoted := tag[:i+1]
		tag = tag[i+1:]

		if k == key {
			v, err := strconv.Unquote(quoted)
			if err != nil {
				return "", false
			}
			return v, true
		}
	}
	return "", false
}
