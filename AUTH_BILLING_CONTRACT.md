# Unified IAM Auth + Tenant Billing Contract

The ONE way every Hanzo product surface authenticates a user and bills their
usage. hanzo.chat, hanzo.app, studio.hanzo.ai, and console.hanzo.ai all
implement THIS contract against `hanzoai/cloud` (api.hanzo.ai). There is no
per-app billing, no shared API key, and no second way to do any of it.

## The chain, end to end

```
  Browser (a surface)
    │  1. OIDC Authorization Code + PKCE, PUBLIC client (no client secret)
    ▼
  IAM  (hanzo.id / lux.id / zoolabs.id / pars.id …)
    │  2. issues user tokens. owner claim = the user's org (the tenant).
    ▼
  Surface backend / SPA
    │  3. holds the user's IAM token server-side (session / httpOnly cookie).
    │     UI's actively-selected (org, project) = the tenant context.
    │  4. EVERY call to cloud forwards THAT user's IAM bearer, unchanged:
    │        Authorization: Bearer <user IAM token>
    │        X-Project-Id:  <active project>     (optional; org comes from token)
    ▼
  cloud (api.hanzo.ai)  — SanitizeIdentity → BillingGate
    │  5. validates the JWT (JWKS sig + issuer-set + audience + exp).
    │  6. derives (org, project): org is PINNED from the verified `owner`
    │     claim (client-supplied X-Org-Id is stripped); project is the
    │     claim-bound X-Project-Id (else soft-scoped).
    │  7. meters against the org's shared plan allowance; overflow →
    │     pay-as-you-go on the org's linked billing account.
    ▼
  commerce (billing/pricing) — ONE ledger, keyed on (org, project)
```

One identity (the user's IAM token), one tenant key (org from the token's
`owner`, project from the active selection), one ledger. Every surface is a thin
client of this; none of them holds a shared key or bills anything itself.

## 1. Login — OIDC Authorization Code + PKCE, PUBLIC client

- **Public client, no client secret.** The token endpoint auth method is `none`;
  security comes from PKCE (`code_challenge_method=S256`) + the signed `state`,
  not a shared secret baked into a browser-delivered app. A public client cannot
  leak a secret it does not have.
- Strategy registration MUST NOT be conditioned on a client secret. (The chat
  login outage was exactly this: `configureOpenId` was gated on
  `OPENID_CLIENT_SECRET`, so a secretless public client never registered the
  `openid` passport strategy → "OpenID strategy not registered".)
- The `owner` claim is the tenant org. `sub` is the user. A surface reads the org
  from `owner` (fallback `organization`), never from a client-set field.
- Reference implementation: `studio/middleware/iam_auth_middleware.py`
  (`_authorize_redirect` builds the PKCE authorize URL; `handle_callback` adds a
  `client_secret` to the token exchange ONLY if one is configured — public by
  default).

## 2. Tenant context — active (org, project)

- A user belongs to one or more orgs; the token's `owner` is the home org, and
  IAM may carry the full set (`organizations`/`orgs`/`groups`).
- The UI's actively-selected org + project is the tenant context for the session.
  Studio carries the active org in the `studio_active_org` cookie and validates
  it against the token's org set (`middleware/session.py: resolve_org`) — a user
  can only ever select an org their token authorizes.
- cloud pins the billing org from the VERIFIED `owner` claim, so even a forged
  active-org header cannot move spend to another tenant. The active project is
  forwarded as `X-Project-Id` and is honored as a hard scope only when it is
  claim-bound; otherwise it degrades to a soft scope (cannot hard-stop, cannot be
  evaded). See `middleware_billing.go: identityFromCtx`.

## 3. Forwarding to cloud — the user's token, never a shared key

- EVERY request to cloud carries `Authorization: Bearer <the signed-in user's IAM
  token>`. The token is held server-side (session / httpOnly cookie) and is never
  exposed to the browser JS.
- The forwarded token MUST be principal-bound to the authenticated user (`sub`
  equals the session principal) and unexpired. Fail secure: if no such token is
  available, DENY (401 / "sign in") — never fall back to an ambient or service
  credential, which would run as the wrong principal or drain a shared org.
- NO shared keys. NO per-app keys. NO per-user minted `hk-` keys for chat. The
  IAM token IS the credential and the billing identity.
- Reference implementations:
  - `chat/api/server/routes/agents/cloud.js` +
    `chat/packages/api/src/endpoints/custom/tenantBearer.ts` — the ONE resolver
    (`resolveTenantBearer`) both the agents path and the chat-completion path use.
  - The chat-completion endpoints declare `apiKey: "{{LIBRECHAT_OPENID_TOKEN}}"`;
    the custom-endpoint initializer substitutes the resolved session bearer at
    request time (`chat/packages/api/src/endpoints/custom/initialize.ts`).

## 4. Billing — ONE path keyed on (org, project)

cloud is the single meter and gate. `SanitizeIdentity` (mirrored in-binary by
`auth_identity.go`) validates the token and exposes `c.Org()` / `c.User()` /
`ValidatedProject(c)`. `BillingGate` (`middleware_billing.go`) then:

- **Billing key = org.** Prepaid balance is per-org: one org credit covers the
  whole org. `identityFromCtx` sets `User = org` (bare `sub` only when org is
  absent). Keying per-user would 402 a fully funded org.
- **Scope axes = (project, service).** `project` is the caller's claim-bound
  `X-Project-Id`; `service` is SERVER-derived from the route (`/v1/ai/*` → `ai`),
  never a client field, so a caller cannot spoof another service's cap.
- **Shared plan, then PAYG.** `AuthorizeVerdict` checks the org's shared plan
  allowance and per-scope spend caps in one round trip; overflow bills
  pay-as-you-go against the org's linked billing account. Outcomes map to a frozen
  HTTP contract:
  - `200` allowed (with `X-Spend-Warn: <pct>` at a soft cap threshold),
  - `402 insufficient_balance` — add credits at console.hanzo.ai,
  - `402 spend_cap_exceeded` — raise the scope cap at console.hanzo.ai/limits,
  - `503 balance_unavailable` — commerce unreachable, fail-closed.
- **Single charge.** `/v1/ai/*`, `/v1/agents/*`, and the other self-metering
  subsystems record their own per-org usage; the edge gate returns price 0 for
  them so nothing is billed twice (`DefaultPrice` / `selfMeteredPrefixes`).

## 5. Secrets

- Per-tenant, KMS-managed only (`kms.hanzo.ai`, KMSSecret CRDs). No shared
  service key stands in for a user. The only service tokens that exist are
  narrow, per-tenant, and never used to impersonate a user for LLM spend.
- A surface's own OIDC registration is a PUBLIC client — there is no client
  secret to store.

## Surface conformance (as of this contract)

| Surface | Login (PKCE public) | Forwards user token | Org from `owner` | Billed via cloud (org,project) |
|---|---|---|---|---|
| **studio.hanzo.ai** | ✅ reference | ✅ (validates locally) | ✅ | ⚠️ renders run on studio's own GPU workers and self-report to commerce keyed by org via a per-tenant commerce token — org-keyed, but not the forward-bearer-to-gateway path (studio does not call the cloud LLM gateway for its core renders) |
| **console.hanzo.ai** | ✅ | ✅ same-origin `/v1` through the gateway | ✅ | ✅ (it IS the canonical consumer) |
| **hanzo.chat** | ✅ (this change) | ✅ (this change: `resolveTenantBearer`) | ✅ | ✅ (this change: forwards bearer to `/v1/ai/*`) |
| **hanzo.app** | ❌ confidential client (`IAM_CLIENT_SECRET`, userinfo/introspect) | ✅ to its own backend; org from token `owner` | ✅ | ❌ builder AI runs on OpenRouter with an apiKey (`lib/llm/generation-api.ts`), NOT the cloud gateway — off the unified meter |

hanzo.app is the remaining gap: it needs the same treatment chat just got — switch
its IAM registration to a PKCE public client, and route its builder AI generation
through api.hanzo.ai forwarding the user's IAM bearer so usage meters against the
org plan instead of a shared OpenRouter key.
</content>
</invoke>
