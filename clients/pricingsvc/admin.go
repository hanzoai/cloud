// Admin surface for the catalog enablement overlay (global-admin only).
//
//	GET   /v1/admin/catalog                     full catalog + every entry's state
//	PATCH /v1/admin/catalog/models/*            upsert one model overlay (id may
//	                                            contain '/', e.g. anthropic/x)
//	PATCH /v1/admin/catalog/providers/:name     upsert one provider overlay
//
// Gating mirrors the rest of cloud: c.IsAdmin() is the gateway-minted
// X-User-IsAdmin claim, set only on the JWT-validated path (HIP-0026) for
// members of the global `admin` org. Same trust model the pricing /sync trigger
// and provisioningsvc already rely on. Non-admins get 403, never the catalog
// state.
package pricingsvc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/zip"
)

// patchBody is the PATCH payload. Every field is optional (a pointer): only
// fields present in the request body are changed; absent fields preserve the
// existing overlay. A brand-new overlay defaults to enabled (the catalog
// default), so PATCH {"enabled":false} is the first act that hides an entry.
type patchBody struct {
	Enabled   *bool            `json:"enabled,omitempty"`
	BetaOrgs  *[]string        `json:"betaOrgs,omitempty"`
	Overrides *json.RawMessage `json:"overrides,omitempty"`
}

// adminCatalog returns the full catalog (every model + provider) annotated with
// overlay state, for the admin UI. The catalog shape is tenant-independent, so
// the admin always sees the canonical full list (isAdmin gate => nothing hidden).
func adminCatalog(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	if cat == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "catalog overlay not initialised"})
	}

	mstatus, mbody, err := rawDispatch(c, "models", nil)
	if err != nil || mstatus != http.StatusOK {
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "catalog unavailable"})
	}
	var mp struct {
		Updated any     `json:"updated"`
		Models  []Model `json:"models"`
	}
	if err := json.Unmarshal(mbody, &mp); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "catalog decode failed"})
	}

	var pwrap struct {
		Providers map[string]any `json:"providers"`
	}
	if pstatus, pbody, perr := rawDispatch(c, "providers", nil); perr == nil && pstatus == http.StatusOK {
		_ = json.Unmarshal(pbody, &pwrap)
	}

	snap, err := cat.Snapshot(c.Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "overlay read failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"updated":   mp.Updated,
		"models":    VisibleCatalog(mp.Models, snap, "", true),
		"providers": VisibleProviders(pwrap.Providers, snap, "", true),
	})
}

// adminPatchModel upserts the overlay for one model id. The id is a greedy
// wildcard so slashed ids (anthropic/claude-opus-4.6) route intact.
func adminPatchModel(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	id := strings.TrimSpace(c.Param("*"))
	if id == "" {
		return zip.ErrBadRequest("model id required")
	}
	return adminPatch(c, kindModel, id)
}

// adminPatchProvider upserts the overlay for one provider name.
func adminPatchProvider(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return zip.ErrBadRequest("provider name required")
	}
	return adminPatch(c, kindProvider, name)
}

// adminPatch reads the current overlay (or the enabled-default), applies only
// the fields present in the body, and writes it back. Returns the new effective
// overlay so the admin UI can reflect it without a re-fetch.
func adminPatch(c *zip.Ctx, kind, id string) error {
	if cat == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "catalog overlay not initialised"})
	}

	var body patchBody
	if raw := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return zip.ErrBadRequest("invalid JSON body: " + err.Error())
		}
	}
	if body.Overrides != nil && !validOverride(*body.Overrides) {
		return zip.ErrBadRequest("overrides must be a JSON object or null")
	}

	ctx := c.Context()
	cur, ok, err := cat.Get(ctx, kind, id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "overlay read failed"})
	}
	if !ok {
		cur = Overlay{Kind: kind, ID: id, Enabled: true} // catalog default.
	}
	if body.Enabled != nil {
		cur.Enabled = *body.Enabled
	}
	if body.BetaOrgs != nil {
		cur.BetaOrgs = normalizeOrgs(*body.BetaOrgs)
	}
	if body.Overrides != nil {
		cur.Overrides = normalizeOverride(*body.Overrides)
	}
	cur.Kind, cur.ID = kind, id
	cur.UpdatedAt = time.Now().Unix()

	if err := cat.Upsert(ctx, cur); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "overlay write failed"})
	}
	return c.JSON(http.StatusOK, cur)
}

// normalizeOrgs trims, drops empties, and de-duplicates a beta-org list.
func normalizeOrgs(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// validOverride accepts only a JSON object or null (RFC 7386 patch); an array or
// scalar is rejected at the boundary so a malformed patch never reaches the gate.
func validOverride(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return true
	}
	var m map[string]any
	return json.Unmarshal(raw, &m) == nil // only a JSON object unmarshals into a map.
}

// normalizeOverride stores the patch verbatim, or clears it (empty/null/{}).
func normalizeOverride(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" || t == "{}" {
		return nil
	}
	return json.RawMessage(t)
}
