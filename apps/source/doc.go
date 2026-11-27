// Package source is the registry of a person's linked Hanzo Dev sources
// (HIP-1350, slice 1): link, list, read, revoke and heartbeat, served at
// /v1/sources and as the ZAP service source.v1 from the same typed ops.
//
// A source is one installed Dev app linked to one IAM user. Identity is the
// validated token alone — the org and the person never come from a body — and a
// machine token is refused. Reads and revokes are the owner's: an org admin and a
// SuperAdmin have no read path to another person's source. A heartbeat is also
// signed by the source's own device key, so a stolen token cannot impersonate an
// install.
//
// Credentials do not move. The store has no secret column; a heartbeat carries
// provider STATE (ready, needs_login, limited), never a token, email or key.
//
// Usage: a source reports the Dev usage model (per-day tokens and cost by model,
// latest limit windows, credits and status per provider) to POST /v1/sources/{id}/usage,
// signed by its device key; GET /v1/sources/usage totals a person's own rows, and an
// org admin sees per-person totals and nothing more.
//
// Provider accounts, at /v1/sources/accounts: a person's API key is sealed into KMS
// and the row holds only the path, a label and a fingerprint; a login is derived from
// a source's heartbeat and is read-only. policy.go is the route class of each.
package source
