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
	"apps/search/search.go": "Query resolves the tenant from the validated principal at the top of the op.",
	"apps/agents/targets.go": "targetOwns / targetCaller — a machine belongs to the principal that " +
		"registered it, and only that owner or an org admin may patch, delete or manage its route-work " +
		"plane. Ownership needs X-User-Id and org-admin-ness (X-User-IsOrgAdmin), neither of which " +
		"principal.OrgFrom carries. Both fail closed off the HTTP path: no request, no attested caller, " +
		"no management rights.",
	"apps/company/register.go": "reviewer — the Hanzo platform gate on the formation register and on a " +
		"founder KYC decision. Hanzo forms the entity and carries the KYC/AML obligation, so the decision is " +
		"a SuperAdmin one and is ATTRIBUTED: it needs X-User-IsAdmin and X-User-Id, neither of which " +
		"principal.OrgFrom carries. Fails closed off the HTTP path: no request, no attested reviewer.",
	"apps/visor/visor.go": "A tenant-scoped PROXY: client.go forwards the caller's own identity headers " +
		"(and their bearer where no service credential is configured) upstream, so an op without the request " +
		"drops the caller's identity on the far side of the hop.",
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
