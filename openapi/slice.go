package openapi

// slice.go answers "just this part of the document".
//
// The whole document is megabytes. A reader that wants one capability, or one
// operation, cannot afford to fetch it and cannot afford to be handed it: an agent
// that downloads the lot spends its context on 1,800 paths it will never call. So
// the same endpoint narrows by what it was asked, in the order a reader walks:
//
//	GET /v1                              which capabilities exist
//	GET /v1/<name>                       which operations one capability has
//	GET /v1/openapi.json?capability=<n>  those operations, with their schemas
//	GET /v1/openapi.json?operation=<id>  one operation, with its schemas
//	GET /v1/openapi.json?path=<prefix>   the operations under one address
//
// ?tag=<n> is ?capability=<n>: a capability IS its operations' tag, and tag is the
// word a reader who knows OpenAPI rather than this API reaches for. It used to be
// ignored, which answered the whole 4.9 MB document to a reader who had asked for
// a slice of it — the one outcome the query exists to prevent.
//
// A slice is a valid document on its own: the paths asked for, the tags they
// carry, and exactly the schemas those paths reach through $ref, transitively.
// Nothing else is copied, so a slice is as small as the answer.
//
// It reads the one render [serve] already makes, so a slice can never describe an
// API the whole document does not.

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// schemaRef finds the schema names a fragment points at.
var schemaRef = regexp.MustCompile(`#/components/schemas/([^"\\]+)`)

// name is what a capability or operation may be called in a query: the alphabet
// operationIds and product names already use, bounded, so the value is a lookup
// key and never anything else.
var name = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// prefix is what ?path= may be: an absolute address in the document's own
// spelling, template braces included, bounded like name.
var prefix = regexp.MustCompile(`^/[A-Za-z0-9._~{}/-]{0,255}$`)

type member struct {
	path, method, id string
	tags             []string
	raw              json.RawMessage
}

type slicer struct {
	head     map[string]json.RawMessage // every top-level key but paths, tags, components
	tags     map[string]json.RawMessage // tag name -> tag object
	members  []member
	schemas  map[string]json.RawMessage
	security json.RawMessage // components.securitySchemes, small and always needed

	// memo is keyed "<capability>\x00<operation>" and holds only matches. A
	// ?path= slice is never stored: names are bounded by what exists, prefixes
	// are not — every shorter spelling of every path matches — so a memo keyed
	// on them would hold as many copies of the document as a caller cared to ask
	// for.
	memo sync.Map
}

func newSlicer(whole []byte) (*slicer, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(whole, &top); err != nil {
		return nil, err
	}
	s := &slicer{head: map[string]json.RawMessage{}, tags: map[string]json.RawMessage{}}
	for k, v := range top {
		switch k {
		case "paths", "tags", "components":
		default:
			s.head[k] = v
		}
	}

	var tags []json.RawMessage
	if raw, ok := top["tags"]; ok {
		if err := json.Unmarshal(raw, &tags); err != nil {
			return nil, err
		}
	}
	for _, t := range tags {
		var n struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(t, &n); err != nil {
			return nil, err
		}
		s.tags[n.Name] = t
	}

	var paths map[string]map[string]json.RawMessage
	if err := json.Unmarshal(top["paths"], &paths); err != nil {
		return nil, err
	}
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		for _, method := range slices.Sorted(maps.Keys(paths[path])) {
			raw := paths[path][method]
			var op struct {
				ID   string   `json:"operationId"`
				Tags []string `json:"tags"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				// A path item also carries non-operation keys (parameters, summary);
				// they are not members.
				continue
			}
			s.members = append(s.members, member{path: path, method: method, id: op.ID, tags: op.Tags, raw: raw})
		}
	}

	if raw, ok := top["components"]; ok {
		var c struct {
			Schemas         map[string]json.RawMessage `json:"schemas"`
			SecuritySchemes json.RawMessage            `json:"securitySchemes"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		s.schemas = c.Schemas
		s.security = c.SecuritySchemes
	}
	return s, nil
}

// slice returns the document narrowed to the operations that carry capability
// (when named), have operationId operation (when named) and sit under the path
// prefix under (when given). ok is false when nothing matches, so the caller can
// say so rather than serve an empty document.
func (s *slicer) slice(capability, operation, under string) (body []byte, ok bool, err error) {
	key := capability + "\x00" + operation
	if under == "" {
		if v, hit := s.memo.Load(key); hit {
			return v.([]byte), true, nil
		}
	}

	paths := map[string]map[string]json.RawMessage{}
	want := map[string]bool{}
	var queue []string
	chase := func(raw []byte) {
		for _, m := range schemaRef.FindAllSubmatch(raw, -1) {
			n := string(m[1])
			if !want[n] {
				want[n] = true
				queue = append(queue, n)
			}
		}
	}
	tags := map[string]bool{}
	for _, m := range s.members {
		if capability != "" && !slices.Contains(m.tags, capability) {
			continue
		}
		if operation != "" && m.id != operation {
			continue
		}
		if under != "" && !strings.HasPrefix(m.path, under) {
			continue
		}
		if paths[m.path] == nil {
			paths[m.path] = map[string]json.RawMessage{}
		}
		paths[m.path][m.method] = m.raw
		for _, t := range m.tags {
			tags[t] = true
		}
		chase(m.raw)
	}
	if len(paths) == 0 {
		return nil, false, nil
	}

	schemas := map[string]json.RawMessage{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		raw, found := s.schemas[n]
		if !found {
			continue
		}
		schemas[n] = raw
		chase(raw)
	}

	out := make(map[string]any, len(s.head)+3)
	for k, v := range s.head {
		out[k] = v
	}
	out["paths"] = paths
	if len(tags) > 0 {
		var kept []json.RawMessage
		for _, t := range slices.Sorted(maps.Keys(tags)) {
			if raw, found := s.tags[t]; found {
				kept = append(kept, raw)
			}
		}
		if len(kept) > 0 {
			out["tags"] = kept
		}
	}
	comp := map[string]any{}
	if len(schemas) > 0 {
		comp["schemas"] = schemas
	}
	if len(s.security) > 0 && !bytes.Equal(s.security, []byte("null")) {
		comp["securitySchemes"] = s.security
	}
	if len(comp) > 0 {
		out["components"] = comp
	}

	body, err = json.Marshal(out)
	if err != nil {
		return nil, false, err
	}
	if under == "" {
		s.memo.Store(key, body)
	}
	return body, true, nil
}
