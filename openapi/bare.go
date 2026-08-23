package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Bare names every published property that says NOTHING.
//
// [Complete] is the same law one level up: every operation says what it does. It
// is blind to the shapes those operations carry, and that blindness is measurable
// — a fleet whose every operation is described still published thousands of
// properties with no description, so `equityBps` was an integer nowhere documented
// as basis points and `debit` was an integer under one of three sign conventions
// with nothing to say which. A property reaches openapi.yaml, all eight generated
// SDKs and every MCP inputSchema; its description is the only place the units, the
// sign, the closed vocabulary or the absence rule can travel with it.
//
// It REPORTS rather than refuses, which is the one way it differs from Complete.
// The two clients that carry prose cannot reach every published shape yet —
// [Register] derives its schema by reflection and Go drops comments, so a
// Register-declared component publishes shapes without descriptions no matter what
// its source says — and a relay carries another repo's document, whose prose is
// that repo's to write. A hard refusal here would therefore refuse documents no
// diligence in this repo can fix. So the judgement is the caller's: an app gates
// its own subset (TestEveryPublishedFieldIsDescribed) and names the components it
// cannot reach in a ledger that may only shrink. When the last ledger empties this
// folds into Complete and stops being a separate question.
//
// The walk reads the MARSHALLED document, because that is the artifact.
// Components.Schemas is open-typed — Register contributes a *Schema and the typed
// fold contributes zip's own map — so walking the Go value would silently skip
// whichever half it did not expect, and a check that skips passes for the wrong
// reason. It descends into nested shapes too (an inline object inside a property,
// array items, additionalProperties, and each alternative of allOf/anyOf/oneOf),
// since an SDK reads those exactly as it reads a top-level component.
//
// Paths are dotted from the component name — `Machine.gpuCount`,
// `projectsCreate.repo.url` — and sorted, so two runs over one document produce
// the same list and a ledger can name an entry exactly.
func Bare(doc *Document) ([]string, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal document: %w", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	var paths []string
	for name, schema := range published.Components.Schemas {
		var node any
		if err := json.Unmarshal(schema, &node); err != nil {
			return nil, fmt.Errorf("decode schema %s: %w", name, err)
		}
		paths = append(paths, bare(node, name)...)
	}
	sort.Strings(paths)
	return paths, nil
}

// bare walks one decoded schema and returns the dotted paths of its undescribed
// properties, including those of every shape nested inside it.
func bare(node any, path string) []string {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var paths []string
	if props, ok := m["properties"].(map[string]any); ok {
		for field, raw := range props {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				paths = append(paths, path+"."+field)
			}
			paths = append(paths, bare(p, path+"."+field)...)
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		paths = append(paths, bare(m[key], path+"[]")...)
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := m[key].([]any); ok {
			for _, alt := range list {
				paths = append(paths, bare(alt, path)...)
			}
		}
	}
	return paths
}
