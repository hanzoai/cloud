package pricing

// Admin surface for the catalog enablement overlay (SuperAdmin only).
//
//	GET   /v1/admin/pricing/catalog                     full catalog + every entry's state
//	PATCH /v1/admin/pricing/catalog/models/*            upsert one model overlay (id may
//	                                            contain '/', e.g. anthropic/x)
//	PATCH /v1/admin/pricing/catalog/providers/:name     upsert one provider overlay
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

// overlayPatch is the PATCH payload BOTH overlay writes take — the models
// wildcard and the providers op — so the two routes cannot come to disagree
// about what an overlay write accepts. Every field is optional (a pointer):
// only fields present in the request body are changed; absent fields preserve
// the existing overlay. A brand-new overlay defaults to enabled (the catalog
// default), so PATCH {"enabled":false} is the first act that hides an entry.
//
// The name says which noun it patches because publishing it puts it in the
// fleet's FLAT schema namespace, where one name may mean only one shape. It was `patchBody` while it published nothing, which is
// exactly the generic name two apps eventually claim with two shapes — and the
// weave then refuses both. Qualify before you publish; renaming afterwards is a
// rename in every generated SDK.
//
// `url:"-"` on every field is what keeps the body the only source. zip binds
// query OVER a decoded body, so without it a converted route silently accepts
// `?enabled=true` — a source the raw handler never read. Harmless for these
// five today (setScalar leaves a pointer alone, zip@v1.36.3 typed.go:393) and
// not harmless for the next field somebody adds as a plain string or bool, so
// the tag states the rule where a reader of the struct sees it.
type overlayPatch struct {
	// Enabled sets the "ga" half of the state directly: true makes the entry
	// generally available, false hides it from the public. Absent leaves it
	// alone. Sent beside State it WINS, because it is applied after.
	Enabled *bool `json:"enabled,omitempty" url:"-"`
	// Beta sets the beta half directly, and it only means anything while the
	// entry is not enabled: true keeps the orgs in BetaOrgs seeing it, false is
	// the absolute kill switch. Absent leaves it alone; sent beside State it
	// wins, because it is applied after.
	Beta *bool `json:"beta,omitempty" url:"-"`
	// State is the high-level tri-state setter ("off"|"beta"|"ga") that sets
	// enabled+beta coherently; the low-level Enabled/Beta pointers (applied after)
	// override it for fine control. Anything else is 400.
	State *string `json:"state,omitempty" url:"-"`
	// BetaOrgs REPLACES the entry's beta grant list — it is not merged, so the
	// list sent is the list stored, and `[]` revokes every grant. Entries are
	// trimmed and de-duplicated. Absent leaves the existing grants alone.
	BetaOrgs *[]string `json:"betaOrgs,omitempty" url:"-"`
	// Overrides is an RFC 7386 merge patch applied over the entry's catalog
	// values, stored and echoed back VERBATIM, so its keys are the catalog's own
	// and not this API's. It must be a JSON object or null — an array or a scalar
	// is 400 — and is bounded in size and nesting depth. Absent leaves the stored
	// override alone; `{}` or null CLEARS it.
	Overrides *json.RawMessage `json:"overrides,omitempty" url:"-"`
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
	// the caller is an admin, so the org is empty and isAdmin true: the ORG rule
	// hides nothing here. The credential limit still applies — it belongs to the
	// key rather than to the holder, and this is the surface where a key that
	// reaches everything does the most.
	return &adminCatalogOut{
		Models:    Reachable(VisibleCatalog(mp.Models, snap, "", true), callerGrant(ctx)),
		Providers: VisibleProviders(pwrap.Providers, snap, "", true),
		Updated:   mp.Updated,
	}, nil
}

// modelPatchIn is the model id from the URL plus the overlay patch.
//
// The id keeps a wire name rather than being URL-only. A typed op is reached off
// HTTP as well — the call plane hands the arguments across as the body with no
// path map — so `json:"-"` would leave those callers no way to name which model
// they mean. The URL is still the authority: bindURL binds path LAST, so a body
// id cannot redirect a write away from the model the address names.
type modelPatchIn struct {
	// ID is the model the overlay belongs to. Over HTTP it is the whole remaining
	// path, so a slashed id (acme/some-model-1) addresses intact, and the path
	// wins over anything sent in the body.
	ID string `json:"id" url:"*1"`
	overlayPatch
}

// PatchModel turns one model off, into beta for named orgs, or generally available.
//
// Sets one model's availability overlay — and the price overrides applied on top
// of the catalog — then answers the new effective overlay, so a console needs no
// second read. The model id is the whole remaining path, so a slashed id like
// `acme/some-model-1` addresses intact.
//
// SuperAdmin only; every other caller is 403. The overlay is PLATFORM-WIDE —
// this is the catalog every org prices against, not a per-org setting — and
// `betaOrgs` is what narrows a beta to named orgs.
//
// Only the fields the patch names change; an entry with no overlay yet starts
// from the catalog default, which is enabled. `state` is the coherent tri-state
// setter (`off`|`beta`|`ga`) and the low-level `enabled`/`beta` flags are applied
// AFTER it, so they win where both are sent; anything else in `state` is 400. A
// field sent as an explicit `null` arrives indistinguishable from an absent one,
// so null does not clear anything.
//
// The rule worth reading twice: a disabled entry that still carries beta orgs IS
// a beta — `{"enabled":false,"betaOrgs":["acme"]}` leaves acme seeing the model.
// Only an explicit `off` (or `beta:false`) with an empty list is the absolute
// kill switch that a user's own beta opt-in can never re-open.
//
// `overrides` is an RFC 7386 merge patch, stored and echoed back verbatim; it
// must be a JSON object or null — an array or a scalar is refused — and is
// bounded in size and nesting depth. An uninitialised overlay store answers 503.
func adminPatchModel(ctx context.Context, in *modelPatchIn) (*Overlay, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("model id required")
	}
	return applyPatch(ctx, kindModel, id, in.overlayPatch)
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
	overlayPatch
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
	return applyPatch(ctx, kindProvider, name, in.overlayPatch)
}

// applyPatch is the overlay upsert itself, with no request in sight: read the
// current overlay (or the enabled-default), apply only the fields the patch
// carries, write it back, and answer with the new effective overlay.
//
// Both typed ops on this surface — the model one and the provider one — call it
// directly, so the two cannot drift about what a patch means.
func applyPatch(ctx context.Context, kind, id string, body overlayPatch) (*Overlay, error) {
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
