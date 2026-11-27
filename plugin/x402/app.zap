# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package x402

struct Receipt {
    ID         text @0
    Resource   text @8
    Payer      text @16
    From       text @24
    Payee      text @32
    PayeeOrg   text @40
    Amount     text @48
    Nonce      text @56
    Network    text @64
    SettledVia text @72
    TxHash     text @80
    Category   text @88
    SettledAt  i64  @96
}

struct priceTable {
    X402Version i64         @0
    Prices      list<bytes> @8
}

struct productRef {
    Product text @0
}

struct quoted {
    Error text  @0
    Fee   bytes @8
}

struct settlementList {
    Settlements list<bytes> @0
}

struct settlementRef {
    ID text @0
}

struct settlementsQuery {
    Role text @0
    Year i64  @8
}

interface x402 {
    # Lists the flat fees this deployment sells per request over x402: one
    # function invoke, one agent run, one provisioned resource of each kind. Each row
    # is the route's own fee knob, paid into the platform's wallet, and carries the
    # exact terms that route's 402 challenge states, so a client can sign before it
    # is refused.
    # A payment is made on the route itself: the client calls it with the signed
    # PAYMENT-SIGNATURE header, is served once, and reads the receipt on
    # PAYMENT-RESPONSE. The payer must be signed in, and the payment is drawn from
    # its Hanzo wallet, which must cover the fee. The payment is settled only once the
    # call is done and billable, so a call that fails costs nothing. A fee whose knob
    # is zero is free and is not listed; with no platform wallet configured the table
    # is empty.
    get_x402_prices() returns (rep: priceTable)
    # Answers one fee's x402 challenge: always 402, with the PAYMENT-REQUIRED
    # header the fee's route sends an unfunded caller, and the same row the price
    # table lists as the body.
    # It sells nothing itself. A payment sent here is never settled; it is made on
    # the route the row names, where it buys one call. An unknown product, or one
    # not sold here, is 404.
    get_x402_prices_by_product(req: productRef) returns (rep: quoted)
    # Lists the caller's x402 receipts, newest first: as payer, what its
    # wallet paid; as payee, what its org was paid — each settled payment with what it
    # bought, both parties, the exact amount and when. A payer is the caller's own
    # account, which for a SuperAdmin inspecting another org is still its own and for
    # a member of the signup org is their own wallet, never the org's; a payee is the
    # caller's own org. An unsettled claim is not a receipt and is never listed.
    get_x402_settlements(req: settlementsQuery) returns (rep: settlementList)
    # Reads one x402 payment receipt by id.
    # It is scoped to the caller's own account — the wallet that was drawn, in the
    # books that hold it — so one tenant can never read another's settlement, nor one
    # member of the signup org another's, and an id that exists but belongs to
    # somebody else is a 404 exactly like one that does not exist. A caller with no
    # billable identity is refused outright.
    get_x402_settlements_by_id(req: settlementRef) returns (rep: Receipt)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   priceTable.Prices  x402.priceRow (list element)
#   quoted.Fee  x402.priceRow
#   settlementList.Settlements  x402.Receipt (list element)
