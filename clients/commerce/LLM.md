# Commerce - LLM Context

## Overview

Multi-tenant e-commerce platform. Standalone Go binary with embedded SQLite, migrated from Google App Engine. Zero GAE dependencies remain.

**Live at**: https://commerce.hanzo.ai | **Version**: v1.33.0

## Architecture

```
Commerce App (Cobra CLI + Gin HTTP + Hooks + Events)
  |
  +-- User SQLite (data/users/{userID}/data.db) + sqlite-vec
  +-- Org SQLite (data/orgs/{orgID}/data.db) + sqlite-vec
  +-- PostgreSQL (alternative, pgvector)
  +-- ClickHouse via hanzo/datastore-go (analytics)
```

## Multi-Tenancy

- Namespace-based: `Organization.Name` IS the namespace
- `middleware.Namespace()` sets context namespace for downstream datastore
- `rest.New()` auto-applies namespace middleware unless `DefaultNamespace = true`
- Dual auth: legacy access token (org-bound) + IAM JWT (OIDC/JWKS via hanzo.id)
- Every org (incl. `"platform"`) is STRICTLY scoped to its own name. The legacy
  `"platform" -> "" (cross-org) namespace` bypass was REMOVED (1.42.40, Red M1):
  it keyed cross-org datastore on an org-NAME string, not real platform-admin
  identity. Cross-org access gates on `auth.IAMClaims.GlobalAdmin()` ONLY.

## Gateway Trust Headers (2026-04-27)

commerced does NOT validate JWTs in-binary. The trust boundary is hanzoai/gateway.
Only the gateway-minted X-* identity headers are trusted. Headers are stripped
unconditionally on ingress to prevent client spoofing (see gateway/auth_middleware.go
stripIdentityHeaders).

**Directly-exposed edge (`COMMERCE_EDGE_AUTH=true`) — boundary mount point (Red CRITICAL, 1.42.40):**
commerce-api is reachable in-cluster at `commerce.hanzo.svc:8001` WITHOUT the
gateway, so it strips + re-mints identity itself via `middleware.EdgeAuth`. That
boundary MUST be installed before any route group: gin applies `engine.Use()`
only to routes registered AFTER the call. It is now installed in `Bootstrap`
(`server.go installIdentityBoundary`, called before `setupRoutes`), so it covers
`/_/commerce/*`, `/v1/commerce/*` AND the post-Bootstrap `/v1` `api.Route()`
bundle. Previously it was mounted from `embed.go` AFTER Bootstrap, so gin left
the setupRoutes groups unguarded and an in-cluster pod could `POST
/_/commerce/tenants` with forged `X-Org-Id: admin` + `X-User-IsGlobalAdmin: true`
→ 201 (platform superadmin by header forgery). The boundary NEVER 401s opaque
service tokens (not JWTs) and does NOT strip `X-Hanzo-Org`, so the cloud-api →
commerce per-org billing money path is untouched (`require=false`;
`COMMERCED_REQUIRE_IDENTITY` is incompatible with the no-X-Org-Id service-token
path). Regression: `edgeauth_standalone_test.go`, `middleware/edgeauth_test.go`.

| Header                | Source                  | Use                                         |
|-----------------------|-------------------------|---------------------------------------------|
| X-Org-Id              | JWT `owner` claim       | Org slug; namespace + scope                 |
| X-User-Id             | JWT `sub` claim         | User identity                               |
| X-User-Email          | JWT `email` claim       | Email; audit + notifications                |
| X-User-IsAdmin        | JWT `isAdmin` claim     | "true" iff ORG-level admin (an org owner)   |
| X-User-IsGlobalAdmin  | gateway-derived         | "true" iff PLATFORM (global) admin          |
| X-Roles               | JWT `roles` claim       | Comma-joined role names (admin/owner/etc.)  |
| X-User-Permissions    | gateway-derived         | bit.Field as base-10 int; 0 fails closed    |

Fail-closed contract: missing X-User-IsAdmin -> IsAdmin=false; missing
X-User-IsGlobalAdmin -> IsGlobalAdmin=false. Missing X-User-Permissions ->
bit.Field(0). Identity headers absent -> handler chain falls through to legacy
auth (or 401 when COMMERCED_REQUIRE_IDENTITY).

**Org-admin vs global-admin (Red — anti-conflation):** `X-User-IsAdmin` is the
ORG-level admin role — an org owner (e.g. `maxpower`) carries `isAdmin=true`
within their own org. It is ONLY for org-scoped RBAC. Cross-org / superadmin
actions (e.g. POST `/_/commerce/tenants`) MUST gate on
`auth.IAMClaims.GlobalAdmin()` — the explicit `isGlobalAdmin` claim
(X-User-IsGlobalAdmin) OR `owner=="admin"` — NEVER on `IsAdmin` nor an
org-mintable role NAME like "superadmin". `iammiddleware.GetIAMClaims` populates
both `IsAdmin` (X-User-IsAdmin) and `IsGlobalAdmin` (X-User-IsGlobalAdmin); the
gateway mints X-User-IsGlobalAdmin only for a real global admin and strips it on
ingress, so it can't be forged. Predicates: `checkout.isSuperadmin` =
`GlobalAdmin()`; `checkout.isTenantAdmin` = the robust org-level `IsAdmin` claim
(not a role string). Tests: `auth/globaladmin_test.go`,
`checkout/admin_tenants_authz_test.go`, `middleware/edgeauth_test.go`.

### EdgeAuth admin billing-view override (middleware/edgeauth.go, 1.42.36+)

At the standalone edge (COMMERCE_EDGE_AUTH=true) EdgeAuth normally locks every
`/billing/` request to the caller's OWN org (X-Org-Id + user/userId/customerId
== claims.Owner) — strict per-org isolation. A GLOBAL ADMIN may retarget the
view to another org via `?org=<slug>`: `resolveBillingSubject()` sets both the
namespace (X-Org-Id) and the locked subject to the requested org. The override
is HONORED only when `isGlobalAdmin(claims)` holds — `claims.IsGlobalAdmin` OR
`claims.Owner=="admin"` (NOT plain `IsAdmin`, which is an org-level role: an org
owner like maxpower has it). For everyone else the `?org` param is
consumed-and-ignored (stripped, never honored) so isolation can never be
weakened. Tests: middleware/edgeauth_test.go (admin-switch, non-admin-isolation,
bad-slug reject). `auth.IAMClaims` carries `IsGlobalAdmin` (json `isGlobalAdmin`).

Call sites read identity via:
- `pkg/auth.OrgID(ctx)` / `UserID(ctx)` / `UserEmail(ctx)` (preferred)
- `iammiddleware.GetIAMClaims(c)` returns a real `*auth.IAMClaims` populated from
  headers (Owner=X-Org-Id, Subject=X-User-Id, Name=X-User-Id, Email=X-User-Email,
  IsAdmin=X-User-IsAdmin=="true", Roles split from X-Roles). Never nil; do not
  add nil-guards downstream.

## Key Directories

```
commerce/
  cmd/commerce/    CLI entry point
  commerce.go      Main app framework
  db/              SQLite, Postgres, ClickHouse backends
  hooks/           Hook system (Base-compatible): Hook[T], TaggedHook[T], Resolver
  events/          Unified event forwarding to ClickHouse/Insights/Analytics
  insights/        Hanzo Insights integration + Gin middleware
  api/             HTTP handlers (store, cart, analytics, namespace, etc.)
  models/          Data models
  middleware/      HTTP middleware (auth, namespace, IAM)
  infra/           Infrastructure clients (Redis, Meilisearch, etc.)
```

## Running

```bash
go run cmd/commerce/main.go serve --dev     # Development
./commerce serve 0.0.0.0:80                 # Production
```

## Environment Variables

| Variable | Default | Notes |
|----------|---------|-------|
| `COMMERCE_DIR` | `./commerce_data` | Data directory |
| `COMMERCE_SECRET` | `change-me-in-production` | Encryption secret |
| `COMMERCE_HTTP` | `127.0.0.1:8090` | Listen address |
| `REDIS_URL` | - | `redis://[:pass@]host:port[/db]` (priority over VALKEY_URL) |
| `COMMERCE_DATASTORE` | - | ClickHouse DSN |
| `INSIGHTS_ENABLED` | `false` | Hanzo Insights product analytics |
| `ANALYTICS_ENABLED` | `false` | Umami-like web analytics |
| `KMS_ENABLED` | `false` | Enable KMS secret management |
| `KMS_URL` | - | KMS base URL |
| `KMS_CLIENT_ID` | - | KMS Universal Auth client ID |
| `KMS_CLIENT_SECRET` | - | KMS Universal Auth client secret |
| `KMS_PROJECT_ID` | - | KMS project/workspace ID |
| `KMS_ENVIRONMENT` | `prod` | KMS environment |

## Analytics Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/analytics/event` | POST | Single event |
| `/api/v1/analytics/events` | POST | Batch events |
| `/api/v1/analytics/identify` | POST | User identification |
| `/api/v1/analytics/ast` | POST | astley.js page AST (JSON-LD) |
| `/api/v1/analytics/pixel.gif` | GET | Pixel tracking |
| `/api/v1/analytics/ai/message` | POST | AI message event |
| `/api/v1/analytics/ai/completion` | POST | AI completion event |

## Dependencies

**Core**: cobra, go-sqlite3, gin, hanzoai/datastore-go
**Infra**: go-redis/v9, minio-go/v7, meilisearch-go, nats.go, temporal SDK
**Vector**: Qdrant via REST/HTTP (port 6333). No gRPC, no vector-go SDK -- plain net/http + encoding/json.

## Security Audit (2026-02-14)

Fixed 6 multi-tenancy issues (all compile clean):

1. Namespace API had NO authentication -- added Admin token requirement
2. IAM middleware never resolved org from JWT `owner` claim -- now sets gin context
3. Store listing handlers used unscoped datastore -- added `orgNamespacedDB()` helper
4. Cart handlers used unscoped datastore -- changed to `datastore.New(org.Namespaced(c))`
5. Analytics trusted client-supplied org_id -- now overrides with authenticated org
6. `"platform"` org namespace bypass documented, `IsPlatformOrg()` helper added

## KMS Integration (2026-02-17)

Secrets management via KMS (REST API). KMS is the **single source of truth** for all payment provider credentials — no fallback to org-stored fields, no raw K8s secrets for payment providers.

**Architecture**: Credential Hydration (KMS-only, no fallback).

```
READ paths (hydration → org fields → downstream):
  checkout handlers → getOrganizationAndOrder() → kms.Hydrate() → org.StripeToken() etc.
  checkout sessions → Sessions() → kms.Hydrate() → org.StripeToken()
  subscriptions     → Subscribe/Update/Unsubscribe → hydrateOrg() → org.StripeToken()
  stripe webhooks   → Webhook() → getToken() + kms.Hydrate() → GetStripeAccessToken()

WRITE paths (credentials → KMS):
  seed command      → commerce seed → Client.SetSecret() for Stripe + Square
  stripe connect    → OAuth callback → org.Update() + Client.SetSecret()
  integration sync  → admin Upsert → org.Update() + Client.SetSecret()
```

**Hydration**: `kms.Hydrate(cc, org)` fetches all 25 provider credential fields from KMS and populates the org's integration struct fields. Called once after org resolution at every entry point. Missing secrets are silently skipped. The CachedClient's 5min TTL prevents repeated KMS calls.

**Secret path convention**:
```
/tenants/{orgName}/stripe/STRIPE_LIVE_ACCESS_TOKEN
/tenants/{orgName}/stripe/STRIPE_TEST_ACCESS_TOKEN
/tenants/{orgName}/stripe/STRIPE_PUBLISHABLE_KEY
/tenants/{orgName}/square/SQUARE_PRODUCTION_ACCESS_TOKEN (+ LOCATION_ID, APPLICATION_ID)
/tenants/{orgName}/square/SQUARE_SANDBOX_ACCESS_TOKEN (+ LOCATION_ID, APPLICATION_ID)
/tenants/{orgName}/authorizenet/AUTHORIZENET_LIVE_LOGIN_ID (+ TRANSACTION_KEY)
/tenants/{orgName}/authorizenet/AUTHORIZENET_SANDBOX_LOGIN_ID (+ TRANSACTION_KEY)
/tenants/{orgName}/paypal/PAYPAL_LIVE_* (EMAIL, SECURITY_USER_ID, SECURITY_PASSWORD, SECURITY_SIGNATURE, APPLICATION_ID)
/tenants/{orgName}/paypal/PAYPAL_TEST_* (same 5 fields)
```

**Write paths**: `commerce seed` writes env vars TO KMS. Stripe Connect OAuth callback writes tokens to KMS after exchange. Admin integration upsert syncs Stripe creds to KMS.

**Config**: `KMS_ENABLED`, `KMS_URL`, `KMS_CLIENT_ID`, `KMS_CLIENT_SECRET`, `KMS_PROJECT_ID`, `KMS_ENVIRONMENT`

**Cache**: 5min TTL, extends to 30min on KMS failure (stale-while-revalidate).

**K8s**: Single "secret zero" (`commerce-kms-auth`) holds KMS Universal Auth credentials. All payment credentials live in KMS only.

## Checkout Sessions

`POST /v1/checkout/sessions` — AUTHENTICATED hosted-checkout mint (Square Payment
Link). Same `publishedRequired` gate as every sibling checkout route
(`TokenRequired(Admin, Published)`): a service token, a per-org **Published**
storefront token, or an IAM principal. **There is no anonymous path** — an
unauthenticated POST is 401 BEFORE any org resolution or Square call (v1.46.4
closed the anon mint hole; the earlier "public, no token auth" design was the
CRIT vulnerability).

**v1.46.5 closed the opaque-bearer bypass (CRIT, two layers).** RED live-proved
that `Authorization: Bearer <any-opaque-non-JWT-string>` + `X-Org-Id: <target>`
minted for the attacker's chosen org: EdgeAuth *restored* the client X-Org-Id for
any opaque bearer (before the token was validated) → `IAMTokenRequired` trusted
the header (set `iam_authenticated`, no token check) → `TokenRequired`'s IAM
branch was a bare `c.Next()` (no mask check). Fixed at BOTH layers, each of which
independently kills it:
1. **EdgeAuth (`middleware/edgeauth.go`)** — an opaque bearer's client X-Org-Id is
   NEVER restored to the trusted header; it is stashed in a PRIVATE ctx key
   (`ctxKeyClientOrg`) that ONLY `TokenRequired`'s service-token branch reads,
   AFTER verifying the bearer == `COMMERCE_SERVICE_TOKEN`. X-Org-Id stays
   stripped, so an unvalidated token can never resolve an org via
   `IAMTokenRequired`. The service-token branch reads that key (then falls back
   to the raw X-Org-Id header for gateway/EdgeAuth-off deployments), so per-org
   billing is unchanged.
2. **`middleware/accesstoken.go` `TokenRequired`** — the IAM branch now ENFORCES
   the requested masks (`len(masks)==0 || hasScope(c, permissions)`), so a
   forged/low-priv IAM principal (perms=0) is denied a `TokenRequired(Admin,
   Published)` mint (403). No-mask gates (billing) still admit any authenticated
   principal. Plus `iammiddleware.IsIAMAuthenticated` no longer falls back to raw
   `X-Org-Id` header presence — only the validated `iam_authenticated` gin key
   counts. Regression: `middleware/edgeauth_test.go`,
   `middleware/accesstoken_test.go`, `middleware/iammiddleware/authz_test.go`.

**v1.46.6 — Red re-review hardening (no bypass reopened).** Red confirmed 1.46.5
fully closes the bypass (SHIP); 1.46.6 addresses its LOW+INFO findings:
`checkout/wire.Instructions` now uses `GetOrganizationOK` and fails CLOSED with
401 (was a `GetOrganization` MustGet panic → 500 on any org-less call after
EdgeAuth strips X-Org-Id; also never leaks an org's bank/routing/SWIFT to an
unauthenticated caller — test `api/checkout/wire/instructions_test.go`);
`oauthmiddleware.TokenRequired`'s (currently unwired) IAM branch converged to the
same mask enforcement; stale `api/billing/handlers.go` comment corrected. Keep
`COMMERCE_EDGE_AUTH=true` pinned — the Layer-1 defense depends on it.

**Request**: `{ company, providerHint, currency, customer, items, successUrl, cancelUrl, couponCode? }`
**Response**: `{ checkoutUrl, sessionId }`

Security invariants (do not regress):
- **One org source**: the org is SOLELY the authenticated principal's
  (`authedOrg(c)` = `middleware.GetOrganization` + KMS hydrate). There is NO
  request-body `org`/`tenant` field and NO `?org`/`X-Org-Id`/`X-IAM-Org` override
  on the mint path — a caller can only ever mint for its OWN tenant.
- **Fail-closed creds**: an org without its own per-tenant KMS Square creds
  cannot mint live — `squareCheckoutClientForOrg` has NO production env/platform
  fallback (`isPlatformOrg` removed). Only SANDBOX keeps the env fallback (no
  money at risk), behind auth, so the per-org sandbox storefront still mints.
- **Redirect allowlist**: `successUrl`/`cancelUrl` are bounded to the org's own
  domains via `checkout.AllowedCheckoutRedirect` (org `Websites` + brand
  first-party hosts + brand registrable domains) — closes the open-redirect /
  phishing pivot on the minted link. Enforced for authed callers too.
- Item prices are server-authoritative from the org's per-org catalog
  (`catalogPrice`); the client `unitPrice` is ignored. Emits `checkout.started`.

The legitimate storefront BFF (server-side, holds the service token or the
per-org Published storefront token — mint via `POST /v1/store/storefront-token`)
is the ONE reachable checkout entry. A browser must never call this endpoint
directly with a client-chosen org.

## SQLite Query Engine (2026-02-23)

**Critical: PascalCase→camelCase conversion**. The SQLite query builder converts Go struct
field names (PascalCase) to JSON field names (camelCase) automatically via `toJSONFieldName()`.
This is needed because `json.Marshal()` uses json struct tags (camelCase), but callers use Go
field names in `Filter()`/`Order()` calls (legacy from Cloud Datastore migration).

- `Filter("Test=", false)` → `json_extract(data, '$.test') = 0` (not `$.Test`)
- `Filter("DestinationKind=", kind)` → `json_extract(data, '$.destinationKind')`
- Nested paths handled: `Filter("Account.TransactionHash=", h)` → `$.account.transactionHash`
- Boolean false/0 handled via COALESCE: `COALESCE(json_extract(...), 0) = 0` (handles NULL from omitempty)

**Data directory**: `{COMMERCE_DIR}/orgs/{orgName}/data.db` per org. System data in `orgs/system/data.db`.

**PVC**: `commerce-data` (10Gi, do-block-storage) — deployment uses `Recreate` strategy (not RollingUpdate) because the PVC is ReadWriteOnce.

## At-Rest Encryption (SQLCipher via hanzoai/sqlite)

Per-tenant SQLite files hold money data (balances, transactions, usage), so they
can be encrypted at rest with `github.com/hanzoai/sqlite` v0.2.0 (SQLCipher
AES-256), the SAME envelope scheme Hanzo IAM uses — one at-rest model platform-wide.
Code: `db/encryption.go` (key posture + per-tenant DEK), `db/migrate.go` (migration).

- **Master key from KMS, ONE source**: `COMMERCE_KMS_MASTER_KEY` (64 hex), read
  ONLY by `resolveMasterKey()`. Materialised from `kms.hanzo.ai` into
  `commerce-secrets` by the existing `commerce-kms-sync` KMSSecret (path
  `/commerce`), same mechanism as `HUSD_TREASURY_KEY`. Never in git/code.
- **Envelope**: each `data.db` has its own random DEK → SQLCipher pages; the DEK
  is wrapped (AES-256-GCM, principal-AAD bound) under `KEK = DeriveKey(master,
  principal, tenantID)` into a `<data.db>.dek` sidecar. Rotating the master key
  only rewraps the sidecar (O(1), never bricks a file).
- **Posture decided once**: unset key → unencrypted (dev/CI). Set + codec linked
  → encrypted. Set + cgo-but-no-libsqlcipher → **refuse to boot** (CodecLinked()
  probe), never silent plaintext. Set + existing file without a `.dek` sidecar →
  refuse (migrate first).
- **Migration** (`cmd/commerce-encrypt-dbs`, idempotent, keeps `.plaintext.bak`):
  WAL-safe — folds the source WAL with a verified TRUNCATE checkpoint (opened R/W,
  not mode=ro), verifies per-table row-hash parity, then checkpoints+asserts the
  encrypted temp WAL-free before an atomic cutover (sidecar last = fail-closed).

**Rollout (deliberate; NOT yet enabled — key unset ⇒ current plaintext behavior):**
1. Build the image with libsqlcipher linked: add `libsqlite3` to the build tags,
   `CGO_CFLAGS=-DSQLITE_HAS_CODEC -DSQLITE_USE_URI=1 -I/usr/include/sqlcipher`,
   `CGO_LDFLAGS=-lsqlcipher`, `apk add sqlcipher-dev` (builder) + `sqlcipher-libs`
   (runtime), and a test stage with `SQLITE_REQUIRE_CODEC=1` so a mis-link fails CI.
2. Generate a 32-byte key, store at KMS `/commerce/COMMERCE_KMS_MASTER_KEY`.
3. Scale commerce to 0 (single-writer PVC), run `commerce-encrypt-dbs` as a Job
   mounting `commerce-data` (or an idempotent initContainer), verify ciphertext +
   `.plaintext.bak`.
4. Add the `COMMERCE_KMS_MASTER_KEY` env (secretKeyRef `commerce-secrets`, optional)
   to the commerce CR; scale up. Daemon now opens encrypted, refuses plaintext.

## Billing API (2026-02-23)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/billing/balance` | GET | Balance by user+currency (cents) |
| `/api/v1/billing/balance/all` | GET | All currency balances |
| `/api/v1/billing/usage` | POST | Record API usage (withdraw) |
| `/api/v1/billing/deposit` | POST | Create deposit transaction |
| `/api/v1/billing/credit` | POST | Grant starter credit ($100, 365-day expiry, no card required) |
| `/api/v1/billing/zap` | POST | Clear balance |

All require `permission.Admin` token (org live/test JWT). Cloud-api connects via `commerceEndpoint` + `commerceToken` env vars.

**Current org**: `hanzo` (ID: `gzh2BOBnV6gKZQ0CP`)

## Cross-Compilation (2026-02-23)

Colima QEMU crashes Go's HTTP/2 and module loader on ARM Mac. Use zig for cross-compilation:

```bash
go mod vendor
CC="zig cc -target x86_64-linux-musl" CXX="zig c++ -target x86_64-linux-musl" \
  CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -mod=vendor -ldflags="-s -w -extldflags '-static'" -o commerce ./cmd/commerce/main.go
```

Push to GHCR (Docker Hub credentials not available locally):
```bash
docker build --platform linux/amd64 -t ghcr.io/hanzoai/commerce:hotfix .
docker push ghcr.io/hanzoai/commerce:hotfix
```

## App Engine Migration (2026-02-24)

All App Engine dependencies removed. Context handling modernized:
- `middleware.RequestContext()` (was `AppEngine()`) — stores Go request context in Gin
- `middleware.GetContext(c)` (was `GetAppEngine()`) — retrieves it; falls back to `c.Request.Context()`
- Gin context key changed from `"appengine"` → `"context"` everywhere
- Legacy aliases `AppEngine`/`GetAppEngine` kept as `var` for backward compat
- Legacy GAE Python utils moved to `.legacy/` (bulkloader, datastore-admin, salesforce-metadata)
- ORM: all models use `mixin.Model[T]` with generic CRUD, namespace support, and `orm.Register[T]()`

## OSS Contributor HUSD Payouts (on-chain, 2026-06-27)

25% of cloud revenue is payable to OSS contributors in HUSD (Hanzo USD
stablecoin) on the Hanzo EVM. The split + SBOM attribution + payout algorithm
live in `models/contributor/`; the executor is `cron/payout/contributor/`.

- **Executor**: `contributor.Payout(ctx, Config)` computes period revenue,
  splits 25% across components by real SBOM weight, and disburses each
  allocation by `PayoutMethod`: `credits` (CreditGrant), `stripe` (queued
  transfer), `crypto` (on-chain HUSD ERC-20 transfer).
- **Crypto path**: `executeCryptoPayout` → `util/blockchain.TransferToken`
  (geth-free seam) → `thirdparty/ethereum.transferToken` (luxfi/geth signing,
  legacy EIP-155 tx). The impl is registered by a blank import of
  `thirdparty/ethereum` in `cmd/commerce/main.go` — without it,
  `TransferToken` returns `ErrNoTokenTransfer` (crypto payouts skipped, never
  silently lost). go.work + `replace => ./thirdparty/ethereum` link it under
  the `GOWORK=off` Docker build.
- **Config** (KMS → env, `HUSDConfig.LoadFromEnv`): `HUSD_TOKEN_ADDRESS`,
  `HUSD_CHAIN_ID`, `HUSD_RPC_URL`, `HUSD_TOKEN_DECIMALS`, `HUSD_TREASURY_KEY`.
  Fails closed: no `HUSD_TOKEN_ADDRESS`/`HUSD_TREASURY_KEY` ⇒
  `ErrHUSDNotConfigured` (mainnet has no default token addr, so an unset deploy
  can't mis-pay). `HUSD_TREASURY_KEY` is KMS-only (org hanzo, path
  `/commerce-secrets`, key `HUSD_TREASURY_KEY` → KMSSecret `commerce-kms-sync`
  → `commerce-secrets` Secret). Non-secret config is plain env on the operator
  CR (`universe .../operator/crs/commerce-v1.yaml`).
- **Trigger**: `POST /v1/contributor/payouts/execute` (admin) runs the executor
  for the org namespace — dry-run by default, `?execute=true` to disburse.
  Mirrors `billing auto-recharge/run-all`; drive it from a curl CronJob:
  `curl -XPOST -H "Authorization: Bearer $COMMERCE_SERVICE_TOKEN" \`
  `http://commerce.hanzo.svc:8001/v1/contributor/payouts/execute?execute=true`.
  `GET /payouts/preview` shows the allocation without paying.
- **TESTNET-FIRST**: deployed config points at Hanzo testnet (chainId 36962,
  HUSD = "Lux Dollar" `0xc57b7eCE…4D66`, 18 decimals,
  `http://hanzod.hanzo-testnet.svc.cluster.local:9630/ext/bc/C/rpc`).
  Proven on testnet 2026-06-27: tx
  `0xe5cf03378e2d9dd121dfc5631fa112b2ea03717c9928167d54195ae785866978`
  (treasury → contributor, 25.50 HUSD, status 0x1, block 9).
  Live proof: `go test -tags onchain -run TestOnChainHUSDPayout_Testnet`.
- **MAINNET SWITCH (OFF; requires sign-off + funded mainnet treasury)**:
  `HUSD_TOKEN_ADDRESS=0xe9e32EF8aaECB68794Da3E1E9191b0a64CeC2c83`,
  `HUSD_CHAIN_ID=36963`,
  `HUSD_RPC_URL=http://hanzod.hanzo-mainnet.svc.cluster.local:9630/ext/bc/C/rpc`,
  and re-point `HUSD_TREASURY_KEY` to a funded mainnet treasury.
- **Gas note**: `thirdparty/ethereum` `GasPrice()` queries `eth_gasPrice`
  (respects the chain's 25 gwei `minBaseFee`); the old hardcoded `1` wei caused
  "transaction underpriced" on Hanzo EVM.

## Versioning (tag == binary)

`commerce.Version` is a `var` (commerce.go), default = the current release.
CI injects the immutable image tag at build time so `/healthz` `version` always
equals the deployed tag:

- `docker-deploy.yml` passes `VERSION=<git tag>` (build-arg) on `v*` tag pushes.
- `Dockerfile` / `Dockerfile.sqfix` strip the leading `v` and apply
  `-ldflags "-X github.com/hanzoai/commerce.Version=<ver>"`.
- Branch builds leave `VERSION` empty → the in-source default holds.

Cut releases with a `v*` git tag (`git tag -aX vX.Y.Z && git push origin vX.Y.Z`)
so the build produces `ghcr.io/hanzoai/commerce:X.Y.Z` whose binary reports
`X.Y.Z`. Do NOT ship named tags (`:X.Y.Z-foo`) for prod — they re-introduce the
tag/binary drift (the live 1.42.31-testmode image reported binary 1.42.5 because
`Version` was a hardcoded const nobody bumped).

## commerce-site edge (no nginx)

`commerce.hanzo.ai` serves the marketing SPA via `ghcr.io/hanzoai/static`
(`Dockerfile.site`, `--spa`, :3000) — NEVER nginx. The host's routing lives in
the hanzoai/ingress file provider (`universe infra/k8s/ingress/routes.yaml`):
a high-priority `commerce-hanzo-ai-api` router sends `/v1`, `/api`, `/healthz`
to the commerce API (`commerce.hanzo.svc:8001`); the low-priority
`commerce-hanzo-ai` router catches the rest → the SPA. The operator
`commerce-site` Service CR has `ingress.enabled: false` (a plain Ingress can't
express Traefik priority). The from-source `Dockerfile.site` Next.js build is
currently broken (unbuilt workspace dists, a phantom `@kapaai/react-sdk` import,
a `@/providers` alias); 0.2.0 re-serves the proven 0.0.1 assets — fix the app
build before bumping content.

## SBOM-driven OSS-developer payout (2026-06-25)

Every deploy emits an SBOM; Hanzo pays upstream OSS developers **up to 25%** of
all Hanzo cloud spend, attributed by the dependencies actually deployed. One
pipeline, decomplected into orthogonal pieces.

### The pipeline (one way, all images)

```
arcd build  --syft-->  CycloneDX SBOM  --normalize-->  POST /v1/billing/sbom
  (deploy)             (per image digest)               (ZAP 0x20 OR HTTP)
                                                              |
org usage  -->  RecordUsage  --go-->  engine.AccrueOSSPayout  v
(charge)        (existing)            (mirrors TrackRevenueShare)
                                          |
                  ossattr.Attribute(spend, SBOM pkgs, policy)   <= 25% pool
                  ossfunding.Resolve(purl) -> maintainer target
                                          |
                                          v
                  OSSAccrual ledger lines (per package, idempotent)
                                          |
                  GET /v1/billing/oss-payout/summary  (disbursement view)
```

### Components

| Concern | Where | Notes |
|--------|-------|-------|
| **Attribution math** (the heart) | `ossattr/` (leaf, stdlib-only) | Pure `Attribute(spendCents, []Package, Policy) -> Result`. 25% cap is `ossattr.MaxPoolFraction` (code, not config). Pro-rata by weight; largest-remainder apportionment conserves the pool to the cent. Deterministic + sorted. |
| **Policy** | `config/oss-payout.json` + `config/oss_payout.go` | `poolFraction` (clamped <=0.25), `directWeight` (1.0), `transitiveWeight` (0.25). Embedded JSON, `sync.Once` (mirrors referral-program.json). |
| **Maintainer resolution** | `ossfunding/` (leaf) | `FromPURL` (pure, offline): github.com/<owner> PURLs -> `github_sponsors:<owner>`; golang.org/x/* -> `golang`; npm/pypi w/o VCS -> Unresolved -> **held**. Network FUNDING.yml/registry enrichment behind `Resolver` (out of hot path). |
| **SBOM storage** | `models/sbomrecord/` | Per image digest; `Ingest()` is the ONE upsert (idempotent on digest), shared by HTTP + ZAP. Components are a noindex JSON blob. Global "system" namespace. |
| **Accrual ledger** | `models/ossaccrual/` | Append-only per-package line; idempotency key = `ossattr.AccrualID(org, txn, purl)`. Status: pending\|held\|queued\|settled. "system" namespace (Hanzo->OSS liability, aggregated across orgs). |
| **Accrual engine** | `billing/engine/osspayout.go` | `AccrueOSSPayout(...)` — fire-and-forget hook in `RecordUsage`, structural twin of `TrackRevenueShare`. Unions SBOM components, runs `ossattr.Attribute`, resolves funding, writes lines. |
| **HTTP surface** | `api/billing/osspayout.go` | `POST /sbom`, `GET /sbom`, `GET /oss-accruals`, `GET /oss-payout/summary` (admin-token). |
| **ZAP-native path** | `infra/zap_osspayout.go` | `OpSBOMIngest = 0x20` on the existing ZAP node; `RegisterSBOMIngest(store)` wired in commerce.go to the SAME `sbomrecord.Ingest`. |
| **SBOM emit (build side)** | `arc/cmd/arcd/sbom.go` | `dockerBuilder.emitSBOM` after `docker push`: `syft <digest> -o cyclonedx-json` -> `normalizeSBOM` (pure; direct/transitive from the dep graph) -> POST. Env: `SBOM_INGEST_URL/TOKEN/ORG`. Best-effort (never fails the build). |

### The 25% math

`pool = floor(spend * min(poolFraction, 0.25))`. Each package weight =
`base(scope) * criticality` (direct base 1.0, transitive 0.25, criticality
default 1.0). `share_i = pool * weight_i / sum(weight_j)`, apportioned to integer
cents so `sum(shares) == pool` exactly. Same PURL across SBOMs is de-duped
(highest weight wins) so each package is paid once. No packages / zero weight ->
whole pool **held**, never invented or lost.

### Proven (tests, all green)

- `ossattr`: cap enforced (incl. over-cap clamp), cent conservation over 5000
  spends, direct=4x transitive, criticality, dedup, held-pool, determinism, big
  spend exactness.
- `ossfunding`: PURL->target resolution incl. held cases.
- `billing/engine` (real SQLite): deploy SBOM -> $100 spend -> **2500c (25%)**
  across 4 pkgs; gin direct=1000c->`github_sponsors:gin-gonic`(pending),
  react transitive=250c->**held**; idempotent (2 fires -> 1 line); no-SBOM no-op.
- `api/billing` (HTTP, real router): ingest -> summary rollup; cap + held +
  resolution proven over the wire.
- `arc/cmd/arcd`: `normalizeSBOM` CycloneDX->ingest with direct/transitive
  graph tagging + PURL-less drop.

### Money-out (designed, creds-gated — NO fake payouts)

Accrual is real + provable now. Disbursement is a **separate** job that reads
`GET /oss-payout/summary` (status=pending, resolved targets), batches by funding
kind, pays, and flips lines pending->queued->settled (referencing a `Payout`).
Rails are integration/creds-gated:
- **GitHub Sponsors** — no programmatic payout API; needs the Sponsors GraphQL +
  an org Sponsors account funding source. Per-maintainer sponsorship.
- **Open Collective** — API for expenses/payouts; needs an OC account + API key.
- **Stripe Connect** — `Transfer`/`Payout` to connected accounts; needs the
  maintainer to onboard a connected account (KYC). Best general rail.
All require: a funded Hanzo source account, per-rail API credentials in **KMS**
(never plaintext), maintainer onboarding/KYC, tax handling (1099/W-8). The held
pool waits until a target is resolved + onboarded.

## Three money buckets — credits vs prepaid vs card (billing/bucket)

The ledger was CONFLATED: `starter-credit`/`included-credit:` (non-cash grants)
and `topup`/deposits (real money) were all `transaction.Deposit` rolled into ONE
`balance`. They are now split HONESTLY from the real tags — no fabricated numbers.

- **`billing/bucket`** (pure, no I/O, unit-tested) is the ONE classifier + spend
  projection:
  - `DepositKind(tags)` → `Credit` (tag `starter-credit`, prefix `included-credit:`,
    `credit:`, `grant:`) vs `Prepaid` (everything else — topup/husd/bare deposit =
    real money; UNKNOWN→Prepaid is fail-closed: never mint spendable-on-GPU value
    from a mystery deposit).
  - `IsGPUWithdrawal(tags)` → gpu (`gpu`/`gpu-*`/`gpu:*`).
  - `Compute(transs, id, now) → Split{CreditsGranted, CreditsRemaining,
    PrepaidBalance, PrepaidAvailable, Balance, Holds, Available}`.
- **Spend policy** (documented, enforced): non-GPU usage draws CREDITS FIRST then
  prepaid; GPU usage draws ONLY prepaid. The projection reconciles to the cent:
  `CreditsRemaining + PrepaidBalance == Balance` (the same balance the gateway
  gate debits — bucket.Compute is a faithful superset of util.TallyTransactions).
- **Read**: `GET /v1/billing/balance` + `/me/balance` now ALSO return
  `creditsGranted`, `creditsRemaining`, `prepaidBalance`, `prepaidAvailable`, and
  `card{onFile,brand,last4,isDefault}` (additive — the pre-split
  `{balance,holds,available}` is unchanged, so the cloud proxy + console BFF
  forward the split verbatim; console shows credits vs prepaid distinctly).
  `util.GetRawByCurrency` fetches the raw (un-expiry-filtered) ledger the split needs.
- **GPU rule (server-enforced, fail-closed)** — `api/billing/gpu_charge.go`:
  - `GET /v1/billing/gpu-eligibility?user=&amountCents=&minPrepaidCents=` — the
    launch gate: `{eligible,reason}` where reason ∈ `card_required` /
    `insufficient_prepaid` / `ok`, reading `PrepaidAvailable` (never the combined
    balance) + card-on-file.
  - `POST /v1/billing/gpu-charge {user,amountCents,tag?}` — the ONLY writer of a
    `gpu`-tagged Withdraw. Two gates: (1) a chargeable card MUST be on file
    (402 `card_required`); (2) `PrepaidAvailable >= amountCents` (402
    `insufficient_prepaid`) — credits are NEVER consulted, so a GPU can never draw
    a grant. `gpuTag()` forces the recorded tag into the gpu bucket.
  Both mounted on the admin group; the cloud GPU launch gate calls them. A GPU
  charge NEVER calls `BurnCredits`.
- **CreditGrant** (`models/creditgrant`, `BurnCredits`) is a SEPARATE grant ledger
  used only for subscription/invoice collection (cycle/invoices/subscriptions) —
  NOT the pay-as-you-go wallet the balance endpoint reads. Left as-is.

## Chain-Backed Credit Ledger — HUSD-on-Hanzo-EVM (steps 1-6 BUILT + testnet-proven)

Kills the recurring C1 money-mint vuln class by construction: a credit exists
ONLY as HUSD (Hanzo USD, ERC-20 on the Hanzo EVM) minted by the KMS treasury key
— which commerce does NOT hold. Commerce can only REQUEST a mint; it becomes a
read-only indexer of on-chain balances. No commerce code path can create money.

**Foundation landed (branch `feat/chain-backed-credit-ledger` / `chain-ledger-wip`,
7-step plan — steps 1-3 done, 4-7 staged):**

1. **`treasury.DeriveAccount(masterSeed, orgID)`** (`treasury/derive.go`) — per-org
   on-chain address, BIP-32-style HMAC-SHA512 → secp256k1 (`luxfi/crypto`, no
   geth). Deterministic across restarts, collision-safe for string org ids,
   golden-vector pinned. Private key (settlement-signing, step 5) in memory only.
2. **`treasury.Mint(ctx, MintRequest)`** (`treasury/treasury.go`) — the ONE mint
   path: treasury-signed on-chain HUSD transfer to the org address. TWO locks:
   `mintauth.Require(ctx)` (gated HTTP caller needs proven authority; crons
   ungated) + KMS key custody (`husd.Config.Configured()` else `ErrNotConfigured`,
   no DB fallback). Idempotent by key (deterministic `IssuanceID`; replay → stored
   receipt, no new tx; concurrent same-key → exactly ONE on-chain mint). Bucket
   (credit|prepaid) recorded off-chain → ledger tag (`credit:husd`|`husd`).
   Production store: `models/husdissuance` (kind **281**) + `treasury/datastorestore`
   (deterministic-id ON CONFLICT, mirrors `models/idempotencykey`).
3. **`billing/husdindex`** — chain is source of truth. `client.go` = CGO-free
   JSON-RPC read client (BlockNumber/BalanceOf/TransfersTo). `index.go` = `Sync`
   projector: scan Transfer events → project idempotent bucket-tagged credits
   (dedup on `txHash:logIndex`), reconcile indexed == `balanceOf` to the cent.

- **`util/husd`** = the ONE HUSD config + cent↔wei source (contributor payout
  aliases it now — DRY). `mintauth.Require` = the shared "gated & unauthorized ⇒
  refuse" policy (sink `Enforce` + mint service both call it).
- **Decomplected for CGO-free proofs**: core logic (derive/mint/index) has no
  sqlite/geth dependency, so unit tests + the live proof run `CGO_ENABLED=0`
  locally (the `luxfi/accel` cgo link + `mattn/go-sqlite3` both fail on a laptop;
  datastore adapters are CI-tested). geth `core/types`/`rlp` are avoided in the
  live signer too — their transitive `luxfi/pq@v1.0.3` pull trips the upstream
  force-moved-tag go.sum mismatch; the proof signs EIP-155 with `luxfi/crypto` +
  hand-rolled RLP (production uses the geth `thirdparty/ethereum` signer via the
  same `util/blockchain.TransferToken` seam).

**PROVEN on testnet (chainId 36962), end-to-end, independently verified via cast:**
- Provisioned a fresh HUSD test token `0xe7f1725e7734ce288f8367e1bb143e90bb3f0512`
  (the ephemeral testnet had reset — no token, unfunded treasury; funded treasury
  `0xe6da…a51a` from the genesis hardhat account `0xf39F…2266`, minted 1M HUSD to
  treasury). Node serves the new `/v1/bc/C/rpc` surface (post `/ext→/v1` cutover).
- Live mint tx `0xd01bc1c1733e83c93a5143552af1ba7e3ac045b2433c2fecff07a29685cf976a`
  (status 0x1, block 25): `treasury.Mint($12.34, credit)` → treasury → derived
  org addr `0x3560…9950`. Org `balanceOf` == 1234c; treasury balance dropped by
  EXACTLY 12.34 (value from real supply, not fabricated); indexer `Sync` projected
  a `credit:husd` 1234c credit == on-chain; replay of the idem key sent NO second
  tx. Run: `HUSD_RPC_URL=… HUSD_TREASURY_KEY=… HUSD_TOKEN_ADDRESS=… CGO_ENABLED=0
  go test -tags onchain -run TestOnChain_ChainBackedLedger ./treasury`.

**Headline: on testnet, minting is treasury-only (commerce holds no key, cannot
mint) and idempotent, and the indexer reflects on-chain balance — proven live.**

### Steps 4-6 (BUILT + testnet-proven, branch `chain-ledger-wip`)

4. **Mint-path rewire** (`billing/husdledger` + `api/billing/husd_mint.go`).
   `husdledger.Service` is the ONE mint entrypoint (`MintCredit`): treasury
   on-chain mint → bounded synchronous `ProjectTx` so `GET /v1/billing/*/balance`
   reflects it on return; a background/CronJob `SyncOnce` backfills. Adapters:
   `ledgerStore` (projects a bucket-tagged `transaction.Deposit` — the SAME row
   the balance read tallies — keyed to the mint's `Subject` in its `Test`
   partition, idempotent on `txHash:logIndex`), `addressBook` (derive every org
   address from the org list + seed), `cursorStore` (`models/husdcursor` kind
   **282**), and `datastorestore.ByTxHash` (the issuance store IS the
   `IssuanceLookup`, system-ns). `MintRequest`/`Issuance`/`Credit` gained
   `Subject` (per-org address, per-subject ledger) + `Test`. Handlers
   `Deposit`/`GrantStarterCredit`/`PostMyWelcome`/`GrantStarter`/`topup` branch to
   the chain mint when enabled (else the DB path, unchanged). Wired at Bootstrap
   from `husd.Config` + `HUSD_ORG_DERIVATION_SEED`. The **seed is the ledger's
   sole intent signal** (`husdledger.ValidateConfig`, PR #69): ENABLED only with
   a seed AND token+key; token+key WITHOUT a seed is the INERT state
   (`Enabled()==false`, DB credit path) — NOT a boot error — because the shared
   `util/husd` token+key is ALSO the OSS contributor-payout config, which runs
   with no ledger seed. Boot refuses ONLY on an incoherent LEDGER config (seed
   set but token/key missing). This decouple is why v1.46.39 boots inert in prod
   (the CR carries HUSD token+key for OSS payouts, no seed) instead of
   crash-looping; the mainnet cutover enables the ledger by provisioning the seed.
   **Proven**: live `TestOnChain_Step4_ProjectTx` (mint `0x3324a75a…` → ProjectTx
   from the REAL receipt → one credit subject/bucket/test) + CI `ledgerStore`
   sqlite test (ProjectTx → real Deposit → `bucket` balance, idempotent).
5. **Settlement** (`husdledger/settlement.go`, `husd.SettlementDrift`, kind **283**).
   `drift = balanceOf(orgAddr) − max(0, ledgerSpendable)`; sweep org→treasury,
   signed by the ORG's derived key, when `drift ≥ threshold`. Self-correcting +
   idempotent. `POST /v1/billing/husd/settle` (platform-only). **Proven**: live
   `TestOnChain_Step5_Settlement` (mint $50 → gas-fund org → org-key sweep
   `0xf323bf25…` → org drops EXACTLY $20 == spendable, treasury +$20) + CI
   `SettleOrg` sqlite test (real ledger $50−$20 → sweep $20, reconcile, idempotent).
6. **Migration + reconcile** (`husdledger/migration.go`). Snapshot each subject's
   `bucket.Split` → treasury-mint the equivalent per bucket → neutralize legacy
   deposits with two idempotent offset withdraws (non-GPU cancels credit
   credits-first; GPU-tagged cancels prepaid prepaid-only) so the split is
   preserved → assert chain `balanceOf` == pre-migration DB balance == post DB,
   exact to the cent, zero settlement drift. `POST /v1/billing/husd/migrate`
   (dry-run default, platform-only). **Proven**: CI `MigrateOrg` sqlite+fakeChain
   test (alice 2500c/2000p + bob 1000p → reconcile 5500==5500==5500, buckets
   preserved, idempotent).

### Step 7 — DB-mint deletion (the mainnet cutover GATE, NOT done this session)

The named money-in paths (deposit/welcome/topup) route through the chain when
enabled; the DB write stays as belt-and-suspenders (bypassed on the chain path,
still guarded by #65's `mintauth` sink) until prod migration is 100%. Before the
deletion, these REMAINING credit-mint surfaces MUST also route through
`chainMintCredit` (else they DB-mint credit outside the chain when enabled):
`billing/allotment` (monthly included-credit), `billing/trial`,
`billing/credit.GrantIfEligibleNow` (shared auto-grant helper),
`api/billing/refund.go`, `api/billing/topup_token.go`, `api/billing/webhooks.go`
(settled-payment deposits), `api/transaction/create.go` (raw `/v1/transaction`).
(`api/checkout/util/capture.go` credits an ORDER wallet, not the gateway-spendable
iam-user wallet — out of scope.) THEN delete the DB deposit writes; the
`mintauth` sink stays as the final backstop.

**Mainnet cutover sign-off gate** (separate; do NOT touch mainnet HUSD):
(a) route the remaining credit-mint surfaces above; (b) provision the mainnet
HUSD token + a FUNDED mainnet treasury (`HUSD_TOKEN_ADDRESS`, `HUSD_CHAIN_ID=36963`,
`HUSD_RPC_URL=…hanzo-mainnet…`, `HUSD_TREASURY_KEY` + `HUSD_ORG_DERIVATION_SEED`
in KMS) with gas float per org address (settlement needs it); (c) run
`/husd/migrate?execute=true` per org, assert reconcile; (d) enable the chain path;
(e) run `/husd/sync` + `/husd/settle` on a CronJob; (f) after migration is 100%,
land step 7's deletion. Testnet-first is complete; mainnet is the sign-off.

Config: `HUSD_TOKEN_ADDRESS`, `HUSD_CHAIN_ID`, `HUSD_RPC_URL`, `HUSD_TOKEN_DECIMALS`,
`HUSD_TREASURY_KEY`, `HUSD_ORG_DERIVATION_SEED` (all KMS), `HUSD_INDEX_CONFIRMATIONS`
(default 1), `HUSD_SETTLE_THRESHOLD_CENTS` (default 1). Testnet proof env: token
`0xe7f1725e…0512`, chainId 36962, RPC `…/v1/bc/C/rpc`, treasury `0xe6dad4…a51a`.

## Gotchas

- New ORM kinds MUST be registered in `util/hashid/kind.go` (monotonic, never
  reorder) or `Create()` panics "Unknown kind" — `sbom-record`=262, `oss-accrual`=263,
  B2B `company`=264/`employee`=265/`quote`=266/`quote-message`=267/`approval`=268,
  `gift-card`=269/`gift-card-redemption`=270, `exchange`=271, `idempotency-key`=272,
  `husd-issuance`=281 (chain-backed credit ledger).

## Medusa parity (native Go /v1 — 1.42.41)

The `/v1` model bundle (`api/api/api.go`, mounted at `/v1/*` by `cmd/commerced`
+ `mount.go`; the `/v1/commerce/*` prefix is the SEPARATE checkout/tenant surface)
covers Medusa v2's admin domains natively — no Medusa/Node fork. Reference:
`~/work/medusa/medusa/packages/medusa/src/api/admin/*` + `packages/modules/*`.

### Newly wired (models existed, routes were orphaned → 404 in prod)
`api/api/api.go` now calls these 5 previously-unwired sub-routers:
- `fulfillment` — fulfillmentset, servicezone, geozone, shippingoption,
  shippingoptionrule, shippingprofile, fulfillmentprovider, `POST /fulfillment/:id/ship|cancel`
- `tax` — taxregion, taxrate, taxraterule, taxprovider, `POST /tax/calculate`
- `customergroup` — customergroup + `/:id/members`, customergroupmembership
- `apikey` — publishableapikey, role, apipermission
- `notification` — notification

### Net-new native domains
- **Gift cards** (`models/giftcard`, `models/giftcardredemption`, `api/giftcard`):
  `gift-card` code+initial balance; balance is a PROJECTION `initial − Σ redemptions`
  (no mutable counter to race). `POST /giftcard/:id/redeem|void|` + `GET /balance|redemptions`.
  Admin-gated. Redeem is idempotent (see money design below).
- **B2B** (`models/company|employee|quote|approval`, `api/b2b`): company →
  employees (spending limits, `RemainingSpendCents`/`WithinLimit` pure fns) →
  quotes (RFQ, `POST /:id/accept|reject`, message thread) → approvals
  (`approval.NextStatus` pure transition, `POST /:id/approve|reject`).
- **Order exchange** (`models/exchange`, `api/exchange`): return + replacement,
  `DifferenceDueCents`, `POST /exchange/:id/confirm|cancel`.
- **Idempotency guard** (`models/idempotencykey`): reusable `Begin/Complete`
  for money-moving requests; wired into `api/checkout` `Refund` via
  `X-Idempotency-Key` (replay returns stored response; in-flight → 409).

### Platform product catalog — commerce is the CMS SOT (1.42.41)
`models/catalogentry` (`catalog-entry`, kind 273) is the source-of-truth for the
platform product catalog (Models, Vector, KMS, …) — the list docs.<brand> + the
console sidebar + pricing all derive from. **commerce owns the DATA (source +
seed + edits); `@hanzo/products` (hanzoai/ui/pkgs/products) owns the SCHEMA** +
the iconKey→component and brandColor→css code-maps. Conform to its CatalogEntry
exactly — the shape is NOT ours to change.
- **`GET /v1/commerce/catalog?brand=<b>`** (public, no auth, on the commerce
  public group so it serves that exact path) → `{brand, categories[], products[]}`;
  `products` is the `@hanzo/products` CatalogEntry[] (the client reads `.products`,
  tolerating a bare array or `{catalog|data|items}` too).
- CatalogEntry contract fields (`models/catalogentry/projection.go` Item): `id`
  (== slug, unique), `name`, `category` (EXACTLY one of the 13 canonical labels),
  `brandColor` (a swatch KEY like "violet" — NOT hex; client maps key→css),
  `iconKey` (a lucide export NAME like "Brain" — NOT a component), `slug`,
  `route` ("/<slug>"), `docsUrl` ("https://docs.hanzo.ai/docs/services/<slug>"),
  `apiPath` (/v1-prefixed), `pricingId` (plans/<key>.json key OR **null** — emit
  `*string`, nil→null). `brands` is a category-DERIVED convenience, never a
  hand-authored filter. Extra fields (gcp/repo/admin/status/priceCents/order/
  productId) are additive — the client ignores unknowns.
- **Brand scope is by CATEGORY** (`categoriesForBrand`, mirroring `@hanzo/products`
  `catalogForBrand`/`BRAND_CATEGORIES`): hanzo=all 13; lux/zoo/pars=5
  (Web3/Network/Security/Dev/Settings). There is NO per-entry brand filter — the
  same `system`-namespace store serves every brand; an entry shows iff its
  category is in the requested brand's set. Slug is globally unique.
- **Admin CRUD** `/v1/catalog/entries` (+ `/seed`) gates on
  `auth.IAMClaims.GlobalAdmin()` — the catalog is cross-tenant PLATFORM data in
  the `system` namespace, so an org-level admin must NOT edit it. Keyed by slug.
- **Seed**: the embedded `seed/hanzo-catalog.json` is the `@hanzo/products`
  snapshot (`hanzoai/ui/pkgs/products/snapshot/catalog.json`) VERBATIM — 95
  products with correct brandColor/iconKey/route/docsUrl/apiPath/pricingId.
  `SeedIfEmpty` auto-runs on first boot (count-gated no-op once populated;
  `COMMERCE_CATALOG_SEED=false` to skip). Idempotent + non-destructive — never
  clobbers CMS edits. Re-baseline by re-copying the `@hanzo/products` snapshot.
- Namespace: platform-global (`system` ns), namespaced via CONTEXT
  (`nscontext.WithNamespace`) — struct `SetNamespace` is a no-op for queries (they
  read ns from `d.Context`), a latent gotcha `api/billing/osspayout systemDB`
  also trips (works only because its reads+writes are symmetric in default ns).

### Money-correctness design (idempotency WITHOUT working transactions)
- `datastore.RunInTransaction` (`datastore/datastore.go`) is a **NO-OP** — it
  runs `fn(New(ctx))` with no tx/lock/isolation. `db.SQLiteDB.RunInTransaction`
  IS real (writeMu.Lock+BeginTx) but `mixin.Model[T]` routes to the no-op. So
  model-level read-modify-write is NOT atomic. The pre-existing Square refund
  (`api/checkout/square/refund.go`) is TOCTOU-vulnerable to concurrent
  double-refund (guards over-refund but not races/replays).
- The money-safe primitive used here: **deterministic-id ledger records**. A
  redemption/guard's STORAGE id = `sha256(scope‖key)` via `orm.WithStringKey`.
  Concurrent duplicate submits collapse onto ONE row via the storage
  `ON CONFLICT(id,kind,namespace)` upsert — proven (giftcard 25-goroutine
  same-key test → 1 debit). Balance is a projection so distinct concurrent
  redemptions are additive, never lost. Refund guard replays the stored
  response. The narrow concurrent-first-submit window is closed at the gateway
  (Square/Stripe honor the same idempotency key on the refund call).
- **GOTCHA — a struct field named `Key` SHADOWS `Model[T].Key()`** and silently
  breaks `mixin.Entity` (`.Query()` returns nil entity → nil panic in `Get`).
  Name idempotency fields `IdemKey`, not `Key`. Compiler catches it only via an
  explicit `var _ mixin.Entity = (*T)(nil)` assertion.
- **GOTCHA — namespace scoping keys off CONTEXT, not the datastore struct.**
  `db.SetNamespace(ns)` sets `d.namespace` but the SQL layer reads
  `nscontext.GetNamespace(ctx)` (`db/query.go getNamespace`). Production is
  correct (`org.Namespaced(c)` → `nscontext.WithNamespace`); tests MUST build
  the datastore from a namespaced CONTEXT (`datastore.New(nscontext.WithNamespace(ctx, ns))`),
  not `SetNamespace`, or cross-tenant isolation appears broken in tests only.
- `datastore/query/query.go` `ById` switch: all hashid-only `Model[T]` kinds
  (no slug/name secondary lookup) MUST be in the "return not-found" case, else a
  non-hashid id hits `default:` → HTTP 500 instead of 404. 43 such kinds were
  missing and are now listed.

### Endpoint contract note (console → /v1/commerce)
The model bundle serves `/v1/product` etc. (bare `/v1`). console2 today reaches
it via a same-origin `/commerce` BFF proxy (`app/commerce/[...path]/route.ts` →
`commerce.hanzo.svc:8001/v1/*`). To honor the "console calls `/v1/commerce/*`,
nothing before /v1/" law without breaking the live `/v1/billing/*` money path,
add an ingress rewrite on the console host (`console.hanzo.ai/v1/commerce/* →
commerce/v1/* strip`) rather than re-prefixing the backend bundle.

- Healthcheck: use `curl -f` not `wget --spider` (Gin only handles GET)
- `go-sqlite3` must be pinned to v1.14.x (`replace` in go.mod) — the transitive
  v2.0.3+incompatible fails to compile on musl/Alpine (pread64/pwrite64)
- Meilisearch v0.35.1 changed `AddDocuments`/`DeleteDocuments` signatures
- Production Dockerfile uses CGO_ENABLED=1 for SQLite (not CGO_ENABLED=0)
- Global entities (Organization, User, Token) use `DefaultNamespace = true` by design
- PVC is ReadWriteOnce — use Recreate deployment strategy, not RollingUpdate
- Filter field names are PascalCase (Go struct) — auto-converted to camelCase JSON
- Boolean `false` with `omitempty` may be omitted from JSON — COALESCE handles NULL=false
