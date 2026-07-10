# Referral Tracking Pipeline

Technical specification for the end-to-end referral, credit grant, revenue share,
and OSS contributor payout pipeline across IAM, Commerce, Analytics, and Console.

## Current State Assessment

### What Exists (production-ready)

| Component | Location | Status |
|-----------|----------|--------|
| `Referrer` model | `models/referrer/referrer.go` | Complete. Code, userId, affiliateId, program link, blacklist/duplicate detection, IP-based fraud |
| `Referral` model | `models/referral/referral.go` | Complete. Event type (new-order, new-user), referrer link, fee struct, revoke flag |
| `ReferralProgram` model | `models/referralprogram/referralprogram.go` | Complete. Actions (StoreCredit, SendUserEmail, SendWoopra), triggers (CreditGTE, ReferralsGTE, Always) |
| `Affiliate` model | `models/affiliate/affiliate.go` | Complete. Commission (percent, flat, minimum), schedule (daily/weekly/monthly), Stripe Connect fields |
| `Fee` model | `models/fee/fee.go` | Complete. Platform/Stripe/Affiliate/Partner types, pending/payable/transferred lifecycle |
| `CreditGrant` model | `models/creditgrant/creditgrant.go` | Complete. Amount, remaining, priority burn-down, expiry, meter eligibility, tags |
| `Contributor` model | `models/contributor/contributor.go` | Complete. Git identity, SBOM attribution, payout method/target, earnings tracking |
| `SBOMEntry` model | `models/contributor/sbom.go` | Complete. Component identity, git-blame authors, usage count, revenue percent |
| `Transaction` model | `models/transaction/transaction.go` | Complete. Deposit/withdraw/transfer/hold, source/destination, tags, metadata, expiry |
| `referral-program.json` | `config/referral-program.json` | Complete. 4 tiers (starter/growth/pro/partner), OSS config, fraud rules |
| Referrer API | `api/referrer/referrer.go` | Partial. Create + Get only. No list, no code-based lookup |
| Billing credit grants API | `api/billing/credit_grants.go` | Complete. Create, list, balance, breakdown-by-tag, void, burn-down algorithm |
| Billing usage API | `api/billing/usage.go` | Complete. Record withdraw, query usage, meter event compat |
| Contributor API | `api/contributor/contributor.go` | Complete. Register, SBOM CRUD, payout calculate/preview, earnings/attributions |
| Payout algorithm | `models/contributor/payout.go` | Complete. Weighted component attribution, min threshold, sorted allocations |
| Affiliate payout cron | `cron/payout/affiliate/affiliate.go` | Complete. Iterates orgs, finds affiliates with Stripe tokens, transfers fees by schedule |
| Event publisher | `events/publisher.go` | Complete. NATS/JetStream, order.created/completed/refunded, checkout.started |
| Hook system | `hooks/hooks.go` | Complete. OnModelCreate/Update/Delete, lifecycle hooks, priority chain |
| IAM middleware | `middleware/iammiddleware/iammiddleware.go` | Complete. JWT validation, org auto-provisioning, role mapping |
| Starter credit | `billing/credit/credit.go` | Complete. $5/30-day, idempotent, transactional |
| Referral email template | `templates/email/referral-sign-up.html` | Complete |
| SignUpForm | `~/work/hanzo/id/components/SignUpForm.tsx` | Complete but NO referral code handling |

### What Does NOT Exist

1. **No `ref` query param handling** anywhere in the signup flow. `hanzo.id/signup` ignores `?ref=CODE`.
2. **No referral code lookup endpoint**. Cannot resolve `CODE` to a `Referrer` by code.
3. **No IAM-to-Commerce webhook** on user creation. IAM creates the user; Commerce never learns about it in the referral context.
4. **No credit grant on referral**. `SaveReferral()` only creates transaction deposits (old system). The newer `CreditGrant` model is not wired to referrals.
5. **No revenue share tracking** on API usage. `RecordUsage()` creates withdraw transactions but does not check if the user was referred or calculate affiliate commission.
6. **No referral-specific analytics events**. The event publisher has no `referral.*` subjects.
7. **No console referral dashboard**. No pages in console.hanzo.ai for viewing referral stats, sharing links, or viewing earnings.
8. **No SBOM scan cron**. The contributor API accepts manual SBOM entries but nothing scans repos automatically.
9. **No contributor payout execution**. `calculatePayouts` returns a summary but does not create transactions or Stripe transfers.
10. **No IAM webhook configuration** for user signup events.

## Architecture

### Design Principle

Commerce is the single source of truth for all monetary operations. IAM handles identity. Analytics handles telemetry. Console handles UI. No service writes monetary records except Commerce.

### Data Flow

```
                    hanzo.ai/signup?ref=CODE
                           |
                    [hanzo.id/signup]
                      |           |
               stores ref in     calls IAM
               sessionStorage    /api/signup
                      |           |
               [hanzo.id/callback]
                      |
              POST Commerce /v1/referral/claim
              {referralCode, refereeUserId}
                      |
               [Commerce]
              1. Lookup referrer by code
              2. Create Referral record
              3. Create CreditGrant for referee
              4. Create CreditGrant for referrer
              5. Publish referral.completed event
              6. Emit analytics event
```

### Why Client-Side Claim (Not IAM Webhook)

IAM supports webhooks, but they fire for every user operation (update, login, etc.), not just signup. Filtering is unreliable. The referral code is a frontend concern (query param) that IAM's signup API does not accept or store.

The correct design: the frontend stores the `ref` param in `sessionStorage` before signup, then after successful signup + login, the callback page calls Commerce to claim the referral. This is:

- Idempotent (Commerce deduplicates by referee userId)
- Auditable (explicit API call with auth token)
- Decoupled (IAM does not need to know about referrals)
- Fraud-resistant (requires valid IAM token, not just a code)

## Flow 1: Referral Link

### Sequence

```
1. Referrer visits console.hanzo.ai/referral
2. Console calls: GET /v1/referrer/me  (IAM token, userId from JWT)
   - If no referrer exists, Console calls: POST /v1/referrer
     {userId, programId: "hanzo-referral-v1"}
   - Commerce generates unique code, returns Referrer with code
3. Referrer shares link: hanzo.ai/signup?ref={code}

4. Referee clicks link, lands on hanzo.id/signup?ref={code}
5. SignUpForm reads ?ref param, stores in sessionStorage as hanzo_referral_code
6. User fills form, submits to IAM /api/signup
7. IAM creates user, redirects to hanzo.id/callback
8. Callback page:
   a. Exchanges code for tokens (existing flow)
   b. Reads hanzo_referral_code from sessionStorage
   c. If present, calls: POST /v1/referral/claim
      Authorization: Bearer {iam_token}
      {code: "{referral_code}"}
   d. Clears sessionStorage key
9. Commerce /v1/referral/claim handler:
   a. Extract userId from IAM JWT claims
   b. Lookup Referrer by code
   c. Check fraud rules (self-referral, cooldown, IP, daily limit)
   d. Check idempotency (referee userId already has a referral)
   e. Create Referral {type: "new-user", userId: referee, referrer: {...}}
   f. Create CreditGrant for referee ($20, 90-day expiry, tag: "referral-bonus")
   g. Create CreditGrant for referrer ($20, 90-day expiry, tag: "referral-reward")
   h. Publish event: commerce.referral.completed
   i. Return 201 {referralId, creditGranted: true}
```

### API Changes Required

**Commerce** -- new endpoint:

```
POST /v1/referral/claim
Authorization: Bearer {IAM JWT}
Content-Type: application/json

Request:
{
  "code": "ABC123"
}

Response (201):
{
  "referralId": "...",
  "referrerId": "...",
  "refereeId": "...",
  "creditGranted": {
    "referee": {"amountCents": 2000, "expiresIn": "2160h"},
    "referrer": {"amountCents": 2000, "expiresIn": "2160h"}
  }
}

Errors:
400 - missing code
404 - invalid referral code
409 - user already claimed a referral
403 - self-referral blocked
429 - referrer hit daily limit
```

**Commerce** -- new endpoint:

```
GET /v1/referrer/me
Authorization: Bearer {IAM JWT}

Response (200):
{
  "referrer": { ... },
  "referralCount": 12,
  "tier": "growth",
  "code": "ABC123",
  "shareUrl": "https://hanzo.ai/signup?ref=ABC123"
}

Response (404):
User has no referrer record. Console should call POST /v1/referrer to create one.
```

**Commerce** -- new endpoint:

```
GET /v1/referrer/code/:code
(public, no auth required -- used for link validation)

Response (200): { "valid": true, "referrerName": "Jane D." }
Response (404): { "valid": false }
```

## Flow 2: Credit Grant

### Sequence

Credits are created in Flow 1 step 9f/9g. Consumption happens via existing billing infrastructure.

```
1. CreditGrant created with:
   - userId: referee or referrer IAM user ID
   - amountCents / remainingCents: from tier config
   - currency: "usd"
   - priority: 10 (lower than purchased credits at priority 5)
   - expiresAt: now + tier.limits.creditExpiryDays
   - tags: "referral-bonus" (referee) or "referral-reward" (referrer)
   - metadata: {referralId, referrerCode, tier}

2. User makes API call (e.g. LLM inference via api.hanzo.ai)
3. Cloud-API calls: POST /v1/billing/usage {user, amount, model, ...}
4. Commerce records withdraw transaction
5. Cloud-API (or billing cycle) calls BurnCredits() which:
   a. Gets active grants sorted by priority ASC, expiry ASC
   b. Deducts from lowest-priority, earliest-expiring first
   c. Referral credits (priority 10) burn after purchased (5) but before trial (20)

3. User views console.hanzo.ai/billing:
   GET /v1/billing/credit-balance/breakdown?userId=...
   Returns: { "breakdown": { "referral-bonus": {cents: 1500}, "purchased": {cents: 5000} } }
```

### No New Code Required for Consumption

`BurnCredits()` in `api/billing/credit_grants.go` already handles priority-based burn-down with meter eligibility. The referral flow only needs to create the grants with correct priority and tags.

## Flow 3: Revenue Share (Affiliate Commission)

### Sequence

When a referred user generates billable usage, the referrer earns a percentage.

```
1. Referred user makes API calls
2. Cloud-API calls: POST /v1/billing/usage
3. Commerce RecordUsage handler (MODIFIED):
   a. Records withdraw transaction (existing)
   b. Checks if user has an active referral:
      Query referral WHERE userId = {user} AND revoked = false
   c. If referred, lookup referrer's tier from referral-program.json
   d. If tier has revenueSharePercent > 0:
      Create Fee {
        type: "affiliate",
        affiliateId: referrer.affiliateId,
        amount: usage.amount * tier.revenueSharePercent / 100,
        currency: "usd",
        status: "pending",
        paymentId: transaction.id
      }
   e. Fee matures to "payable" after cooldown period (7 days, fraud config)

4. Monthly affiliate payout cron (existing cron/payout/affiliate/):
   a. Finds all affiliates with Stripe Connect tokens
   b. Queries payable fees within schedule cutoff
   c. Creates Stripe transfers
   d. Marks fees as "transferred"

5. Alternative: credit-based payout for affiliates without Stripe Connect:
   a. Sum payable fees for affiliate
   b. Create CreditGrant {tag: "affiliate-earnings", amount: sum}
   c. Mark fees as "transferred"
```

### API Changes Required

**Commerce** -- modify `RecordUsage()`:

Add a goroutine after the main transaction that checks for active referral and creates affiliate fees. This is fire-and-forget; usage recording must not fail because of referral tracking.

**Commerce** -- new endpoint:

```
GET /v1/referrer/:id/earnings
Authorization: Bearer {IAM JWT}

Response (200):
{
  "referrerId": "...",
  "tier": "growth",
  "totalReferrals": 15,
  "totalEarnedCents": 45000,
  "pendingCents": 5000,
  "paidCents": 40000,
  "breakdown": [
    {"month": "2026-03", "earnedCents": 12000, "referrals": 3}
  ]
}
```

## Flow 4: OSS Contributor Payouts

### Sequence

```
1. Monthly cron job (new: cron/payout/contributor/contributor.go):
   a. Fetch total billable revenue for the period from transaction ledger
   b. Fetch all SBOM entries for component revenue attribution
   c. Fetch all active, verified contributors
   d. Call CalculatePayouts() (existing algorithm in models/contributor/payout.go)
   e. For each allocation above MinPayoutCents:
      - If contributor.payoutMethod == "stripe":
        Create Stripe transfer to contributor.payoutTarget
      - If contributor.payoutMethod == "crypto":
        Queue crypto payout (manual approval required)
      - If contributor.payoutMethod == "credits":
        Create CreditGrant {tag: "oss-earnings", userId: contributor.userId}
   f. Update contributor.totalEarned, contributor.lastPaid
   g. Publish event: commerce.contributor.payout

2. SBOM scan cron (new: cron/sbom/scan.go):
   a. For each component in referral-program.json ossContributors.componentWeights:
      - Clone/pull repo
      - Run git blame, aggregate by author email
      - Upsert SBOMEntry with authors and line counts
      - Match authors to Contributor records by gitEmail/gitLogin
      - Update Contributor.attributions
   b. This runs weekly, not monthly
```

### API Changes Required

None beyond what already exists. The contributor API already has `calculatePayouts` and `previewPayouts` endpoints. The cron just executes what the preview shows.

## Flow 5: Analytics Integration

### New Event Subjects

Add to `events/schema.go`:

```go
SubjectReferralClicked   = "commerce.referral.clicked"
SubjectReferralSignup    = "commerce.referral.signup"
SubjectReferralCompleted = "commerce.referral.completed"
SubjectCreditGranted     = "commerce.credit.granted"
SubjectCommissionEarned  = "commerce.commission.earned"
SubjectContributorPayout = "commerce.contributor.payout"
```

### Event Publishing Points

| Event | Where | When |
|-------|-------|------|
| `referral.clicked` | hanzo.ai landing page (client-side analytics) | User visits `?ref=CODE` |
| `referral.signup` | hanzo.id callback page (client-side analytics) | Signup completes with ref code present |
| `referral.completed` | Commerce `/v1/referral/claim` handler | Claim succeeds, credits granted |
| `credit.granted` | Commerce `CreateCreditGrant` handler | Any credit grant created |
| `commission.earned` | Commerce `RecordUsage` handler (after fee creation) | Affiliate fee created |
| `contributor.payout` | Commerce contributor payout cron | Payout executed |

Client-side events (`referral.clicked`, `referral.signup`) go to the existing Commerce analytics endpoint:

```
POST /v1/analytics/event
{
  "event": "referral_clicked",
  "properties": {"code": "ABC123", "source": "landing_page"}
}
```

Server-side events go to NATS/JetStream via the existing `Publisher`.

### Console Dashboard Data

Console fetches from Commerce, not from an analytics service:

```
GET /v1/referrer/me           -- code, share URL, tier
GET /v1/referrer/:id/stats    -- referral count, conversion rate, earnings
GET /v1/billing/credit-grants -- user's credits with tag filtering
```

Analytics data (click counts, funnel visualization) comes from the analytics
pixel/event data that Commerce already collects and stores in ClickHouse.

## Implementation Plan

### Phase 1: Core Referral Flow (1 week)

Changes by repo:

**Commerce** (`~/work/hanzo/commerce/`):
1. Add `GET /v1/referrer/me` endpoint -- lookup by userId from IAM JWT
2. Add `GET /v1/referrer/code/:code` endpoint -- public code validation
3. Add `POST /v1/referral/claim` endpoint -- the main claim handler
4. Modify referrer creation to auto-generate unique codes (currently caller-supplied)
5. Wire claim handler to create CreditGrant records (not legacy transactions)
6. Add referral event subjects to `events/schema.go`
7. Add fraud checks: self-referral, cooldown, IP dedup, daily limit per referrer

**hanzo.id** (`~/work/hanzo/id/`):
1. `SignUpForm.tsx`: read `?ref` param from URL, store in `sessionStorage`
2. `callback/page.tsx`: after token exchange, if `sessionStorage` has ref code,
   call `POST commerce.hanzo.ai/v1/referral/claim` with IAM bearer token

**hanzo.ai** (landing/marketing site):
1. Ensure `?ref=CODE` param is preserved through any redirects to `hanzo.id/signup`
2. Fire `referral_clicked` analytics event when `?ref` is in URL

### Phase 2: Revenue Share (1 week)

**Commerce**:
1. Modify `RecordUsage()` to check for active referral and create affiliate fees
2. Add fee maturity cron: pending -> payable after cooldown days
3. Add `GET /v1/referrer/:id/earnings` endpoint
4. Wire tier lookup from `referral-program.json` based on referral count

### Phase 3: Console Dashboard (1 week)

**Console** (`~/work/hanzo/console/` or equivalent):
1. Referral page: show code, share URL, copy button
2. Referral stats: total referred, conversion funnel, earnings
3. Billing page: credit balance breakdown showing referral credits
4. Earnings page: pending/paid commission, monthly breakdown

### Phase 4: OSS Contributor Payouts (2 weeks)

**Commerce**:
1. SBOM scan cron (`cron/sbom/scan.go`): clone repos, git blame, upsert entries
2. Contributor payout cron (`cron/payout/contributor/contributor.go`): execute payouts
3. Stripe Connect integration for contributor payouts
4. Contributor dashboard API endpoints

## File Changes Summary

### New Files

| File | Purpose |
|------|---------|
| `commerce/api/referral/claim.go` | Claim endpoint, code lookup, fraud checks |
| `commerce/api/referral/referral.go` | Route registration for referral endpoints |
| `commerce/cron/payout/contributor/contributor.go` | Monthly contributor payout execution |
| `commerce/cron/sbom/scan.go` | Weekly SBOM scan cron |

### Modified Files

| File | Change |
|------|--------|
| `commerce/api/referrer/referrer.go` | Add `GET /me`, `GET /code/:code` |
| `commerce/api/billing/usage.go` | Add referral fee creation in goroutine |
| `commerce/api/api/api.go` | Register referral claim routes |
| `commerce/events/schema.go` | Add referral/credit/commission subjects |
| `commerce/events/publisher.go` | Add `PublishReferralCompleted()`, etc. |
| `id/components/SignUpForm.tsx` | Store `?ref` in sessionStorage |
| `id/app/callback/page.tsx` | Claim referral after login |

### No Changes Required

| File | Reason |
|------|--------|
| `models/referrer/referrer.go` | Model is complete, SaveReferral() works |
| `models/referral/referral.go` | Model is complete |
| `models/creditgrant/creditgrant.go` | Model is complete |
| `models/affiliate/affiliate.go` | Model is complete |
| `models/fee/fee.go` | Model is complete |
| `api/billing/credit_grants.go` | BurnCredits() already handles priority burn |
| `cron/payout/affiliate/affiliate.go` | Existing affiliate payout works |
| `middleware/iammiddleware/iammiddleware.go` | IAM auth already works |

## Fraud Prevention

All fraud rules from `referral-program.json`:

| Rule | Implementation |
|------|---------------|
| `requireEmailVerification` | IAM enforces email verification before token issuance |
| `blockSelfReferral` | Claim handler checks referrer.userId != referee userId |
| `cooldownDays: 7` | Claim handler checks referee account age >= 7 days from IAM `createdTime` claim |
| `maxReferralsPerDay: 50` | Claim handler counts referrals created today for this referrer |
| `blacklistSameIP` | Claim handler checks referrer.client.ip != request IP (existing Referrer.Blacklisted field) |

Additional:
- Idempotency: one referral per referee userId (query before create)
- Rate limit: standard API rate limiting on claim endpoint
- Revocation: admin can set `referral.revoked = true` to claw back credits

## Tier Promotion

Tier is computed dynamically, not stored:

```go
func (r *Referrer) CurrentTier(program ReferralProgram) Tier {
    count := r.ReferralCount() // query count
    for i := len(program.Tiers) - 1; i >= 0; i-- {
        if count >= program.Tiers[i].MinReferrals {
            return program.Tiers[i]
        }
    }
    return program.Tiers[0]
}
```

When a referral is claimed, the credit amounts come from the referrer's current tier at claim time. Revenue share percentage also comes from the tier at the time usage is recorded.

## Error Handling

Every operation in the claim flow is individually error-handled:

1. Code lookup fails: 404, no side effects
2. Fraud check fails: 403/409/429, no side effects
3. Referral creation fails: 500, no credits granted (atomic)
4. Referee credit grant fails: 500, referral exists but is incomplete -- logged, retriable
5. Referrer credit grant fails: 500, referee got credit, referrer did not -- logged, retriable
6. Event publish fails: logged, non-blocking (goroutine)

The claim endpoint should wrap steps 5-6 (referral create + both credit grants) in a datastore transaction where possible. If the datastore does not support multi-entity transactions, use compensating actions (void grants if referral create fails).

## Production Readiness Checklist

- [ ] All new endpoints have admin token OR IAM JWT auth
- [ ] Claim endpoint is idempotent (safe to retry)
- [ ] Credit grants have correct priority ordering
- [ ] Fraud rules match referral-program.json config
- [ ] Events published to NATS for downstream consumers
- [ ] Structured logging at every decision point
- [ ] No plaintext secrets (Stripe Connect tokens in KMS)
- [ ] Tests: claim happy path, self-referral block, duplicate claim, daily limit, tier promotion
- [ ] Health check unaffected by new routes
- [ ] Graceful degradation: if NATS is down, claim still succeeds (event publish is fire-and-forget)
