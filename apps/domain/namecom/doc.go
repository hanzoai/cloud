// Package namecom is a minimal client for the name.com Core API v4 — the
// wholesale registrar surface Hanzo Domains resells: check availability, price,
// register, renew, transfer, and set nameservers/contacts on a domain.
//
// Contract (name.com Core API v4):
//   - Base URL: https://api.name.com (production), https://api.dev.name.com (test).
//   - Auth: HTTP Basic — username + API token.
//   - Actions use a ":verb" suffix on the collection/resource, e.g.
//     POST /v4/domains:checkAvailability, POST /v4/domains/{domain}:setNameservers.
//
// Credentials are NEVER hard-coded here: the caller passes the username/token it
// read from the platform secret store (KMS), exactly as the Cloudflare purger reads
// CF_API_TOKEN. This package holds no secret custody of its own.
package namecom
