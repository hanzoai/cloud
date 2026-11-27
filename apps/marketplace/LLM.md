# LLM.md — apps/marketplace

Guidance for apps/marketplace. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Experts: a person is a marketplace listing (`apps/marketplace/expert.go`)

A person who makes their time available is a listing of `kind: expert`, hired through
the same job as every other kind (`POST /v1/marketplace/jobs`, then accept, deliver,
release, dispute, refund, feedback). Nothing in jobs or payment knows about experts.

- **The person is the proof.** `tool` and `ref` are the caller's IAM subject (`X-User-Id`),
  never taken off the wire; a different `tool` is a 422. Any member of an org lists
  themself (not only an admin), once per org (unique index on `publisher_org, person`;
  a second is 409). The expert or an org admin edits and withdraws it. A SuperAdmin
  inspecting another org lists no expert there.
- **Fields beyond Listing**, on `Listing.expert` (columns `rate, areas, languages,
  timezone, availability, person, verified`): `rate` is the hourly USD rate, `price`
  stays the fixed engagement; an expert sets one or both, and any positive amount needs
  `recipient` as for every priced listing. `areas` is the closed list `Areas`
  (configure, base, commerce, bot, agents, cloud, models, creative, freelance), kept in
  list order; `languages` are 1 to 8 lowercase BCP 47 tags; `timezone` is an IANA name;
  `availability` is free-form hours. A PATCH `expert` replaces the whole profile.
- **The shop** (`GET /v1/marketplace/shop?kind=expert&area=<slug>`) shows `expert`
  without `person` and with `tool: ""`. `area` filters; `q` also reads areas and
  languages; an hourly rate counts as priced.
- **`verified` is staff's.** `POST /v1/marketplace/listings/:id/verify {verified}` is
  SuperAdmin only (`cloud.Super`, owner == admin; an org admin and the expert get 403),
  writes the row and the audit record (`marketplace.expert.verified` /
  `marketplace.expert.unverified`, in the acting org's trail) in one transaction, and no
  other write touches the column: an edit keeps it.
- **Events** (`eventpeer.EventCapture`, product `marketplace`, detached and bounded as in
  `apps/commerce/emit.go`, filed in the expert's org, subject = the expert's IAM subject,
  event id = name:listing-or-job so a repeat is one row). On the bus the name folds to
  `event.expert_listed` and so on (`apps/event/bus.go subjectFor`).

  | name | when | attributes |
  |---|---|---|
  | `expert.listed` | an expert listing is published | listing, org, person, areas, public |
  | `expert.verified` | staff verify it (not on unverify or repeat) | listing, org, person, areas |
  | `expert.job.delivered` | the expert delivers a job hired through the listing | listing, org, person, areas, job, buyer, rating (the expert's mean stars so far; absent until reviewed) |
  | `expert.review` | the buyer rates that job | listing, org, person, areas, job, buyer, rating (the stars given) |

  A job hired through any other listing, and the expert's own rating of the buyer, state nothing.
- **Skills and links** (`expert.skills`, `expert.links`, columns `skills`, `links` as JSON
  arrays): 0 to 20 skills of 1 to 40 characters, 0 to 8 https URLs of at most 200 bytes with a
  host and no login. Input is trimmed, blanks dropped, a skill kept once by its first
  spelling whatever its case, a link kept once; too long, too many, a control character or a
  bad URL is a 400. An edit that names `expert` replaces the whole profile, so omitted means
  empty. Skills are searched by `q`.
- **The rewards program, beside the listing** (`reward.go`). Every expert row of the shop, the
  shop item and the caller's own listings gains `expert.reward` only when the program says the
  expert's profile is public: `level, levelName, points, tier?, badges (top 3, rarest first),
  pillars`. `tier` is the highest of the nine token-milestone badges held (`tiers`, ids from
  the seed: first-100k, million-token-club, five-million, ten-million, century, billionaire,
  titan, colossus, trillion-club; labels 100K+ to 1T+), never a count; its `id` is the badge id
  and the `tokenTier` filter's name for it is `Param` (100k to 1t). The program is another
  process, so the marketplace asks it over the plane (`reward_people`), as the platform, from
  a context with no request (a request on the context would be forwarded in place of the
  platform). It is an addition: not mounted, slow (3s), erroring or silent about a subject
  means the field is absent and the read succeeds. Answers, and the silence about a subject,
  are kept a minute per subject; a failed ask is not repeated for ten seconds. No subject,
  email, org, redeemable balance, claim or usage figure ever reaches a read.
- **Order, filters and pages** on `GET /v1/marketplace/shop?kind=expert` (`roster.go`):
  `sort` (tokens, level, points, rating, jobs, rate, newest; default newest), `dir` (asc, desc;
  default desc), `language` (a tag; `zh` keeps `zh-cn`), `verified` (true keeps verified, false
  is no filter), `minLevel` (0 is no filter), `badge`, `tokenTier` (at least), `rateMin` and
  `rateMax` (USD, hourly rate, an expert with none is excluded when either is given),
  `product`. Bad values are 400s; any of these without `kind=expert` is a 400 (`area` and the
  ordinary filters keep working as before). With none of the new parameters the read is the
  store's SQL page and costs what it always did. With any, the experts the ordinary filters
  match are read whole (more than 5,000 is a 400 "narrow them"), dressed, filtered, ordered and
  paged in Go; `total` is the kept count, `facets` are the ordinary filters'. Reward orders
  and filters keep only public profiles, and an order puts what it cannot place (private
  profile, unrated, no hourly rate, score 0 in the product) last in either direction; every
  tie is broken by the newer listing, then the id, so pages never repeat or skip.
- **Expertise per product** (`expertise.go`). Products, in order: dev, bot, team, base,
  commerce, cloud, agents, kai, enso, zen, creative (the `products` table holds each one's
  label, areas, badges, model family and public repos, the one place those are mapped). An
  expert's score in a product is the sum of up to four parts, shown as
  `products: [{product, score, parts: {usage, badges, contributions, jobs}}]` (best three,
  score above 0) and `top: [product]` (products in which they rank among the best three of
  all public experts; the ranking is of everyone, cached a minute, and a listing outside it
  is scored alone and ranks in nothing). `product=<p>` orders the shop by that score, score 0
  last; nobody is dropped.

  | part | real source | counts for |
  |---|---|---|
  | `usage` | `hanzo.usage_rollup_daily` tokens by model family, through `reward_people` | kai, enso, zen only |
  | `badges` | badges held, from the seed: try-dev, try-bot, try-team, try-base, try-commerce, zen-pioneer, enso-navigator, kai-strategist | those eight products |
  | `contributions` | awarded `github.pr.merged` / `bounty.completed` claims by `Fields.repo` against the `repos` table | every product that has a repo row |
  | `jobs` | the listing's released jobs and mean stars (area to products: configure to dev and team, base, commerce, bot, agents, cloud, creative, models to kai, enso and zen; freelance to none) | any expert, public profile or not |

  Usage, badges and contributions count only for a public reward profile. **Missing, never
  estimated:** per-product usage for dev, bot, team, base, commerce, cloud, agents and creative
  (the usage ledger `hanzo.cloud_usage` records model, provider, org, user and project, never the
  product or app a call came through), and badges for cloud, agents and creative (the seed has
  none). `Weights` (`weights` in `expertise.go`, no env): usage `10 * log10(1 + tokens / 100,000)`,
  badge 20, merged PR 25, bounty 50, job `10 * stars/5` (0.5 for a listing nobody rated). A job
  is counted for every product of every area the listing names, since a job records no area.
