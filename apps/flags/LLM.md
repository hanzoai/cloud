# LLM.md — apps/flags

Guidance for apps/flags. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Beta access is ONE platform flag: `beta` (apps/flags `access.go`)

    GET  /v1/flag/platform                  {"flags":{"beta":bool}} for the signed-in caller; 401 unsigned, 503 store unreadable
    plane flags_on {key}                    → {on}; the caller's validated org + user ride the call (client.FlagsOn)
    GET  /v1/admin/flags                    the board; `beta` carries principal:true + its stored definition
    PUT  /v1/admin/flags/beta               SuperAdmin writes the definition (flags_set refuses anyone else)
- **The predicate:** a caller has beta iff the platform switch `beta` is on for it — its
  definition in the reserved platform/platform store, evaluated by the one engine
  (`flags.On` → `Assign`) against the gateway-validated principal: properties `org` (the
  validated org, `principal.Acting`, never a raw X-Org-Id) and `user` (the IAM `sub`). The
  rollout buckets the org; in a shared sign-up org (`authz.IsHeldOrg`, not `admin`) it
  buckets the person, so no rollout sweeps everyone who signed up. Nothing stored, an
  inactive definition, or one the engine cannot compute is off; an unreadable store is an
  error and every reader takes it as off.
- **A principal switch** (`Def.Principal`) is a bool with no env and default false
  (`Register` panics otherwise), has no deployment-wide value (`Bool("beta")` is false,
  `flags_value` refuses it), and the board shows `principal` + `definition` instead.
- **Targeting is the cockpit's** (hanzoai/admin Feature flags, `lib/targets.ts`): rules
  on `org` and `user` are written as anchored, escaped `regex` (`^(?:acme|globex)$`),
  because the engine's `exact` folds case and `acme` ≠ `ACME`; a rollout is a group with no
  properties. Empty targeting is written `active:false` — an active definition with no
  groups is on for EVERYONE.
- **Nobody sets it for themselves:** no tenant can name the platform store
  (`cloud.Reserved`), `flags_set` answers only `principal.SuperFrom` (the cockpit, the usage
  policy and the launch registry all act for a SuperAdmin), and an org admin writing `beta`
  into its own store at `PUT /v1/flag/defs/beta` changes nothing (`access_test.go`).
- **Readers:** pricing (`ops.beta`: a `beta` catalog item shows to a caller the flag is on
  for; asked only when the snapshot holds a beta item and the caller has a validated org,
  never for an admin, 3 s bound, silence is off) and the consoles (`viewer.beta` from
  `GET /v1/flag/platform`). SuperAdmin (`owner == "admin"`) still sees everything by its own
  predicate, not by the flag.
- **OPEN:** a beta model is still callable by id: the ai gateway asks neither the overlay nor
  `flags_on` when a model is called. Use-site enforcement is one `flags_on` away.
- **A capability staged `beta` reads it too** (`cloud.Stage`: `refuse(name, "stage", beta)`,
  asking `flags_on` with the whole principal via `cloud.As`); an alpha row is still let in by
  the per-capability flag `hold` reads. HIP-0139 §8.2 still says "the flag named for the
  capability" for both and needs that line amended. No row is staged beta today.
- **A `user` rule names a credential's user, not a person:** a person's token presents its
  IAM `sub`, their API key `<owner>/<name>`, an app `app:<owner>/<name>`, so a rule on the
  `sub` admits the session and not the key; name the org to cover both. Over the plane the
  subject is the caller's User as carried (zip.Caller has no stability bit), so a token with
  no `sub` would present its username; IAM always signs `sub`.
- **Rollout is sampling, not access control:** it hashes the org's name, which a customer
  picks, so a customer can grind its way into a small percentage. Name who must have beta.
- **Write path:** `flags_set` refuses a principal switch's definition that is not the cockpit's
  shape (`targetable`: `active` stated — the engine reads an absent one as TRUE — one rule a
  group on `org`/`user`, `regex` over `^(?:a|b)$` of `regexp.QuoteMeta` names, explicit
  `rollout_percentage` 0–100, no variants, payloads, aggregation or cohorts, no
  active-with-no-groups, no shared org named) and STORES ITS RE-ENCODING, never the bytes
  sent, so a key repeated in another case cannot mean one thing to the check and another to
  the engine. The write lands on the audit trail with what it replaced (`flag.platform.put`)
  first; no trail, no write. A rule naming a shared org also matches nobody at evaluation: a
  shared-org caller carries no `org` property. `On` requires a validated caller
  (`principal.ValidatedFrom`), so an org parked with nobody signed in is refused whether the
  flags app is co-resident or across a socket.
- **The reserved store has one door:** `storeFor` refuses org `platform` (`cloud.Reserved`) for
  every tenant path — /v1/flag, `PutDef`/`GetDef`/`Assign` (experiments) — and the platform
  store is opened only by `Client.platform()`. Reserving `platform` in `authz.IsReservedOrg`
  (IAM's personal-org slug turns a signup as `platform@…` into that org) is the owner's.
- **Deploy:** read the live grants before rolling (`GET /v1/admin/beta/grants?active=true`
  and `GET /v1/admin/pricing/enablement`, both gone after), then target them in the cockpit.
  Pricing's first boot logs each dropped list (`dropped a beta grant list`). Rolling pricing
  back needs `ALTER TABLE catalog_overlay ADD COLUMN beta_orgs TEXT NOT NULL DEFAULT '[]'` on
  `catalog.db` first. The beta app's `beta.db` (applications with applicants' answers and
  grants) is orphaned on the data volume: delete it once the grants are carried over.

## The apps in beta: one principal switch each, `beta.<id>` (apps/flags `betas.go`, `grant.go`)

    GET  /v1/flag/platform                  every principal switch for the caller: {"flags":{"beta":…,"beta.crm":…,…}}
    POST /v1/admin/flags/beta.<id>/grant    {user:<IAM sub>, on:bool} lets one person in or out; answers the board
    plane flags_grant {key,user,on,actor}   the same, SuperAdmin only (client.FlagsGrant)
    GET  /v1/flag/optin                     {"on","blocked","public":[ids]} the caller's opt-in to public betas
    PUT  /v1/flag/optin                     {on} the caller's OWN opt-in; 403 while the org blocks it
    PUT  /v1/flag/optin/org                 {blocked} org admin or SuperAdmin; audited flag.optin.org; never a shared org
- **Two stages** (apps/flags `optin.go`, `access.go`): PRIVATE, every beta until promoted, is on
  only for whom its rules name. PUBLIC is a definition that also holds the opt-in group, one
  rule `{key:"optin", value:"^(?:true)$"}` written through `flags_set`/`flags_grant` like any
  rule (`targetable` admits that value and no other); it is on as well for every person who
  opted in and whose org did not block it. `On` computes the `optin` property from the
  reserved store's `flag_optin` / `flag_optin_org` rows; a read error is no opt-in.
- **Staff:** a SuperAdmin (`principal.SuperFrom`) has `beta` and every `beta.<id>`, whatever
  is stored. Acting in the admin org without that bit is nobody's staff.
- **Which apps and models:** `flags.Betas`, the ids of `@hanzo/app` `lib/betas.ts` (hanzoai/app
  `pkg/app`): every app or model no one has yet on hanzo.ai or platform.hanzo.ai (the six creative
  models Koma, Iro, Mori, Oto, Koe and Uta, and Zen 7 in research preview, among them). Each registers a principal switch `beta.<id>`
  (category Beta) with the same rules as `beta`: nothing stored is off, only a SuperAdmin writes.
  A switch only ADDS the app to the Hanzo app's rail More; it hides nothing anyone has. Keep the
  two lists equal by hand; an id only one side names is off for everyone.
- **A grant is one person:** `grantOp` edits the stored definition under one lock: the person's
  id joins (or leaves) the switch's `user` rule, every other group (orgs, a rollout) is kept, and
  the switch is active while any group is left. It is checked by `targetable` and on the audit
  trail (`flag.platform.put`, before and after) before it lands. Two grants made together both land.
  The board's principal switches carry `users` and `orgs`, read off the definition.
- **Where a request comes from:** the person's CRM beta record (`apps/marketing/beta.go`:
  `data.betas`, `data.requested[<id>]` stamped, `beta_requested` on the event plane), filed from
  hanzo.ai/beta and from the app's Betas list. The CRM's person view grants from it (hanzoai/console).
- **CRM's own data is a second gate:** `apps/entitlement` `Beta = ["crm"]` still decides per ORG
  whether the CRM's documents answer (`elective.go`), granted when a person of the org applies to
  the beta signed in or by a SuperAdmin at `POST /v1/entitlement/orgs/:org`. `beta.crm` puts CRM
  in the person's app; an org without the entitlement still has no CRM data. Fold one into the
  other before a second product joins `entitlement.Beta`.
