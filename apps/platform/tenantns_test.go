package platform

import (
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/namespace"
)

// tenantns_test.go — the write and the read must derive ONE namespace.
//
// platform WRITES a tenant's App CRs into tenantNamespace(org); apps/deploy READS
// them from tenantNS(org) ("tenant-"+slug), and apps/provisioning places a
// dedicated database with a third copy of the same rule. All three take the
// ALREADY-SANITIZED slug that tenant(s,c) yields — but platform's copy sanitized
// it AGAIN, and namespace.Sanitize is NOT idempotent: it is the identity only on
// a clean label, and re-folding its own <fold>-<hash> output appends a SECOND
// hash (looksSuffixed denies the fast path, deliberately, so a name cannot squat
// on the rendered form of another).
//
// So for every org whose name is not already a clean slug, platform wrote to
// tenant-<double> while every reader scanned tenant-<single>. It fails closed —
// nothing crosses a tenant boundary — but the org's CD panel is silently EMPTY
// and its CRs are orphaned in a namespace nothing lists. Same class as the
// confinement asymmetry red found: Sanitize applied an unequal number of times
// on two sides of one comparison.
func TestTenantNamespaceIsDerivedExactlyOnce(t *testing.T) {
	for _, raw := range []string{
		"acme",     // clean: Sanitize is the identity, so the bug is invisible here
		"Acme",     // case ⇒ folded + suffixed
		"team.a",   // punctuation ⇒ folded + suffixed
		"ACME-Ltd", // both
		"a-really-long-organisation-name-that-exceeds-the-thirty-two-byte-label",
		"acme-0123456789abcdef", // already shaped like Sanitize's own output
	} {
		t.Run(raw, func(t *testing.T) {
			slug := namespace.Sanitize(raw) // what tenant(s, c) hands every handler
			if slug == "" {
				t.Fatalf("precondition: %q must resolve", raw)
			}
			write := tenantNamespace(slug)
			// The rule apps/deploy (scope.go tenantNS) and apps/provisioning
			// (dedicated.go tenantNamespace) both apply to the same slug.
			read := "tenant-" + slug
			if write != read {
				t.Fatalf("ORPHAN: platform writes %q but every reader scans %q — the org's board is silently empty",
					write, read)
			}
		})
	}
}

// The empty guard survives: a caller with no org still lands somewhere inert
// rather than in the bare "tenant-" namespace.
func TestTenantNamespaceKeepsItsEmptyGuard(t *testing.T) {
	if got := tenantNamespace(""); got != "tenant-unknown" {
		t.Fatalf("tenantNamespace(\"\") = %q, want tenant-unknown", got)
	}
}

// The rule must be applied in ONE place. This reads the source of the function
// itself and fails if a Sanitize call ever returns to it — the recurrence guard
// for a bug a comment cannot prevent.
//
// It is a structural check because the real fix is a TYPE: a distinct
// SanitizedOrg that only namespace.Sanitize can mint would make double
// application a compile error rather than a silent namespace split. That type
// belongs in github.com/hanzoai/namespace, which all three copies of this rule
// already import (apps/platform, apps/deploy, apps/provisioning) — a cross-repo
// release, and the follow-up this guard holds the line for until it lands.
func TestTenantNamespaceDoesNotSanitize(t *testing.T) {
	src, err := os.ReadFile("k8s.go")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := funcBody(string(src), "func tenantNamespace(")
	if !ok {
		t.Fatal("tenantNamespace not found in k8s.go")
	}
	if strings.Contains(body, "Sanitize(") {
		t.Fatalf("tenantNamespace sanitizes again; its input is ALREADY a slug and Sanitize is not idempotent:\n%s", body)
	}
	// And it still refuses anything that is not a label, so a caller that skips
	// tenant(s, c) lands inert rather than rendering a malformed namespace.
	if got := tenantNamespace("acme/../hanzo"); got != "tenant-unknown" {
		t.Fatalf("a non-label input produced %q", got)
	}
}

// funcBody returns the source between a function's opening line and the first
// line that is exactly "}" — enough to read one small function.
func funcBody(src, decl string) (string, bool) {
	i := strings.Index(src, decl)
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	if before, _, ok := strings.Cut(rest, "\n}\n"); ok {
		return before, true
	}
	return rest, true
}
