package openapi

// graphql.go projects a COMPOSED document into GraphQL SDL.
//
// It is a fourth projection of the one control plane, beside the document
// itself, the hypermedia index and the agent catalog — and it exists because the
// projection zip already ships cannot serve the fleet. zip renders SDL by
// reflecting the Go types in a process's own registry, which is exactly right
// for a binary that holds its subsystems. The shipped topology is a light host
// plus one binary per app, so the host's registry holds almost nothing and its
// live router is proxy prefixes: the schema it rendered advertised ONE field
// against a surface of two thousand.
//
// The document is what the host does have. It embeds every app's subset at build
// time and composes them to answer /v1/openapi.json, so the whole typed surface
// is already in the process — as JSON Schema rather than as Go types, which is
// why this is a second implementation rather than a second caller of zip's. The
// input is a different KIND of thing; the output is the same kind of answer.
//
// WHAT IT DOES NOT DO: invent. An operation the document does not mark
// dispatchable is not published as a field, because a schema that advertises a
// name the fleet answers `unknown` to is worse than one that admits its size.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// JSON is the escape hatch, declared once. JSON Schema is an open vocabulary and
// GraphQL's type system is closed, so a shape this cannot name — a free-form
// object, a oneOf, a map with no declared properties — is published as JSON
// rather than dropped. Dropping it would silently narrow the surface; naming it
// JSON says "this is a value, and its shape is not in the schema".
const jsonScalar = "JSON"

// asSchema reads one component schema out of the open map the document holds.
// Components are `any` because JSON Schema is an open vocabulary: in-process they
// are *Schema, and a document that came back through JSON is generic maps. One
// round trip normalizes both to the subset this projection models, and drops the
// keywords it does not — which is the same thing GraphQL would do with them.
func asSchema(v any) *Schema {
	if s, ok := v.(*Schema); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var s Schema
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

// graph is one projection in flight: the names it has minted, and which of them
// are needed on the input side.
type graph struct {
	comps  map[string]*Schema // component name -> schema
	name   map[string]string  // component name -> GraphQL type name
	input  map[string]bool    // component names reachable from a request body
	opaque map[string]bool    // component names published as JSON, not as a type
}

// GraphQL renders the document as GraphQL SDL: one field per dispatchable
// operation, one type per component schema.
//
// Deterministic — every map is walked in sorted order — because this is served
// on a request and compared in a test, and a schema that differs run to run is a
// schema nothing can diff.
func GraphQL(d *Document) string {
	g := &graph{
		comps:  map[string]*Schema{},
		name:   map[string]string{},
		input:  map[string]bool{},
		opaque: map[string]bool{},
	}
	if d.Components != nil {
		for n, v := range d.Components.Schemas {
			if s := asSchema(v); s != nil {
				g.comps[n] = s
			}
		}
	}
	g.mint()

	queries, mutations := g.fields(d)

	var b strings.Builder
	b.WriteString("# Generated from this deployment's composed document — one field per\n")
	b.WriteString("# dispatchable operation. There is no hand-written schema to drift from it.\n\n")
	b.WriteString("scalar " + jsonScalar + "\n\n")
	writeFields(&b, "Query", queries)
	writeFields(&b, "Mutation", mutations)
	g.writeTypes(&b)
	return b.String()
}

// mint assigns every component a GraphQL type name, refusing a collision by
// qualifying rather than by overwriting. Two components that sanitize to one name
// would otherwise silently become one type, and the second one's fields would be
// the ones a caller got.
func (g *graph) mint() {
	names := make([]string, 0, len(g.comps))
	for n := range g.comps {
		names = append(names, n)
	}
	sort.Strings(names)

	taken := map[string]string{}
	for _, n := range names {
		base := typeName(n)
		pick := base
		for i := 2; ; i++ {
			if prior, clash := taken[pick]; !clash || prior == n {
				break
			}
			pick = fmt.Sprintf("%s%d", base, i)
		}
		taken[pick] = n
		g.name[n] = pick
		if !g.renderable(g.comps[n]) {
			g.opaque[n] = true
		}
	}
}

// renderable reports whether a component becomes a TYPE or becomes JSON. GraphQL
// forbids a type with no fields, and there is no honest name for a oneOf or for a
// map whose keys are the data.
func (g *graph) renderable(s *Schema) bool {
	if s == nil || len(s.OneOf) > 0 {
		return false
	}
	if s.Type != "" && s.Type != "object" {
		return false
	}
	for name := range s.Properties {
		if fieldName(name) != "" {
			return true
		}
	}
	return false
}

// fields projects the operations. An operation earns a field when it is
// DISPATCHABLE and names itself; the document marks that subset already, so this
// reads the mark rather than deciding it a second way.
func (g *graph) fields(d *Document) (queries, mutations []string) {
	paths := make([]string, 0, len(d.Paths))
	for p := range d.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		item := d.Paths[p]
		methods := make([]string, 0, len(item))
		for m := range item {
			methods = append(methods, m)
		}
		sort.Strings(methods)

		for _, m := range methods {
			op := item[m]
			if op == nil || !op.Tool || op.OperationID == "" {
				continue
			}
			name := fieldName(op.OperationID)
			if name == "" {
				continue
			}
			f := g.field(name, op)
			if strings.EqualFold(m, "get") {
				queries = append(queries, f)
				continue
			}
			mutations = append(mutations, f)
		}
	}
	sort.Strings(queries)
	sort.Strings(mutations)
	return queries, mutations
}

// field is one operation: its arguments, its result, and the sentence the
// document already carries about it.
func (g *graph) field(name string, op *Operation) string {
	var args []string
	seen := map[string]bool{}
	for _, p := range op.Parameters {
		a := fieldName(p.Name)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		req := ""
		if p.Required {
			req = "!"
		}
		args = append(args, a+": "+g.scalarOf(p.Schema)+req)
	}
	sort.Strings(args)

	// The body arrives as ONE argument rather than flattened into many. Flattening
	// would put a body property and a query parameter in one namespace, where the
	// collision is silent and the caller cannot tell which one it set.
	if t := g.bodyType(op.RequestBody); t != "" {
		args = append(args, "body: "+t)
	}

	sig := name
	if len(args) > 0 {
		sig += "(" + strings.Join(args, ", ") + ")"
	}
	out := "  " + sig + ": " + g.resultOf(op.Responses) + "\n"
	if s := describe(op); s != "" {
		return "  # " + s + "\n" + out
	}
	return out
}

// scalarOf renders a parameter's schema, which the router asserts as an open map.
func (g *graph) scalarOf(m map[string]any) string {
	if m == nil {
		return "String"
	}
	s := asSchema(m)
	if s == nil {
		return "String"
	}
	return g.typeOf(s, false)
}

// typeOf renders one schema as a GraphQL type reference. input selects the input
// side of a component, because GraphQL will not accept an object type as an
// argument and will not return an input type.
func (g *graph) typeOf(s *Schema, input bool) string {
	if s == nil {
		return jsonScalar
	}
	if ref := component(s.Ref); ref != "" {
		return g.refOf(ref, input)
	}
	switch s.Type {
	case "array":
		return "[" + g.typeOf(s.Items, input) + "]"
	case "string":
		return "String"
	case "integer":
		return "Int"
	case "number":
		return "Float"
	case "boolean":
		return "Boolean"
	case "object", "":
		if len(s.Properties) > 0 {
			// An INLINE object. GraphQL has no anonymous type, and minting one per
			// operation would name thousands of types after the place they appeared
			// rather than after what they are.
			return jsonScalar
		}
		return jsonScalar
	}
	return jsonScalar
}

// refOf resolves a component reference, marking it needed on the input side so
// writeTypes knows to emit the input twin.
func (g *graph) refOf(name string, input bool) string {
	t, ok := g.name[name]
	if !ok || g.opaque[name] {
		return jsonScalar
	}
	if input {
		g.markInput(name, map[string]bool{})
		return t + "Input"
	}
	return t
}

// markInput records that a component, and everything it holds, is reachable from
// a request body. The seen set is what keeps a recursive schema from recursing
// here — a type referring to itself is ordinary in both vocabularies.
func (g *graph) markInput(name string, seen map[string]bool) {
	if seen[name] || g.input[name] || g.opaque[name] {
		return
	}
	seen[name] = true
	g.input[name] = true
	s := g.comps[name]
	if s == nil {
		return
	}
	for _, p := range s.Properties {
		for _, ref := range refsOf(p) {
			g.markInput(ref, seen)
		}
	}
}

// bodyType names the input type an operation's request body accepts.
func (g *graph) bodyType(body any) string {
	s := mediaSchema(body)
	if s == nil {
		return ""
	}
	if ref := component(s.Ref); ref != "" {
		return g.refOf(ref, true)
	}
	if s.Type == "array" && s.Items != nil {
		return "[" + g.typeOf(s.Items, true) + "]"
	}
	return jsonScalar
}

// resultOf names what an operation answers with. An operation whose success
// carries no described shape answers JSON rather than nothing: it still answers.
func (g *graph) resultOf(responses any) string {
	s := successSchema(responses)
	if s == nil {
		return jsonScalar
	}
	return g.typeOf(s, false)
}

// writeTypes emits every component that became a type, and the input twin of
// every one a body can reach.
func (g *graph) writeTypes(b *strings.Builder) {
	names := make([]string, 0, len(g.comps))
	for n := range g.comps {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		if g.opaque[n] {
			continue
		}
		g.writeType(b, "type", g.name[n], g.comps[n], false)
	}
	for _, n := range names {
		if g.opaque[n] || !g.input[n] {
			continue
		}
		g.writeType(b, "input", g.name[n]+"Input", g.comps[n], true)
	}
}

func (g *graph) writeType(b *strings.Builder, kind, name string, s *Schema, input bool) {
	props := make([]string, 0, len(s.Properties))
	for p := range s.Properties {
		props = append(props, p)
	}
	sort.Strings(props)

	var fields []string
	for _, p := range props {
		f := fieldName(p)
		if f == "" {
			continue
		}
		fields = append(fields, "  "+f+": "+g.typeOf(s.Properties[p], input)+"\n")
	}
	if len(fields) == 0 {
		return
	}
	b.WriteString(kind + " " + name + " {\n")
	for _, f := range fields {
		b.WriteString(f)
	}
	b.WriteString("}\n\n")
}

// writeFields emits a root type, or nothing when it has no fields: an empty
// `type Query {}` is invalid SDL, and a deployment with only mutations is a real
// thing.
func writeFields(b *strings.Builder, root string, fields []string) {
	if len(fields) == 0 {
		return
	}
	b.WriteString("type " + root + " {\n")
	for _, f := range fields {
		b.WriteString(f)
	}
	b.WriteString("}\n\n")
}

// describe is the one-line sentence a field carries, flattened: a comment that
// runs to a second line is a comment that ends the type.
func describe(op *Operation) string {
	s := op.Summary
	if s == "" {
		s = op.Description
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// component is the name in a local $ref, or "" for anything else. A ref that
// points outside this document names nothing this schema can publish.
func component(ref string) string {
	const at = "#/components/schemas/"
	if !strings.HasPrefix(ref, at) {
		return ""
	}
	return strings.TrimPrefix(ref, at)
}

// refsOf is every component one schema reaches directly.
func refsOf(s *Schema) []string {
	if s == nil {
		return nil
	}
	if n := component(s.Ref); n != "" {
		return []string{n}
	}
	var out []string
	out = append(out, refsOf(s.Items)...)
	out = append(out, refsOf(s.AdditionalProperties)...)
	for _, p := range s.Properties {
		out = append(out, refsOf(p)...)
	}
	for _, o := range s.OneOf {
		out = append(out, refsOf(o)...)
	}
	return out
}

// mediaSchema digs the schema out of a requestBody, which the document holds
// open-typed.
func mediaSchema(body any) *Schema {
	m, ok := asMap(body)
	if !ok {
		return nil
	}
	content, ok := asMap(m["content"])
	if !ok {
		return nil
	}
	for _, media := range []string{"application/json", "*/*"} {
		if mt, ok := asMap(content[media]); ok {
			if s := asSchema(mt["schema"]); s != nil {
				return s
			}
		}
	}
	return nil
}

// successSchema is the shape of the 2xx a caller expects, preferring the plain
// 200 and taking the first other success in sorted order so the pick is stable.
func successSchema(responses any) *Schema {
	m, ok := asMap(responses)
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(m))
	for c := range m {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	if _, ok := m["200"]; ok {
		codes = append([]string{"200"}, codes...)
	}
	for _, c := range codes {
		if !strings.HasPrefix(c, "2") {
			continue
		}
		if s := mediaSchema(m[c]); s != nil {
			return s
		}
	}
	return nil
}

func asMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return m, true
}

// typeName renders a component name as a GraphQL type name: leading upper, and
// nothing in it that GraphQL's grammar refuses.
func typeName(s string) string {
	n := sanitize(s)
	if n == "" {
		return "Type"
	}
	if c := n[0]; c >= 'a' && c <= 'z' {
		return strings.ToUpper(n[:1]) + n[1:]
	}
	if n[0] >= '0' && n[0] <= '9' {
		return "T" + n
	}
	return n
}

// fieldName renders a property or argument name, or "" when nothing valid is
// left of it. A field GraphQL cannot spell is omitted rather than renamed into
// something the caller cannot map back to the wire.
func fieldName(s string) string {
	n := sanitize(s)
	if n == "" || (n[0] >= '0' && n[0] <= '9') {
		return ""
	}
	return n
}

// sanitize keeps what GraphQL's name grammar allows: letters, digits, underscore.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "_")
}

// Field is what one GraphQL field needs in order to become a request: whose app
// answers it, and the method and address to send.
//
// It is derived from the SAME document [GraphQL] renders, at the same time, so
// the schema a caller reads and the dispatch a caller gets cannot describe
// different surfaces. A field published without a way to send it is the failure
// this whole door exists to avoid.
type Field struct {
	// App is the subsystem that answers, as the manifest names it.
	App string
	// Method and Path are the operation's own address; Path still carries its
	// {braces}, because substituting them is the caller's arguments' job.
	Method string
	Path   string
	// Query and Path names split the arguments by where they belong on the wire.
	Query []string
	Route []string
	// Body says the operation takes one, which is the `body` argument.
	Body bool
}

// Fields indexes every dispatchable operation by the field name [GraphQL]
// published it under.
func Fields(d *Document) map[string]Field {
	out := make(map[string]Field)
	for path, item := range d.Paths {
		for method, op := range item {
			if op == nil || !op.Tool || op.OperationID == "" {
				continue
			}
			name := fieldName(op.OperationID)
			if name == "" {
				continue
			}
			f := Field{App: op.App, Method: strings.ToUpper(method), Path: path}
			for _, p := range op.Parameters {
				a := fieldName(p.Name)
				if a == "" {
					continue
				}
				if p.In == "path" {
					f.Route = append(f.Route, p.Name)
					continue
				}
				f.Query = append(f.Query, p.Name)
			}
			sort.Strings(f.Query)
			sort.Strings(f.Route)
			f.Body = mediaSchema(op.RequestBody) != nil
			out[name] = f
		}
	}
	return out
}
