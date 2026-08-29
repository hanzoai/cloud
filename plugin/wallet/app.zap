# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package wallet

struct Wallet {
    ID             text  @0
    Scope          bytes @8
    Name           text  @16
    Custody        text  @24
    Tier           text  @32
    Chain          text  @40
    Address        text  @48
    KeyRef         text  @56
    FinanceAccount text  @64
    CreatedAt      i64   @72
}

struct WalletAccount {
    ID        text @0
    Org       text @8
    Name      text @16
    CreatedAt i64  @24
}

struct accountList {
    Accounts list<bytes> @0
}

struct createAccountIn {
    Name text @0
}

struct createWalletIn {
    AccountID text @0
    Agent     text @8
    Name      text @16
    Custody   text @24
    Tier      text @32
    Chain     text @40
}

struct listWalletsIn {
    Project text @0
    Agent   text @8
    Account text @16
}

struct safeProposal {
    R           text @0
    S           text @8
    SafeAddress text @16
    SafeTxHash  text @24
    WalletID    text @32
}

struct safeTxIn {
    ID      text @0
    To      text @8
    Value   text @16
    Data    text @24
    ChainID i64  @32
    Nonce   i64  @40
}

struct signIn {
    ID      text @0
    Message text @8
    Digest  text @16
}

struct signature {
    Address   text @0
    Digest    text @8
    Signature text @16
    WalletID  text @24
}

struct walletList {
    Wallets list<bytes> @0
}

struct walletRef {
    ID text @0
}

interface wallet {
    # Returns the caller org's wallets, newest first, optionally NARROWED
    # within the org by project, agent or account. The org is always the bound
    # isolation boundary — the filters only ever narrow inside it, so a caller can
    # never widen past its own org.
    get_wallet(req: listWalletsIn) returns (rep: walletList)
    # Returns the caller org's wallet accounts, newest first. Accounts
    # are physically org-scoped, so another tenant's are not reachable from here.
    get_wallet_accounts() returns (rep: accountList)
    # Returns one of the caller org's wallets: its scope, custody kind,
    # tier, chain and on-chain address. The custody handle to the signing material is
    # never part of the answer. A wallet id another org owns reads as not found, so
    # the response cannot confirm that it exists.
    get_wallet_by_id(req: walletRef) returns (rep: Wallet)
    # Provisions a new signing identity under one of the caller org's
    # accounts and answers the stored wallet including its on-chain address. The
    # custody backend generates the key material — a KMS-sealed secp256k1 key, an
    # MPC threshold key on the ring, or a Safe smart wallet owned by one — and the
    # HANDLE to it is kept server-side and never returned. A custody kind the
    # deployment has not wired fails CLOSED with 503: a signature is never
    # fabricated. The wallet is scoped to the org, the caller's ambient project, and
    # optionally an agent and the named account; those narrowings are what its key
    # ref is derived from, so each must be a url-safe segment.
    post_wallet(req: createWalletIn) returns (rep: Wallet)
    # Opens a named wallet account for the caller's org. An account is
    # a GROUPING of wallets, not a key or a balance: wallets are created under one
    # and can be listed by it. The org is stamped by the server from the validated
    # principal, so a request can never open an account in another tenant.
    post_wallet_accounts(req: createAccountIn) returns (rep: WalletAccount)
    # Rolls one wallet's signing material through its own custody backend
    # and answers the wallet with whatever address that produced. For KMS custody a
    # fresh secp256k1 key is generated and sealed, which CHANGES the address — funds
    # and approvals at the old address do not move. For a Safe the address is
    # counterfactual and the owner shares are ring-managed, so rotation is a no-op
    # and the address is unchanged. A backend that is not configured fails closed
    # with 503 rather than leaving the wallet half-rotated.
    post_wallet_by_id_keys(req: walletRef) returns (rep: Wallet)
    # Produces a secp256k1 signature from one of the caller org's wallets over
    # a 32-byte digest, through whichever custody backend that wallet uses. Give it
    # either a `digest` (32 bytes as hex, signed verbatim) or a `message` (hashed
    # with Keccak256 first) — exactly one is required. The private key never leaves
    # its backend: KMS custody opens the sealed key in-process, MPC custody produces
    # a threshold signature on the ring. The answer carries the digest that was
    # signed alongside the signature, so a caller can verify what it got.
    post_wallet_by_id_sign(req: signIn) returns (rep: signature)
    # Composes a Safe transaction on the MPC ring and answers its
    # EIP-712 hash together with the owner approval the ring's threshold signature
    # produced. Only a wallet whose custody is "safe" can do this — any other custody
    # is a 400, because the backend itself is asked whether it can propose rather
    # than the kind being switched on. The ring computes the Safe-tx hash bound to
    # the Safe contract and the chain id, so the hash a caller gets back is the one
    # the Safe will verify. This PROPOSES: it does not execute the transaction.
    post_wallet_by_id_transactions(req: safeTxIn) returns (rep: safeProposal)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   Wallet.Scope  wallet.Scope
#   accountList.Accounts  wallet.WalletAccount (list element)
#   walletList.Wallets  wallet.Wallet (list element)
