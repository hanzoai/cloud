// Register is the seam through which a subsystem DECLARES the payload types the
// router cannot derive. The projector (From) reads only two sources: the live
// route table, which says WHICH operations exist, and this registry, which says
// what a declared operation's bodies CONTAIN. The drift-proof property survives
// because the registry cannot add an operation: a registration whose route is
// not in the router simply never renders, so the document still cannot disagree
// with the router — schemas are additive metadata on routes that exist.
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

// registration is one declared operation body pair; nil means "not declared",
// never "empty".
type registration struct {
	req  reflect.Type
	resp reflect.Type
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

// Register declares the request and response body types for one route, keyed by
// the fiber pattern exactly as the route is registered. Pass the zero value of
// the handler's own binding struct (or a slice of the view type for list
// endpoints); pass nil for a side that has no body. Called from the owning
// subsystem's init, next to its route table.
//
// A duplicate registration for the same (method, path) is a programming error
// at init time and panics loudly rather than letting two declarations race for
// one operation.
func Register(path, method string, req, resp any) {
	key := opKey{method: strings.ToUpper(method), path: path}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[key]; dup {
		panic(fmt.Sprintf("openapi: duplicate Register for %s %s", key.method, key.path))
	}
	registry[key] = registration{req: reflect.TypeOf(req), resp: reflect.TypeOf(resp)}
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

// apply attaches the registration's bodies to op, emitting named schemas into c.
// The success response is stated under the "2XX" range key: the handler's exact
// status code is not derivable (201 vs 200 lives in the handler body), and the
// range is what this generator can honestly assert — the success body's shape.
func (r *registration) apply(op *Operation, c *components) error {
	if r.req != nil {
		s, err := schemaOf(r.req, c)
		if err != nil {
			return fmt.Errorf("%s request: %w", op.OperationID, err)
		}
		op.RequestBody = &RequestBody{Content: map[string]Media{"application/json": {Schema: s}}}
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

var jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

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
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
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
