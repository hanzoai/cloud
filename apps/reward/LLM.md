# LLM.md — apps/reward

Guidance for apps/reward. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Rewards (apps/reward, the `reward` capability)

`hanzoai/reward` (the rules engine: programs, rules, claims, the append-only ledger,
levels, badges) is mounted at `/v1/reward`, its staff queue at `/v1/admin/reward`.
`apps/reward` hands it the store and the happenings and decides nothing: it states
events, the engine makes a claim for each rule that reads one, every rule is manual,
and staff verify each claim at admin.hanzo.ai against the evidence it carries.
Eager (manifest) because the feeders run in the child.

- **Store.** `IAM_STORE_BACKEND=sql` puts the engine's rows on hanzo-sql over ZAP
  (`IAM_SQL_ADDR`, `store.Connect`, which writes none of IAM's into it); otherwise the
  encrypted `reward` SQLite file from `locus.System`. Kinds are `reward-*`.
- **Evidence.** An event that took place on someone else's site must carry
  `{source, url, checks: [{fetched_at, status, public, ref_found?, author_match}], recheck?}`
  or `needs_review: true` with no checks. A feeder never claims a check it did not run:
  when GitHub cannot be read (rate limit, outage) the claim arrives `needs_review`.
- **Feeders** (all idempotent by event id; `sent:` marks and `cursor:` days live in the
  store; loops run under `Coord.Lead`):
  - `usage.tokens` (`usage.go`): milestones crossed since the last pass, one event per
    threshold, `tokens` = the lifetime total, read from `hanzo.usage_rollup_daily`.
    Forward only: history is the legacy backfill's.
  - `model.used` (`usage.go`): first day a person's rollup rows show a Hanzo model with
    tokens; an id with a `/` (a vendor's path) is never a model. Forward only.
  - `referral.activated` / `referral.retained` (`referral.go`): from the referee org's
    first and last day of usage, credited to the referrer org's owner (first live admin).
  - `referral.linked` (`referral.go`): each referral edge links the referee org's owner
    to the referrer org's owner as sponsor (once; the engine refuses self and cycles).
    The referred person is welcomed too (`referral.welcomed`, the seed's `welcome-bonus`,
    +10 once). The program's `upline` setting ([10, 5, 2] in the seed, edited in admin) pays the
    sponsors one to three levels up that share of every claim awarded to a member, as
    lifetime points only, never redeemable or credit. `/v1/reward/me` shows `network`.
  - `referral.network` (`referral.go`): +1 to the referrer's own sponsor for each friend
    who activates (depth 2 only). `referral.milestone`: every fifth activated friend a
    referrer has (5, 10, ...), one event per multiple, paying the Spark ... Cosmos badges.
  - `tenure.reached` (1, 6, 12, 24, 36 months since first sign-up), `streak.reached` (7, 30,
    100 consecutive days with metered usage, the longest run in the rollup) and
    `founder.eligible` (`tenure.go`): daily, history included. Founders are everyone with a
    beta or alpha waitlist record, and every account made before the program's
    `foundingCutoff` once staff set one (no default; only applicants qualify until then).
    CRM applicants with no account earn nothing until they have one.
  - `github.*` (`github.go`): the provider app tells every verified `pull_request`,
    `issues`, `star`, `watch`, `fork` delivery over the plane (`reward_github`); the
    GitHub account is matched by the numeric id IAM wrote at sign-in (`iam_github_people`,
    oldest live person row stands). Each is confirmed against GitHub's API before it is
    stated. A bounty label (`bounty-starter|standard|advanced`) states `bounty.completed`
    instead of the ordinary merge. `POST /v1/reward/me/github/check` finds a person's
    stars, follows and forks (GitHub sends no follow delivery); `POST
    /v1/reward/me/github/badge` verifies a README badge (public, non-fork repo owned by the linked
    account whose README carries a badge IMAGE, in markdown or an img tag, query ignored:
    `api.hanzo.ai/v1/reward/badge/<handle>.svg` (profile), `api.hanzo.ai/v1/leaderboard/badge/<handle>.svg`
    (rank) or `hanzo.ai/badges/built-with-hanzo[-dark].svg` (built)); one event per kind found,
    each paying once per repo and kind (`repo+badge`). Deploy to Hanzo is not built yet.
  - `social.post.verified` (`post.go`, `social/`): see below.
  - `discord.joined`: the provider app states it (`reward_event`, the plane op any
    process uses to state a catalog event; the platform alone asks) when a person finishes
    the Discord link (identify + IAM sign-in).
- **Posts.** A pasted (or scanned) post is read server-side through `apps/reward/social`
  (one SSRF guard: https, port 443, allow-listed hosts, public addresses only, no
  redirects). Read from public data: X (oEmbed), Bluesky, Reddit, Hacker News, Mastodon,
  DEV, Medium (author RSS, latest ~10 only) and Hashnode (page). Not readable, so stated
  `needs_review` at once: LinkedIn, TikTok (its oEmbed names no caption or author),
  YouTube, Threads, Instagram, Facebook (a key or token is needed and none is known to
  exist in KMS), and every blog not on the allow-list. The post must be public, the
  member's (when they said who they are; a typed handle can fail the author check but
  never vouch for it, so a match is `unknown`) and carry their org's referral code; a
  member of a held org has no code of their own and holds nothing. Held PENDING for 7 days and looked at every day; the first
  look that finds it gone, private or without the link marks it REMOVED. A removed post
  back up restarts the week via `POST /v1/reward/me/posts/:id/recheck` (10 an hour) or
  staff's `POST /v1/admin/reward/posts/:id/recheck`. At day 7 it is stated as
  `social.post.verified` with the whole history as evidence; pending is never in the
  ledger. `week` is the ISO week of publication; the seed's `weekly-share` is keyed
  `network+week`. LinkedIn and every network cloud cannot read are stated at once as
  `needs_review`.
- **Levels.** The seed has levels 0 to 99 in ten bands (Photon ... Singularity), level n
  at `10*n*n` lifetime points; `/v1/reward/me` and the profile carry `band`; the standing
  board's reveal rungs are the band boundaries (`apps/leaderboard/standing.go`). The engine
  tops an already-seeded program up on mount (new rules and badges appended, the level
  table replaced only if staff never changed it).
- **Public rules.** `GET /v1/reward/rules` (no credential) lists the active rules of the
  active programs by pillar, each with the event in plain words, points, badge, credit
  (only where a credit action exists), a limits sentence and whether staff review it.
  It is the one list of what the program pays: the bounty board's hard-coded rules are
  gone. The program's `upline` and budget stay the staff's.
- **Legacy backfill** (`POST /v1/admin/reward/legacy`, SuperAdmin, run once): tokens,
  referred signups, referred orgs with spend, verified stars and follows, bounty awards,
  each by `client.Points` (the one score), credited to people; the trail names who ran it.
  No source records a fork before the program, so none is written.
- **What the marketplace asks** (`reward_people`, a plane op; `profiles.go`). For the
  subjects it names (at most 200) and the model families it wants usage on, it answers
  only for a subject whose profile is public: the profile as `Show` gives it (level,
  points, the three rarest badges, claims awarded per pillar) plus every badge id held,
  the awarded `github.pr.merged` and `bounty.completed` claims counted by repo and event
  (`Fields.repo`, lowercase), and lifetime tokens and requests per family from the usage
  rollup (a model id is of a family when it is the family or the family then a dash or a
  digit, and never a `provider/model` path). A private profile, none made and an unknown
  subject are all absent. Platform only (`platform(ctx)`). The IAM directory that maps a
  subject to the ledger's `owner/name` is kept five minutes (`rolled`).
- **Not done.** `product.used` (no ledger or warehouse record of a member's use of a
  product exists to read), YouTube, Threads, Instagram and Facebook auto-verification (no API token known
  to exist in KMS; they go to review), X and LinkedIn by connected account (apps/social
  holds an org's publishing apps, not a member's reading connection), credit actions
  (`Config.Credits` is nil: the seed has none).
