// The enablement REGISTRY surface (#30/#31) — the three-level model over the ONE
// catalog overlay store: global off|beta|ga, per-org beta grants, and user
// self-service beta opt-in. It reuses the SAME overlay the /v1/pricing catalog gate
// and /v1/admin/catalog admin surface use — there is ONE enablement registry and
// ONE resolver (Overlay.visibleTo / .State), never a parallel copy.
//
//	GET  /v1/admin/enablement          SuperAdmin: the full managed registry
//	PUT  /v1/admin/enablement          SuperAdmin: set an item off|beta|ga (+ grant orgs)
//	GET  /v1/enablement                any authed: the caller's EFFECTIVE view + betas
//	POST /v1/enablement/optin          authed: opt the caller's OWN org into a beta
//	POST /v1/enablement/optout         authed: opt the caller's own org back out
//
// SECURITY — the two-way crux RED verifies:
//   - Global state is SUPERADMIN only (c.IsAdmin()). A customer/org-admin can
//     never flip an item's off/beta/ga.
//   - Self opt-in is scoped to the VALIDATED caller org (principal.Org — a
//     gateway-minted principal, NOT a raw client X-Org-Id, which SanitizeIdentity
//     restores on the bearer-less path) — so a user can only ever enable their OWN
//     org, and only for a BETA item: the store's OptIn refuses `ga` (already on)
//     and `off` (the kill switch), so an opt-in can never bypass an admin `off`.
package pricing

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// enablementKinds bounds the item namespace so an arbitrary caller-supplied kind
// can't pollute the overlay store. models + providers come from the catalog;
// `feature` is the generic product/feature flag lane.
var enablementKinds = map[string]bool{kindModel: true, kindProvider: true, "feature": true}

// adminEnablementItem is one row of the admin registry board.
type adminEnablementItem struct {
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	State     string   `json:"state"` // off|beta|ga
	BetaOrgs  []string `json:"betaOrgs"`
	UpdatedAt int64    `json:"updatedAt"`
}

// adminEnablementList answers GET /v1/admin/enablement — every MANAGED item and its
// global state + beta grants. Untouched catalog items are absent (they are `ga` by
// default); the console composes the candidate list from the live model catalog.
func adminEnablementList(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	if cat == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "enablement store not initialised"})
	}
	snap, err := cat.Snapshot(c.Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "enablement read failed"})
	}
	items := make([]adminEnablementItem, 0, len(snap))
	for _, o := range snap {
		items = append(items, adminEnablementItem{
			Kind: o.Kind, ID: o.ID, State: o.State(), BetaOrgs: o.BetaOrgs, UpdatedAt: o.UpdatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
	return c.JSON(http.StatusOK, map[string]any{"items": items})
}

// setEnablementBody is the SuperAdmin PUT payload.
type setEnablementBody struct {
	Kind     string    `json:"kind"`
	ID       string    `json:"id"`
	State    string    `json:"state"`              // off|beta|ga (required)
	BetaOrgs *[]string `json:"betaOrgs,omitempty"` // optional grant list (replaces)
}

// adminEnablementSet answers PUT /v1/admin/enablement — set an item's global state
// (and optionally its beta-org grant list). SuperAdmin only. Generic over kind,
// so it manages models, providers, AND product/features through the one store.
func adminEnablementSet(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	if cat == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "enablement store not initialised"})
	}
	var body setEnablementBody
	if raw := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return zip.ErrBadRequest("invalid JSON body: " + err.Error())
		}
	}
	kind := strings.TrimSpace(body.Kind)
	id := strings.TrimSpace(body.ID)
	if !enablementKinds[kind] {
		return zip.ErrBadRequest("kind must be model|provider|feature")
	}
	if id == "" {
		return zip.ErrBadRequest("id required")
	}
	var setEnabled, setBeta bool
	switch strings.ToLower(strings.TrimSpace(body.State)) {
	case "ga", "on", "enabled":
		setEnabled, setBeta = true, false
	case "beta":
		setEnabled, setBeta = false, true
	case "off", "disabled":
		setEnabled, setBeta = false, false
	default:
		return zip.ErrBadRequest("state must be off|beta|ga")
	}
	o, err := cat.mutate(c.Context(), kind, id, func(o *Overlay) error {
		o.Enabled, o.Beta = setEnabled, setBeta
		if body.BetaOrgs != nil {
			o.BetaOrgs = normalizeOrgs(*body.BetaOrgs)
		}
		return nil
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "enablement write failed"})
	}
	return c.JSON(http.StatusOK, adminEnablementItem{Kind: o.Kind, ID: o.ID, State: o.State(), BetaOrgs: o.BetaOrgs, UpdatedAt: o.UpdatedAt})
}

// userEnablementItem is one row of the caller's effective view.
type userEnablementItem struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	State     string `json:"state"`     // off|beta|ga
	Effective bool   `json:"effective"` // visible to the caller's org
	OptedIn   bool   `json:"optedIn"`   // caller's org on the beta list
	CanOptIn  bool   `json:"canOptIn"`  // beta && not yet opted in
}

// enablementView answers GET /v1/enablement — the caller's EFFECTIVE enablement
// across every managed item, plus the betas they may opt into. Read-only and safe
// for any principal; an anonymous caller (no org) simply sees the public (ga) items
// as effective and no opt-in affordance.
func enablementView(c *zip.Ctx) error {
	if cat == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "enablement store not initialised"})
	}
	// Resolve the tenant through the SAME validated-principal gate the pricing
	// catalog read plane uses (trustedOrg → principal.Org): an unvalidated
	// caller (no gateway-minted principal) resolves to "" and simply sees the
	// public (ga) items as effective with no opt-in affordance — never another
	// org's beta state via a restored client X-Org-Id.
	org := trustedOrg(c)
	snap, err := cat.Snapshot(c.Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "enablement read failed"})
	}
	items := make([]userEnablementItem, 0, len(snap))
	betas := make([]userEnablementItem, 0)
	for _, o := range snap {
		optedIn := org != "" && o.optedIn(org)
		item := userEnablementItem{
			Kind:      o.Kind,
			ID:        o.ID,
			State:     o.State(),
			Effective: o.visibleTo(org),
			OptedIn:   optedIn,
			CanOptIn:  o.State() == "beta" && !optedIn && org != "",
		}
		items = append(items, item)
		if item.CanOptIn {
			betas = append(betas, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Kind+items[i].ID < items[j].Kind+items[j].ID })
	return c.JSON(http.StatusOK, map[string]any{"org": org, "items": items, "betas": betas})
}

// optBody is the self-service opt-in/out payload.
type optBody struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// enablementOptIn answers POST /v1/enablement/optin — opt the caller's OWN org into
// a beta item. The subject is the VALIDATED caller org (principal.Org), and
// the store refuses anything not in beta, so this can neither target another org nor
// bypass an `off`. Requires an authenticated principal with an org.
func enablementOptIn(c *zip.Ctx) error {
	return enablementOpt(c, true)
}

// enablementOptOut answers POST /v1/enablement/optout — the reverse.
func enablementOptOut(c *zip.Ctx) error {
	return enablementOpt(c, false)
}

func enablementOpt(c *zip.Ctx, in bool) error {
	if cat == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "enablement store not initialised"})
	}
	// Resolve the subject through principal.Org — a VALIDATED principal only
	// (X-User-Id present, gateway-minted). A raw c.Org() would trust a client
	// X-Org-Id that SanitizeIdentity restores on the bearer-less direct-to-pod
	// path (see clients/principal.Validated) — i.e. an off-gateway caller sending
	// `X-Org-Id: victim` with no credential could opt an org it does not own into
	// (or out of) a beta. Keying on the validated tenant closes that cross-tenant
	// write: a caller can only ever opt in THEIR OWN validated org. (RED MEDIUM.)
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to manage beta features")
	}
	var body optBody
	if raw := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return zip.ErrBadRequest("invalid JSON body: " + err.Error())
		}
	}
	kind := strings.TrimSpace(body.Kind)
	id := strings.TrimSpace(body.ID)
	if !enablementKinds[kind] {
		return zip.ErrBadRequest("kind must be model|provider|feature")
	}
	if id == "" {
		return zip.ErrBadRequest("id required")
	}

	var (
		o   Overlay
		err error
	)
	if in {
		o, err = cat.OptIn(c.Context(), kind, id, org)
	} else {
		o, err = cat.OptOut(c.Context(), kind, id, org)
	}
	if err == errNotBeta {
		return zip.ErrBadRequest("item is not in beta — nothing to opt into")
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "opt-in write failed"})
	}
	return c.JSON(http.StatusOK, userEnablementItem{
		Kind:      o.Kind,
		ID:        o.ID,
		State:     o.State(),
		Effective: o.visibleTo(org),
		OptedIn:   o.optedIn(org),
		CanOptIn:  o.State() == "beta" && !o.optedIn(org),
	})
}
