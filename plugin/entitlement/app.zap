# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package entitlement

struct entitlementsView {
    Enabled list<text> @0
}

struct mutateReq {
    Org    text       @0
    Add    list<text> @8
    Remove list<text> @16
}

struct orgRef {
    Org text @0
}

interface entitlement {
    # Get lists the products an org has ENABLED — its own intent, which the console's
    # paid-product sidebar reads to decide what to show. It is distinct from what the
    # org's plan ENTITLES it to (that is GET /v1/entitlement, resolved from commerce).
    # A caller may only read its OWN org's row; a platform super admin may read any.
    get_entitlement_orgs_by_org(req: orgRef) returns (rep: entitlementsView)
    # Post turns products on or off for an org and returns the enabled set afterwards.
    # A product may only be ENABLED if the org's plan already ENTITLES it, so enabling
    # never spends new money — a product the plan does not grant answers 402 and the
    # console routes that to an upgrade prompt. DISABLING is never gated. A platform
    # super admin bypasses the plan check (operator comp/grant) and may target any org;
    # everyone else may only change their own. Commerce unreachable is a 503, never an
    # implicit yes.
    post_entitlement_orgs_by_org(req: mutateReq) returns (rep: entitlementsView)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_entitlement  projectionView.Apps  map[string]bool  (map)
