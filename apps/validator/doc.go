// Package validators is one-click validator onboarding: prove your Genesis NFT,
// get a node provisioned, queue its registration.
//
// The /v1/validator/* routes are the "click → provision node + queue
// registration" pipeline behind GDA/SDM validator onboarding on lux.cloud.
//
// The end-to-end claim, all server-enforced at cloud's ONE auth boundary
// (SanitizeIdentity → principal.Org):
//
//  1. GET  /v1/validator/challenge?tokenId=N  → a single-use, org-bound nonce +
//     the exact message to personal_sign.
//  2. POST /v1/validator {tokenId,nonce,signature} → verify the wallet controls
//     the signature AND holds Validator-tier GenesisNFT #tokenId on Ethereum
//     mainnet (ownerOf), then: generate a luxd staking identity → seal it into
//     KMS (never plaintext) → write a LuxNetwork CR for a NEW node (never the
//     live luxd) → ENQUEUE an owner-gated registration (NEVER auto-submitted to
//     any P-Chain). Returns the slot + node + registration status.
//  3. GET  /v1/validator            → the org's claimed slots + node status.
//  4. GET  /v1/validator/:tokenId   → one slot's detail.
//
// Tenant isolation is the org (principal.Org — the VALIDATED IAM owner, never a
// client header); every store query filters WHERE org=?. The tokenId IS the
// validator slot. serve.go auto-registers GET /v1/validator/health.
package validator
