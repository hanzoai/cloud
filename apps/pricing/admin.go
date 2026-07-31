package pricing

// Admin surface for the catalog enablement overlay (SuperAdmin only).
//
//	GET   /v1/admin/catalog                     full catalog + every entry's state
//	PATCH /v1/admin/catalog/models/*            upsert one model overlay (id may
//	                                            contain '/', e.g. anthropic/x)
//	PATCH /v1/admin/catalog/providers/:name     upsert one provider overlay
//
// Gating mirrors the rest of cloud: c.IsAdmin() is the gateway-minted
// X-User-IsAdmin claim, set only on the JWT-validated path (HIP-0026) for
// members of the global `admin` org. Same trust model the pricing /sync trigger
// and provisioning already rely on. Non-admins get 403, never the catalog
// state.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// Bounds on the admin-supplied overrides patch. Overrides are small metadata/
// pricing patches; these caps are cheap guards that bound the blast radius of a
// forged-admin write (the recursive merge is depth-sensitive). See FIX #5.
const (
	maxOverrideBytes = 64 << 10 // 64 KiB
	maxOverrideDepth = 32
)

// patchBody is the PATCH payload. Every field is optional (a pointer): only
// fields present in the request body are changed; absent fields preserve the
// existing overlay. A brand-new overlay defaults to enabled (the catalog
// default), so PATCH {"enabled":false} is the first act that hides an entry.
type patchBody struct {
	Enabled *bool `json:"enabled,omitempty"`
	Beta    *bool `json:"beta,omitempty"`
	// State is the high-level tri-state setter ("off"|"beta"|"ga") that sets
	// enabled+beta coherently; the low-level Enabled/Beta pointers (applied after)
	// override it for fine control.
	State     *string          `json:"state,omitempty"`
	BetaOrgs  *[]string        `json:"betaOrgs,omitempty"`
	Overrides *json.RawMessage `json:"overrides,omitempty"`
}

// adminCatalogOut is the whole catalog with every entry's enablement state.
// Field order matches the sorted keys of the map it replaces, so the bytes on
// the wire are unchanged.
type adminCatalogOut struct {
	// Models is every model the catalog holds — disabled ones included — each
	// carrying its enablement state under "_overlay".
	Models []Model `json:"models"`
	// Providers is every provider the catalog holds, keyed by name, each
	// carrying its enablement state under "_overlay".
	Providers map[string]any `json:"providers"`
	// Updated is when the catalog was last refreshed, as the pricing source
	// recorded it.
	Updated any `json:"updated"`
}

// GetAdminCatalog returns the full model and provider catalog annotated with
// each entry's enablement state, for the operator console. Nothing is hidden:
// this is the admin's view of what exists and what is currently off, in beta or
// generally available. SuperAdmin only; every other caller is refused.
func (o ops) adminCatalog(ctx context.Context, _ *pricingNoInput) (*adminCatalogOut, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if cat == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "catalog overlay not initialised")
	}

	mstatus, mbody, err := rawDispatch(ctx, "models", nil)
	if err != nil || mstatus != http.StatusOK {
		return nil, zip.Errorf(http.StatusBadGateway, "catalog unavailable")
	}
	var mp struct {
		Updated any     `json:"updated"`
		Models  []Model `json:"models"`
	}
	if err := json.Unmarshal(mbody, &mp); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog decode failed")
	}

	var pwrap struct {
		Providers map[string]any `json:"providers"`
	}
	if pstatus, pbody, perr := rawDispatch(ctx, "providers", nil); perr == nil && pstatus == http.StatusOK {
		_ = json.Unmarshal(pbody, &pwrap)
	}

	snap, err := cat.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "overlay read failed")
	}
	// The catalog shape is tenant-independent and the gate above already proved
	// the caller is an admin, so the org is empty and isAdmin true: nothing hidden.
	return &adminCatalogOut{
		Models:    VisibleCatalog(mp.Models, snap, "", true),
		Providers: VisibleProviders(pwrap.Providers, snap, "", true),
		Updated:   mp.Updated,
	}, nil
}

// adminPatchModel upserts the overlay for one model id. The id is a greedy
// wildcard so slashed ids (anthropic/claude-opus-4.6) route intact.
func adminPatchModel(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(c.Param("*"))
	if id == "" {
		return zip.ErrBadRequest("model id required")
	}
	return adminPatch(c, kindModel, id)
}

// providerPatchIn is the provider name from the URL plus the overlay patch.
//
// The patch fields are POINTERS on purpose: absent means "leave this alone", and
// only a pointer can tell absent from a zero the caller meant. Note that
// encoding/json also leaves a pointer nil for an explicit null WITHOUT calling
// UnmarshalJSON, so {"enabled":null} and {} arrive identically — which is what
// this route already did, and so is preserved.
type providerPatchIn struct {
	// Name is the provider the overlay belongs to, from the URL.
	Name string `json:"name"`
	patchBody
}

// PatchProvider sets one provider's availability overlay.
//
// The overlay decides whether a provider is off, in beta for named orgs, or
// generally available, and carries the price overrides applied on top of the
// catalog. Only the fields the patch names change; every other field keeps the
// value it had, and an absent overlay starts from the catalog default (enabled).
// Answers the new effective overlay, so a console needs no second read.
//
// SuperAdmin only.
func adminPatchProvider(ctx context.Context, in *providerPatchIn) (*Overlay, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("provider name required")
	}
	return applyPatch(ctx, kindProvider, name, in.patchBody)
}

// adminPatch reads the current overlay (or the enabled-default), applies only
// the fields present in the body, and writes it back. Returns the new effective
// overlay so the admin UI can reflect it without a re-fetch.
//
// It is the *zip.Ctx door onto [applyPatch], kept for the ONE route that cannot
// be a typed op: PATCH /v1/admin/catalog/models/* routes through a greedy
// wildcard (see ops.go). The typed providers op calls applyPatch directly, so
// both doors run the same code and cannot drift.
func adminPatch(c *zip.Ctx, kind, id string) error {
	var body patchBody
	if raw := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return zip.ErrBadRequest("invalid JSON body: " + err.Error())
		}
	}
	out, err := applyPatch(c.Context(), kind, id, body)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// applyPatch is the overlay upsert itself, with no request in sight: read the
// current overlay (or the enabled-default), apply only the fields the patch
// carries, write it back, and answer with the new effective overlay.
func applyPatch(ctx context.Context, kind, id string, body patchBody) (*Overlay, error) {
	if cat == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "catalog overlay not initialised")
	}
	if body.Overrides != nil {
		if err := checkOverride(*body.Overrides); err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
	}

	cur, ok, err := cat.Get(ctx, kind, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "overlay read failed")
	}
	if !ok {
		cur = Overlay{Kind: kind, ID: id, Enabled: true} // catalog default.
	}
	if body.State != nil {
		switch strings.ToLower(strings.TrimSpace(*body.State)) {
		case "ga", "on", "enabled":
			cur.Enabled, cur.Beta = true, false
		case "beta":
			cur.Enabled, cur.Beta = false, true
		case "off", "disabled":
			cur.Enabled, cur.Beta = false, false
		default:
			return nil, zip.ErrBadRequest("state must be off|beta|ga")
		}
	}
	if body.Enabled != nil {
		cur.Enabled = *body.Enabled
	}
	if body.Beta != nil {
		cur.Beta = *body.Beta
	}
	if body.BetaOrgs != nil {
		cur.BetaOrgs = normalizeOrgs(*body.BetaOrgs)
	}
	if body.Overrides != nil {
		cur.Overrides = normalizeOverride(*body.Overrides)
	}
	// Backward-compat with the pre-tri-state PATCH: a disabled entry carrying a
	// non-empty beta-org list IS a beta (those orgs see it), unless the caller
	// explicitly set state/beta. Preserves the old {enabled:false,betaOrgs:[x]}
	// contract while the explicit `beta` flag makes an intentional `off` (empty
	// betaOrgs) an absolute kill switch a self-opt-in can never re-open.
	if body.State == nil && body.Beta == nil && !cur.Enabled && len(cur.BetaOrgs) > 0 {
		cur.Beta = true
	}
	cur.Kind, cur.ID = kind, id
	cur.UpdatedAt = time.Now().Unix()

	if err := cat.Upsert(ctx, cur); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "overlay write failed")
	}
	return &cur, nil
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

// checkOverride validates an overrides patch at the boundary: a JSON object or
// null (RFC 7386 — an array or scalar is rejected), within a byte budget, and
// not pathologically deep (the merge is recursive). Cheap guards that bound a
// forged-admin's blast radius.
func checkOverride(raw json.RawMessage) error {
	if len(raw) > maxOverrideBytes {
		return fmt.Errorf("overrides too large (max %d bytes)", maxOverrideBytes)
	}
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return fmt.Errorf("overrides must be a JSON object or null")
	}
	if jsonDepth(m) > maxOverrideDepth {
		return fmt.Errorf("overrides nested too deep (max %d)", maxOverrideDepth)
	}
	return nil
}

// jsonDepth returns the maximum object/array nesting depth of a decoded value.
func jsonDepth(v any) int {
	switch t := v.(type) {
	case map[string]any:
		max := 0
		for _, e := range t {
			if d := jsonDepth(e); d > max {
				max = d
			}
		}
		return max + 1
	case []any:
		max := 0
		for _, e := range t {
			if d := jsonDepth(e); d > max {
				max = d
			}
		}
		return max + 1
	default:
		return 0
	}
}

// normalizeOverride stores the patch verbatim, or clears it (empty/null/{}).
func normalizeOverride(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" || t == "{}" {
		return nil
	}
	return json.RawMessage(t)
}
