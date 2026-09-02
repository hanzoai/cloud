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

package knowledge

import (
	"github.com/hanzoai/cloud"
	"context"
	"sort"

	"github.com/hanzoai/cloud/apps/principal"
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
	// Provider is the source's id and the address every connector op takes it by
	// (/v1/knowledge/connectors/:provider). One of github, slack, google, notion.
	Provider string `json:"provider"`
	// DisplayName is the label to show a person. First-party connectors carry a
	// written name ("GitHub", "Google Drive"); a piece-backed one falls back to the
	// provider capitalized, because the rich activepieces metadata lives behind a
	// cross-service call this read will not make.
	DisplayName string `json:"displayName"`
	// Description is one line of shop copy: what connecting this source pulls in.
	// Native connectors carry written prose; a piece-backed one reads
	// "activepieces connector (<piece>)".
	Description string `json:"description"`
	Kind        string `json:"kind"` // "native" | "piece"
	// Configured is whether THIS DEPLOYMENT holds the OAuth client credentials for
	// the provider. False means Connect would dead-end, so the console can offer it
	// disabled instead of broken. It is deployment-wide and says nothing about
	// whether the caller's org has connected the source — that is the connector
	// list's `status`.
	Configured bool `json:"configured"`
}

// catalogOut is the unified connector catalog.
type catalogOut struct {
	// Connectors is every connectable source, sorted by provider.
	Connectors []catalogEntry `json:"connectors"`
}

// ListConnectorCatalog returns the ONE catalog of everything a caller can
// connect: every first-party connector and every long-tail one, in a single list
// sorted by provider. `configured` reports whether this deployment holds OAuth
// credentials for a source, so the console can show Connect rather than a dead
// button, and `kind` is a badge only — the connect and sync lifecycle is
// identical for both. The catalog itself is org-independent; a validated
// principal is still required. It is metadata only: no secret is ever returned.
//
// Response: {"connectors": [{"provider": "github", "displayName": "GitHub", "description": "Repositories, READMEs, and issues.", "kind": "native", "configured": true}]}
func (o ops) listCatalog(ctx context.Context, _ *cloud.Unit) (*catalogOut, error) {
	// A valid principal is required (the catalog is only served to authenticated
	// callers), though the catalog content itself is org-independent.
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
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
			e.DisplayName = pieceDisplayName(ctx, pc.piece, provider)
			e.Description = "activepieces connector (" + pc.piece + ")"
		} else {
			e.DisplayName = provider
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return &catalogOut{Connectors: out}, nil
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
