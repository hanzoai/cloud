# LLM.md — apps/leaderboard

Guidance for apps/leaderboard. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The public standing, and the one score (`apps/leaderboard/standing.go`, `client/points.go`)

`GET /v1/leaderboard/standing` is public: no principal is asked for. The op resolves the caller
with `principal.OrgFrom` and reads an absent one as a reader (the pattern `apps/research`'s
public listing uses); every other op of the capability still 401s without one, and
`TestStandingIsPublicAndTheRestIsNot` holds both. Auth is loosened nowhere else.

- **Who is ranked**: every org IAM holds (`iampeer.IAMDirectory` as the admin org), held orgs
  (`authz.IsHeldOrg`) aside. Orgs with 0 points sit at the bottom, in order of signup. Rank is
  points, then the earlier signup (`created`), then org id.
- **Points are `client.Points`, one function** the standing, the bounty board
  (`BountyHunter.TotalScore`) and the beta waitlist all call: 1 per 100,000 tokens ever used (the
  all-time usage rollup), +1 per org that signed up with the referral code, +20 per such org with
  metered spend, +1 per verified GitHub star (once per person per repo), +1 per founder follow,
  plus bounty award points. Stars and follows are counted from the `actions` of the beta
  waitlist records, by the org each record acts in; a held org's records are no one's. NOT in it
  yet, because cloud records neither: forks (no `fork` webhook handler in
  `apps/provider/github_webhook.go`, and no GitHub-login to IAM-user map across tenants), stars of
  repos outside `hanzoai/*`, and the +10 OSS badge (no verifier). Nothing is estimated for them.
- **The window is the enforcement.** A signed-in reader gets ranks `(own - reveal)` to `(own +
  5)`; `reveal` is `revealSteps`: 0 pts 5, 100 pts 10, 500 pts 25, 2,500 pts 100, 10,000 all.
  `locked` counts the ranks above and they are never sent. A reader signed out, or an org IAM does
  not hold, gets the last 10 ranks. `self.next` is the next rung: ranks it unlocks, the points it
  needs (`at`) and what is left (`more`).
- **Names**: masked in the middle (`mask`: 6+ characters keep two at each end, shorter keep the
  first); the caller's own whole with `self`; an org that listed (`PUT /v1/leaderboard/optin[/org]`)
  whole; an org that withdrew has `handle: ""`. An org's own answer (org_optin) is final; else its
  users' (user_optin). A row carries rank, handle, points, self and nothing else.

### The pulse and the live feed (`pulse.go`, `feed.go`)

Both public, both aggregate or masked, neither takes a principal.

- `GET /v1/leaderboard/pulse` (typed): 30 days by day and the last 24 hours by hour of tokens
  used and orgs active (usage rollup by day, `hanzo.cloud_usage` by hour), orgs signed up (IAM
  directory `created`), orgs that signed up through a referral link (the referral ledger over the
  plane, `client.ReferralSeries`: moments only, no org), and orgs trying a model for the first
  time (first day of each org-model pair in the rollup; by day only), with the models most tried
  (never a base model Zen is built from). A series whose source cannot be read is left out of the
  answer and named in `missing`, never drawn as zeroes. `products` and `shares` are always in
  `missing`: the usage ledger has a model column and no product one, and no share press or
  verification is written to the warehouse. One shared read a minute (`memo`).
- `GET /v1/leaderboard/feed` is a RAW route (SSE; a typed op writes one JSON out), so it is in
  `untypedByDesign` and `openapi/untyped.json`. A hub per process (`hubFor`) holds the last 20
  lines and pushes each to its readers; a reader is replayed the 20 and cut off if it falls 64
  behind. Lines: `joined` (bus pulse `org`), `referred` (`referral`, announced where a referral is
  claimed), `bounty` (announced by the award), `expert` (announced where a public expert listing
  is created), and, from one read a minute that runs only while someone watches, `milestone` (the
  highest token milestone an org crossed since the last read), `level` (the highest level),
  `tried` (an org-model pair new since the last read) and `hour` (the tokens the last whole hour
  carried). The first read only sets the baseline; nothing is said per request. 30 lines a minute
  leave the hub. An empty hub is filled from what is durable (newest orgs, awards). Names are the
  standing's (`handleOf`); a line holds `id, kind, at, handle, detail` and nothing else.

- `GET /v1/leaderboard/badge/:handle` (`badge.go`) is a RAW route (`image/svg+xml` bytes +
  ETag/304; in `untypedByDesign` and `openapi/untyped.json`), public (no credential), rendering
  a listed org's standing rank and points via `github.com/hanzoai/reward/emblem`. Only orgs with
  `choices[org].Listed == true` resolve (by org slug or chosen handle, case-insensitive); unlisted,
  withdrawn, held and unknown handles all answer 404. Cached hourly in-process (bounded 4096 entries,
  serving stale on transient warehouse errors) and at the edge (`public, max-age=3600, s-maxage=3600,
  stale-while-revalidate=86400`), invalidated immediately on opt-in writes.

### The galaxy (`galaxy.go`)

`GET /v1/leaderboard/galaxy` (typed, public, no principal asked; `hanzo.ai/leaderboard` draws it)
is the whole line at once: every org the standing ranks (held orgs aside) is a star.

- **The window is the enforcement, as in the standing.** A star's exact `points` and its `mask` leave
  the server only for the ranks the reader may see, by the one rule the standing's rows are cut by
  (`seen`, from `revealSteps` and `viewport`; `standing` calls it too): a signed-in reader's own rank,
  5 below and as many above as their points earned; any other reader, the signed out, an org the board
  does not hold and the platform's own, the last `tailRows`. The reader's own star is always inside.
  Every other star keeps its place, `band`, `size` and `sponsor`, has `points: -1` and `mask: ""`, and is
  drawn: the sky is the same for everyone (`TestTheSkyIsTheSameSkyForEveryReader`).
  `TestTheGalaxyIsCutByTheStandingsWindow` holds, for eight kinds of reader, the ranks the galaxy
  carries points for equal to the rows the standing sends, with the same points and names.
  `size` is `bits.Len(points)`: a factor of two, all a locked star shows of its score. The shared read holds the
  exact columns; `galaxyRead.window` cuts a copy per request.
- **Columns, not objects**: `stars.{id,points,size,band,sponsor,mask}` are arrays of one length, best first
  (the standing's order), about 53% of the bytes of one object per star at ~1,850 orgs
  (`TestColumnsAreAboutHalfTheBytesOfObjects` logs both).
- **Bands are the reward program's table.** `bands` is the band names of the program's levels in order and
  `band` indexes it. The read asks the reward capability (`reward_levels`, a peer op, `apps/reward/levels.go`:
  platform only, `Program.LevelOf` settles an empty band) once a read of the galaxy, and uses what it
  answers: a table staff edit shows within the minute. The seeded table (100 levels, level n from 10 n²
  points, ten bands Photon to Singularity: `seeded`) stands in only where that read fails or answers no table
  (`tableOf`: a first level above 0, a level not above the last, or a level with no band). The leaderboard and
  reward are separate processes, so this is a peer call, not an import.
- **`center`** is `tokens24h` and `activeOrgs24h` from one read of `hanzo.cloud_usage` since the
  pulse's first hour (`newAxes(now).hour0`), so the tokens equal the sum of `/pulse` `tokens.hours`.
- **Opaque ids**: `id` is the first `idWidth(n)` bytes (4 at ~1,850 orgs, 6 base64url characters) of
  HMAC-SHA256(org) under a key drawn for each read (`opaque`; a draw with two equal ids is drawn
  again). The same star has the same id in `id` and in every `sponsor` that names it, within one
  read; the next read, a minute on, has new ids, so an id follows no star over time and no guess of
  an org id reproduces one. Never an org id, a handle beyond `mask`, or an email.
- **`mask`** is `handleOf`, the standing's name: masked in the middle, whole where the org listed
  itself, empty where it withdrew (a withdrawn org is still a star; its `points` is not -1). It reads the
  choices once a read, so an opt-out shows within the minute, where the standing shows it at once.
- **`sponsor`** is the referral ledger's edges (`referralpeer.ReferralEdges`, as the brand, 1000 a
  page) between two ranked orgs. One referrer an org: the first claim stands. A self-edge, an edge to
  an org off the line, and an edge that would close a loop (`reaches`) are left out, so the links are
  a forest a page follows up without a visited set.
- **`self`** is the signed-in reader's star id (empty otherwise), set with the window on a copy of the view
  per request. `Cache-Control: no-store` as the standing's, since the answer differs by reader; the cache is the
  server's, one read a minute (`galaxies`, `memo`). A source that cannot be read serves the last good read, or
  `available: false` with empty columns.
