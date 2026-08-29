# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package pricing

struct Overlay {
    Kind      text       @0
    ID        text       @8
    Enabled   bool       @16
    Beta      bool       @17
    BetaOrgs  list<text> @24
    Overrides bytes      @32
    UpdatedAt i64        @40
}

struct adminEnablementBoard {
    Items list<bytes> @0
}

struct adminEnablementItem {
    Kind      text       @0
    ID        text       @8
    State     text       @16
    BetaOrgs  list<text> @24
    UpdatedAt i64        @32
}

struct enablementBoard {
    Betas list<bytes> @0
    Items list<bytes> @8
    Org   text        @16
}

struct enablementOptRef {
    Kind text @0
    ID   text @8
}

struct modelPatchIn {
    ID text @0
}

struct pricingHealth {
    Service text @0
    Status  text @8
}

struct pricingModelRef {
    Name text @0
}

struct pricingSyncOut {
    Status  text @0
    Updated text @8
}

struct providerPatchIn {
    Name text @0
}

struct setEnablementBody {
    Kind     text       @0
    ID       text       @8
    State    text       @16
    BetaOrgs list<text> @24
}

struct userEnablementItem {
    Kind      text @0
    ID        text @8
    State     text @16
    Effective bool @24
    OptedIn   bool @25
    CanOptIn  bool @26
}

interface pricing {
    # Returns every item an operator has set an enablement state on —
    # its global state (off, beta or ga) and the orgs granted its beta. An item
    # nobody has touched is absent, because an untouched item is generally
    # available; the console composes the candidate list from the live catalog.
    # SuperAdmin only; every other caller is refused.
    get_admin_pricing_enablement() returns (rep: adminEnablementBoard)
    # Returns the whole pricing catalog in one document: Zen and
    # third-party models, providers, model families, the free-model list, plan and
    # infrastructure pricing. Every model and provider it names is filtered to what
    # the caller's org may see — the same gate the leaf routes apply, so this can
    # never be an un-gated second source for what they hide.
    get_pricing()
    # Returns the public cloud section of the catalog in one
    # document: its instance plans, its regions and its block-storage prices. The
    # section's internal half — the provider costs Hanzo pays and the plan-to-
    # provider routing table — is stripped before it is served, so this is what a
    # customer may see and nothing more.
    get_pricing_cloud()
    # Returns the block-storage prices of the cloud
    # section: the per-GB monthly rate and the volume size bounds a caller may ask
    # for.
    get_pricing_cloud_storage()
    # Returns the compute section of the catalog: the cloud
    # provider and region the prices are quoted for, the monthly markup applied to
    # them, the full instance-size tier list and the named presets. It is the
    # whole section as the pricing source records it, un-gated — no model or
    # provider identity appears in it.
    get_pricing_compute()
    # Returns the Hanzo Datastore rate card: the tier list, the
    # per-GB storage and egress usage rates, the annual discount and the trial. It is
    # the section as authored, un-gated — no provider identity appears in it.
    # The route was missing while the data existed, so this 404d and every visitor to
    # hanzo.ai's Infrastructure tab was told pricing was "temporarily unavailable".
    get_pricing_datastore()
    # Returns what the caller's org can actually use: every managed
    # item with its global state, whether it is effective here, whether this org is
    # already opted into its beta, and whether it may still opt in. Read-only and
    # safe for any caller — one without a validated principal simply sees the
    # generally-available items and no opt-in affordance, never another org's state.
    get_pricing_enablement() returns (rep: enablementBoard)
    # Health reports that the pricing subsystem is mounted and serving. It answers
    # from the process itself and consults neither the catalog bundle nor the
    # enablement store, so it stays "ok" while either is degraded.
    get_pricing_health() returns (rep: pricingHealth)
    # Returns one model's catalog entry — its pricing, context window and
    # capabilities as the pricing source records them. A model hidden for the
    # caller's org answers the same 404 an unknown name does, so a disabled model
    # gets no existence oracle.
    get_pricing_model_by_name(req: pricingModelRef)
    # Returns the pricing policy document: the revenue-sharing
    # terms (the idle-resale share and the open-source share, each with its
    # percentage and who is eligible) and the commitments Hanzo makes about how it
    # bills — no hidden fees, no egress charges, no surprise bills.
    get_pricing_policy()
    # Returns the managed-service rate cards — Search, Crawl,
    # Vector, Console and Managed Services — each with its own tiers, and some with
    # usage rates or a comparison table. It is the section as authored, un-gated.
    # These are DISPLAY rate cards: what a product costs, not what a plan grants. No
    # entitlement or limit fields ride here, so nothing can bill off them.
    get_pricing_services()
    # Returns the catalog's headline statistics — model counts by
    # family and the provider directory. The provider sub-object is filtered to what
    # the caller's org may see, so a disabled provider's name never leaks; the
    # aggregate counts are the catalog's own, over everything it holds.
    get_pricing_summary()
    # Turns one model off, into beta for named orgs, or generally available.
    # Sets one model's availability overlay — and the price overrides applied on top
    # of the catalog — then answers the new effective overlay, so a console needs no
    # second read. The model id is the whole remaining path, so a slashed id like
    # `acme/some-model-1` addresses intact.
    # SuperAdmin only; every other caller is 403. The overlay is PLATFORM-WIDE —
    # this is the catalog every org prices against, not a per-org setting — and
    # `betaOrgs` is what narrows a beta to named orgs.
    # Only the fields the patch names change; an entry with no overlay yet starts
    # from the catalog default, which is enabled. `state` is the coherent tri-state
    # setter (`off`|`beta`|`ga`) and the low-level `enabled`/`beta` flags are applied
    # AFTER it, so they win where both are sent; anything else in `state` is 400. A
    # field sent as an explicit `null` arrives indistinguishable from an absent one,
    # so null does not clear anything.
    # The rule worth reading twice: a disabled entry that still carries beta orgs IS
    # a beta — `{"enabled":false,"betaOrgs":["acme"]}` leaves acme seeing the model.
    # Only an explicit `off` (or `beta:false`) with an empty list is the absolute
    # kill switch that a user's own beta opt-in can never re-open.
    # `overrides` is an RFC 7386 merge patch, stored and echoed back verbatim; it
    # must be a JSON object or null — an array or a scalar is refused — and is
    # bounded in size and nesting depth. An uninitialised overlay store answers 503.
    patch_admin_pricing_catalog_models_by_wildcard1(req: modelPatchIn) returns (rep: Overlay)
    # Sets one provider's availability overlay.
    # The overlay decides whether a provider is off, in beta for named orgs, or
    # generally available, and carries the price overrides applied on top of the
    # catalog. Only the fields the patch names change; every other field keeps the
    # value it had, and an absent overlay starts from the catalog default (enabled).
    # Answers the new effective overlay, so a console needs no second read.
    # SuperAdmin only.
    patch_admin_pricing_catalog_providers_by_name(req: providerPatchIn) returns (rep: Overlay)
    # Opts the caller's OWN org into a beta item. The org is the
    # caller's validated one, so this can never target another org, and the registry
    # refuses anything not in beta — so it can neither re-open an item an operator
    # turned off nor touch one that is already generally available. Requires a
    # signed-in caller with an org.
    post_pricing_enablement_optin(req: enablementOptRef) returns (rep: userEnablementItem)
    # Removes the caller's OWN org from a beta item's grant list, the
    # reverse of OptIntoBeta and idempotent. The org is the caller's validated one,
    # so this can never revoke another org's grant. Requires a signed-in caller with
    # an org.
    post_pricing_enablement_optout(req: enablementOptRef) returns (rep: userEnablementItem)
    # Refreshes the third-party section of the catalog from its upstream
    # listings and returns the time the refreshed catalog was stamped with. The
    # fetch runs in Go and the markup transform in the pricing bundle. SuperAdmin
    # only; every other caller is refused.
    post_pricing_sync() returns (rep: pricingSyncOut)
    # Sets one item's global enablement state — off, beta or ga — and
    # optionally replaces the list of orgs granted its beta. It is generic over
    # kind, so the same call manages models, providers and product features through
    # the one registry. `off` is an absolute kill switch: a self-service opt-in can
    # never re-open it. SuperAdmin only; every other caller is refused.
    put_admin_pricing_enablement(req: setEnablementBody) returns (rep: adminEnablementItem)
}

# ---------------------------------------------------------------------
# 18 op(s) here. What follows is what this schema does not carry.
#
# dropped (2) — the value does not cross, and nothing fails:
#   modelPatchIn.overlayPatch  pricing.overlayPatch  (promoted, not carried)
#   providerPatchIn.overlayPatch  pricing.overlayPatch  (promoted, not carried)
#
# blocked (19) — the op is absent; the field has no wire form:
#   get_admin_pricing_catalog  adminCatalogOut.Models  []pricing.Model  (no wire form)
#   get_admin_pricing_catalog  adminCatalogOut.Providers  map[string]interface {}  (map)
#   get_admin_pricing_catalog  adminCatalogOut.Updated  interface {}  (any)
#   get_pricing_base  pricingPlanList.Plans  []pricing.pricingBlob  (no wire form)
#   get_pricing_blockchain  pricingPlanList  pricing.pricingPlanList  (reaches one)
#   get_pricing_cloud_plans  pricingPlanList  pricing.pricingPlanList  (reaches one)
#   get_pricing_cloud_regions  pricingRegionList.Regions  []pricing.pricingBlob  (no wire form)
#   get_pricing_compute_presets  pricingPresetList.Presets  []pricing.pricingBlob  (no wire form)
#   get_pricing_featured  pricingModelList.Models  []pricing.Model  (no wire form)
#   get_pricing_featured  pricingModelList.Updated  interface {}  (any)
#   get_pricing_free  pricingModelList  pricing.pricingModelList  (reaches one)
#   get_pricing_gpu  pricingTierList.Tiers  []pricing.pricingBlob  (no wire form)
#   get_pricing_iam  pricingPlanList  pricing.pricingPlanList  (reaches one)
#   get_pricing_models  pricingModelList  pricing.pricingModelList  (reaches one)
#   get_pricing_paas  pricingPlanList  pricing.pricingPlanList  (reaches one)
#   get_pricing_providers  pricingProviderList.Providers  map[string]interface {}  (map)
#   get_pricing_providers  pricingProviderList.Updated  interface {}  (any)
#   get_pricing_subscriptions  pricingPlanList  pricing.pricingPlanList  (reaches one)
#   get_pricing_tools  pricingToolList.Tools  []pricing.pricingBlob  (no wire form)
#
# opaque (3) — crosses, arrives without its name:
#   adminEnablementBoard.Items  pricing.adminEnablementItem (list element)
#   enablementBoard.Betas  pricing.userEnablementItem (list element)
#   enablementBoard.Items  pricing.userEnablementItem (list element)
