# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package validator

struct challengeIn {
    TokenID text @0
}

struct challengeView {
    ExpiresAt  i64  @0
    Message    text @8
    Nonce      text @16
    TokenID    u64  @24
    TTLSeconds i64  @32
}

struct listIn {
    Limit text @0
}

struct slotRef {
    TokenID text @0
}

struct slotView {
    BLSPubkey    text  @0
    CRName       text  @8
    CreatedAt    i64   @16
    Namespace    text  @24
    Network      text  @32
    NodeID       text  @40
    NodeStatus   text  @48
    Registration bytes @56
    Slot         u64   @64
    TokenID      u64   @72
    UpdatedAt    i64   @80
    Wallet       text  @88
}

struct validatorClaim {
    TokenID   u64  @0
    Nonce     text @8
    Signature text @16
}

struct validatorList {
    Data    list<bytes> @0
    Network text        @8
}

interface validator {
    # Returns the validator slots the caller's org has claimed.
    # One entry per claimed slot with its node identity, its live-ish node status and
    # the owner-gated registration queued for it, if any. Slots are org-scoped by the
    # validated identity, so a caller can only ever see their own — a slot claimed by
    # another org is not merely hidden from this list, it is unreachable through the
    # whole surface.
    get_validator(req: listIn) returns (rep: validatorList)
    # Returns one claimed validator slot, scoped to the caller's org.
    # A slot another org holds, and a slot nobody holds, are both 404 — never a
    # different status, so this route cannot be used to probe which slots are taken.
    get_validator_by_tokenid(req: slotRef) returns (rep: slotView)
    # Issues the single-use nonce and the exact message a wallet must sign
    # to claim a validator slot.
    # The nonce is bound to (validated org, slot) and stored server-side, so a
    # signature obtained for one org or one slot can never be replayed for another,
    # and the message POST /v1/validator verifies is rebuilt from those same server
    # facts rather than trusted from the caller. Redeem it with
    # POST /v1/validator before it expires; it can be redeemed once.
    # A tokenId outside the Validator tier is refused here rather than after signing.
    get_validator_challenge(req: challengeIn) returns (rep: challengeView)
    # Claims a validator slot and provisions its node, after proving the
    # caller's wallet owns the slot's NFT.
    # The pipeline, all server-enforced: burn the single-use challenge (so a replayed
    # or forged nonce dies before any chain read), recover the signer from the message
    # this server rebuilds, require that wallet to hold Validator-tier GenesisNFT
    # #tokenId on Ethereum mainnet, generate a fresh luxd staking identity and seal it
    # into KMS, write a LuxNetwork CR for a NEW node, and ENQUEUE an owner-gated
    # registration. The registration is never auto-submitted to any P-Chain — the
    # owner co-signs it out of band — and the stake weight is set at co-sign time,
    # never derived from the NFT.
    # It fails CLOSED at every gate: a bad signature, a non-owner, a non-tier slot or
    # an unavailable KMS all leave no claim persisted and no key material exposed.
    # Re-claiming a slot this org already holds re-applies the node CR and returns 200
    # with the existing identity (keys and NodeID are stable); a slot held by another
    # org is 409. A cluster-less deployment still claims the slot, seals the keys and
    # queues the registration, reporting the node as "node_pending".
    post_validator(req: validatorClaim) returns (rep: slotView)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   slotView.Registration  validator.registrationView
#   validatorList.Data  validator.slotView (list element)
