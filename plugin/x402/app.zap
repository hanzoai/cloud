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
    SettledAt  i64  @88
}

struct settlementRef {
    ID text @0
}

interface x402 {
    # Settlement reads one x402 payment receipt by id.
    # It is scoped to the caller's PAYER org — the ledger that was debited — so one
    # tenant can never read another's settlement, and an id that exists but belongs
    # to somebody else is a 404 exactly like one that does not exist. A caller with
    # no billable identity is refused outright.
    get_x402_settlements_by_id(req: settlementRef) returns (rep: Receipt)
}
