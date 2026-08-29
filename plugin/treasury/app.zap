# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package treasury

struct accountsIn {
    Scope text @0
    Org   text @8
}

struct accountsOut {
    Scope    text        @0
    Tenant   text        @8
    Accounts list<bytes> @16
}

struct anchorOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct journalIn {
    Limit i64 @0
}

struct policyOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct policyRequest {
    RevenueShareBps i64 @0
}

struct seedOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct seedRequest {
    AmountCents i64  @0
    Memo        text @8
    Ref         text @16
}

struct signerOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct sweepOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct sweepRequest {
    Period       text @0
    RevenueCents i64  @8
}

interface treasury {
    # Returns the ledger accounts the caller may see, with their
    # balances. It is tenant-isolated SERVER-SIDE: an ordinary caller sees ONLY
    # accounts under its own "org:<tenant>:" prefix, never house accounts and never
    # another tenant's. A SuperAdmin may widen with ?scope=house (the reserve,
    # revenue and payout house accounts) or ?org=<tenant> — the only way to cross the
    # tenant boundary, and only for platform sudo. The answer is honestly empty until
    # a tenant has ledger postings.
    get_treasury_accounts(req: accountsIn) returns (rep: accountsOut)
    # Commits the current ledger root to Hanzo L1, making the books
    # tamper-evident on chain, and returns the anchoring status. When the chain path
    # is wired it signs and submits the anchor transaction and records it; when it is
    # not, it returns the root that WOULD be committed plus the exact remaining
    # wiring step and records nothing false. A submit that fails still answers 200
    # with the anchor's own status set to "error" — the attempt is the product.
    # SuperAdmin only.
    post_admin_treasury_anchor() returns (rep: anchorOut)
    # Sets the revenue-share basis points a sweep accrues into the
    # reserve fund and returns the stored policy. 0–10000; the change is audited.
    # SuperAdmin only.
    post_admin_treasury_policy(req: policyRequest) returns (rep: policyOut)
    # Injects bootstrap capital into the reserve fund so backed payouts
    # can begin before the first revenue-share sweep, and returns the journal entry
    # it wrote. A repeat of the same ref is at-most-once and reports created=false.
    # SuperAdmin only.
    post_admin_treasury_seed(req: seedRequest) returns (rep: seedOut)
    # Posts the revenue-share accrual for one period — revenue into the
    # reserve fund, at the current policy's basis points — and returns what it moved.
    # It is idempotent per period: a re-run of a period already swept accrues nothing
    # and reports created=false. SuperAdmin only.
    post_admin_treasury_sweep(req: sweepRequest) returns (rep: sweepOut)
    # Installs the reserve's threshold MPC wallet as the signer
    # for on-chain anchors, and returns its EVM address so an operator can fund it
    # for gas. It provisions-or-resolves the caller org's treasury wallet on the
    # deployed MPC ring and installs it, so every later anchor commits the ledger
    # root SIGNED BY THE QUORUM WALLET instead of a lone KMS key. Idempotent — a
    # repeat resolves the same wallet, which is why the address is a PUT. SuperAdmin
    # only.
    put_admin_treasury_anchor_signer() returns (rep: signerOut)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# blocked (2) — the op is absent; the field has no wire form:
#   get_admin_treasury  adminReportOut.Data  treasury.adminReportData  (reaches one)
#   get_treasury  TreasuryReport.ByProgramCents  map[string]int64  (map)
#
# opaque (6) — crosses, arrives without its name:
#   accountsOut.Accounts  treasury.accountView (list element)
#   anchorOut.Data  treasury.anchorData
#   policyOut.Data  treasury.policyData
#   seedOut.Data  treasury.seedData
#   signerOut.Data  treasury.signerData
#   sweepOut.Data  treasury.sweepData
