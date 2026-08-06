package prompts

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/zap-proto/zip"
)

// catalog.json is the Hanzo starter prompt library (source of truth:
// hanzoai/prompts), vendored so the unified `cloud` binary ships it with no
// external dependency. It is READ-ONLY reference content, NOT org data: an
// org's own library stays honestly empty until a user explicitly imports a
// starter (which is just a normal POST /v1/prompts). We never auto-populate a
// tenant's store — "honest by construction, no fakes".
//
//go:embed catalog.json
var catalogJSON []byte

// CatalogEntry is one starter prompt as the console browse UI consumes it.
type CatalogEntry struct {
	// Name is the starter's suggested handle. It is NOT taken in your org: the
	// catalog is shared reference content, so this name is free until you import it,
	// and posting it under a name you already use appends a version to yours.
	Name string `json:"name"`
	// Type labels the template's kind, defaulted to "text" for entries that declare
	// none.
	Type string `json:"type"`
	// Prompt is the starter's full template body, ready to POST as-is. Entries too
	// large to create (over 64 KiB) are dropped from this list rather than offered.
	Prompt string `json:"prompt"`
	// Tags is the starter's suggested taxonomy, carried through unchanged if you
	// import it.
	Tags []string `json:"tags"`
	// Labels is the starter's second suggested taxonomy, same treatment as Tags.
	Labels []string `json:"labels"`
}

// starterCatalog decodes and validates the embedded library once. Entries that
// would fail the create-handler guards (name shape, reserved, size) are dropped
// so anything the browse UI offers can actually be imported via POST.
var starterCatalog = sync.OnceValues(func() ([]CatalogEntry, error) {
	var all []CatalogEntry
	if err := json.Unmarshal(catalogJSON, &all); err != nil {
		return nil, fmt.Errorf("prompts: decode embedded catalog: %w", err)
	}
	out := make([]CatalogEntry, 0, len(all))
	for _, e := range all {
		if e.Name == "" || reserved[e.Name] || !nameRE.MatchString(e.Name) || len(e.Prompt) > maxContent {
			continue
		}
		if e.Type == "" {
			e.Type = "text"
		}
		out = append(out, e)
	}
	return out, nil
})

// Catalog returns the read-only starter prompt library shipped with the binary —
// reference content every tenant sees the same, NOT the caller's own prompts and
// never mixed into them. An org's library stays honestly empty until someone
// explicitly imports a starter, which is an ordinary POST /v1/prompts. Entries that
// would fail the create guards are dropped, so everything offered here can actually
// be imported.
func (o promptOps) catalog(_ context.Context, _ *noInput) (*catalogList, error) {
	entries, err := starterCatalog()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	return &catalogList{Data: entries}, nil
}
