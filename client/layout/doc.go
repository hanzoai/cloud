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
//	sdk/plane/zap_gen.go        emitted from these offsets as Go constants
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
