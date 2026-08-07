package cloud

// The brand registry has more than one consumer, and only one of them is Go.
//
// brand.go is read at runtime by the token validator: it decides which `iss` a
// token may carry. But the SPAs this repo ships (apps/*/ui/dist, go:embed'd into
// the binary) carry their OWN copy of the same table — TypeScript, compiled into
// the bundle at build time — and it is that copy which decides where a browser is
// SENT to sign in. The agent-skills catalogue carries a third copy, and it is
// what an autonomous agent reads to learn where to get a token.
//
// So the same fact is written in three places and enforced in one. When they
// disagree the failure is invisible from inside Go: every Go test passes, the
// bundle ships, and a user is redirected to an identity host the server will not
// accept — or, as measured here, to `zoo.id`, which has NO DNS RECORD AT ALL
// while the live Zoo IAM stamps iss=zoolabs.id. Both shipped bundles named it.
//
// The severity is not the outage. A brand table that names a host the registry
// does not know is the precondition for cross-brand token confusion: a bundle
// that can be pointed at an issuer the validator never vouched for, or at an
// attacker-registrable name (zoo.id was unregistered), is a bundle that can be
// made to hand a credential to the wrong estate. The registry is the closed set;
// this test is what keeps the artifacts inside it.
//
// The test therefore reads the ARTIFACTS, not a mirror of a mirror — the exact
// bytes that go:embed puts in the binary and the CDN serves. A pin against a
// hand-written copy of the Go table would have passed the whole time.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/brand"
)

// originRE finds every absolute https origin in a text artifact. Minified JS
// keeps string literals verbatim, so the issuer a bundle was compiled with is
// present as-is.
var originRE = regexp.MustCompile(`https://([A-Za-z0-9.-]+)`)

// identityHost reports whether a host is an IDENTITY host — one that answers
// OIDC — as opposed to an API or marketing host that happens to be ours.
//
// Every issuer in the estate is either a `.id` name (hanzo.id, lux.id,
// zoolabs.id, pars.id) or an `id.` subdomain (id.bootno.de). That is the shape
// the rule keys on, so a NEW misspelling is caught by the shape rather than by
// an allowlist somebody must remember to extend. Measured across every shipped
// artifact, the rule selects exactly the four issuer-shaped hosts present and
// nothing else — no API host, no CDN, no XML namespace.
func identityHost(h string) bool {
	return strings.HasSuffix(h, ".id") || strings.HasPrefix(h, "id.")
}

// shippedArtifacts returns the text artifacts this repo ships to a client: the
// built SPA bundles that are go:embed'd, and the agent-skills catalogue.
func shippedArtifacts(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range []string{"apps", "webui"} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable subtree is not this test's business
			}
			slash := filepath.ToSlash(p)
			inDist := strings.Contains(slash, "/dist/")
			inCatalog := strings.Contains(slash, "/skills/catalog/")
			if !inDist && !inCatalog {
				return nil
			}
			switch filepath.Ext(p) {
			case ".js", ".mjs", ".json", ".md", ".html", ".css":
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// TestShippedArtifactsNameOnlyRegisteredIssuers is the drift gate: every
// identity host named by anything this repo ships must be an issuer the brand
// registry declares.
//
// It fails on the bundle that was live at tracker.hanzo.ai — its brand table
// said zoo -> https://zoo.id while brand.go said https://zoolabs.id.
func TestShippedArtifactsNameOnlyRegisteredIssuers(t *testing.T) {
	declared := map[string]bool{}
	for _, iss := range brand.Issuers() {
		declared[strings.TrimPrefix(iss, "https://")] = true
	}
	if len(declared) == 0 {
		t.Fatal("brand.Issuers() is empty — the registry cannot vouch for anything")
	}

	files := shippedArtifacts(t)
	// A scan that silently matches nothing is the estate's favourite way to be
	// green while proving nothing. If the artifacts move, this test says so.
	if len(files) == 0 {
		t.Fatal("no shipped artifacts found — this gate scanned nothing")
	}

	seen := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range originRE.FindAllStringSubmatch(string(raw), -1) {
			host := strings.ToLower(strings.TrimRight(m[1], "."))
			if !identityHost(host) {
				continue
			}
			seen++
			if !declared[host] {
				t.Errorf("%s names identity host %q, which brand.Issuers() does not declare.\n"+
					"  A shipped artifact may only send a user to an issuer this binary validates.\n"+
					"  Registered: %v\n"+
					"  Fix the artifact's brand table (admin: pkgs/admin/src/brand.ts) and rebuild dist,\n"+
					"  or add the brand to brand/brand.go — never leave the two disagreeing.",
					f, host, brand.Issuers())
			}
		}
	}
	if seen == 0 {
		t.Fatal("no identity host found in any shipped artifact — the scan is not reaching the brand tables")
	}
}
