# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package plan

struct planEntitlements {
    Entitlements    bytes      @0
    ID              text       @8
    LicenseFeatures list<text> @16
}

struct planHealth {
    Service text @0
    Status  text @8
}

struct planList {
    Plans list<bytes> @0
}

struct planRef {
    ID text @0
}

struct planRegionList {
    Regions list<bytes> @0
}

struct planResolution {
    Entitlements    bytes      @0
    ID              text       @8
    LicenseFeatures list<text> @16
    PriceRef        bytes      @24
    TenantID        text       @32
}

struct planSchemas {
    Entitlements bytes @0
    Plan         bytes @8
}

struct planTierList {
    Tiers list<bytes> @0
}

struct planToolList {
    Tools list<bytes> @0
}

interface plan {
    # Returns the Hanzo cloud plan catalog: every cloud tier with its
    # price, included capacity, limits and feature list, scoped to the caller's
    # catalog. A reseller org sees its own overrides in place of the canonical
    # records it has replaced, and the canonical record for every tier it has not.
    get_plan() returns (rep: planList)
    # Returns the blockchain RPC plan catalog: the tiers metered
    # in monthly compute units, with their prices, limits and overage terms. It is
    # the canonical catalog for every caller — these plans carry no reseller
    # overrides.
    get_plan_blockchain() returns (rep: planList)
    # ListDNSPlans returns the DNS plan catalog: the tiers priced on zones, records
    # per zone and queries per day. It is the canonical catalog for every caller —
    # these plans carry no reseller overrides.
    get_plan_dns() returns (rep: planList)
    # Returns what one plan GRANTS and not what it costs: the
    # canonical namespaced entitlement block and the flat license-feature list
    # derived from it. It is the entitlement half of ResolvePlan, over the same
    # catalog and the same 404 for an id no catalog holds — the read a licensing or
    # quota gate makes.
    get_plan_entitlements_by_id(req: planRef) returns (rep: planEntitlements)
    # ListGPUTiers returns the rentable GPU configurations, each with its accelerator
    # count and model, VRAM, vCPUs, host memory and hourly price.
    get_plan_gpu() returns (rep: planTierList)
    # Health reports that the plans subsystem is mounted and serving. It answers from
    # the process itself and consults neither the catalog bundle nor the goja host,
    # so it stays "ok" while either is degraded.
    get_plan_health() returns (rep: planHealth)
    # Returns the published pricing policy: whether pricing is
    # transparent, the revenue-sharing terms (idle compute resale and the open-source
    # share) and the principles the catalog is priced by.
    get_plan_policy()
    # Returns the regions cloud capacity is offered in, each with its
    # display name and physical location.
    get_plan_regions() returns (rep: planRegionList)
    # Resolves one plan to everything a consumer of the catalog needs at
    # once: its canonical entitlement block, the flat license-feature list a signed
    # license carries, its billing reference, and the catalog it came from. The id
    # may be the plan's id or its slug, and it is resolved against the caller's
    # catalog, so a reseller's override wins over the canonical record. An id no
    # catalog holds answers 404.
    get_plan_resolve_by_id(req: planRef) returns (rep: planResolution)
    # Returns the two JSON Schema documents this surface speaks:
    # entitlements.schema.json, which declares every entitlement key with its type,
    # unit and enum, and plan.schema.json, which a catalog plan record conforms to.
    get_plan_schema() returns (rep: planSchemas)
    # Returns the block-storage price block: the price per GB per
    # month and the volume size bounds a cloud plan may attach.
    get_plan_storage()
    # Returns the subscription ladder — the personal and team
    # tiers a customer buys to use the cloud, each with its monthly and annual price,
    # seat rules, limits and billing reference. Scoped to the caller's catalog.
    get_plan_subscriptions() returns (rep: planList)
    # Returns the per-use price of every metered tool — web search,
    # code interpreter, image generation, speech — each with the unit it is billed
    # in.
    get_plan_tools() returns (rep: planToolList)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_plan_vocab  planVocab.Keys  map[string]json.RawMessage  (map)
