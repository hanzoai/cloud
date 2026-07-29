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
// principal.OrgFrom(ctx) would do, the answer is not this. Adding a fifth means
// editing this map and writing the justification, which is a decision someone
// makes on purpose rather than a drift nobody notices.
//
// The three identity gates are here because they need MORE of the validated
// principal than the org: admin-ness lives in a header (X-User-IsAdmin) that
// only the request carries. The proxy is here because forwarding is the point.
var allowedRequestUses = map[string]string{
	"apps/admin/core/typed.go": "Admit / AdmitScoped — the SuperAdmin and white-label tenant gates. " +
		"Both read validated identity beyond the org (IsAdmin, the WL allowlist), which principal.OrgFrom does not carry.",
	"apps/account/account.go": "requestCaller — account IS the signed-in caller's own account, and resolving " +
		"them needs more of the validated principal than the org: the user id (X-User-Id), the IAM username " +
		"(X-User-Name) that IAM's user-key ops parse, and validated-ness itself, none of which principal.OrgFrom " +
		"carries. ONE function, which every op in the package asks; it fails closed off the HTTP path. Two ops " +
		"then reuse the request it hands back for a second, non-identity reason: the CSRF issuer pins " +
		"Cache-Control on its response, and embed-status reads the SuperAdmin claim.",
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
	"apps/team/typed.go": "sessionOf / admin / noStore — team authenticates its billing and files planes " +
		"with its OWN HS256 session token, which rides in Authorization or the HttpOnly account-token cookie; " +
		"principal.OrgFrom carries neither, and bots/sync additionally needs admin-ness (X-User-IsAdmin). " +
		"It is ONE file for the whole subsystem on purpose — the resolvers live here so the planes that use " +
		"them do not each reach for the request. All three fail closed off the HTTP path: no request, no " +
		"token, no identity.",
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
				"principal.OrgFrom(ctx) and delete the call. If it genuinely needs the request — an identity gate "+
				"reading more than the org, or a proxy that FORWARDS the caller's identity — add %q to "+
				"allowedRequestUses with the reason, so the next reader knows why it is here.", file, file)
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
