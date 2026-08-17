package projects

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ONE NOUN, and this is the test that makes retiring the other two possible.
//
// A static site is addressed three ways here. /v1/projects is the original name,
// /v1/platform/sites is a byte-identical alias of it, and /v1/sites is the noun
// the thing actually is. They were never equal: deployments lived under projects
// while releases and publish lived under sites, so shipping a site meant knowing
// which half of its lifecycle sat under which name — and delete, rename, purge
// and domains existed only under the two older names.
//
// /v1/sites is now a strict SUPERSET. That is the precondition for deprecation:
// a caller can move to it without losing a verb, and only then can the aliases
// go. This test is what stops the superset quietly lapsing — add a route to
// /v1/projects alone and it fails here, naming the verb that has no home under
// the noun.
//
// It reads the SOURCE rather than the router. Registration is the fact being
// asserted, the paths are literals by necessity (zipdoc keys prose on the literal
// string), and a fixture rebuilt from a router would be a second copy of the
// thing under test.
func TestSitesIsTheSupersetNoun(t *testing.T) {
	src, err := os.ReadFile("projects.go")
	if err != nil {
		t.Fatalf("read routes: %v", err)
	}
	verbs := verbsByNoun(string(src))

	sites := verbs["/v1/sites"]
	if len(sites) == 0 {
		t.Fatal("no /v1/sites routes found — this test is watching nothing")
	}

	var missing []string
	for _, older := range []string{"/v1/projects", "/v1/platform/sites"} {
		for v := range verbs[older] {
			if !sites[v] {
				missing = append(missing, older+" has "+v+", /v1/sites does not")
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("/v1/sites is no longer a superset, so the older nouns cannot be retired:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// verbsByNoun maps each address space to the set of "METHOD /suffix" it serves,
// with the prefix removed so the three are comparable. Both registration styles
// count: zip.X for typed ops and app.Post for the untyped archive upload, which
// is a route a client can call and therefore a verb the noun either has or lacks.
func verbsByNoun(src string) map[string]map[string]bool {
	typed := regexp.MustCompile(`zip\.(Get|Post|Put|Patch|Delete)\(r, "(/v1/[^"]+)"`)
	raw := regexp.MustCompile(`app\.(Post|Get|Put|Patch|Delete)\("(/v1/[^"]+)"`)

	out := map[string]map[string]bool{}
	// Longest first: /v1/platform/sites must not be read as /v1/sites.
	nouns := []string{"/v1/platform/sites", "/v1/projects", "/v1/sites"}
	for _, m := range append(typed.FindAllStringSubmatch(src, -1), raw.FindAllStringSubmatch(src, -1)...) {
		method, path := m[1], m[2]
		for _, n := range nouns {
			if path != n && !strings.HasPrefix(path, n+"/") {
				continue
			}
			suffix := strings.TrimPrefix(path, n)
			if suffix == "" {
				suffix = "/"
			}
			if out[n] == nil {
				out[n] = map[string]bool{}
			}
			out[n][method+" "+suffix] = true
			break
		}
	}
	return out
}
