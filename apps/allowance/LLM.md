# LLM.md — apps/allowance

Guidance for apps/allowance. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The allowance leg — a spent plan falls through to credit

`Stand` is the money ladder: an active licence admits, otherwise the prepaid
ledger decides, and Unpaid requires BOTH authorities to have answered no. It
admitted a subscriber UNCONDITIONALLY, so the usage windows a plan publishes —
that the rollup reports and the account page renders — bound nothing on the
request path.

`StandWithin(ctx, lic, allow, w)` adds the leg. The policy it encodes: a plan
INCLUDES usage and prepaid credit is money bought separately, so a subscriber who
has spent their included usage does not stop — they fall through to the credit
leg and pay as they go, and are refused only when they have neither. Allowance
first, then credit.

**It can only ever REMOVE an admission, so every uncertainty admits.** An absent
reader, a plan declaring no windows, a ledger that did not answer: all
`AllowanceUnknown`, all still `Subscribed`. Refusing a paying customer because a
counter was unreadable is a worse failure than serving one request past a bound,
and this ladder already takes that side everywhere — "a balance that cannot be
read is unknown, never zero".

`Stand` is now `StandWithin` with the leg unknown, so every existing client behaves
byte-identically. A test pins that equivalence across every licence x ledger
combination; another pins each branch of the new leg. `AllowanceChecker` is
OPTIONAL and resolved by type assertion the way `PlanChecker` is — a commerce
that cannot answer simply does not implement it.

Counting lives in commerce (`api/billing/windows.go`): four calendar-aligned
windows over the `api-usage` ledger rows, one query, and `plan.LevelWindows`
scales them by price level. See commerce/LLM.md "One way to price a UNIT".

## The Free plan is one pool: `GET /v1/allowance` says so

The Free plan is limited usage from ONE pool every free user shares: the
OpenRouter accounts (`OPENROUTER_API_KEY`, `_2`, `_3` in KMS) that hanzoai/ai
spends in turn for free routes (ai LLM.md "The free pool"). Two bounds, one
place each, both SuperAdmin switches in the admin.hanzo.ai flags cockpit:

- **Per person** — `ai_session_free` (3 a session: a window that opens at the
  person's first free call after the last one ended and runs
  `ai_plan_session_hours`, 5, held to 1..168 — the same window a paid plan's
  session is, apps/ai/limits), counted by apps/allowance on served calls only; a
  negative figure reads as the shipped 3. `ai_allowance_free` (a UTC day) and
  `ai_rate_free` (a UTC hour) are UNSET, and unset is no limit; an admin can add
  either. The session's start is kept on the KV per person (`coord.Span`, in
  whole seconds of the allowance process's clock) and its count under
  `session:<start>:<person>`, so the reset is exact to the second from the call
  that opened it; a burst that opens one counts into the one that opened first
  (`TestABurstIsHeldToThree`). A take answers the session it counted in
  (`start`) and a give names it back (`AllowanceIn.Start`), so an unserved
  call's unit returns to that session and no other. A visitor on the public lane
  has no session: `ai_allowance_public`, 3 a UTC day. A named person whose plan
  could not be read (a commerce blip) is held to that same figure on their
  SESSION, the count a Free person already keeps. While the KV does not answer,
  a pod whose share rounds to none refuses with `resets` a minute out (`outage`).
  The usage policy page (admin.hanzo.ai) shows these as `sessions`,
  `sessionHours` and `tiers`.
- **Org switches** — `ai_allowance_org_<org>` and `ai_rate_org_<org>` (-1 =
  follow the tier) exist for `admin` and `hanzo`. **hanzo is the signup org
  every consumer joins** (`account.SignupOrg`), so `ai_allowance_org_hanzo` and
  `ai_rate_org_hanzo` hold EVERY consumer to their figure, not Hanzo's people
  alone. The session has one org switch, `ai_session_org_admin`; there is no
  `ai_session_org_hanzo`.
- **Taking a switch back** — unset it: `PUT /v1/admin/flags/<key>` with
  `{"active": false}` (an inactive definition reads as unset; idempotent). Never
  store 0 to mean "no limit": an image from before the session reads a stored
  `ai_allowance_free` 0 as no limit at all, so a rollback after it leaves Free
  unbounded, while unset rolls back to that image's own 3 a day. After the
  session deploy, unset `ai_allowance_free` unconditionally; read the
  `*_org_hanzo` switches before relying on them.
- **The pool** — its state is what the accounts said, held in the ai process
  (`ai_pool` plane op, apps/ai/pool_rpc.go): `available` | `busy` |
  `exhausted` and when it refills. No account count crosses.

`GET /v1/allowance` answers `{plan, limit, used, spent, window, resets,
pooled, pool: {state, resets}}`; `window` is `hour`, `session` or `day`, and
`resets` is 0 for a session not yet running. `pooled` is true exactly where a window bounds
the caller; `pool` is read from ai for the usage page only (2s bound, absent
when ai does not answer) — the gate's `allowance_read` never waits on it. A paid
plan is `limit: 0`, not pooled, no pool. The usage panes (@hanzo/usage panel,
@hanzo/build Settings → Usage) draw this and nothing invented.

At the limit: 429 `allowance_spent` for a person's share. From zen (Zen and
Enso free SKUs, apps/zen `freeSpent`): `{"error":{"message":"Usage reached",
"code":"allowance_spent","type":"insufficient_quota","limit":"session",
"resets_at":"<RFC3339 UTC>","billingUrl":…}}` plus `X-RateLimit-Reset` (unix
seconds); a client words the reset in its reader's local time. From ai's gate
(other free models): hanzoai/ai's sentence, `resets_at`, `upgrade_url`. 429 `pool_busy` / `pool_exhausted` when an account is
benched and the pool cannot serve. `upgrade_url` is hanzo.ai/pay. Retry-After is
named only when the reset is minutes away (apps/zen `maxRetryAfter`, for a plan's
caps too): Hanzo Dev's coding harness honours it to the letter, and told to come
back at 00:00 UTC it slept inside a fifteen-minute run until the run died, saying
nothing. The reset is still in `resets_at`, `X-RateLimit-Reset` and the message.

A payment moves the caller at once: allowance reads the tier through commerce
on every ask and holds nothing, so the next call after commerce records the
subscription is on the paid plan's terms (apps/allowance/pool_test.go,
apps/ai/limits `TestAPlanBoughtIsReadOnTheNextCall`).
