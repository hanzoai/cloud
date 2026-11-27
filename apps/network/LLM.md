# LLM.md — apps/network

Guidance for apps/network. Cross-cutting rules stay in the root LLM.md.

## The waitlist keeps only a keyed digest, never an identifier (`access.go`, `waitlist.go`, `access_store.go`)

`POST /v1/network/waitlist` is anonymous and answers 202 `{"status":"pending"}` for a new
address or X handle and for every repeat: each ask reads and upserts the one record, and
only the caller's rate answers 429. Role, source and locale are kept; the identifier is not.

- The brand org's Base collection `network_waitlist` has exactly `id`, `kind`, `role`,
  `source`, `locale` and `created_at` (plus Base's own `collectionId`/`collectionName`).
  There is no `identifier` field and no free-text trace of the value in any row.
- `id` is the first 15 hex characters of HMAC-SHA-256 under the KMS secret
  `network/invite-hmac` (`secret.Sealed`, org `hanzo`, env `prod`) over
  `"waitlist\x00" + kind + "\x00" + normalized identifier`. Email is lower-cased; an X
  handle loses its `@` and is lower-cased for the digest. No wallet address is taken and
  no X handle is looked up.
- The domain prefix keeps a waitlist digest distinct from an invite code's MAC and from
  the derived key used for rate-limit tallies, all under that one key. No second KMS
  secret is minted or needed; before an operator's first issue seals it, the op answers
  503 and mints nothing.
- The key is brand-tenanted, so `door` (access.go) reads it on the SAME brand context
  the Base calls use (`brandContext`, access_store.go): a stranger has no org for a
  plane call to act for, and no header or principal can redirect the read. Only the
  KMS call is addressed to the brand; the SuperAdmin's issue keeps its principal for
  the audit. A KMS fault stays the generic 503, never the "not configured" answer
  and never a body naming the ref or the org.
- Lookups and repeats are by digest, so an idempotent repeat keeps the first row's role,
  source, locale and timestamp. The 202 answer, headers, generic refusals and rate
  limits (10 per caller an hour; hourly deployment count only warns) are unchanged.
- `waitlist_test.go` asserts no identifier in a stored row, an answer or the log
  (`noIdentifierIn`), and pins the digest to the key, the domain and the single KMS ref;
  `invite_test.go`'s `TestNothingSensitiveIsLogged` covers the fault path too.

## Generated artifacts

Handler doc comments are lifted by `go generate ./apps/network` into
`apps/network/zipdoc_gen.go`. `make -f mk/fleet.mk documents` regenerates
`plugin/network/openapi.json`, `openapi.yaml`, `private.yaml`, `client/catalog.json`,
`client/mcp.json` and the skills catalog, and writes `openapi/closure.json`.
`make zipdoc-check`, `make test` and `make -f mk/fleet.mk check` refuse a stale artifact.
