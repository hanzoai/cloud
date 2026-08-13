// Register and Describe are the seam through which a subsystem DECLARES what
// the router cannot derive: the payload types an operation binds (Register)
// and, for an operation whose handler the wire refuses to let become a typed
// op, its prose (Describe). The projector (From) reads only two sources: the
// live route table, which says WHICH operations exist, and this registry, which
// says what a declared operation's bodies CONTAIN and what its refused handler
// DOES. The drift-proof property survives because the registry cannot add an
// operation: a declaration whose route is not in the router simply never
// renders, so the document still cannot disagree with the router — schemas and
// prose are additive metadata on routes that exist.
//
// The schema itself is derived by reflection from the very Go structs the
// handler binds (json tags), stated once at the registration site next to the
// route. There is no hand-written schema to fall out of sync with the code:
// change the struct and the document follows.
package openapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Media is an OpenAPI media type object: the schema of one content type.
type Media struct {
	Schema *Schema `json:"schema,omitempty"`
}

// RequestBody is an OpenAPI request body object.
type RequestBody struct {
	Content map[string]Media `json:"content"`
}

// Response is an OpenAPI response object. Description is required by the spec.
type Response struct {
	Description string           `json:"description"`
	Content     map[string]Media `json:"content,omitempty"`
}

// registration is one operation's declared halves. The BODY half (Register):
// req/resp types, nil meaning "not declared", never "empty"; alts is set only
// when req is [OneOf] — the alternative shapes in declared order, kept as types
// because reflect.TypeOf on the OneOf slice itself knows only that it is a
// slice of any. The PROSE half (Describe): summary/description for an operation
// whose handler cannot be a typed op. Each half carries its own presence flag
// so the duplicate check guards the half actually being re-declared — one
// package Registering the bodies and Describing the prose of one operation is
// two halves of one declaration, not a clash.
type registration struct {
	req       reflect.Type
	alts      []reflect.Type
	resp      reflect.Type
	respBytes string // non-JSON response media type ([Bytes]); empty ⇒ resp is JSON
	declared  bool   // the body half is present (Register ran)

	summary     string
	description string
	described   bool // the prose half is present (Describe ran)

	id string // stated instead of derived; empty ⇒ derived (see identify)
}

// opKey addresses a registration the same way the router addresses a route:
// METHOD + the fiber pattern verbatim (/v1/platform/projects/:project/apps).
type opKey struct {
	method string
	path   string
}

var (
	regMu    sync.Mutex
	registry = map[opKey]registration{}
)

// Binary is the request declaration for a body that is not JSON at all: opaque
// bytes under the caller's own content type — an uploaded PDF or image, an
// OFX/QFX/CSV bank statement, a script. Pass it as Register's req for a route
// whose handler reads the body raw.
//
// It exists because "no declaration" and "a byte body" were rendering
// IDENTICALLY, and they are opposite facts. An operation with no requestBody is
// what a route that takes no body publishes, so every SDK generator reading the
// document emitted a call with no payload parameter for routes that cannot work
// without one (POST /v1/books/scan eats a receipt; /v1/books/bank/import eats a
// statement). OpenAPI's own spelling for an opaque body is a string of format
// binary, which is what this renders — the honest declaration a Go struct cannot
// make, since no struct describes a file.
//
// It is the REQUEST half; [Bytes] is the response half, which states its media
// type because a served asset has one.
type Binary struct{}

var binaryReq = reflect.TypeFor[Binary]()

// Bytes is the RESPONSE declaration for a body that is not JSON: an asset the
// caller loads rather than decodes. Type is the media type the handler actually
// sets, because a document that says application/json over JavaScript is a
// document that lies to whoever generates against it.
//
//	openapi.Register("/v1/event.js", "GET", nil, openapi.Bytes{Type: "application/javascript"})
//
// Empty Type means opaque bytes (application/octet-stream). Like [Binary] it
// names no component: an asset has no fields.
type Bytes struct{ Type string }

// OneOf is the request declaration for a body whose wire is POLYMORPHIC: one path
// that accepts several unrelated JSON shapes, all decoded by the same handler.
// Pass the zero value of each shape, in the order a reader should meet them:
//
//	openapi.Register("/v1/event", "POST",
//	    openapi.OneOf{Event{}, []Event{}, CaptureBatch{}}, CaptureResult{})
//
// It exists for the same reason [Binary] does — the honest declaration a single Go
// struct cannot make. The alternative was to name ONE of the shapes and call it the
// wire, which is a document that omits the two forms every batching client actually
// sends, and an SDK whose only ingest call cannot send a batch. OpenAPI's own
// spelling for "several shapes, caller picks" is `oneOf`, which is what this
// renders; each alternative is derived by reflection exactly as a lone req is, so a
// named struct among them still becomes one shared component.
//
// REQUEST-only, like Binary: a response that varies by shape is a fact no route
// here needs stated yet, and inventing the second half before one asks is how one
// seam becomes two.
type OneOf []any

var oneOfReq = reflect.TypeFor[OneOf]()

// Register declares the request and response body types for one route, keyed by
// the fiber pattern exactly as the route is registered. Pass the zero value of
// the handler's own binding struct (or a slice of the view type for list
// endpoints), [Binary] for a raw byte body, and nil for a side that has no body.
// Called from the owning subsystem's init, next to its route table.
//
// A duplicate registration for the same (method, path) is a programming error
// at init time and panics loudly rather than letting two declarations race for
// one operation.
func Register(path, method string, req, resp any) {
	key := opKey{method: strings.ToUpper(method), path: path}
	half := registration{req: reflect.TypeOf(req), resp: reflect.TypeOf(resp), declared: true}
	if b, asset := resp.(Bytes); asset {
		// The media type IS the declaration; Bytes itself names no component, so
		// the reflected type is dropped rather than published as a schema.
		half.resp = nil
		half.respBytes = b.Type
		if half.respBytes == "" {
			half.respBytes = "application/octet-stream"
		}
	}
	if alts, poly := req.(OneOf); poly {
		if len(alts) == 0 {
			panic(fmt.Sprintf("openapi: empty OneOf for %s %s — a polymorphic body has alternatives", key.method, path))
		}
		half.alts = make([]reflect.Type, len(alts))
		for i, a := range alts {
			half.alts[i] = reflect.TypeOf(a)
		}
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg := registry[key]
	if reg.declared {
		panic(fmt.Sprintf("openapi: duplicate Register for %s %s", key.method, key.path))
	}
	reg.req, reg.alts, reg.resp, reg.respBytes, reg.declared = half.req, half.alts, half.resp, half.respBytes, true
	registry[key] = reg
}

// Describe declares the PROSE for one route — the summary and description a
// consumer reads — keyed by the fiber pattern exactly as the route is
// registered, like [Register]. Called from the owning subsystem's init, next to
// the wire fact that keeps the handler untyped.
//
// It exists for the operation a typed op cannot carry. A typed op's prose is
// lifted from its doc comment by zipdoc, so the ONLY operations with nowhere to
// state prose are the ones the wire refuses to let become typed ops — SSE
// streams, raw proxies, redirects. Leaving those bare publishes an operationId
// and NOTHING else: every SDK generated off the document offers a call it
// cannot explain, and a spec-derived CLI a command with no help text. Describe
// is that prose's seam, with the same drift-proof property Register has: a
// description whose route is not in the router never renders, so prose is
// additive metadata on routes that exist — the registry still cannot add an
// operation.
//
// Empty prose is refused loudly: a Describe that states nothing is a
// programming error, not a declaration.
func Describe(path, method, summary, description string) {
	key := opKey{method: strings.ToUpper(method), path: path}
	if strings.TrimSpace(summary) == "" && strings.TrimSpace(description) == "" {
		panic(fmt.Sprintf("openapi: empty Describe for %s %s — a declaration states something", key.method, key.path))
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg := registry[key]
	if reg.described {
		panic(fmt.Sprintf("openapi: duplicate Describe for %s %s", key.method, key.path))
	}
	reg.summary, reg.description, reg.described = summary, description, true
	registry[key] = reg
}

// identify states an operation's id instead of letting it be derived.
//
// Every other id comes from [zip.ID] on method+path, and that single rule is what
// lets an agent call the name it just read in the document. The rule drops the
// default version segment, because a segment every address carries tells no two
// operations apart — which leaves ONE shape ambiguous: two addresses that differ
// only by that segment. An embedded SPA is exactly it, mounting at /tasks while
// its API answers at /v1/tasks.
//
// The page yields, not the API: the thing people call keeps the clean name, and
// the shell says it is the shell. Unexported because [DescribeSPA] is the one
// declaration that needs it — a second caller would mean a second ambiguity, and
// that is worth reading this comment first.
func identify(path, method, id string) {
	key := opKey{method: strings.ToUpper(method), path: path}
	regMu.Lock()
	defer regMu.Unlock()
	reg := registry[key]
	reg.id = id
	registry[key] = reg
}

// DescribeRest declares prose for every method at path that nothing has described
// yet, and is how an address bound with All() is covered WITHOUT a hand-copied list
// of methods.
//
// A wildcard route accepts every method this generator publishes, but the
// interesting ones are described individually — GET reads, POST acts, PUT is not
// routed — and each site then enumerated the methods it cared about and stopped.
// Every one of them stopped at the same five, so OPTIONS and TRACE were published
// bare from six different addresses: the same omission written six times, which is
// what a hand-copied list of a thing the generator owns always becomes.
//
// So the leftovers are asked for rather than listed. Call it AFTER the per-method
// prose for that path; it skips what is already declared, so it cannot collide with
// them, and a method added to the generator is covered here the day it appears
// instead of the day somebody notices.
func DescribeRest(path, summary, description string) {
	for _, m := range Methods() {
		regMu.Lock()
		done := registry[opKey{method: m, path: path}].described
		regMu.Unlock()
		if !done {
			Describe(path, m, summary, description)
		}
	}
}

// describedRoutes returns the routes that have DECLARED prose, sorted, so the
// bijection [Complete] enforces can be read in the one direction the registry
// cannot answer by itself: not "does this operation have prose" but "does this
// prose have an operation".
func describedRoutes() []opKey {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]opKey, 0, len(registry))
	for k, r := range registry {
		if r.described {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].method < out[j].method
	})
	return out
}

// registered returns the declaration for a live route, or nil.
func registered(method, path string) *registration {
	regMu.Lock()
	defer regMu.Unlock()
	if r, ok := registry[opKey{method: method, path: path}]; ok {
		return &r
	}
	return nil
}

// components accumulates the named schemas one From call emits, and remembers
// which Go type claimed each name so two DIFFERENT types colliding on one name
// is refused instead of silently merged.
type components struct {
	schemas map[string]any // *Schema values; open-typed to share Document.Components with the typed fold
	types   map[string]reflect.Type
}

func newComponents() *components {
	return &components{schemas: map[string]any{}, types: map[string]reflect.Type{}}
}

// apply attaches the registration's declared halves to op — prose verbatim,
// bodies emitting named schemas into c. The success response is stated under
// the "2XX" range key: the handler's exact status code is not derivable (201 vs
// 200 lives in the handler body), and the range is what this generator can
// honestly assert — the success body's shape.
func (r *registration) apply(op *Operation, c *components) error {
	if r.described {
		op.Summary, op.Description = r.summary, r.description
	}
	if r.id != "" {
		op.OperationID = r.id
	}
	switch {
	case r.req == binaryReq:
		// Not JSON, and not a component: an opaque body has no fields to name, so
		// it is declared inline under the content type that means "bytes".
		op.RequestBody = &RequestBody{Content: map[string]Media{
			"application/octet-stream": {Schema: &Schema{Type: "string", Format: "binary"}},
		}}
	case r.req == oneOfReq:
		// One path, several accepted shapes. The alternatives are derived exactly
		// as a lone request type is, so a named struct among them lands in
		// components once and is $ref'd from here.
		alts := make([]*Schema, len(r.alts))
		for i, t := range r.alts {
			s, err := schemaOf(t, c)
			if err != nil {
				return fmt.Errorf("%s request alternative %d: %w", op.OperationID, i, err)
			}
			alts[i] = s
		}
		op.RequestBody = &RequestBody{Content: map[string]Media{
			"application/json": {Schema: &Schema{OneOf: alts}},
		}}
	case r.req != nil:
		s, err := schemaOf(r.req, c)
		if err != nil {
			return fmt.Errorf("%s request: %w", op.OperationID, err)
		}
		op.RequestBody = &RequestBody{Content: map[string]Media{"application/json": {Schema: s}}}
	}
	if r.respBytes != "" {
		// An asset, declared inline under the media type the handler sets: no
		// fields to name, so no component — the response mirror of binaryReq.
		op.Responses = map[string]*Response{
			"2XX": {Description: "Success", Content: map[string]Media{
				r.respBytes: {Schema: &Schema{Type: "string", Format: "binary"}},
			}},
		}
		return nil
	}
	if r.resp != nil {
		s, err := schemaOf(r.resp, c)
		if err != nil {
			return fmt.Errorf("%s response: %w", op.OperationID, err)
		}
		op.Responses = map[string]*Response{
			"2XX": {Description: "Success", Content: map[string]Media{"application/json": {Schema: s}}},
		}
	}
	return nil
}

var jsonMarshaler = reflect.TypeFor[json.Marshaler]()

// schemaOf derives JSON Schema from a Go type by reflection, following
// encoding/json's rules (tags name fields, "-" and unexported fields are
// absent, anonymous embedded structs flatten). A NAMED struct becomes a
// component referenced by $ref — one named type per Go struct — and anonymous
// structs inline. A type this function cannot state (chan, func) is an error,
// never a guess.
func schemaOf(t reflect.Type, c *components) (*Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// A custom marshaler makes the type's Go shape NOT its wire shape, so the
	// honest answer is unconstrained. It is checked BEFORE the kind switch because
	// the rule is about the marshaler, not about being a struct: json.RawMessage is
	// a []byte that emits raw JSON, and reading it as a slice published every
	// declared raw-JSON field as a base64 STRING — the one shape it is never.
	if t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler) {
		return &Schema{}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}, nil
	case reflect.Bool:
		return &Schema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}, nil
	case reflect.Interface:
		return &Schema{}, nil // any — the type genuinely constrains nothing
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: "string"}, nil // []byte marshals as base64 string
		}
		items, err := schemaOf(t.Elem(), c)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "array", Items: items}, nil
	case reflect.Map:
		elem, err := schemaOf(t.Elem(), c)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "object", AdditionalProperties: elem}, nil
	case reflect.Struct:
		if name := t.Name(); name != "" {
			if prev, taken := c.types[name]; taken {
				if prev != t {
					return nil, fmt.Errorf("component name %q claimed by both %v and %v", name, prev, t)
				}
				return &Schema{Ref: "#/components/schemas/" + name}, nil
			}
			c.types[name] = t
			s := &Schema{} // placed before recursing so a self-reference resolves
			c.schemas[name] = s
			obj, err := structSchema(t, c)
			if err != nil {
				return nil, err
			}
			*s = *obj
			return &Schema{Ref: "#/components/schemas/" + name}, nil
		}
		return structSchema(t, c)
	default:
		return nil, fmt.Errorf("cannot derive JSON schema for %v (kind %v)", t, t.Kind())
	}
}

// structSchema builds the object schema for a struct's fields.
func structSchema(t reflect.Type, c *components) (*Schema, error) {
	props := map[string]*Schema{}
	if err := structFields(t, c, props); err != nil {
		return nil, err
	}
	return &Schema{Type: "object", Properties: props}, nil
}

func structFields(t reflect.Type, c *components, props map[string]*Schema) error {
	for f := range t.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			// Embedded without a tag: encoding/json flattens its fields.
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				if err := structFields(ft, c, props); err != nil {
					return err
				}
				continue
			}
		}
		if f.PkgPath != "" {
			continue // unexported: encoding/json never emits it
		}
		name := tag
		if name == "" {
			name = f.Name
		}
		s, err := schemaOf(f.Type, c)
		if err != nil {
			return err
		}
		props[name] = s
	}
	return nil
}
