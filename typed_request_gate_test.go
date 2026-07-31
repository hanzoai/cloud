package cloud_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cloud.Request hands a typed op the raw request its signature dropped. It is a
// deliberate escape hatch and it is the seam that rots if nobody counts it:
// every use gives back some of what typing bought, and nothing about the
// signature stops the next one.
//
// So the count enforces itself. Each entry below is a call site and the reason
// it needs the REQUEST rather than just the tenant — which is the test: if
// principal.OrgFrom(ctx) would do, the answer is not this. Adding one means
// editing this map and writing the justification, which is a decision someone
// makes on purpose rather than a drift nobody notices.
//
// Three reasons earn an entry, and they are the whole list. Most are identity
// gates: they need MORE of the validated principal than the org, and admin-ness
// (X-User-IsAdmin), the user id and the payer live in headers only the request
// carries. One is a proxy, where forwarding the caller's identity is the point.
// One is a URL-borne value on a BODY-carrying route, which zip cannot name on an
// In without also accepting it in the body — a wire that route has never had.
var allowedRequestUses = map[string]string{
	"apps/admin/core/typed.go": "Admit / AdmitScoped — the SuperAdmin and white-label tenant gates. " +
		"Both read validated identity beyond the org (IsAdmin, the WL allowlist), which principal.OrgFrom does not carry.",
	"apps/account/account.go": "requestCaller — account IS the signed-in caller's own account, and resolving " +
		"them needs more of the validated principal than the org: the user id (X-User-Id), the IAM username " +
		"(X-User-Name) that IAM's user-key ops parse, and validated-ness itself, none of which principal.OrgFrom " +
		"carries. ONE function, which every op in the package asks; it fails closed off the HTTP path. Two ops " +
		"then reuse the request it hands back for a second, non-identity reason: the CSRF issuer pins " +
		"Cache-Control on its response, and embed-status reads the SuperAdmin claim.",
	"apps/automations/automations.go": "auditHTTP — the tamper-evident record for an enable/disable is an " +
		"ATTRIBUTION, and every fact it carries beyond the org (the validated user id, the email, " +
		"org-admin-ness, the method, the path, the source IP, the request id) rides on the request, " +
		"which principal.OrgFrom does not. The tenant itself is resolved with principal.OrgFrom " +
		"(tenantOf, right beside it), never through the request. ONE function, so the two typed ops " +
		"and the untyped CHANGE_STATUS operation share one seam; off the HTTP path there is no " +
		"request and no actor, and an unattributable audit record is worse than none, so it appends " +
		"nothing.",
	"apps/cloudflare/cloudflare.go": "authWrite / resolveAccount / the acting-org stamp. A mutation on the " +
		"org's Cloudflare account requires ORG ADMIN (X-User-IsOrgAdmin), which principal.OrgFrom does not " +
		"carry; every served response stamps X-Hanzo-Org with the org whose token was used, so a per-org " +
		"caller can prove no tenant comingling; and the ?account= override is read off the URL rather than " +
		"modeled as an In field, because zip binds an In field from the BODY too and this route has never " +
		"accepted an account there. authWrite fails closed off the HTTP path: no request, no attested " +
		"caller, no mutation.",
	"apps/provisioning/typed.go": "tenantOf — the provisioning control plane ALLOCATES and DESTROYS real " +
		"backend resources, and its tenant is not the org principal.OrgFrom carries. Two facts differ, both " +
		"live: the org is folded through sanitizeOrg (cloud.SanitizeOrg — the slug every physical name, S3 " +
		"bucket and tenant-<org> namespace is keyed on, so a read that skipped the fold would look in a " +
		"different bucket than the create wrote), and an ORG-LESS SuperAdmin is bucketed under the literal " +
		"\"admin\" org, which OrgFrom cannot express — it refuses an empty org outright, so reading the tenant " +
		"through it alone would turn that live admin bucket into a 403. ONE function, which all 21 typed ops " +
		"ask, delegating to the same tenant() the untyped create beside them uses; fails closed off the HTTP " +
		"path, where there is no principal and therefore no tenant to key on.",
	"apps/channels/routes.go": "requireOrgAdmin — the mutation gate on the chat plane. Approving a " +
		"pairing and editing a channel allowlist decide WHO may talk to the org's bots, so both take " +
		"admin of the org: X-User-IsAdmin / X-User-IsOrgAdmin, two claims principal.OrgFrom does not " +
		"carry and that must never become In fields a caller could assert for itself. ONE function, " +
		"which both write ops ask; the TENANT is resolved with principal.OrgFrom (tenant, right beside " +
		"it), never through the request. Fails closed off the HTTP path: no request, no attested admin, " +
		"no mutation.",
	"apps/wallets/wallets.go": "actor / ambientProject — TWO facts a wallet write needs beyond the org, " +
		"neither of which principal.OrgFrom carries and neither of which may be an In field. actor is the " +
		"validated user id (X-User-Id) the tamper-evident audit trail ATTRIBUTES a key creation, rotation, " +
		"signature or Safe proposal to. ambientProject is the caller's server-minted project scope " +
		"(X-Project-Id, which the gateway and cloud.SanitizeIdentity bind from a validated claim after " +
		"stripping any client copy) — it becomes a SEGMENT OF THE KMS KEY REF, so a caller-supplied one " +
		"would address key material under a scope no minter ever validated. TWO functions in ONE file, so " +
		"eight typed ops share one seam; the TENANT is resolved with principal.OrgFrom (tenant, right " +
		"beside them), never through the request. Both fail closed off the HTTP path: no request means an " +
		"unattributed audit record (emitAudit falls back to the subsystem name, as it always did) and the " +
		"org's default project scope.",
	"apps/code/code.go": "meter — the PAYER and the PROJECT a code retrieval is charged and scoped " +
		"against. The payer is principal.Ledger, the SELECTED billing org, which a SuperAdmin masquerade " +
		"deliberately moves OFF the effective org, so it is not what principal.OrgFrom carries. The " +
		"project is X-Project-Id, minted SERVER-SIDE from a validated claim after any client copy is " +
		"stripped; a caller-supplied one would bill and scope an embedding call under a project no " +
		"minter ever validated, so neither may be an In field. ONE function, which all seven typed ops " +
		"ask; the TENANT is resolved with principal.OrgFrom (tenant, right beside it), never through the " +
		"request. Fails closed off the HTTP path: no request means the unbilled, default-project answer, " +
		"and tenant() has already refused before any op reaches it.",
	"apps/search/search.go": "Query resolves the tenant from the validated principal at the top of the op.",
	"apps/tracker/typed.go": "scope / requireBody. scope needs the IAM PROJECT (X-Project-Id), which " +
		"picks the physical per-(org,project) store a tracker read opens — principal.OrgFrom carries the org " +
		"and nothing else, so an op without it would open a different file than the create wrote. requireBody " +
		"replays the c.Bind refusal the raw PATCH handlers answered on a bodyless request, at the point in " +
		"the sequence they reached it; zip's typed decode is tolerant and would have turned that 400 into a " +
		"200-with-nothing-changed. Both fail closed off the HTTP path.",
	"apps/campaign/typed.go": "requireBody — the three writes that bind a body (create, update, addChannel) " +
		"have always refused a request with none, or with a content type this service does not parse, with " +
		"c.Bind's own 400. zip's typed decode is TOLERANT by construction (it skips an empty body and leaves " +
		"the In at its zero value), so without this a bodyless create would write empty values instead of " +
		"refusing. It calls the SAME c.Bind over an empty target, so it is one decision rather than a second " +
		"implementation free to drift, and it is a no-op off the HTTP path where there is no body to require.",
	"apps/legal/typed.go": "checkBody / noStore / audited. checkBody replays decode's 1 MiB REQUEST-BODY " +
		"CAP — a typed op receives its DECODED In, so a size check inside one runs after the parse it exists " +
		"to precede, and cloud's global limit is far larger than this package's 413. noStore pins " +
		"Cache-Control on the two document reads, which return contract text and must never be cached; a " +
		"typed op returns its Out and has no response value of its own. audited carries the ACTOR — the " +
		"validated subject, email and admin bit, all headers principal.OrgFrom does not carry — onto the " +
		"tamper-evident trail, and an audit record without its actor is a log, not a trail.",
	"apps/authors/typed.go": "requireAdmin / connect+verify / requireBody. requireAdmin is the SuperAdmin " +
		"gate on the six /v1/admin/authors ops, reading X-User-IsAdmin, which principal.OrgFrom does not " +
		"carry. connect and verify need the validated user subject (X-User-Id) to ask IAM for the caller's " +
		"LINKED forge account, which is the strong proof of a login and the difference between a claimed " +
		"and a proven identity. requireBody replays the c.Bind refusal the payout route has always answered " +
		"on a bodyless request. All fail closed off the HTTP path.",
	"apps/ingress/ingress.go": "admin — the SuperAdmin gate on the fleet EDGE's config. The edge is platform " +
		"infrastructure (AC-6), so every /v1/ingress op requires SuperAdmin, which is X-User-IsAdmin — a claim " +
		"principal.OrgFrom does not carry. Fails closed off the HTTP path: no request, no attested admin, no " +
		"edge config.",
	"apps/agents/targets.go": "targetOwns / targetCaller — a machine belongs to the principal that " +
		"registered it, and only that owner or an org admin may patch, delete or manage its route-work " +
		"plane. Ownership needs X-User-Id and org-admin-ness (X-User-IsOrgAdmin), neither of which " +
		"principal.OrgFrom carries. Both fail closed off the HTTP path: no request, no attested caller, " +
		"no management rights.",
	"apps/agents/routing_http.go": "claimKeyOf — the route-work plane authenticates a MACHINE with a " +
		"claim key that rides in its own header (X-Target-Key), which is the second of that plane's two " +
		"independent proofs alongside the validated org. principal.OrgFrom carries the org and nothing else, " +
		"and the key is a credential rather than an addressing value, so it must not become an In field a " +
		"caller can also put in the body. Fails closed off the HTTP path: no request, no key, and " +
		"verifyClaimKey refuses an empty one.",
	"apps/company/register.go": "reviewer — the Hanzo platform gate on the formation register and on a " +
		"founder KYC decision. Hanzo forms the entity and carries the KYC/AML obligation, so the decision is " +
		"a SuperAdmin one and is ATTRIBUTED: it needs X-User-IsAdmin and X-User-Id, neither of which " +
		"principal.OrgFrom carries. Fails closed off the HTTP path: no request, no attested reviewer.",
	"apps/crm/applications.go": "actor — a Startup Program stage event is attributed to the STAFF USER " +
		"who moved it, and the validated user id lives in X-User-Id, which principal.OrgFrom does not " +
		"carry (the org is the pipeline's owner, not the mover). Fails closed off the HTTP path: no " +
		"request, no attested caller, so the event is attributed to \"staff\" rather than to an invented one.",
	"apps/visor/visor.go": "A tenant-scoped PROXY: client.go forwards the caller's own identity headers " +
		"(and their bearer where no service credential is configured) upstream, so an op without the request " +
		"drops the caller's identity on the far side of the hop.",
	"apps/tools/typed.go": "projectOf / callerOf / audit — the tool plane is scoped to (org, PROJECT), and " +
		"the project is a server-minted header (X-Project-Id) that principal.OrgFrom does not carry, so " +
		"resolving it needs the REQUEST; principal.Project is still the one function that reads it. An " +
		"activation write is also ATTRIBUTED — the store records the validated user id (X-User-Id) who turned " +
		"a tool on — and every fact an audit record carries beyond the org (the user, the email, admin-ness, " +
		"the method, the path, the source IP, the request id) rides on the request too. The TENANT itself is " +
		"resolved with principal.OrgFrom (tenantOf, in this same file), never through the request. THREE " +
		"functions in ONE file, so fourteen typed ops share one seam; all fail closed off the HTTP path — no " +
		"request means the default project, no actor, and no audit record, since an unattributable record is " +
		"worse than none, and tenantOf refuses before any of them is reached.",
	"apps/team/typed.go": "tokenOf / sessionOf / admin / noStore / cookie — team authenticates its billing, " +
		"files and collaborator planes with its OWN HS256 session token, which rides in Authorization or the " +
		"HttpOnly account-token cookie; principal.OrgFrom carries neither (nor the WORKSPACE claim the " +
		"collaborator plane gates on), and bots/sync additionally needs admin-ness (X-User-IsAdmin). The " +
		"cookie WRITER is the other end of that same identity — the account-token cookie is set on the " +
		"RESPONSE, which only the request reaches. It is ONE file for the whole subsystem on purpose — the " +
		"resolvers live here so the planes that use them do not each reach for the request. All of them fail " +
		"closed off the HTTP path: no request, no token, no identity, and no browser to sign out.",
	"apps/ml/typed.go": "tenantFrom — ml's tenant boundary is a per-org(+project) KUBERNETES NAMESPACE, and " +
		"deriving it takes two facts principal.OrgFrom does not carry: the org SUB-SCOPE (X-Project-Id, " +
		"which suffixes the namespace) and platform-admin-ness (X-User-IsAdmin, which buckets an org-less " +
		"admin under \"ml-admin\" — OrgFrom refuses an empty org outright, so reading the tenant through it " +
		"alone would turn that live admin bucket into a 403). ONE function, which every typed op asks, " +
		"delegating to the same tenant() the untyped handlers beside them use; fails closed off the HTTP " +
		"path, where there is no principal and therefore no namespace to name.",
	"apps/o11y/typed.go": "callerIsAdmin / callerValidated / callerProject — the o11y surface's ONE identity " +
		"seam. The scoped reads switch on platform-sudo (X-User-IsAdmin: the infra-log god-view and the " +
		"whole-product RED), the status probe gates on validated-ness alone (infra health is not " +
		"tenant-partitioned, so an org-less but validated caller is served), and the annotation queues narrow " +
		"by project (X-Project-Id). None of the three rides on principal.OrgFrom. Concentrated in one file so " +
		"the escape hatch is one pin with one justification rather than the same call in three handlers; all " +
		"three fail closed off the HTTP path.",
	"apps/guide/guide.go": "superAdminOK / ledgerOf — the brand-blueprint SuperAdmin gate and the payer ONE " +
		"grounded AI completion is billed to. Admin-ness lives in X-User-IsAdmin, and the payer is the SELECTED " +
		"billing org (principal.Ledger, which a SuperAdmin masquerade moves off the effective org); neither is " +
		"what principal.OrgFrom carries. Both fail closed off the HTTP path: no request, no platform rights and " +
		"no ledger to charge.",
	"apps/books/typed.go": "sandboxFrom — the ledger selector for the books ops whose In is their " +
		"request BODY. It is not identity: it picks between two of the CALLER'S OWN books, and every " +
		"bodyless read beside them names it as an In field. A body-carrying op cannot — zip's binder " +
		"fills an In field from the BODY as well as the URL and the document publishes it as a body " +
		"property, so naming it would start accepting a selector these routes have never taken there. " +
		"TestTheLedgerSelectorStaysOnTheURLForBodyWrites (apps/books/wire_test.go) is that measurement " +
		"and goes red the day it moves. ONE function, which every body-carrying op asks, reading " +
		"through the same sandboxQuery the untyped handlers beside them use; LIVE off the HTTP path.",
	"apps/books/ask.go": "narrateAsk — the payer for the ONE grounded completion an Ask narrates with. " +
		"The bill lands on principal.Ledger, the SELECTED billing org, which a SuperAdmin masquerade " +
		"moves off the effective org — so principal.OrgFrom would charge the org being INSPECTED for a " +
		"platform admin's reading of its books. Empty off the HTTP path, where the meter no-ops rather " +
		"than billing the wrong ledger.",
	"apps/engine/engine.go": "caller — AUTHENTICATION with no tenant, which principal.OrgFrom " +
		"cannot express at all: every op here reads a deployment-global platform fact (the host's " +
		"accelerators, the build's capabilities), so there are no org-scoped rows and no org to scope " +
		"by, and OrgFrom answers with an org or refuses. What the gate needs is the one bit beside " +
		"it — principal.Validated — so an org-less but signed-in operator is admitted and an " +
		"anonymous caller is not. ONE function, which every op asks; fails closed off the HTTP path.",
	"apps/tools/charge_peer.go": "chargePeer — the tool plane's payment seam reaching the x402 " +
		"process, which is a PROXY that forwards the caller's identity and carries two facts of the " +
		"request across the boundary with it. The payer is principal.Ledger, which folds in the " +
		"SuperAdmin masquerade (X-User-IsAdmin plus the X-User-Owner home claim) that " +
		"principal.OrgFrom cannot carry — reading the tenant through OrgFrom would charge the " +
		"INSPECTED org's ledger for a platform admin's call. The client's signed authorization rides " +
		"the X-Payment header, and the 402 challenge the rail answers with has to be written back onto " +
		"THIS process's response, because the rail has no response to write it to. Neither is nameable " +
		"as an In field: the tool call's body names a tool, and a payer a caller could state is a " +
		"caller that could spend another tenant's ledger. ONE function, asked by the one dispatch " +
		"path; off the HTTP path there is no payer and no proof, and the rail refuses a priced " +
		"resource on those terms rather than serving it.",
	"apps/x402/x402.go": "payerOf — the receipt lookup is scoped to the org whose LEDGER was " +
		"DEBITED, which is what every settlement row is keyed on and what the Enforce middleware " +
		"beside it charges. principal.Ledger folds in the SuperAdmin masquerade rule (X-User-IsAdmin " +
		"plus the X-User-Owner home claim) that principal.OrgFrom cannot carry, so reading the tenant " +
		"through OrgFrom would silently widen a platform admin's read from their OWN receipts to the " +
		"inspected org's. ONE function, asked by the one typed op; empty off the HTTP path, where an " +
		"empty payer is refused rather than treated as a wildcard.",
	"apps/pricing/ops.go": "callerIsAdmin — the catalog's SuperAdmin gate. Every read op here also " +
		"branches on it (an admin sees disabled models, flagged, where a customer sees them hidden), and " +
		"admin-ness lives in a header (X-User-IsAdmin) that principal.OrgFrom does not carry. The tenant " +
		"itself is read with principal.OrgFrom (callerOrg, right beside it), never through the request. " +
		"False off the HTTP path: no request, no attested caller, no admin view.",
	"apps/compliance/compliance.go": "the reviewer gates, emitAudit and noStore. A verification or " +
		"accreditation DECISION is role-gated (SuperAdmin or org admin — X-User-IsAdmin / " +
		"X-User-IsOrgAdmin) and ATTRIBUTED to the reviewer's user id (X-User-Id), none of which " +
		"principal.OrgFrom carries; emitAudit is the same attribution for the tamper-evident trail " +
		"(user id, email, admin-ness, method, path, source IP, request id); noStore pins " +
		"Cache-Control: no-store on the PII-bearing responses, which only the request reaches. The " +
		"tenant itself is read with principal.OrgFrom (tenant, right beside them), never through the " +
		"request. All fail closed off the HTTP path: no request, no attested reviewer, no audit actor " +
		"to invent, nothing cached.",
	"apps/leaderboard/leaderboard.go": "requestOf — the leaderboard's ONE identity seam, asked by the " +
		"three readers beside it. A public board is a CONSENT surface, so it turns on facts " +
		"principal.OrgFrom does not carry: the caller's own ledger row is keyed by the validated username " +
		"(X-User-Name, selfLedgerID — without it a member cannot find or set their own opt-in), naming " +
		"other members and setting the ORG's listing require org-admin-ness (X-User-IsOrgAdmin), and the " +
		"rollup seed requires platform-admin-ness (X-User-IsAdmin). noStore is the fourth reader and is " +
		"not identity at all: per-tenant analytics must never be held by a browser or an intermediary, " +
		"and only the request reaches the RESPONSE header. The tenant itself is read with " +
		"principal.OrgFrom (tenantOf, in this same file), never through the request. ONE function, so " +
		"six typed ops share one seam; every reader fails closed off the HTTP path — no request means no " +
		"self, no admin and no elevation.",
	"apps/marketplace/marketplace.go": "projectOf / callerOf / record. An install is scoped to (org, " +
		"PROJECT) and the project is a server-minted header (X-Project-Id) that principal.OrgFrom does " +
		"not carry, so the activation write and the tool-existence check both need it — and they must " +
		"agree with the tool plane, which reads the same principal.Project. An activation is also " +
		"ATTRIBUTED: the store records the validated user id (X-User-Id) who turned a capability on. " +
		"record is the tamper-evident trail for a publish/unpublish/install, and every fact it carries " +
		"beyond the org (user id, email, admin-ness, method, path, source IP, request id) rides on the " +
		"request too. The tenant itself is read with principal.OrgFrom (tenantOf, in this same file), " +
		"never through the request. All three fail closed off the HTTP path: no request means the org's " +
		"default project, no actor, and no audit row — an unattributable record is worse than none.",
	"apps/deploy/typed.go": "scopeOf / consoleUser — the deploy console's TENANT SCOPE, which is not the " +
		"org principal.OrgFrom carries. Two facts say why, both live: a platform SuperAdmin has NO org at all " +
		"(X-User-IsAdmin, a header OrgFrom does not carry) and reads every platform namespace rather than one " +
		"tenant's, so an org is not merely absent from that scope, it would be wrong; and a normal org's scope " +
		"is the injective provisioning.SanitizeOrg slug — the name of the tenant-<org> namespace its App CRs " +
		"live in — not the verbatim owner claim. consoleUser is the second: the session read must ANSWER for " +
		"an anonymous caller rather than refuse one, and it reports the validated user ID (X-User-Id) " +
		"beside admin-ness. TWO functions in ONE file, delegating to the same resolveScope every raw handler " +
		"beside them uses; both fail closed off the HTTP path, where there is no attested caller and therefore " +
		"no scope.",
	"apps/graph/graph.go": "forwarded — the chain-data reads PROXY to the deployment's indexer and " +
		"graph, and where no service token is configured they pass the CALLER's own Authorization " +
		"through (client.go's authorize). That credential is the caller's, not an addressing value, so " +
		"it must not become an In field a caller could also put in a body — and principal.OrgFrom " +
		"carries the org and nothing else. The org gate itself is principal.OrgFrom (gate, right beside " +
		"it), never the request. Empty off the HTTP path, where there is no request and so no identity " +
		"to forward — the upstream read then goes out unauthenticated, which is what a public ledger " +
		"read already is.",
	"apps/prefs/prefs.go": "subjectFrom — the preference OWNER, and it is not the org. The isolation " +
		"key is the canonical `<owner>/<name>` identity, so it needs the validated USER claim " +
		"(X-User-Id) and validated-ness itself alongside the org; principal.OrgFrom carries only the " +
		"owner half, and keying on that alone would hand every member of an org the same document. ONE " +
		"function, delegating to the same subject() the untyped PATCH beside it uses; fails closed off " +
		"the HTTP path, where there is no principal and therefore no `own` document to serve.",
	"apps/admission/waitlist.go": "requestHost — the ?host= default. This route resolves ONE host to " +
		"its waitlist mode, and when the query is omitted the host it has always answered for is the " +
		"one the REQUEST was addressed to (the Host header). That is a property of the request and of " +
		"nothing else — it is not identity, and modeling it as a second In field would let a caller set " +
		"the fallback, which is the one thing a fallback must not be. Empty off the HTTP path, which " +
		"resolves to known=false: the same fail-open answer an unregistered host gets.",
	"apps/gateway/gateway.go": "caller / target — the edge config plane's identity seam. The PLATFORM " +
		"scope (CORS allowlist, pre-auth per-IP cap) is SuperAdmin-only, which is X-User-IsAdmin, and every " +
		"write is stamped with the validated user id (X-User-Id) — neither is what principal.OrgFrom carries. " +
		"The ?org=<slug> a SuperAdmin targets another tenant with is read off the URL rather than modelled as " +
		"an In field, because zip binds an In field from the BODY too and the PUT has never accepted an org " +
		"there — and a tenant key taken from an input is a cross-tenant write the caller asserted for itself. " +
		"The caller's OWN org is read with principal.OrgFrom, never through the request. Fails closed off the " +
		"HTTP path: no request, no attested admin, no edge config.",
	"apps/sbom/sbom.go": "ingest's SuperAdmin gate. The SBOM store is GLOBAL by design (an SBOM belongs to " +
		"an image DIGEST, not a tenant), so there is no org predicate here at all — the one identity fact this " +
		"surface reads is X-User-IsAdmin, which the build fleet / CI carries and principal.OrgFrom does not. " +
		"Fails closed off the HTTP path: no request, no attested admin, no ingest.",
	"apps/entitlements/entitlements.go": "resolveOrg — the ONE trust decision both enablement ops share. It " +
		"needs two facts beyond the parked org: SuperAdmin-ness (X-User-IsAdmin, which lets an operator " +
		"comp a product to ANY org) and the VALIDATED owner claim to compare the :org path segment against, " +
		"which is what makes that segment an address the gate re-checks rather than an assertion it believes. " +
		"It also hands back the request so a write is ATTRIBUTED to the validated user id. Fails closed off " +
		"the HTTP path: no request, no attested principal, no access.",
	"apps/entitlements/projection.go": "the platform-sudo bit on the console's paywall read. \"admin\" is " +
		"not a commerce product — it is X-User-IsAdmin, resolved from the unforgeable claim rather than " +
		"through CheckEntitlement — and principal.OrgFrom does not carry it. The org itself is read with " +
		"principal.OrgFrom, right above. Off the HTTP path it is simply false, which is this read's " +
		"fail-safe-to-LOCKED direction.",
	"apps/translate/translate.go": "reviewer — a review-lane write is ATTRIBUTED to the human who made it, " +
		"and the validated user id lives in X-User-Id, which principal.OrgFrom does not carry (the org says " +
		"which tenant, not which person). It is an attribution and never an authority: the org gate above it " +
		"already ran. Empty off the HTTP path, where the entry records no actor rather than inventing one — " +
		"exactly how a pre-attribution row already reads.",
	"apps/flags/routes.go": "callerOf — the flag plane's ONE identity seam. A flag is scoped to (org, " +
		"PROJECT) and an audited write records the ACTOR, so it needs two facts beyond the tenant: the " +
		"project scope (X-Project-Id, principal.ProjectScope) and the validated user id, neither of " +
		"which principal.OrgFrom carries. All three are request facts and none may be an In field — an " +
		"In field is caller-supplied, so a scope key read from one is a cross-scope read the caller " +
		"asserted for itself. ONE function, so every typed op shares one seam; it fails closed off the " +
		"HTTP path, where there is no principal and therefore no scope and no actor.",
	"apps/do/do.go": "org — the DigitalOcean plane's ONE tenant-resolution point, and it fails closed " +
		"in the same place it decides. It reads the request because tenant() turns on two facts the org " +
		"key alone does not carry: whether the principal was VALIDATED at all, and whether it is a " +
		"SuperAdmin, whose empty org falls back to the \"admin\" namespace — which principal.OrgFrom " +
		"cannot express, since it refuses an empty org outright. Off the HTTP path there is no request, " +
		"and the answer is a refusal rather than an invented identity.",
	"apps/treasury/treasury.go": "admin / myAccounts — the ledger's tenant boundary and the one way " +
		"across it. Every ordinary caller sees only accounts under its own \"org:<tenant>:\" prefix; a " +
		"SuperAdmin may widen to the house scope or to another tenant, and platform-sudo is " +
		"X-User-IsAdmin, a claim principal.OrgFrom does not carry. The ?org=<tenant> that names the " +
		"crossed-to tenant is read off the URL rather than modelled as an In field, because a tenant key " +
		"taken from an input is a cross-tenant read the caller asserted for itself. Both fail closed off " +
		"the HTTP path: no request, no attested admin, no widening.",
	"apps/research/research.go": "project — the caller's project SUB-SCOPE, a column inside the org " +
		"rather than a tenant key. It is the server-minted X-Project-Id claim, which principal.OrgFrom " +
		"does not carry, and it must not become an In field either: a caller could then name a project " +
		"it was not scoped to. ONE function beside orgStore, which resolves the TENANT with " +
		"principal.OrgFrom and never through the request. Off the HTTP path it answers " +
		"principal.DefaultProject — the whole-org view, which is the honest answer where there is no " +
		"request rather than a refusal.",
	"apps/usage/account.go": "caller / noStore — the usage plane is scoped to (org, SUBJECT): the " +
		"account board carries the caller's OWN linked provider accounts, so it needs the validated user id " +
		"(c.User()) as well as the tenant, and principal.OrgFrom carries only the tenant. noStore is the " +
		"other half of the same request — per-tenant MONEY must never be cached by a browser or an " +
		"intermediary, and Cache-Control is a RESPONSE header only the request reaches. TWO functions in " +
		"ONE file, so all five ops share one seam; the tenant-only read (orgOf, right beside them) goes " +
		"through principal.OrgFrom and never through the request. Both fail closed off the HTTP path: no " +
		"request, no subject, and no response to mark.",
	"apps/venue/venue.go": "writer — the org-admin gate every cloud-account MUTATION keeps, and the " +
		"request the fold then rides on. Linking, syncing or unlinking a customer's cloud account requires " +
		"admin of the caller's OWN org (X-User-IsOrgAdmin), and the fold that follows needs four more facts " +
		"principal.OrgFrom does not carry: the project the discovered clusters are recorded in and whether " +
		"that project was validated, the ledger the new folds are billed to, and the request id and client " +
		"IP the meter attributes them by. ONE function, so the three writes share one seam; the tenant " +
		"itself is read with principal.OrgFrom (tenant, right beside it) and the two READS never reach the " +
		"request at all. Fails closed off the HTTP path: no request, no attested admin, no mutation.",
	"apps/world/news.go": "scopeOf — world is scoped to (org, PROJECT) and every store statement carries " +
		"both. The project is a server-minted claim (X-Project-Id) that principal.OrgFrom does not carry, " +
		"and it is a TENANT key, so it must not become an In field a caller supplies for itself. The " +
		"?project query beside it is only ever cross-CHECKED against that claim and cannot be an In field " +
		"either: zip binds an In field from the BODY as well as the URL, so PUT /v1/world/pipeline would " +
		"start rejecting a body that named a project — a wire the route has never had. ONE function, which " +
		"all four scoped ops ask, delegating to the same scope() the SSE handler beside them uses; fails " +
		"closed off the HTTP path, where there is no principal and therefore no tenant.",
	"apps/destinations/destinations.go": "orgAdmin — the gate every destination MUTATION keeps " +
		"(disconnect and test both forget or spend a credential). It reads org-admin-ness, which is " +
		"X-User-IsOrgAdmin, a claim principal.OrgFrom does not carry. The tenant itself is read with " +
		"principal.OrgFrom (tenantOf, right beside it), never through the request. ONE function, so the " +
		"two mutating ops share one seam; it fails closed off the HTTP path: no request, no attested " +
		"caller, no mutation.",
}

// TestRequestEscapeHatchIsPinned fails when a new cloud.Request call site
// appears, and says what to do about it.
func TestRequestEscapeHatchIsPinned(t *testing.T) {
	found := map[string]int{}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "webui", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if n := strings.Count(string(b), "cloud.Request("); n > 0 {
			found[filepath.ToSlash(path)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for file := range found {
		if _, ok := allowedRequestUses[file]; !ok {
			t.Errorf("NEW cloud.Request call site in %s.\n"+
				"cloud.Request is the escape hatch that hands a typed op its raw request, and it is pinned so it "+
				"cannot grow quietly. Before adding this one: if the op needs only its tenant, use "+
				"principal.OrgFrom(ctx) and delete the call. If the op can NAME the value, declare it on its In "+
				"and let zip bind it off the URL. If it genuinely needs the request — an identity gate reading "+
				"more than the org, a proxy that FORWARDS the caller's identity, or a URL-borne value on a "+
				"BODY-carrying route, which an In field cannot take without also accepting it in the body — add "+
				"%q to allowedRequestUses with the reason, so the next reader knows why it is here.", file, file)
		}
	}
	for file := range allowedRequestUses {
		if _, ok := found[file]; !ok {
			t.Errorf("%s no longer calls cloud.Request — remove it from allowedRequestUses so the pin keeps "+
				"describing the code that exists.", file)
		}
	}

	if t.Failed() {
		var have []string
		for f, n := range found {
			have = append(have, f+" ("+itoa(n)+")")
		}
		sort.Strings(have)
		t.Logf("call sites now: %s", strings.Join(have, ", "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
