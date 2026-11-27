package openapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/sdk/spec"
)

// refPrefix is how a JSON Schema names a component in this document.
const refPrefix = "#/components/schemas/"

// Publish projects the public document out of the full one.
//
// It reads ONE fact per operation — the audience [stamp] wrote — and nothing
// else. Components are pruned to the transitive closure of what the surviving
// operations actually reference, so the public document carries no named type
// that only an internal operation binds — an SDK generated from it mints the
// types its own calls need and no others.
//
// It refuses to produce an EMPTY document: a projection with no customer surface
// is a document composed wrong, and publishing it would ship an SDK with no
// calls in it.
func Publish(d *spec.Document) (*spec.Document, error) {
	out := &spec.Document{
		OpenAPI: d.OpenAPI,
		Info:    publicInfo,
		Servers: d.Servers,
		Paths:   map[string]spec.PathItem{},
	}
	need := map[string]bool{}
	for _, path := range slices.Sorted(maps.Keys(d.Paths)) {
		item := d.Paths[path]
		for _, method := range slices.Sorted(maps.Keys(item)) {
			op := item[method]
			if !op.Public {
				continue
			}
			if out.Paths[path] == nil {
				out.Paths[path] = spec.PathItem{}
			}
			out.Paths[path][method] = op
			if err := refs(op, need); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
		}
	}
	if len(out.Paths) == 0 {
		return nil, fmt.Errorf("the public projection is EMPTY: none of the %d paths in this document "+
			"is a customer operation under /v1/ — publishing it would ship an SDK with no calls in it", len(d.Paths))
	}

	said := make(map[string]string, len(d.Tags))
	for _, t := range d.Tags {
		said[t.Name] = t.Description
	}
	names := map[string]bool{}
	for _, item := range out.Paths {
		for _, op := range item {
			for _, t := range spec.Products(op.Tags) {
				names[t] = true
			}
		}
	}
	for name := range names {
		out.Tags = append(out.Tags, spec.Tag{Name: name, Description: said[name]})
	}
	sort.Slice(out.Tags, func(i, j int) bool { return out.Tags[i].Name < out.Tags[j].Name })

	if len(need) > 0 {
		var have map[string]any
		if d.Components != nil {
			have = d.Components.Schemas
		}
		kept, err := closure(have, need)
		if err != nil {
			return nil, err
		}
		out.Components = &spec.Components{Schemas: kept}
	}
	// LAST, after the components block this projection builds for itself: the
	// credential rides in it, and a fresh block assigned over it would drop the one
	// component every published SDK needs to send a token at all.
	spec.Secure(out)
	return out, nil
}

// refs collects the component names one operation reaches.
//
// Through JSON rather than by walking the Go value, because an operation's
// bodies arrive in two shapes that are JSON-identical and structurally nothing
// alike: [Register] builds closed *RequestBody / map[string]*Response values,
// while a composed or folded operation carries zip's decoded map[string]any. One
// reader for both is the only way this cannot miss a $ref by arriving through
// the wrong client.
func refs(op *spec.Operation, need map[string]bool) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	walk(tree, need)
	return nil
}

// walk adds every component name a decoded JSON tree references.
func walk(n any, need map[string]bool) {
	switch v := n.(type) {
	case map[string]any:
		for k, e := range v {
			if k == "$ref" {
				if s, isString := e.(string); isString {
					if name, ours := strings.CutPrefix(s, refPrefix); ours {
						need[name] = true
					}
				}
				continue
			}
			walk(e, need)
		}
	case []any:
		for _, e := range v {
			walk(e, need)
		}
	}
}

// closure returns the named schemas reachable from need, following the $refs
// inside the schemas themselves — a kept type's own fields may name others, and
// a document missing those is a document no generator can bind.
//
// A referenced name the document does not define is refused rather than dropped.
// That is a broken document either way; the difference is whether it breaks here,
// naming the operation, or in whichever SDK generator meets the dangling $ref
// first.
func closure(schemas map[string]any, need map[string]bool) (map[string]any, error) {
	out := map[string]any{}
	queue := make([]string, 0, len(need))
	for name := range need {
		queue = append(queue, name)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if _, done := out[name]; done {
			continue
		}
		s, defined := schemas[name]
		if !defined {
			return nil, fmt.Errorf("component %q is referenced by a public operation and defined nowhere "+
				"in this document — every generated client would bind a type that does not exist", name)
		}
		out[name] = s
		more := map[string]bool{}
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("component %q: %w", name, err)
		}
		var tree any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, fmt.Errorf("component %q: %w", name, err)
		}
		walk(tree, more)
		for next := range more {
			queue = append(queue, next)
		}
		sort.Strings(queue)
	}
	return out, nil
}
