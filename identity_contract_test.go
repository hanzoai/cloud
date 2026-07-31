package cloud

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
)

// EVERY identity header cloud's boundary writes must be a name the estate strips.
//
// This is the one property that makes a forged identity header impossible rather
// than unlikely, and it cannot be held by reading code: cloud wrote its headers as
// STRING LITERALS while authz owned the names, so the two lists were free to drift
// and one already had — X-App-Id was written here and named nowhere in the estate,
// so an edge stripping authz.Headers would have left a client copy standing.
//
// The test greps the source rather than exercising a request because the hazard is a
// write that no test happens to reach. A literal is what it catches; using the
// constants is what makes it pass.
func TestEveryHeaderWrittenIsAName(t *testing.T) {
	known := map[string]bool{}
	for _, h := range append(append([]string{}, authz.Headers...), authz.Retired...) {
		known[strings.ToLower(h)] = true
	}

	// Any X-* header set with a string literal, anywhere in the identity boundary.
	lit := regexp.MustCompile(`Header\.Set\("(X-[A-Za-z0-9-]+)"`)
	for _, f := range []string{
		"middleware_identity.go", "auth_identity.go", "auth_apikey.go",
		"token_validator.go", "identity_cache.go", "org_scope.go",
	} {
		src, err := os.ReadFile(f)
		if err != nil {
			continue // the file may be gone; that is not a failure of this property
		}
		for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
			t.Errorf("%s writes %q as a STRING LITERAL — use the authz.Header* constant, "+
				"so the name it writes and the name ingress strips are one name", f, m[1])
		}
	}

	// And the constants it does use are all names the estate strips.
	ref := regexp.MustCompile(`authz\.(Header[A-Za-z]*)`)
	byName := map[string]string{
		"HeaderOrg": authz.HeaderOrg, "HeaderWorkspace": authz.HeaderWorkspace,
		"HeaderProject": authz.HeaderProject, "HeaderUser": authz.HeaderUser,
		"HeaderUserName": authz.HeaderUserName, "HeaderUserEmail": authz.HeaderUserEmail,
		"HeaderUserOwner": authz.HeaderUserOwner, "HeaderUserAdmin": authz.HeaderUserAdmin,
		"HeaderUserOrgAdmin": authz.HeaderUserOrgAdmin, "HeaderApp": authz.HeaderApp,
		"HeaderBillingAccount": authz.HeaderBillingAccount, "HeaderScope": authz.HeaderScope,
		"HeaderScopeRole": authz.HeaderScopeRole, "HeaderUserPermissions": authz.HeaderUserPermissions,
		"HeaderRequestID": authz.HeaderRequestID,
	}
	src, err := os.ReadFile("middleware_identity.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range ref.FindAllStringSubmatch(string(src), -1) {
		v, ok := byName[m[1]]
		if !ok {
			continue
		}
		seen++
		if v != authz.HeaderRequestID && !known[strings.ToLower(v)] {
			t.Errorf("cloud writes authz.%s (%q), which the estate does not strip", m[1], v)
		}
	}
	if seen == 0 {
		t.Error("no authz.Header* reference found — this test would pass vacuously")
	}
}
