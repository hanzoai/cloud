package cloud_test

import (
	"bytes"
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
// it needs the REQUEST rather than just the caller's identity — which is the
// test: if principal.OrgFrom(ctx) would do, the answer is not this, and neither
// is it if principal.ValidatedFrom(ctx) would (the SAME two facts Bridge parks,
// for a plane with no org-scoped rows — authentication without a tenant is not a
// reason to hold the request). Adding one means editing this map and writing the
// justification, which is a decision someone makes on purpose rather than a
// drift nobody notices.
//
// Three reasons earn an entry, and they are the whole list. Most are identity
// gates: they need MORE of the validated principal than the org, and admin-ness
// (X-User-IsAdmin), the user id and the payer live in headers only the request
// carries. One is a proxy, where forwarding the caller's identity is the point.
// One is a URL-borne value on a BODY-carrying route, which zip cannot name on an
// In without also accepting it in the body — a wire that route has never had.
var allowedRequestUses = map[string]string{
	"apps/provisioning/inventory.go": "operatorOf — an identity gate reading strictly more than the " +
		"org. These two reads span the WHOLE vector backend across every tenant, so the fact they turn " +
		"on is platform sudo (principal.IsSuperAdmin, which is the reserved admin org's membership as " +
		"SanitizeIdentity minted it) and not which org is asking — an org-scoped answer would be the " +
		"wrong answer here, not a narrower one. There is no ctx-side twin of that predicate to read " +
		"instead: it is a header the route middleware parks, and the other From accessors exist " +
		"because their facts have a ctx slot. It cannot be an In field for the obvious reason — a " +
		"caller that could name itself the operator would be one. ONE function, which both ops ask, " +
		"and it fails closed off the HTTP path, where there is no principal to be sudo.",
	"apps/flow/billing.go": "gate + meter — a workflow run executes a component graph that bills a " +
		"model provider, so it is gated before it runs and debited after, and the money gate needs " +
		"strictly more of the validated principal than the org: the PAYER (principal.Payer, which is " +
		"the org's pool or the person's wallet — account.Payer decides, and in the shared signup org " +
		"they differ), the validated project sub-scope (principal.ValidatedProject: the project AND " +
		"whether a claim backs it), and the request id + client IP the debit is attributed with. None " +
		"is the org and none can be an In field — a caller that could name its own payer would bill " +
		"another org. Two call sites, one per half of the pair, and both fail closed off the HTTP " +
		"path, where there is no principal and so nobody to charge.",
	"apps/commerce/risk.go": "screen.op / seen — the fraud screen in front of the typed mint op, and an " +
		"identity gate reading strictly more than the org. The AMOUNT comes off the decoded In and the " +
		"SETTLEMENT off the returned receipt, both deliberately not read from the wire (see screen.op), " +
		"so the request is consulted for exactly the facts no projection can carry on a type: the payer " +
		"(principal.Subject, which is the validated caller and not the tenant), the door actually reached " +
		"(c.Path(), which is /mcp on the agent plane and the mint on the browser's — the request is " +
		"parked by the app-wide Bridge, which runs for both), and the address + " +
		"jurisdiction signals a credit decision is made on. None of those is the org, and none may become " +
		"an In field — a caller that could name its own payer or jurisdiction would screen as someone " +
		"else. It fails OPEN of nothing: a call with no request at all (the CLI's LocalInvoke) resolves " +
		"no payer and is screened as that state rather than exempted from it, and the handler's own " +
		"payingOrg gate refuses it after.",
	"apps/dataset/dataset.go": "who — the dataset plane's caller resolver, and the ONE place an op " +
		"establishes who is asking. The TENANT is resolved through apps/tenant, which reads " +
		"principal.OrgFrom and nothing else; the request is needed for the other half, which is a " +
		"different value on purpose: the BILLING identity. Materialising and tracing a lineage are " +
		"priced acts, and the gate + debit need the ledger (principal.Ledger), the validated project " +
		"sub-scope (principal.ValidatedProject — two facts, the project and whether a CLAIM backs it), " +
		"the acting user for the register's `by` column, and the request id + client IP the debit is " +
		"attributed with. None of those are the org and none can be an In field — a caller that could " +
		"name its own ledger would bill another org. ONE function, which every op asks; it fails closed " +
		"off the HTTP path, where there is no principal and so no tenant to act for.",
	"apps/lsp/lsp.go": "the money gate in front of a query, for the same reason as apps/flow/billing.go " +
		"above: answering one may have to CHECK OUT the repository and index it, so standing is " +
		"required before the work starts rather than after. The gate needs strictly more of the " +
		"validated principal than the org — the payer (principal.Ledger, which is the org's pool or " +
		"the person's own ledger) and the validated project sub-scope (principal.ValidatedProject: the " +
		"project AND whether a claim backs it, which is the per-project spend cap and a different " +
		"question from the attribution the debit records). Neither is the org and neither can be an In " +
		"field — a caller that could name its own payer would bill another org. ONE call site, guarded " +
		"by onHTTP so it fails closed off the HTTP path, where there is no principal and so nobody to " +
		"charge; the cold price is gated on every query because whether a workspace is warm is not " +
		"known until the pool is asked, and the debit in apps/lsp/meter.go charges the real one.",
	"apps/o11y/summary.go": "brandForRequest — the o11y summary is white-labelled by the request HOST " +
		"(BrandForHostOK(c.Host())), a value that is neither the org nor nameable on an In field: it is " +
		"the vhost the caller reached, read only to pick the brand the summary renders for.",
	"apps/kms/typed.go": "admit — the secret broker's one admission door, and an identity gate reading " +
		"strictly more than the org. A member READS a secret and an admin WRITES one (cloud.Scope, the " +
		"estate's split, not this subsystem's invention), and admin-ness is platform sudo or org-admin: " +
		"two facts the identity middleware parks in headers and principal.OrgFrom does not carry. " +
		"Neither may become an In field for the obvious reason — a caller that could name itself an " +
		"admin would be one. The TENANT still comes from principal.OrgFrom, so the org key and the " +
		"authority arrive by their own proper doors. ONE function, which every typed op here asks, and " +
		"it fails closed off the HTTP path, where there is no principal to be an admin of anything.",
	"apps/admin/core/typed.go": "Admit / AdmitScoped — the SuperAdmin and white-label tenant gates. " +
		"Both read validated identity beyond the org (IsAdmin, the WL allowlist), which principal.OrgFrom does not carry.",
	"apps/taxonomy/ops.go": "writable — the product catalogue's authority, and an identity gate reading " +
		"strictly more than the org. Every row belongs to an org, so the question is WHICH catalogue this " +
		"caller may change, and it is answered from two facts principal.OrgFrom does not carry: platform " +
		"sudo (cloud.Super over cloud.AuthorityOf, for the hanzo-owned rows every tenant sees) and " +
		"org-admin-ness (principal.IsOrgAdmin, for the caller's own rows). Both are claims the identity " +
		"boundary mints into headers and neither may ever be an In field — a caller that could name itself " +
		"an editor, or name whose catalogue to write, would be one. ONE function, which all five ops ask: " +
		"the four writes take the owner from it, and the read only widens with it (an unpublished row is " +
		"shown to whoever may edit it). It fails closed off the HTTP path, where there is no attested " +
		"caller — a CLI LocalInvoke edits nothing and sees what a signed-out visitor sees.",
	"apps/account/account.go": "requestCaller — account IS the signed-in caller's own account, and resolving " +
		"them needs more of the validated principal than the org: the user id (X-User-Id), the IAM username " +
		"(X-User-Name) that IAM's user-key ops parse, and validated-ness itself, none of which principal.OrgFrom " +
		"carries. ONE function, which every op in the package asks; it fails closed off the HTTP path. Two ops " +
		"then reuse the request it hands back for a second, non-identity reason: the CSRF issuer pins " +
		"Cache-Control on its response, and embed reads the SuperAdmin claim.",
	"apps/auto/auto.go": "auditHTTP — the tamper-evident record for an enable/disable is an " +
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
	"apps/reference/reference.go": "actor / the refresh gate — two facts beyond the org, both identity. " +
		"An override is an adverse-action input (it is why a signup was refused), so the row records the " +
		"validated user id who wrote it, which principal.OrgFrom does not carry; and refreshing the shared " +
		"baseline every org reads is platform work, gated on the SuperAdmin claim (X-User-IsAdmin), which " +
		"must never become an In field a caller could assert for itself. The TENANT is resolved with " +
		"principal.OrgFrom (nsOf, right beside them) and never through the request, so no read or write is " +
		"scoped by anything the request carries.",
	"apps/provisioning/typed.go": "tenantOf — the provisioning control plane ALLOCATES and DESTROYS real " +
		"backend resources, and its tenant is not the org principal.OrgFrom carries. Two facts differ, both " +
		"live: the org is folded through namespace.Sanitize (the slug every physical name, S3 " +
		"bucket and tenant-<org> namespace is keyed on, so a read that skipped the fold would look in a " +
		"different bucket than the create wrote), and an ORG-LESS SuperAdmin is bucketed under the literal " +
		"\"admin\" org, which OrgFrom cannot express — it refuses an empty org outright, so reading the tenant " +
		"through it alone would turn that live admin bucket into a 403. ONE function, which all 21 typed ops " +
		"ask, delegating to the same principal.Acting the untyped create beside them uses; fails closed off the HTTP " +
		"path, where there is no principal and therefore no tenant to key on.",
	"apps/channels/routes.go": "requireOrgAdmin — the mutation gate on the chat plane. Approving a " +
		"pairing and editing a channel allowlist decide WHO may talk to the org's bots, so both take " +
		"admin of the org: X-User-IsAdmin / X-User-IsOrgAdmin, two claims principal.OrgFrom does not " +
		"carry and that must never become In fields a caller could assert for itself. ONE function, " +
		"which both write ops ask; the TENANT is resolved with principal.OrgFrom (tenant, right beside " +
		"it), never through the request. Fails closed off the HTTP path: no request, no attested admin, " +
		"no mutation.",
	"apps/wallet/wallets.go": "actor / ambientProject — TWO facts a wallet write needs beyond the org, " +
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
		"and principal.Acting has already refused before any op reaches it.",
	"apps/search/search.go": "Query resolves the tenant from the validated principal at the top of the op.",
	"apps/graph/graph.go": "actor — every assertion records WHO asserted it, and that identity is " +
		"the caller's home org plus their user name (principal.Owner + c.User), strictly more than " +
		"the tenant OrgFrom carries. The org itself still resolves through principal.OrgFrom beside " +
		"it, so the request supplies only the asserting identity, never the scope.",
	"apps/seo/typed.go": "who — an identity gate reading strictly more than the org, and the two facts " +
		"it reads are not the same fact. cloud.Request says a request EXISTS; principal.ValidatedFrom " +
		"says the identity middleware minted the caller from a verified credential rather than the " +
		"caller writing X-Org-Id on a bearer-less request themselves. A surface that checks only the " +
		"first admits a forged header, and this one spends money at a vendor per call. ONE function, " +
		"which every op asks; it fails closed OFF the HTTP path too, where there is no request at all " +
		"— the CLI projection invokes an op with nothing to read, and the honest answer there is that " +
		"there is no principal, not that there is a default one.",
	"apps/admin/core/fanin.go": "Delegate — a read that FORWARDS the caller's own identity, and the one " +
		"place the fleet overview lifts it off the request. The value it needs is the principal WHOLE, not " +
		"the org: the overview answers for every tenant, so each per-tenant read re-points the operator's " +
		"authority at someone else's books (cloud.As), and an org is exactly the thing it must not be " +
		"fixed to. It is here rather than inside the fan-out because BUILDING the principal reads the " +
		"request's headers, and fasthttp's header store shares one scratch buffer across reads — twelve " +
		"goroutines each building their own race on it, which -race proves. So it is called ONCE, ahead " +
		"of the fan-out, and every read after is a cheap re-pointing of a value nobody else holds. Fails " +
		"closed off the HTTP path: no request means the plain context, which carries no operator standing " +
		"and is refused by the reads themselves.",
	"apps/todo/source.go": "scopeForge. The forge-backed board needs the caller's IAM USERNAME " +
		"(X-User-Name) as well as their org: the org says WHICH tenant's work to ask the forge for, and the " +
		"username is who the forge is asked AS (Forgejo Sudo), which drops privilege to that user so the " +
		"forge's own ACL re-checks the answer. principal.OrgFrom carries the org and nothing else, so an op " +
		"without the request could not name an actor — and an actorless forge call would fall back to the " +
		"deployment's machine credential, reading every repository that token can see. Fails closed off the " +
		"HTTP path with the same 403.",
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
	"apps/author/typed.go": "requireAdmin / connect+verify / requireBody. requireAdmin is the SuperAdmin " +
		"gate on the six /v1/admin/author ops, reading X-User-IsAdmin, which principal.OrgFrom does not " +
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
		"resolved with principal.Acting, never through the request. THREE " +
		"functions in ONE file, so fourteen typed ops share one seam; all fail closed off the HTTP path — no " +
		"request means the default project, no actor, and no audit record, since an unattributable record is " +
		"worse than none, and principal.Acting refuses before any of them is reached.",
	"apps/team/typed.go": "callerOf / sessionOf / admin / noStore / cookie — team authenticates its billing, " +
		"files and collaborator planes with a CALLER (identity.who): an IAM access token, else team's own " +
		"HS256 session token, riding Authorization or an HttpOnly cookie. principal.OrgFrom carries none of " +
		"those (nor the WORKSPACE an HS256 workspace token pins, which the collaborator plane gates on), and " +
		"bots/sync additionally needs admin-ness (X-User-IsAdmin). The cookie WRITER is the other end of that " +
		"same identity — the account-token cookie is set on the RESPONSE, which only the request reaches. It " +
		"is ONE file for the whole subsystem on purpose — the resolvers live here so the planes that use them " +
		"do not each reach for the request. All of them fail closed off the HTTP path: no request, no " +
		"credential, no identity, and no browser to sign out.",
	"apps/ml/typed.go": "tenantFrom — ml's tenant boundary is a per-org(+project) KUBERNETES NAMESPACE, and " +
		"deriving it takes two facts principal.OrgFrom does not carry: the org SUB-SCOPE (X-Project-Id, " +
		"which suffixes the namespace) and platform-admin-ness (X-User-IsAdmin, which buckets an org-less " +
		"admin under \"ml-admin\" — OrgFrom refuses an empty org outright, so reading the tenant through it " +
		"alone would turn that live admin bucket into a 403). ONE function, which every typed op asks, " +
		"delegating to the same principal.Acting the untyped handlers beside them use; fails closed off the HTTP " +
		"path, where there is no principal and therefore no namespace to name.",
	"apps/risk/typed.go": "gate / caller. gate is the ONE money seam for the model plane, and money is " +
		"the reason it needs more of the principal than the org: the debit is keyed on the SELECTED billing " +
		"ledger (principal.Ledger, which a SuperAdmin masquerade moves off the effective org), narrowed by " +
		"the server-minted project (X-Project-Id, with its validated-ness), and attributed with the user, " +
		"the request id and the client IP — none of which principal.OrgFrom carries. caller is the identity " +
		"a DECISION REGIME is recorded against: a policy version is an adverse-action input (it fixes the " +
		"cut every later score was judged by), so the row stamps the validated user id (X-User-Id), which " +
		"principal.OrgFrom does not carry and which must never be an In field — an attributable record whose " +
		"attribution the caller chose is not attributable. It reads the SAME header gate already reads for " +
		"the meter's actor, which is why it lives in this file rather than beside its one use in " +
		"policy_wire.go: a second file would be this same hatch under a second justification. TWO functions " +
		"in ONE file, so the whole package shares one seam. The TENANT is never read through either: " +
		"tenantFor uses principal.OrgFrom, right beside them. Off the HTTP path there is no ledger and the " +
		"metering pair is a no-op — the rule the rest of the fleet applies — and no identity, which " +
		"plane.enact refuses rather than recording an anonymous change.",
	"apps/o11y/typed.go": "callerIsAdmin / callerProject — the o11y surface's ONE identity seam. The " +
		"scoped reads switch on platform-sudo (X-User-IsAdmin: the infra-log god-view and the " +
		"whole-product RED) and the annotation queues narrow by project (X-Project-Id); neither header " +
		"rides on principal.OrgFrom. The status probe's weaker gate does NOT need the request — infra " +
		"health is not tenant-partitioned, so it serves an org-less but validated caller, which is " +
		"principal.ValidatedFrom, the bit Bridge parks beside the org. Concentrated in one file so the " +
		"escape hatch is one pin with one justification rather than the same call in three handlers; both " +
		"fail closed off the HTTP path.",
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
	"apps/catalog/catalog.go": "browse — the published corpus is read as PublicOrg by everyone, " +
		"signed in or not, so the tenant is re-pointed for the index Ask while the caller's authority " +
		"travels whole. cloud.As needs the request to do that; cloud.For alone drops the caller and the " +
		"public browse 500s with \"index: no org on the call\".",
	"apps/commerce/invoices.go": "eventsFrom/kmsFrom — two request-scoped side channels " +
		"carried in c.Locals(), which no ctx helper exposes. Both are OPTIONAL by design: a missing " +
		"analytics collector must never fail a money move, and a missing KMS client is the dev/test " +
		"posture where credentials come from the environment. Off the HTTP path both answer nil.",
	"apps/books/ask.go": "narrateAsk — the payer for the ONE grounded completion an Ask narrates with. " +
		"The bill lands on principal.Ledger, the SELECTED billing org, which a SuperAdmin masquerade " +
		"moves off the effective org — so principal.OrgFrom would charge the org being INSPECTED for a " +
		"platform admin's reading of its books. Empty off the HTTP path, where the meter no-ops rather " +
		"than billing the wrong ledger.",
	"apps/tools/charge_peer.go": "chargePeer — the tool plane's payment seam reaching the x402 " +
		"process, which is a PROXY that forwards the caller's identity and carries two facts of the " +
		"request across the boundary with it. The payer is principal.Ledger, which folds in the " +
		"SuperAdmin masquerade (X-User-IsAdmin plus the X-User-Owner home claim) that " +
		"principal.OrgFrom cannot carry — reading the tenant through OrgFrom would charge the " +
		"INSPECTED org's ledger for a platform admin's call. The client's signed authorization rides " +
		"the PAYMENT-SIGNATURE header, and the 402 challenge the rail answers with has to be written back onto " +
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
		"is the injective namespace.Sanitize slug — the name of the tenant-<org> namespace its App CRs " +
		"live in — not the verbatim owner claim. consoleUser is the second: the session read must ANSWER for " +
		"an anonymous caller rather than refuse one, and it reports the validated user ID (X-User-Id) " +
		"beside admin-ness. TWO functions in ONE file, delegating to the same resolveScope every raw handler " +
		"beside them uses; both fail closed off the HTTP path, where there is no attested caller and therefore " +
		"no scope.",
	"apps/explorer/explorer.go": "forwarded — the chain-data reads PROXY to the deployment's indexer and " +
		"graph, and where no service token is configured they pass the CALLER's own Authorization " +
		"through (client.go's authorize). That credential is the caller's, not an addressing value, so " +
		"it must not become an In field a caller could also put in a body — and principal.OrgFrom " +
		"carries the org and nothing else. The org gate itself is principal.OrgFrom (gate, right beside " +
		"it), never the request. Empty off the HTTP path, where there is no request and so no identity " +
		"to forward — the upstream read then goes out unauthenticated, which is what a public ledger " +
		"read already is.",
	"apps/projects/typed.go": "callerOf — the projects plane's ONE identity seam, asked by every typed " +
		"op through siteOf/releaseSite. It reads the request because THREE facts the ops turn on are not " +
		"the org principal.OrgFrom carries. The TENANT itself: an org-less platform SuperAdmin is " +
		"bucketed under the literal \"admin\" org (org(), projects.go), which OrgFrom cannot express — it " +
		"refuses an empty org outright, so reading the tenant through it alone would turn that live admin " +
		"bucket into a 403. PLATFORM SUDO (X-User-IsAdmin), which the moderation field on a project " +
		"update and the platform-operator DNS vouch both branch on. And the PAYER: every deploy path " +
		"gates and debits through principal.Ledger — the SELECTED billing org, which a SuperAdmin " +
		"masquerade deliberately moves off the effective org — plus the validated project sub-scope, the " +
		"request id and the client IP the meter attributes spend by. None may be an In field: a tenant " +
		"key or a payer a caller states is a cross-tenant read, or a cross-tenant SPEND, it asserted for " +
		"itself. ONE function, so all 37 typed ops share one seam, delegating to the same org() the " +
		"untyped deploy handler beside them uses; it fails closed off the HTTP path, where there is no " +
		"attested caller and therefore no tenant.",
	"apps/allowance/allowance.go": "get — the allowance holder is a WALLET, not an org. The key is the " +
		"payer principal.WalletOf resolves, which reads the signed `billing_account` claim, the validated " +
		"user name and the SuperAdmin bit besides the org — none of which principal.OrgFrom carries, and " +
		"keying on the org half alone would show every member of a pooled tenant one shared count. The " +
		"same read marks itself no-store, which is a RESPONSE header a typed op's signature drops. It may " +
		"never be an In field: a subject a caller states is someone else's allowance. ONE call site; it " +
		"fails closed off the HTTP path, where there is no principal and so no `own` count to answer.",
	"apps/pref/prefs.go": "subjectFrom — the preference OWNER, and it is not the org. The isolation " +
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
	"apps/entitlement/entitlements.go": "resolveOrg — the ONE trust decision both enablement ops share. It " +
		"needs two facts beyond the parked org: SuperAdmin-ness (X-User-IsAdmin, which lets an operator " +
		"comp a product to ANY org) and the VALIDATED owner claim to compare the :org path segment against, " +
		"which is what makes that segment an address the gate re-checks rather than an assertion it believes. " +
		"It also hands back the request so a write is ATTRIBUTED to the validated user id. Fails closed off " +
		"the HTTP path: no request, no attested principal, no access.",
	"apps/entitlement/projection.go": "the platform-sudo bit on the console's paywall read. \"admin\" is " +
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
	"apps/label/label.go": "actor — WHO asserted, on a record that can be the input to an adverse " +
		"action. It is the pair <home org>/<user>: principal.Owner (X-User-Owner, the identity anchor) " +
		"and c.User() (X-User-Id), NEITHER of which principal.OrgFrom carries — it carries the " +
		"EFFECTIVE org, and for a platform SuperAdmin acting inside a customer's tenant the home and " +
		"the effective org differ, which is exactly the case an adverse-action audit most needs to see. " +
		"Neither may be an In field: an attributable record whose attribution the caller chose is not " +
		"attributable. ONE function, asked from the one tenantOf every op goes through, which resolves " +
		"the TENANT with principal.OrgFrom and never through the request. Fails closed off the HTTP " +
		"path: no request, no attested asserter, no write.",
	"apps/billing/typed.go": "payer / caller — the billing package's ONE resolver of the request for " +
		"its typed ops. The TENANT is resolved with principal.OrgFrom, never through the request; the " +
		"request is needed for the PAYER — the wallet subject principal.Subject resolves from the minted " +
		"X-User-Name and the signed billing_account claim, headers the org does not carry, and the SAME " +
		"resolution the spend gate and the debit use, so a finance view can never read a different wallet " +
		"than the one charged. caller additionally reads the validated user id (X-User-Id) for the " +
		"per-account routed-usage breakdown, which is scoped to the PERSON. Cache-Control rides the " +
		"DECLARED contract instead (zip.WithResponseHeader + each Out's ResponseHeaders), so no-store " +
		"needs no request at all. Both fail closed off the HTTP path: no request, no payer.",
	"apps/billing/peer.go": "serviceOrg — the one read on this surface whose caller is a SERVICE " +
		"rather than a person: the metering edge, presenting a token and the gateway-pinned org with no " +
		"user behind it, which principal.OrgFrom refuses because it composes validated-ness AND an org. " +
		"It must be admitted, and the consumer's own failure mode is why: the edge reads any non-2xx as " +
		"FAIL-OPEN, so refusing the caller this read exists for does not fail a request, it lifts every " +
		"spend ceiling in the org and says nothing. It is the only place in this package that admits " +
		"that principal, and it fails closed off the HTTP path: no request, no service caller.",
	"apps/billing/posture.go": "mayMint / mintedTier / the rollup's plan filter — a caller may NAME a " +
		"tier rather than earn one, through an X-Tier header or an explicit ?tier=, and BOTH are client " +
		"input: the gateway neither mints X-Tier nor strips it. Naming a tier is a MINT — it decides " +
		"which models may be invoked and how many agents may run — so the override is honoured only for " +
		"platform authority or the trusted in-process service token, neither of which principal.OrgFrom " +
		"carries. An unprivileged override is IGNORED rather than refused, the same way the grant path " +
		"already treats the same class of client string, so the answer such a caller gets is simply the " +
		"true one. mayMint additionally gates the money-mode write and the estate-wide recharge sweep. " +
		"The TENANT is principal.OrgFrom throughout. All fail closed off the HTTP path: no request, no " +
		"mint, no override.",
	"apps/billing/subscriptions.go": "pathID / bodyStatedPeriodEnd — the id in the URL is the authority " +
		"for WHICH subscription is acted on, read from the request rather than the input so a body id can " +
		"never redirect a write; and whether the caller actually SAID when to cancel, which the input " +
		"cannot carry because the default is true and a bool's zero value is false. Without that " +
		"distinction a cancel with no body at all would end the subscription immediately and take the " +
		"rest of an already-paid period with it. The TENANT is principal.OrgFrom. Both answer empty off " +
		"the HTTP path, where the id has no other source.",
	"apps/billing/rails.go": "the request HOST — the receiving bank details a wire top-up renders are " +
		"the SERVING BRAND'S own, resolved from the host the customer is paying on (pay.hanzo.ai versus " +
		"pay.lux.network), which is a fact about the request and about nothing else. It is not an In " +
		"field for the reason no identity is: a caller that could name the brand could be shown another " +
		"brand's account. The TENANT is principal.OrgFrom. Fails closed off the HTTP path.",
	"apps/billing/alerts.go": "capAdmin — a spend cap is a FINANCIAL SAFETY control and both directions " +
		"of getting it wrong are expensive: a member who can delete the org's cap has unbounded spend, " +
		"and a member who can set a one-cent enforcing cap has an org-wide 402. So the WRITES require " +
		"platform sudo, org-admin standing or the service token — bits the identity boundary mints and " +
		"principal.OrgFrom does not carry — while the reads beside them stay member-open. It runs in the " +
		"handler rather than on the route because a typed op is reached by four projections and the " +
		"handler is the one point all four pass through. Fails closed off the HTTP path.",
	"apps/billing/statement.go": "the paging window — limit and offset for the transaction listing, " +
		"which are query values and belong to the request that asked for a page. They are read here " +
		"rather than bound as In fields because the store DEFAULTS on an absent or unparseable value and " +
		"an int field cannot tell ?limit=0 (a page of nothing) from ?limit=abc (unset). The TENANT and " +
		"the SUBJECT are resolved through payer, never from the query. Answers the defaults off the HTTP " +
		"path, where there is no page to ask for.",
	"apps/affiliate/typed.go": "sudo / actor / requireBody — the affiliate program's ONE resolver of the request. " +
		"Every /v1/admin route gates on platform sudo (X-User-IsAdmin), which principal.OrgFrom does not " +
		"carry; an application and the user-level referral mirror are ATTRIBUTED to the validated user id " +
		"(X-User-Id) — an attribution, never an authority; and requireBody replays the c.Bind refusal the " +
		"raw write handlers answered on a bodyless request, because zip's typed decode is tolerant and " +
		"without it a bodyless apply would enroll, a bodyless handle post would opt the caller out, and a " +
		"bodyless rate post would set a rate of zero. The TENANT is resolved with principal.OrgFrom " +
		"(tenant, in this same file), never through the request. All fail closed off the HTTP path: no " +
		"request, no attested admin, no actor, and nothing to require a body of.",
	"apps/dataroom/trust_typed.go": "sudo / orgAdmin / actor — the trust centre's ONE resolver of the " +
		"request. The plane has TWO admin scopes and conflating them is the privilege escalation it is " +
		"built to prevent: the cross-tenant roster gates on platform sudo (principal.IsSuperAdmin, the " +
		"reserved admin org's membership as SanitizeIdentity minted it), while changing what an org " +
		"releases gates on being an admin OF THAT ORG (X-User-IsOrgAdmin) — two headers, neither of " +
		"which principal.OrgFrom carries and neither of which has a ctx-side twin to read instead. " +
		"Reading the org-scoped one as platform authority would hand every customer admin the roster; " +
		"reading the platform one as org authority would refuse every legitimate customer. Neither can " +
		"be an In field, for the usual reason — a caller that could name itself an admin would be one. " +
		"actor records WHO granted access, on the validated user id (X-User-Id): an attribution on the " +
		"access record, never an authority. The TENANT is resolved with principal.Acting throughout, " +
		"never through the request. All three fail closed off the HTTP path, which is what closes the " +
		"MCP door and the internal plane — both invoke a typed op DIRECTLY, with no route for the " +
		"group's own sudoGate to sit on.",
	"apps/link/http.go": "scope — the linked-account surface is scoped to (org, SUBJECT): every op keys " +
		"the caller's own provider accounts and usage on the validated user id (c.User()) as well as the " +
		"tenant, and principal.OrgFrom carries only the tenant. ONE function, which every op in the " +
		"package asks — the SAME caller() boundary the raw handlers keyed — so both planes share one " +
		"gate. Off the HTTP path there is no attested caller, so it refuses with exactly the 403 an " +
		"anonymous REST call gets: fail closed, one gate, not two.",
	"apps/usage/account.go": "caller / noStore — the usage plane is scoped to (org, SUBJECT): the " +
		"account board carries the caller's OWN linked provider accounts, so it needs the validated user id " +
		"(c.User()) as well as the tenant, and principal.OrgFrom carries only the tenant. noStore is the " +
		"other half of the same request — per-tenant MONEY must never be cached by a browser or an " +
		"intermediary, and Cache-Control is a RESPONSE header only the request reaches. TWO functions in " +
		"ONE file, so all five ops share one seam; the tenant-only read (orgOf, right beside them) goes " +
		"through principal.OrgFrom and never through the request. Both fail closed off the HTTP path: no " +
		"request, no subject, and no response to mark.",
	"apps/world/news.go": "scopeOf — world is scoped to (org, PROJECT) and every store statement carries " +
		"both. The project is a server-minted claim (X-Project-Id) that principal.OrgFrom does not carry, " +
		"and it is a TENANT key, so it must not become an In field a caller supplies for itself. The " +
		"?project query beside it is only ever cross-CHECKED against that claim and cannot be an In field " +
		"either: zip binds an In field from the BODY as well as the URL, so PUT /v1/world/pipeline would " +
		"start rejecting a body that named a project — a wire the route has never had. ONE function, which " +
		"all four scoped ops ask, delegating to the same scope() the SSE handler beside them uses; fails " +
		"closed off the HTTP path, where there is no principal and therefore no tenant.",
	"apps/platform/ops.go": "caller / request / admit — the PaaS control plane's three identity seams, " +
		"in one file, which every one of its 32 typed ops goes through. caller resolves the tenant with " +
		"platform's own principal.Acting, not principal.OrgFrom: this surface keys NAMESPACES and per-tenant image " +
		"refs on the org, so it needs the injective namespace.Sanitize form and the \"admin\" bucket a " +
		"validated SuperAdmin with no org falls into — neither of which principal.OrgFrom can express, since " +
		"it returns the owner claim verbatim and refuses an empty org outright. It also hands the request " +
		"back because this plane SPENDS the caller's identity rather than only reading it: /v1/platform/run gates " +
		"and meters the caller's own ledger (principal.Ledger, the request id and the client IP), and every " +
		"deploy, preview, promote and rollback writes the actor and request id to the audit log. request is " +
		"for the two ops that authorize on something OTHER than a tenant — /v1/platform/runner compares a shared " +
		"build credential in constant time off the Authorization header, and the release reads gate on " +
		"cloud.Super — so asking for an org would refuse the machine caller the endpoint exists for. admit is " +
		"the fleet board's role gate, cloud.Scope.Admits over cloud.AuthorityOf, which reads X-User-IsAdmin " +
		"and X-User-IsOrgAdmin; it was cloud.Guard around the handler until these routes became typed ops, " +
		"and a typed op has no zip.Handler for a wrapper to compose with. All three fail closed off the HTTP " +
		"path: no request, no tenant, no attested authority, no answer.",
	"apps/destination/destinations.go": "orgAdmin — the gate every destination MUTATION keeps " +
		"(disconnect and test both forget or spend a credential). It reads org-admin-ness, which is " +
		"X-User-IsOrgAdmin, a claim principal.OrgFrom does not carry. The tenant itself is read with " +
		"principal.OrgFrom (tenantOf, right beside it), never through the request. ONE function, so the " +
		"two mutating ops share one seam; it fails closed off the HTTP path: no request, no attested " +
		"caller, no mutation.",
	"apps/security/security.go": "submitScan — a secret scan is a priced act and an audited one, and " +
		"this is the op's ONE request seam. The prepaid gate and the meter need the payer " +
		"(principal.Ledger), the validated project sub-scope (principal.ValidatedProject) and the request " +
		"id + client IP the debit is attributed with; the audit record needs the caller's subject and " +
		"email, admin-ness, and the method and path actually reached. None of those is the tenant, which " +
		"is read with principal.OrgFrom (tenant, in this same file), and none may become an In field — a " +
		"caller that could name its own payer would bill another org. Fails closed off the HTTP path: no " +
		"request, no payer, and a refusal rather than an unbilled scan.",
	"apps/functions/invoke.go": "invoke — an invocation is prepaid compute, which is the whole reason " +
		"it holds the request. The flat request fee is gated BEFORE the sandbox runs and the GB-seconds " +
		"debit taken after it, and both halves need the payer (principal.Ledger), the validated project " +
		"(principal.ValidatedProject) and the request id + client IP they are attributed with — facts " +
		"principal.OrgFrom does not carry and an In field must never supply, since a caller naming its " +
		"own payer would charge another org. The tenant is read with principal.OrgFrom (tenant, in " +
		"functions.go). Fails closed off the HTTP path: no request, no payer, and no free compute.",
	"apps/eval/metrics.go": "admin — the one fact the evals board reads beyond its tenant: platform " +
		"SuperAdmin-ness (c.IsAdmin(), the X-User-IsAdmin the identity boundary mints only for a " +
		"validated owner == AdminOrg), which is what widens the board to AllOrgs. That is a CROSS-TENANT " +
		"authority, so it can never be an In field a caller supplies for itself, and principal.OrgFrom " +
		"does not carry it. The org every query keys on is authoritative from principal.OrgFrom (tenant, " +
		"in eval.go). False off the HTTP path: no request, no attested caller, no cross-tenant board.",
	"apps/experiment/experiments.go": "actorOf / orgAdmin — the experiment plane's two identity seams, " +
		"and neither of them is the tenant. actorOf is the credential's email (c.UserEmail()), what a " +
		"create and a decision are STAMPED with — an attribution, never an authority. orgAdmin is the " +
		"gate promoting a winner takes, org-admin-ness (principal.IsOrgAdmin, X-User-IsOrgAdmin), because " +
		"a promotion rewrites a flag definition and that is the flag write plane's own gate. The tenant " +
		"and its project sub-scope are read with principal.OrgFrom (tenant, right above), never through " +
		"the request. Both fail closed off the HTTP path: no actor to stamp, and no attested admin.",
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
			// A DOT-directory is never this module's source: .git, and equally
			// .claude/worktrees, which holds whole checkouts of this same repo. Naming
			// ".git" alone let those checkouts be walked, so the gate reported every
			// call site two or three times over — under paths that do not exist for
			// anyone else — and a real new call site was indistinguishable from a
			// leftover working copy. One rule, so nothing has to be added here again.
			if path != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "node_modules", "vendor", "webui", "testdata":
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
		// A GENERATED file is a projection of source this walk already reads, so
		// counting it counts the same fact twice — and zipdoc's projection is PROSE:
		// apps/automations names cloud.Request(ctx) in the doc comments explaining
		// why four routes stay untyped, and those sentences are lifted verbatim into
		// zipdoc_gen.go's Description strings. A sentence about the escape hatch is
		// not a use of it. The handler file that carries both the prose and the real
		// call sites is pinned on its own line above.
		if bytes.HasPrefix(b, []byte("// Code generated ")) {
			return nil
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
				"principal.OrgFrom(ctx) and delete the call; if it has no tenant and only gates on being signed "+
				"in, that is principal.ValidatedFrom(ctx). If the op can NAME the value, declare it on its In "+
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
