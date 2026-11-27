# LLM.md — apps/account

Guidance for apps/account. Moved from the root LLM.md; cross-cutting rules stay in the root.

## API keys are ONE noun (`/v1/account/keys`): many per person, each its own policy

`apps/account/keys.go`. Every key is its own credential; creating one never touches
another. `mint`, `issue` and `revoke` are HTTP methods, never path segments.

```
GET    /v1/account/keys  -> {keys:[{id,name,type,prefix,key?,status,creator,created,expires?,
                             revoker?,revoked?,limit?,budget?,spend?,rate?,used?}]}
POST   /v1/account/keys {name,type,limit,expires,budget,rate} -> the key, its secret in `key` ONCE
PATCH  /v1/account/keys/{id} {name?,limit?,expires?,budget?,rate?} -> the key; the secret is unchanged
DELETE /v1/account/keys/{id}            -> the key, status "revoked"
```

**Budget, rate and spend.** `budget {month,total}` (cents) and `rate` (requests a
minute) ride the grant as `spend:month:N`, `spend:total:N`, `rate:minute:N`
(`Grant.Budget`, `Grant.Rate`, `Grant.Policy`); `Reach()` leaves them out and
`ParseLimit` refuses them as a `limit`. The boundary writes `X-Key-Id` as
`<org>/<id>` (the org the key is filed in and its row name; stripped on ingress) and
mints the id on the principal (`principal.Key`). The rate binds in `ScopeRateLimit`,
in a `key|<org>|<id>` bucket before every org rule and exemption. The budget and the
key's `model:` reach bind in `limits.Limiter.Key` (`apps/ai/limits/keys.go`), which
apps/ai's `limitAsk` and apps/zen's `limitGate` both ask first: 402 `key_budget`
naming the budget and when it resets, 402 `key_model` naming the model. hanzoai/ai
carries `X-Key-Id` onto `UsageEvent.Key`; apps/ai `record` and the zen meter add
each call's charge to `keyspend` (one row per key per UTC month in the limits
store). `spend` in the listing is asked of the ai process over the plane
(`client.AISpend`), absent when unreadable. `used` is IAM's sighting of the key
(`usedTime`, at most one write a minute).

What a budget does not bound, said plainly: it is checked when a call is admitted,
so calls already in flight can carry a key past it by their own cost; a feature that buys from the model plane on the caller's behalf
(translate, graph, knowledge, books through `deps.AI`) is billed to the org and not
counted against the key; `project:` reach is carried and not enforced.

**A key is one IAM row, and its policy lives on that row.** The id is the row name
(`<user>-<type>-<8 hex>`; a member's carries `<home>~<user>`). `displayName` is the
name, `prefix` the first 12 characters of the credential (IAM records it at mint),
`expireTime` the expiry, `state: "Revoked"` plus `revoker`/`revokeTime` the
revocation, and `scope` the class plus cloud's grant (`grant.go`). IAM enforces
expiry and revocation where a secret becomes a key (`store.dead`) and answers the
reason as `code` (`key_expired`, `key_revoked`); the boundary turns those into a 401
that says so (`keyRefusal`). Edits are IAM `PUT`s of the row as read: IAM writes
every editable field, keeps the class, and refuses any edit to a revoked row.

**Revoked keys stay listed.** Revoking is a state, not a delete, so the row keeps
who revoked it and when. `GET` reports a degraded IAM as 502, never as "no keys".

**Who sees which keys.** A member sees and manages their own; `cloud.Admin`
(org admin or SuperAdmin acting in the org) sees and revokes every key of the org.
The list is read AS THE CALLER (`orgKeys`, the token `cloud.CallerBearer` relays),
so IAM decides the rows; `hanzo-console`'s own credential reads only the org it
serves (IAM `keys.visible`), which is what an API-key caller falls back to. Creates
and edits stay on that client (key-mint writes in any tenant), editing the row the
caller's listing returned.
Only the person a key belongs to edits it, so an admin cannot repoint a key whose
secret someone else holds. An admin's revoke of another person's key is written to
the audit trail (`account.keys.revoke`), and without a trail it is refused 503. A
key of another member answers 404 exactly as a missing one. An org holds at most
1,000 live keys (`keyCeiling`) and 10,000 rows, revoked ones included
(`rowCeiling`): abuse ceilings, not product limits.

**Permissions are the grant, applied at the boundary.** `product:<capability>`
reads and writes it, `read:<capability>` only reads it (GET/HEAD/OPTIONS), `read:*`
is a read-only key, and a key naming neither reaches everything its holder does.
The capability is `manifest.OwnerOf(path)`, so `/v1/chat/completions` is `ai`. The
check is `idClaims.admits` in `SanitizeIdentity`, on every request a key makes; a
door with no capability (`/.well-known/zip/op/…`) is reached only by `product:*`
or, for a read, `read:*`. A request on a limited key may revoke keys and may not
create or edit them, so no key mints a wider one; a key with a budget or a rate is
limited too. A key presented in `X-Api-Key` meets the same policy as a Bearer, and
a request carrying two different credentials is refused 401. IAM keeps a limited
key off the two paths that enforce no limit: it is not a registry credential and
it cannot mint a token on a member's behalf (`act`).

**Two types, and the type is a field.** `secret` (`sk-`) resolves to the USER, so
it is session-equivalent and belongs on a server. `publishable` (`pk-`) resolves to
just the ORG, is safe in a browser bundle, and is listed with its full value. An
unrecognized type is REFUSED, never defaulted.

**Do NOT spell a public resource under `/v1/iam`.** api.hanzo.ai routes `/v1/iam/*`
to the IAM service (ingress router `api-hanzo-ai-iam-api`), so anything cloud mounts
there is unreachable at the only host callers use: the request lands on IAM's Guard
and 401s. The tell is the body — `{"status":401,"error":"authentication required"}`
is IAM's Guard; cloud's own refusal is a 403.

**GET reads the KEY ROWS, never the user row.** The mint writes a key row; a read of
`schema.User.AccessKey` reports "no key" immediately after a successful POST. That is
the "key never listed" bug and it has recurred twice.
