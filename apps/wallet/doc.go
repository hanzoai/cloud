// Package wallets is blockchain key custody: create wallets, rotate their keys, and
// sign with them.
//
// It serves /v1/wallet/* over one custody client with FOUR interchangeable signing
// backends, selected PER WALLET by its Kind.
//
// TOPOLOGY (HIP-0106). Custody composes the canonical Hanzo key services without
// fusing either into the hot binary:
//
//   - KMS single-sig (KindKMS) is IN-PROCESS via the embedded luxfi/kms client
//     (deps.KMS, a cloud.Store). This is the fully-exercised spine: a real
//     secp256k1 key is generated, its private bytes sealed under the KMS
//     envelope, and every Sign recovers to the wallet address. No network hop.
//
//   - MPC m-of-n (KindMPC), treasury named-signer governance (KindTreasury) and the
//     Safe smart wallet owned by an MPC EOA (KindSafe) DELEGATE over HTTP to the
//     DEPLOYED luxfi/mpc cluster (mpcclient.go, safeclient.go), thin typed REST
//     clients. cloud is a faithful CLIENT of the real service — it never imports
//     github.com/luxfi/mpc (that drags chi/Postgres/HSM/webauthn into the binary).
//     When the cluster is not configured these
//     backends fail CLOSED (ErrMPCNotConfigured); a signature is NEVER fabricated.
//
// It is the recipient side of x402: a priced resource's payee resolves through
// ResolvePaymentTarget here, and x402 settles against that wallet's ledger account.
//
// The client is the whole point: swapping a wallet's custody is a config value on
// one row, not a code path. custody.go owns the interface + the four backends;
// mpcclient.go + safeclient.go own the wire; store.go owns persistence; wallets.go
// owns HTTP.
package wallet
