package pricing

// The enablement REGISTRY surface (#30/#31) — the three-level model over the ONE
// catalog overlay store: global off|beta|ga, per-org beta grants, and user
// self-service beta opt-in. It reuses the SAME overlay the /v1/pricing catalog gate
// and /v1/admin/pricing/catalog admin surface use — there is ONE enablement registry and
// ONE resolver (Overlay.visibleTo / .State), never a parallel copy.
//
//	GET  /v1/admin/pricing/enablement          SuperAdmin: the full managed registry
//	PUT  /v1/admin/pricing/enablement          SuperAdmin: set an item off|beta|ga (+ grant orgs)
//	GET  /v1/pricing/enablement                any authed: the caller's EFFECTIVE view + betas
//	POST /v1/pricing/enablement/optin          authed: opt the caller's OWN org into a beta
//	POST /v1/pricing/enablement/optout         authed: opt the caller's own org back out
//
// SECURITY — the two-way crux RED verifies:
//   - Global state is SUPERADMIN only (c.IsAdmin()). A customer/org-admin can
//     never flip an item's off/beta/ga.
//   - Self opt-in is scoped to the VALIDATED caller org (principal.Org — a
//     gateway-minted principal, NOT a raw client X-Org-Id, which SanitizeIdentity
//     restores on the bearer-less path) — so a user can only ever enable their OWN
//     org, and only for a BETA item: the store's OptIn refuses `ga` (already on)
//     and `off` (the kill switch), so an opt-in can never bypass an admin `off`.

import (
	"context"
	"net/http"
	"sort"
	"strings"

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

// adminEnablementBoard is the managed registry, ordered by kind then id.
type adminEnablementBoard struct {
	// Items is every item an operator has set a state on. An item nobody has
	// touched is absent: it is generally available by default.
	Items []adminEnablementItem `json:"items"`
}

// ListEnablement returns every item an operator has set an enablement state on —
// its global state (off, beta or ga) and the orgs granted its beta. An item
// nobody has touched is absent, because an untouched item is generally
// available; the console composes the candidate list from the live catalog.
// SuperAdmin only; every other caller is refused.
func (o ops) adminEnablementList(ctx context.Context, _ *pricingNoInput) (*adminEnablementBoard, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if cat == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "enablement store not initialised")
	}
	snap, err := cat.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "enablement read failed")
	}
	items := make([]adminEnablementItem, 0, len(snap))
	for _, ov := range snap {
		items = append(items, adminEnablementItem{
			Kind: ov.Kind, ID: ov.ID, State: ov.State(), BetaOrgs: ov.BetaOrgs, UpdatedAt: ov.UpdatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
	return &adminEnablementBoard{Items: items}, nil
}

// setEnablementBody is the SuperAdmin PUT payload.
type setEnablementBody struct {
	// Kind is the item's namespace: "model", "provider" or "feature".
	Kind string `json:"kind"`
	// ID is the item within that namespace — a model id, a provider name, or a
	// feature's key.
	ID string `json:"id"`
	// State is the item's global enablement: "off" (hidden from everyone,
	// absolutely), "beta" (visible only to granted orgs) or "ga" (visible to
	// everyone). Required.
	State string `json:"state"`
	// BetaOrgs REPLACES the item's beta grant list when present. Omit it to
	// leave the existing grants alone.
	BetaOrgs *[]string `json:"betaOrgs,omitempty"`
}

// SetEnablement sets one item's global enablement state — off, beta or ga — and
// optionally replaces the list of orgs granted its beta. It is generic over
// kind, so the same call manages models, providers and product features through
// the one registry. `off` is an absolute kill switch: a self-service opt-in can
// never re-open it. SuperAdmin only; every other caller is refused.
//
// Example: {"kind":"feature","id":"labs","state":"beta","betaOrgs":["acme"]}
func (o ops) adminEnablementSet(ctx context.Context, in *setEnablementBody) (*adminEnablementItem, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if cat == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "enablement store not initialised")
	}
	kind := strings.TrimSpace(in.Kind)
	id := strings.TrimSpace(in.ID)
	if !enablementKinds[kind] {
		return nil, zip.ErrBadRequest("kind must be model|provider|feature")
	}
	if id == "" {
		return nil, zip.ErrBadRequest("id required")
	}
	var setEnabled, setBeta bool
	switch strings.ToLower(strings.TrimSpace(in.State)) {
	case "ga", "on", "enabled":
		setEnabled, setBeta = true, false
	case "beta":
		setEnabled, setBeta = false, true
	case "off", "disabled":
		setEnabled, setBeta = false, false
	default:
		return nil, zip.ErrBadRequest("state must be off|beta|ga")
	}
	ov, err := cat.mutate(ctx, kind, id, func(ov *Overlay) error {
		ov.Enabled, ov.Beta = setEnabled, setBeta
		if in.BetaOrgs != nil {
			ov.BetaOrgs = normalizeOrgs(*in.BetaOrgs)
		}
		return nil
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "enablement write failed")
	}
	return &adminEnablementItem{Kind: ov.Kind, ID: ov.ID, State: ov.State(), BetaOrgs: ov.BetaOrgs, UpdatedAt: ov.UpdatedAt}, nil
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

// enablementBoard is the caller's own view of the registry. Field order matches
// the sorted keys of the map it replaces, so the bytes on the wire are unchanged.
type enablementBoard struct {
	// Betas are the subset of Items the caller's org may still opt into.
	Betas []userEnablementItem `json:"betas"`
	// Items is every managed item, each resolved for the caller's org.
	Items []userEnablementItem `json:"items"`
	// Org is the org this view was resolved for; empty for a caller with no
	// validated principal, who sees only the generally-available items.
	Org string `json:"org"`
}

// GetEnablement returns what the caller's org can actually use: every managed
// item with its global state, whether it is effective here, whether this org is
// already opted into its beta, and whether it may still opt in. Read-only and
// safe for any caller — one without a validated principal simply sees the
// generally-available items and no opt-in affordance, never another org's state.
func (o ops) enablementView(ctx context.Context, _ *pricingNoInput) (*enablementBoard, error) {
	if cat == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "enablement store not initialised")
	}
	// The tenant comes from the VALIDATED principal cloud.Bridge parked, never
	// from a field of In or a raw client header: an unvalidated caller (an
	// off-gateway request carrying X-Org-Id with no credential) resolves to ""
	// and sees the public view, not another org's beta state.
	org := callerOrg(ctx)
	snap, err := cat.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "enablement read failed")
	}
	items := make([]userEnablementItem, 0, len(snap))
	betas := make([]userEnablementItem, 0)
	for _, ov := range snap {
		optedIn := org != "" && ov.optedIn(org)
		item := userEnablementItem{
			Kind:      ov.Kind,
			ID:        ov.ID,
			State:     ov.State(),
			Effective: ov.visibleTo(org),
			OptedIn:   optedIn,
			CanOptIn:  ov.State() == "beta" && !optedIn && org != "",
		}
		items = append(items, item)
		if item.CanOptIn {
			betas = append(betas, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Kind+items[i].ID < items[j].Kind+items[j].ID })
	return &enablementBoard{Betas: betas, Items: items, Org: org}, nil
}

// enablementOptRef names the item a self-service opt-in or opt-out acts on. The
// ORG is never a field here: it is the caller's validated org, read off the
// context, so a caller can only ever move their own.
type enablementOptRef struct {
	// Kind is the item's namespace: "model", "provider" or "feature".
	Kind string `json:"kind"`
	// ID is the item within that namespace.
	ID string `json:"id"`
}

// OptIntoBeta opts the caller's OWN org into a beta item. The org is the
// caller's validated one, so this can never target another org, and the registry
// refuses anything not in beta — so it can neither re-open an item an operator
// turned off nor touch one that is already generally available. Requires a
// signed-in caller with an org.
//
// Example: {"kind":"feature","id":"labs"}
func (o ops) enablementOptIn(ctx context.Context, in *enablementOptRef) (*userEnablementItem, error) {
	return enablementOpt(ctx, in, true)
}

// OptOutOfBeta removes the caller's OWN org from a beta item's grant list, the
// reverse of OptIntoBeta and idempotent. The org is the caller's validated one,
// so this can never revoke another org's grant. Requires a signed-in caller with
// an org.
//
// Example: {"kind":"feature","id":"labs"}
func (o ops) enablementOptOut(ctx context.Context, in *enablementOptRef) (*userEnablementItem, error) {
	return enablementOpt(ctx, in, false)
}

func enablementOpt(ctx context.Context, in *enablementOptRef, joining bool) (*userEnablementItem, error) {
	if cat == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "enablement store not initialised")
	}
	// The subject is the VALIDATED caller org (cloud.Bridge parks what
	// principal.Org decided). A raw X-Org-Id would trust the header
	// SanitizeIdentity restores on the bearer-less direct-to-pod path — i.e. an
	// off-gateway caller sending `X-Org-Id: victim` with no credential could opt
	// an org it does not own into (or out of) a beta. Keying on the validated
	// tenant closes that cross-tenant write. (RED MEDIUM.)
	org := callerOrg(ctx)
	if org == "" {
		return nil, zip.ErrUnauthorized("sign in to manage beta features")
	}
	kind := strings.TrimSpace(in.Kind)
	id := strings.TrimSpace(in.ID)
	if !enablementKinds[kind] {
		return nil, zip.ErrBadRequest("kind must be model|provider|feature")
	}
	if id == "" {
		return nil, zip.ErrBadRequest("id required")
	}

	var (
		ov  Overlay
		err error
	)
	if joining {
		ov, err = cat.OptIn(ctx, kind, id, org)
	} else {
		ov, err = cat.OptOut(ctx, kind, id, org)
	}
	if err == errNotBeta {
		return nil, zip.ErrBadRequest("item is not in beta — nothing to opt into")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "opt-in write failed")
	}
	return &userEnablementItem{
		Kind:      ov.Kind,
		ID:        ov.ID,
		State:     ov.State(),
		Effective: ov.visibleTo(org),
		OptedIn:   ov.optedIn(org),
		CanOptIn:  ov.State() == "beta" && !ov.optedIn(org),
	}, nil
}
