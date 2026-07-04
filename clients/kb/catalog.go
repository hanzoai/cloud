// catalog.go serves the ONE connector catalog: a single list of every source a
// caller can connect, whether it is a FIRST-PARTY Go connector (github/slack/google —
// native list+fetch in sync.go) or a LONG-TAIL activepieces JS connector (notion +
// the ~280 apps the auto engine's piece runner can run). The console renders one list;
// the user does not care which is Go and which is JS. Each entry carries `kind`
// ("native" | "piece") only so the UI can badge it — the connect/sync lifecycle is
// identical for both (OAuth here, KMS token, framework.Ingest).
//
// Separation of concerns: this is the AVAILABLE-to-connect catalog. listConnectors
// (connectors.go) is the caller's per-org connection STATE (connected/doc counts).
// They are distinct surfaces so neither has to carry the other's shape.

package kb

import (
	"context"
	"net/http"
	"sort"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// nativeConnectors describes the first-party Go connectors' display metadata. A
// provider in `providers` but NOT in pieceConnectors is native (its pull is Go code).
var nativeConnectors = map[string]struct {
	displayName string
	description string
}{
	"github": {"GitHub", "Repositories, READMEs, and issues."},
	"slack":  {"Slack", "Channel history and messages."},
	"google": {"Google Drive", "Documents and files."},
}

// kindOf reports whether a provider is a piece-backed (JS) connector or native (Go).
func kindOf(provider string) string {
	if _, ok := pieceConnectors[provider]; ok {
		return "piece"
	}
	return "native"
}

// catalogEntry is one connectable source in the unified catalog.
type catalogEntry struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Kind        string `json:"kind"` // "native" | "piece"
	Configured  bool   `json:"configured"`
}

// listCatalog returns the ONE catalog: every native Go connector plus every long-tail
// piece connector, sorted by provider for a stable listing. `configured` reports
// whether the deployment has OAuth credentials for it (so the console shows Connect vs
// a "not configured" hint). No secret is ever returned; this is metadata only.
func (s *svc) listCatalog(c *zip.Ctx) error {
	// A valid principal is required (the catalog is only served to authenticated
	// callers), though the catalog content itself is org-independent.
	if _, ok := principal.Tenant(c); !ok {
		return zip.ErrForbidden("valid principal required")
	}
	out := make([]catalogEntry, 0, len(providers))
	for provider := range providers {
		_, configured := oauthConfig(provider)
		e := catalogEntry{
			Provider:   provider,
			Kind:       kindOf(provider),
			Configured: configured,
		}
		if meta, ok := nativeConnectors[provider]; ok {
			e.DisplayName = meta.displayName
			e.Description = meta.description
		} else if pc, ok := pieceConnectors[provider]; ok {
			e.DisplayName = pieceDisplayName(c.Context(), pc.piece, provider)
			e.Description = "activepieces connector (" + pc.piece + ")"
		} else {
			e.DisplayName = provider
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return c.JSON(http.StatusOK, map[string]any{"connectors": out})
}

// pieceDisplayName returns a human display name for a piece-backed connector. It is a
// best-effort static label (the provider capitalized); the full activepieces metadata
// (rich display name, logo, actions) is available from the auto engine's
// /v1/auto/pieces surface, which the console can merge for the "add a connector"
// picker. Kept static here so the catalog never blocks on a cross-service call.
func pieceDisplayName(_ context.Context, _ string, provider string) string {
	if len(provider) == 0 {
		return provider
	}
	return string(provider[0]-32) + provider[1:] // "notion" -> "Notion"
}
