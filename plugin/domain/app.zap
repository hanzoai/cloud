# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package domain

struct RegisterResult {
    Holding bytes @0
    Offer   bytes @8
}

struct RenewResult {
    Holding   bytes @0
    PaidCents i64   @8
}

struct availabilityQuery {
    Domain text @0
}

struct holdings {
    Domains list<bytes> @0
}

struct order {
    Domain   text  @0
    Years    i64   @8
    Contacts bytes @16
}

struct quoteList {
    Results list<bytes> @0
}

struct reachability {
    Service    text @0
    Registrar  text @8
    Env        text @16
    Status     text @24
    Configured bool @32
    Reachable  bool @33
    Error      text @40
}

struct renewReq {
    Domain text @0
    Years  i64  @8
}

struct searchQuery {
    Q   text @0
    TLD text @8
}

struct transferReq {
    Domain   text @0
    AuthCode text @8
    Years    i64  @16
}

interface domain {
    # Checks exact names rather than searching for them, and answers the
    # same quote shape search does — purchasable, premium, first-term and renewal price
    # in cents.
    # It requires a validated principal; 403 without one. Nothing is charged and
    # nothing is held. A deployment with no registrar credentials answers 503.
    get_domain_availability(req: availabilityQuery) returns (rep: quoteList)
    # Is the domains your org has bought here, newest registration first, each
    # carrying the name, when it was registered, when it expires, what the org paid,
    # the registrar order id and the nameservers it points at.
    # Scoped to the validated principal's org — 403 without one, and there is no
    # parameter that reaches another org's holdings.
    # This is the deployment's OWN ownership record, not a query to the registrar: it
    # lists what was bought THROUGH this surface, so a domain the org holds elsewhere
    # is not here. The default store is in-process, so a deployment that has not
    # swapped in a durable store answers from what this process registered.
    get_domain_domains() returns (rep: holdings)
    # Reports registrar reachability honestly: ok only when the wholesale
    # credentials are present AND name.com accepted them on a live call made while you
    # waited.
    # Missing credentials or an unreachable registrar is 503 carrying configured,
    # reachable and the reason, so an operator reads the blocker instead of guessing at
    # it. It takes no principal, like every subsystem health probe.
    get_domain_health() returns (rep: reachability)
    # Finds names built from the keyword q, plus the registrar's alternate-TLD
    # suggestions, and answers a quote for each: the name, whether it is purchasable,
    # whether it is premium, the first-term and renewal price in cents, and the TLD.
    # Prices are RETAIL — this deployment's markup is already applied and the wholesale
    # cost is never on the wire.
    # It requires a validated principal; 403 without one. Nothing is charged and
    # nothing is held — a quote is not a reservation, and the price is re-quoted at
    # purchase, so a name quoted here can be gone or dearer by the time you buy it. A
    # deployment with no registrar credentials answers 503.
    get_domain_search(req: searchQuery) returns (rep: quoteList)
    # Buys a domain for your org and answers the ownership record together
    # with the quote it was bought at.
    # The order of operations is the product guarantee: quote, refuse anything
    # unpurchasable or unpriced, AUTHORIZE the org's prepaid balance, provision the
    # authoritative zone in Hanzo DNS, register at the registrar already pointing at
    # Hanzo's nameservers, and only then CAPTURE the charge and record ownership. A
    # registrar failure therefore leaves the balance untouched — the org is never
    # billed for a domain it did not get.
    # It requires a validated principal; that principal's org owns the domain and is
    # the ledger the charge lands on. Re-buying a name the org already holds is 409,
    # not a second purchase.
    # Refusals are distinct on purpose: 402 when the prepaid balance cannot cover the
    # quoted price, 409 when the name is not available, 503 when the deployment has no
    # registrar credentials, and the registrar's own message with its own 4xx — or 502
    # for its 5xx — when it rejects the purchase. Zone provisioning is best-effort: if
    # the zone service is down the domain is still registered against Hanzo's
    # nameservers and the zone reconciles afterwards, rather than the purchase failing.
    post_domain_register(req: order) returns (rep: RegisterResult)
    # Extends a domain your org already owns and answers the updated record with
    # its new expiry alongside what was paid.
    # Ownership is the gate: a name the caller's org does not hold is 404, so a renewal
    # can never reach another tenant's domain.
    # The price is re-quoted at the CURRENT renewal rate rather than the one paid at
    # purchase. If the registrar returns no renewal price the org's original price is
    # charged instead, so a renewal is never accidentally free. The balance is
    # authorized before the registrar is called and captured after it confirms — 402
    # when the prepaid balance cannot cover it, 503 when the deployment has no
    # registrar credentials. Requires a validated principal.
    post_domain_renew(req: renewReq) returns (rep: RenewResult)
    # Moves a domain you own at another registrar onto your org here, using
    # its authCode, and answers the same record-plus-quote a purchase does.
    # It is priced and charged exactly like a registration: authorize the org's prepaid
    # balance, ask the registrar for the transfer, capture only after the registrar
    # accepts. A name the registrar will not price is 409, an insufficient balance is
    # 402, and a deployment with no registrar credentials is 503.
    # It requires a validated principal; the ownership record is written under that org
    # as soon as the registrar ACCEPTS the request, which is not the same instant the
    # transfer completes at the losing registrar. Unlike a registration this does not
    # provision a zone, so the record carries this deployment's configured nameservers.
    post_domain_transfer(req: transferReq) returns (rep: RegisterResult)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   RegisterResult.Holding  domain.Holding
#   RegisterResult.Offer  domain.Offer
#   RenewResult.Holding  domain.Holding
#   holdings.Domains  domain.Holding (list element)
#   order.Contacts  domain.Contacts
#   quoteList.Results  domain.Offer (list element)
