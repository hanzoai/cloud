package prompts

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/hanzoai/zip"
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

// CatalogEntry is one starter prompt as the console2 browse UI consumes it.
type CatalogEntry struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Prompt string   `json:"prompt"`
	Tags   []string `json:"tags"`
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

// catalog → the read-only starter library { data: [CatalogEntry] }. Not
// org-scoped (static reference content), but served under the authenticated
// /v1/prompts surface like the rest of the module.
func (s *svc) catalog(c *zip.Ctx) error {
	entries, err := starterCatalog()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": entries})
}
