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
	"apps/search/search.go": "Query resolves the tenant from the validated principal at the top of the op.",
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
		"claim key that rides in its own header (X-Agent-Claim-Key), which is the second of that plane's two " +
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
