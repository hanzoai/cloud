# LLM.md — apps/catalog

Guidance for apps/catalog. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The cross-org catalog (`apps/catalog`, `/v1/catalog`)

Everything the fleet has built — hanzo, lux and zoo repos, plus every site this
deployment serves — as ONE searchable corpus. It owns no store: the rows live in
`apps/index` under the uid `catalog`, so relevance, paging and encryption at rest
are the index's. What catalog adds is the one thing a per-org index cannot express,
a corpus that spans orgs, and it does that with a SECOND corpus rather than a
weaker filter:

- `~catalog` is the published, world-readable corpus. The leading `~` is
  load-bearing: an org id is minted from a validated IAM owner claim and IAM org
  slugs begin with an alphanumeric, so no principal can ever BE `~catalog`.
- the caller's own org holds their private rows, read with `principal.Org` and
  nothing else. Another tenant never RUNS the query that would return them.

**There is no write route, on purpose.** The first cut had `PUT /v1/catalog` behind
`principal.Super`; that gate is correct and unusable, because SuperAdmin is
human-only here and a cron would have needed a second fabric credential. Instead
`sync.go` reconciles the corpus in-process every hour (first pass delayed 90s so a
boot never waits on the network) from the public repos of the source orgs
(`CLOUD_CATALOG_ORGS`, default the fleet) and `projects.LiveSites`. A failed source
keeps the last good corpus — a GitHub outage must not prune the catalog to empty.

Which corpus a row lands in IS the tenancy rule: a public repo is public by
definition, our OWN org's live sites (`CLOUD_CATALOG_PLATFORM_ORG`, default `hanzo`)
are published because they are the demos a visitor is meant to fork, and every other
org's live sites land in that org's corpus. `TestSyncRoutesSitesByOrg` asserts the
routing itself, because that is where a customer's project would leak.

**`forkable` means we can hand you a public source**, and it has to be able to say
no or it is a label rather than a filter. It was `true` on all 579 rows and read as
`c.Query("forkable") == "true"`, so the facet was `{"true": 579}` and
`?forkable=false` silently meant *no filter*. Now: a repo that is itself a fork of a
third-party upstream is not ours to hand over, a live demo with no public source has
nothing to hand over, and a declared `upstream` credit vetoes both. The query is
tri-state (`boolQuery`/`strconv.ParseBool` — set-true, set-false, unasked) and
`facet` counts both sides through the same loop as every other dimension.
`Entry.Forkable` is NOT `omitempty`: false is an answer.

**`origin` is what a row IS to you**, and it is the axis the two hanzo.app lanes
are cut on. The corpus flattened 579 rows into one list in which a curated starter
you fork FROM, a stranger's remix of one, somebody else's paid UI kit and
`luxfi/node` rendered identically — the labelling complaint underneath was an
information-architecture bug, because there was no field to ask the question with.
Four values, `template | community | third-party | product` (`origin.go`), all
DERIVED:

- the source table (`defaultOrgs`) now says both the brand a person browses by AND
  the lane, because both are facts about the org. `hanzo-templates` → starters,
  the apps orgs and `hanzo-community` → what people built, everything else → our
  own software. `hanzo-community` is listed before it has repos, so the auto-publish
  lane is fed the hour that lands.
- a live site is one of OUR starters' demos when the CURATED GALLERY says its slug
  is, read FORWARD through the three slugs the fork flow derives (`<slug>`,
  `<slug>-<variant>`, `<slug>-template`), so a new template files its own demo and
  a community app cannot fall in by spelling. Recorded lineage outranks that (a
  remix is a remix), and a declared `upstream` outranks everything.
- `fromRepo` lets GitHub's own `fork` bit override the address: a starter we
  vendored from somebody else is not a starter of ours.

`origin` is deliberately NOT braided with authorship. Origin says which lane;
**`org` says whose work it is** — the account that pays for a project, which the
tenancy boundary enforces and no request can forge. Keeping them separate is what
makes *community apps that are NOT ours* askable, which is the whole point of a
community lane. Both are faceted and both filter, plus `?template=<parent id>`
for one lineage — a facet nobody can act on is a rail that lies.

**Visibility, not authorship, decides who appears** (`apps/projects/visibility.go`).
One axis owned by the publisher — `public` (default) or `private` — plus
`hidden`, the platform's subtractive moderation from admin.hanzo.ai. A row is
listed iff `public AND NOT hidden`, enforced in `LiveSites`' own query so a
consumer that forgets to filter cannot leak anything. Publishing is **ungated**
(a community you must be admitted to does not grow); going private is the paid
feature and rides the same `cloud.ResourceMeter` funded-org gate as hosting,
agents and functions, so an unfunded org asking for it gets a 402 rather than
being silently published. Moderation is the one admin-only field, and it is safe
to be one precisely because it only ever subtracts — the same shape as `Apex`'s
reserved-host denylist, never an allowlist.

**Third-party is attributed or NOT LISTED.** A fork holds somebody else's code
under one of our org headers. GitHub's org listing omits `parent`, so `credit`
spends one extra request per fork (a few dozen an hour against a listing pass of
about a dozen) and DROPS any fork it cannot name rather than showing it authorless
— the safe direction for *whose is this* is silence, not a guess. `NOASSERTION` is
GitHub failing to identify a licence, not a licence, so those rows carry the
upstream and state no terms. Live: 437 repos, 40 third-party, every one naming its
real parent.
