# metering — prepaid pay-for-everything across OSS Hanzo products

The ONE way every commercially-deployable OSS Hanzo product gets prepaid
per-org billing wired, and the PaaS one-click deploy that provisions it.
Commerce is the single billing source of truth; this is its canonical client +
the deploy renderer.

## The one way (decomplected)

ONE metering core, THREE adapters (one per request-path shape), ONE deploy
renderer. No product reimplements billing; no second balance gate exists.

```
                      commerce /v1/billing/{balance,usage}   (source of truth)
                                    ^            ^
                                    | Authorize  | Record (per-org debit)
                    ┌───────────────┴────────────┴───────────────┐
                    │   metering.Client  (leaf, stdlib-only)      │   ← the core
                    │   X-Hanzo-Org=<org>  Bearer <KMS token>     │
                    │   fail-closed gate · per-ORG billing key    │
                    └───────────────┬────────────┬───────────────┘
        net/http adapter            │ in-process │            non-Go adapter
   metering.Middleware (gin/chi/    │  hook      │      metering/proxy + cmd/
   mux/net-http Go services)        │            │      meter-proxy (sidecar)
                                    │            │
                              base plugins/metering            vector, search,
                              (core.Router hook)               any HTTP upstream
```

- **Billing is per-ORG.** The balance key is the org slug (X-Org-Id), NOT
  `org/sub` — exactly like the proven LLM gate (`ai/routers/filter_balance.go`
  `resolveBillingKey` -> `user.Owner`). `IdentityFromGatewayHeaders` sets
  `User=<org>` (billing) and `Actor=<org/sub>` (audit). Keying per-user checks
  an empty ledger and denies a funded org — the bug this design prevents.
- **Org routing header is `X-Hanzo-Org`** (commerce's service-token path:
  `middleware/accesstoken.go` `c.GetHeader("X-Hanzo-Org")`). NOT `X-IAM-Org-Id`
  (commerce honors that nowhere). Wrong header -> debits the default `hanzo` ns.
- **Fail-closed.** Balance unknown -> deny (503); out-of-funds -> 402. Set
  `METERING_FAIL_OPEN=true` only where availability outranks revenue.
- **Test ledger.** `METERING_TEST=true` sends `X-Hanzo-Test: true` so balances
  and debits hit commerce's sandbox books, never real money.
- **KMS-only token.** `COMMERCE_SERVICE_TOKEN` is always from a KMS-backed
  secret; never inlined, never read from disk by this package.

## Files

| Path | What |
|------|------|
| `metering.go` | the `Client`: `Authorize` (gate) + `Record` (debit). |
| `middleware.go` | `Middleware` (net/http) + `IdentityFromGatewayHeaders` (per-org). |
| `env.go` | `FromEnv` + canonical env var names. |
| `proxy/price.go` | `PriceTable` + `ParsePriceTable` — billable unit as config. |
| `proxy/proxy.go` | `proxy.New` — metered `httputil.ReverseProxy`. |
| `proxy/cmd/meter-proxy` | the universal sidecar binary (env-driven). |
| `proxy/Dockerfile` | static scratch image -> `ghcr.io/hanzoai/meter-proxy`. |
| `deploy/catalog.go` | product inventory + billable unit per product. |
| `deploy/render.go` | `Render(Product, Tenant)` -> operator `hanzo.ai/v1` Service CR. |
| `../../../base/plugins/metering/` | base's in-process gate (its router isn't net/http). |

## Commercial OSS product inventory (billable unit)

| Product | Image | Unit | Adapter | Status |
|---------|-------|------|---------|--------|
| **vector** (Qdrant) | hanzoai/vector | vector op (write 2c / read 1c) | meter-proxy | **PROVEN live** |
| **search** (Meili) | hanzoai/search | document (index 3c / query 1c) | meter-proxy | **PROVEN live** |
| **base** (PocketBase) | hanzoai/base | record write (1c) | in-process plugin | **PROVEN (gate loop)** |
| gateway/LLM | hanzoai/gateway+ai | tokens | (existing LLM gate) | already live |
| functions | hanzoai/functions | compute-second | Middleware (imperative) | needs price hook |
| sign/esign | hanzoai/sign | request / envelope | Middleware | needs wrap |
| analytics | hanzoai/analytics | event ingested | Middleware | needs wrap |
| bot | hanzoai/bot | request / message | Middleware | needs wrap |
| flow | hanzoai/flow | run / step | Middleware | needs wrap |
| kms | hanzoai/kms | secret op | (likely free/infra) | n/a — platform dep |
| iam | hanzoai/iam | seat (per active user) | sweeper, not per-req | needs seat sweeper |
| s3 (SeaweedFS) | hanzoai/s3 | GB-month + request | sweeper + proxy | needs storage sweeper |

Units fall into three metering shapes:
- **per-request** (vector, search, functions, sign, analytics, bot, flow): the
  proxy (non-Go) or `Middleware` (Go). DONE pattern.
- **per-record/in-process** (base): the in-process plugin. DONE pattern.
- **per-seat / per-GB-month** (iam, s3 storage): a periodic *sweeper* that reads
  the product's state and calls `Record` on a schedule — NOT a request gate.
  Designed, not yet built (the one remaining shape).

## Adopt it (per shape)

**Go net/http service:**
```go
m, _ := metering.FromEnv()
h = m.Middleware(metering.MiddlewareConfig{Provider: "sign", Price: priceFn})(h)
```

**Non-Go product (sidecar, zero product changes):** deploy `meter-proxy` next to
it; set `METER_PROXY_UPSTREAM=http://127.0.0.1:<port>`, `METER_PROXY_PROVIDER`,
`METER_PROXY_PRICES` (the price table), and the commerce env.

**base:** `metering.MustRegister(app, metering.Config{})` in main wiring.

## PaaS one-click deploy

`deploy.Render(product, tenant)` emits a per-tenant `hanzo.ai/v1` Service CR:
the product container + the meter-proxy sidecar (for meterable products), with
`COMMERCE_SERVICE_ORG=<tenant org>`, the token via `secretKeyRef` (never
inlined), a per-tenant PVC, and optional ingress. The control plane
(platform.hanzo.ai / console) applies the CR; the operator reconciles. No new
API surface — the deploy path is "emit a CR", the same lifecycle every Hanzo
service uses. CR is schema-clean against the operator's `Service`/`Container`
types (sidecar carries no `ports` — containers share the pod netns).

## Proof (real metering, live commerce TEST ledger)

- **vector**: 5 upserts×2c + 3 searches×1c through the meter-proxy binary ->
  balance 1000c -> 987c (exactly 13c). Health checks: 0c. Per-org isolation:
  the `hanzo` live ledger (755c) untouched.
- **search**: same binary, search price table: 2 index×3c + 4 query×1c ->
  1000c -> 990c (exactly 10c).
- **fail-closed**: unfunded org -> 402, upstream NEVER reached (0 hits).
- **base**: in-process gate, fake commerce: funded org write -> 1c debit
  (acme 100c -> 99c, debit user = org slug); unfunded -> 402; reads free; health
  skipped.
- Unit suites: `metering`, `metering/proxy`, `metering/deploy`,
  `base/plugins/metering` all green.

## Complement: OSS-payout (money out)

This is money-IN (customers pay per-org for OSS products). The SBOM-driven
OSS-payout pipeline (commerce `ossattr`/`ossfunding`/`billing/engine`) is
money-OUT: the same `RecordUsage` spend feeds `AccrueOSSPayout`, paying upstream
maintainers up to 25%. Two halves of "commercialize OSS", one ledger.
