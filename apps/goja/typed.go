package goja

// typed.go is the TYPED-PLANE KIT for a bundle-backed app: the four pieces every
// app that relays a goja bundle needs in order to declare typed ops over it, kept
// in ONE place because there is now more than one such app.
//
// A bundle-backed route is a relay — the bundle decides the answer, the Go host
// carries it — and two properties of a relay have to survive before an op can
// replace one:
//
//   - WHAT IT ANSWERS. A bundle authors its own refusal envelopes, which zip's
//     error path ({status,code,error}) would overwrite. BundleErr carries the
//     bundle's status and BYTES as a Go error, and Envelope writes them back
//     verbatim. The op still declares its In and Out, so the document, the MCP
//     tool, the CLI command and the SDK method all exist; what it declines to do
//     is invent a SECOND vocabulary for failures the bundle already has words for.
//
//   - WHAT IT ACCEPTS. A bundle validates with COERCING helpers — a numeric
//     string where a number is meant, an id read straight out of the body — so a
//     Go float64 or *string field would refuse input the route accepts today and
//     make it accept LESS. Scalar carries the caller's token to the bundle byte
//     for byte, so the bundle stays the ONLY validator.
//
// SizedIn is the third piece: a typed op never sees the request, so the body cap
// the untyped relay applied is recorded where the bytes are — the input's own
// UnmarshalJSON — and read back after the tenant is resolved, which keeps a 403
// ahead of a 413 for the caller that has both problems.
//
// It lives in apps/goja because that is where the bundle seam already lives
// (BaseHost, BaseRequest, Dispatch): these are the types an app needs to speak to
// a bundle in the typed plane, next to the ones it needs in the untyped one.
// Each app still owns its own dispatch tail — the service, the logger and the
// route names are the app's — and its own message extractor, because the sentence
// inside a refusal envelope is the bundle's own shape.

import (
	"encoding/json"
	"errors"

	"github.com/zap-proto/zip"
)

// Scalar is ONE JSON value carried from the caller to the bundle unchanged — the
// token exactly as it arrived, quotes and all: `"Acme"`, `123`, `null`, and a
// composite token too, which is how a lenient array field is carried per element
// as []Scalar.
//
// It exists because a bundle's validators are LENIENT in a way no Go type is. A
// helper that calls String(v) stores `{"taxId":12345}` as "12345" today, and a
// *string field would refuse it with a 400 — making the route accept LESS. A
// helper that REFUSES a non-string answers in the bundle's own envelope, which a
// Go field refusing it first would replace with zip's. Carrying the token
// verbatim keeps both: the bundle sees what the caller sent and stays the only
// judge of it.
//
// A Go string is the carrier because it is a string KIND, so every projection
// describes the field as `string` — which is what these fields are. The leniency
// is not in the schema; it is named in each field's own prose.
//
// The zero value means ABSENT — no key was on the wire — which is why a field of
// this type is `omitempty` and never a pointer. A pointer would collapse the
// distinction a partial update depends on: encoding/json sets a pointer field to
// nil for an explicit `null` WITHOUT calling UnmarshalJSON, so `{"city":null}`
// and `{}` would arrive identically. A non-pointer Scalar records `null` as the
// four bytes `null`, and an empty JSON string as the two bytes `""`, so neither
// can be confused with absent.
type Scalar string

// UnmarshalJSON keeps the caller's bytes. It cannot fail: whatever the caller
// sent for this field is the bundle's to judge, so nothing is rejected here.
func (s *Scalar) UnmarshalJSON(b []byte) error {
	*s = Scalar(b)
	return nil
}

// MarshalJSON writes the carried token back exactly as it arrived. A value that
// did NOT come off the wire — a hand-built op input, a URL param bound by zip's
// setScalar — is not a JSON token but the string it spells, so it is quoted.
// That keeps the type total: every Scalar marshals to valid JSON.
func (s Scalar) MarshalJSON() ([]byte, error) {
	if json.Valid([]byte(s)) {
		return []byte(s), nil
	}
	return json.Marshal(string(s))
}

// ScalarList is a lenient ARRAY field: each element is carried verbatim, so a
// bundle that reads the elements itself stays the only judge of them.
//
// It exists because Scalar would describe an array field as a `string`, and a
// wrong type in the schema is worse here than in prose: the agent, the SDK and
// the CLI all read it, and a bundle that tests Array.isArray silently substitutes
// an EMPTY list for anything that is not an array. So a caller told `string`
// sends one, the bundle discards it, and the call succeeds having ignored the
// field. Declaring the array is what makes that failure impossible to reach.
//
// Leniency survives anyway, in both directions. A non-array body value is a
// decode error that Fill drops, leaving the field nil — ABSENT, which is exactly
// the `undefined` the bundle turns into its own empty list. And an element that
// is not a string is carried as the token it was, so the bundle sees what the
// caller sent.
type ScalarList []Scalar

// Raw is a field whose value may be ANY JSON — an object, an array, a number —
// carried to the bundle unchanged.
//
// Scalar would carry the same bytes, and for a while that looked like enough.
// The difference is the SCHEMA: Scalar is a string KIND, so every projection
// describes the field as `string`, and a caller told `string` for a field the
// bundle stores with JSON.stringify sends `"{\"label\":\"x\"}"` — a quoted
// string where an object goes. That is the ScalarList mistake in its other
// direction: there the wrong type let a value be silently discarded, here it
// would be silently double-encoded. This type marshals ITSELF, which is the
// question zip's schemaOf asks first (openapi.go:657), so it publishes `{}` —
// "any JSON", which is the only true thing to say about it.
//
// The zero value means ABSENT, the same rule Scalar keeps and for the same
// reason: a partial update needs `{}` and `{"meta":null}` to stay different, and
// a pointer cannot hold that distinction because encoding/json sets a pointer to
// nil for an explicit null WITHOUT calling UnmarshalJSON.
type Raw []byte

// UnmarshalJSON keeps the caller's bytes, whatever shape they are. Like Scalar's,
// it cannot fail: the value is the bundle's to judge.
func (r *Raw) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

// MarshalJSON writes the carried value back exactly as it arrived. An absent
// field marshals as `null`, which is what encoding/json writes for the zero value
// of any empty JSON — so the type is total.
func (r Raw) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

func (r Raw) field() (json.RawMessage, bool) {
	if len(r) == 0 {
		return nil, false // absent: this key was never on the wire
	}
	return json.RawMessage(r), true
}

// BodyField is one field of an assembled bundle body: a Scalar for a single
// token, a ScalarList for a lenient array, a Raw for a value of any shape. The
// interface is CLOSED — its method is unexported — so those three are the whole
// vocabulary, and Body needs no default case for a kind that cannot exist.
type BodyField interface {
	// field reports the field's verbatim JSON and whether it was on the wire at
	// all. A field that was not contributes no key.
	field() (json.RawMessage, bool)
}

func (s Scalar) field() (json.RawMessage, bool) {
	if s == "" {
		return nil, false // absent: this key was never on the wire
	}
	raw, err := s.MarshalJSON()
	return raw, err == nil
}

func (l ScalarList) field() (json.RawMessage, bool) {
	if l == nil {
		return nil, false // absent: no array arrived under this key
	}
	raw, err := json.Marshal([]Scalar(l))
	return raw, err == nil
}

// Body assembles the object the bundle validates from the caller's verbatim
// tokens. A field that was never on the wire contributes NO key — the
// `undefined` a partial update reads.
//
// The result is a plain Go value, not bytes, because that is what crosses into
// goja: the same shape the untyped relay handed the host after decoding the
// caller's bytes into `any`. Key order is lost to the map on the way, and cannot
// matter — a bundle reads its fields by name and echoes no request body back.
func Body(fields map[string]BodyField) (any, error) {
	obj := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		raw, present := v.field()
		if !present {
			continue
		}
		obj[k] = raw
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var body any
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// SizedIn is the request-size half of an input, embedded by every body-carrying
// typed op.
//
// The relay capped a body and answered 413 AFTER resolving the tenant. A typed op
// never sees the request, so the size is recorded where the bytes are — the
// input's own UnmarshalJSON — and read back after the tenant is resolved, which
// is what keeps a 403 ahead of a 413 for the caller that has both problems.
//
// The field is unexported, so it reaches no schema: zip's projector walks an
// untagged embedded struct and keeps only its EXPORTED fields, so embedding this
// adds nothing a caller could send.
type SizedIn struct{ oversize bool }

// Fill decodes the caller's object into v, records whether it exceeded max, and
// NEVER refuses the body. It is the one decode path for a bundle-backed input.
//
// A body that is not an object — an array, a bare scalar, `null` — leaves every
// field absent, which is exactly what the relay did: it decoded into `any` and
// the bundle turned anything that was not an object into `{}`. The bundle then
// answers, in its own envelope, the same "name is required" it always did. A type
// error on ONE key is saved and decoding continues, so a body that echoes
// `"id":123` back at a PATCH still delivers the fields beside it — as the relay
// did, since the URL carries the id and the bundle never read one from the body.
func (s *SizedIn) Fill(max int, b []byte, v any) {
	s.oversize = len(b) > max
	// The error is deliberately dropped, and dropping it is the wire: see above.
	_ = json.Unmarshal(b, v)
}

// Oversize reports whether the body exceeded the cap passed to Fill. The op reads
// it after resolving the tenant, so the 403 stays ahead of the 413.
func (s SizedIn) Oversize() bool { return s.oversize }

// BundleErr is the bundle's OWN non-2xx answer, carried as a Go error so a typed
// op can return it. A bundle authors envelopes cloud has no vocabulary for, and
// zip's error path renders {status,code,error}, which has nowhere to put them. So
// the op returns the bundle's status and its BYTES, and Envelope writes them back
// untouched.
//
// It is not an escape from typing. The op still declares its In and its Out, so
// the document, the MCP tool, the CLI command and the SDK method all exist.
type BundleErr struct {
	// Status is the bundle's own HTTP status.
	Status int
	// Body is the bundle's own response bytes, written back verbatim.
	Body []byte
	// Msg is the human sentence for a caller that is not on the HTTP path.
	Msg string
}

func (e *BundleErr) Error() string { return e.Msg }

// Unwrap gives the error a status and a message OFF the HTTP path, where there is
// no response to write bytes into: an MCP tools/call and an in-process CLI invoke
// run the op without passing through Envelope, so zip's own error handler renders
// this instead — the bundle's status and message in zip's envelope, rather than a
// blanket 500 that loses both.
func (e *BundleErr) Unwrap() error { return &zip.HTTPError{Status: e.Status, Msg: e.Msg} }

// Envelope writes a BundleErr back to the client VERBATIM: the bundle's own
// status, its own bytes, under the bare `application/json` the untyped relay
// beside it sends. Anything else propagates unchanged.
//
// It must be installed on the app's group BEFORE the ops it serves — fiber runs
// middleware in registration order, so one installed after its leaves never runs.
func Envelope() zip.Handler {
	return func(c *zip.Ctx) error {
		err := c.Continue()
		if be, ok := errors.AsType[*BundleErr](err); ok {
			c.SetHeader("Content-Type", "application/json")
			return c.Bytes(be.Status, be.Body)
		}
		return err
	}
}
