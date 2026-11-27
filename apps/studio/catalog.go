package studio

// catalog.go — the public templates: one per kind of thing Studio makes, read-only,
// shipped in the binary. A catalog template has no org and no creator; an org's own
// templates live in its store and never mix with these.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed catalog.json
var catalogJSON []byte

// catalogs decodes and checks the catalog once. A template whose graph does not
// validate is a build defect, so it fails loudly at the first read (and in the
// package's tests) rather than at a customer's run.
var catalogs = sync.OnceValue(func() []Template {
	var all []Template
	if err := json.Unmarshal(catalogJSON, &all); err != nil {
		panic(fmt.Sprintf("studio: the catalog does not read: %v", err))
	}
	seen := map[string]bool{}
	for i, t := range all {
		if t.ID == "" || seen[t.ID] || !kinds[t.Kind] {
			panic(fmt.Sprintf("studio: catalog template %d (%q) has no id, a repeated id or an unknown kind", i, t.ID))
		}
		seen[t.ID] = true
		if _, err := check(t.Graph); err != nil {
			panic(fmt.Sprintf("studio: catalog template %s: %v", t.ID, err))
		}
		all[i].Org, all[i].Creator, all[i].Shared = "", "", false
	}
	return all
})

func catalog() []Template { return catalogs() }

// catalogTemplate is one catalog template by id.
func catalogTemplate(id string) (Template, bool) {
	for _, t := range catalogs() {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
