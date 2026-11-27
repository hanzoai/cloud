// Package token mints and verifies the HS256 JWTs that the team SPA, the
// /v1/team/account API and the /v1/team/transactor data plane all share.
//
// The wire format is byte-compatible with the platform `jwt-simple` HS256 tokens
// (foundations/core/packages/token/src/token.ts): header
// {"typ":"JWT","alg":"HS256"}, a COMPACT JSON payload whose keys appear in
// the fixed order {extra, account, workspace, sub, exp, nbf} with
// undefined/empty keys omitted, base64url WITHOUT padding, and a raw
// HMAC-SHA256 signature over `header.payload`.
//
// The third claim is spelled `workspace` because the TypeScript implementation
// above mints and verifies the same bytes, and the key ORDER is part of the
// signature: a token this package signed as `space` verifies nowhere, and every
// token already issued would stop verifying here. The Go field is Space — the
// estate's word — and the tag pins the wire. They converge when both sides ship
// the new spelling together.
//
// account and workspace, when present, MUST be UUIDs — the upstream
// `generateToken` throws otherwise and the transactor client refuses a
// space token that is missing either, so we enforce the same invariant
// at mint time.
//
// The secret is the shared `SERVER_SECRET` (synced from KMS in production).
// One secret, one wire format — every team surface signs and verifies through
// this package. Ported VERBATIM from github.com/hanzoai/team/pkg/token.
package token
