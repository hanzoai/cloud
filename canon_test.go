package cloud

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Some facts have exactly one owner, and every consumer that answers one for itself
// is a second answer free to drift from the first. Five landed in one day —
// the reserved admin org read from the environment in three places, the KMS secret
// resolver written twice byte for byte, a brand title upper-cased in three, the
// client address derived in two (one of them from the attacker-controlled end of the
// forwarded chain), and a shared-key precondition asked by eleven apps that mostly
// did not use it.
//
// Each was fixed where it was found, which fixes the copies that exist and nothing
// about the next one. A guard per fact is the same mistake one level up: N facts, N
// tests, and the N+1st fact gets none.
//
// So: the RULE is this file's one walker, and the FACTS are the table it reads. A new
// invariant is a row. That is the whole of the maintenance.
//
// It reads source rather than behavior on purpose. Behavior can only catch a copy
// that is wrong TODAY; `display("")` returning "" and brand.Display("") returning
// "Hanzo" agreed everywhere they were called, right up until they would not have.
// The defect is the second answer existing, so the second answer is what is measured.
type canon struct {
	// fact is what is being answered, in the words an operator would use.
	fact string
	// home is where the one answer lives — named in the failure so the fix is
	// the message, not an investigation.
	home string
	// local matches a consumer answering it for itself.
	local *regexp.Regexp
	// owns are the files ALLOWED to match: the home itself, and anywhere the rule
	// is legitimately spelled out. Everything else is a copy.
	owns []string
	// cost is what the second answer buys, stated concretely. A guard nobody can
	// read the reason for is a guard somebody deletes.
	cost string
}

var canons = []canon{{
	fact:  "the reserved admin org (the SuperAdmin predicate)",
	home:  "authz.AdminOrg — the ISSUER's constant, mirroring IAM's own owner == \"admin\"",
	local: regexp.MustCompile(`IAM_ADMIN_ORG`),
	owns:  []string{"middleware_identity.go"}, // names it only to say it is gone
	cost: "a consumer-side value can only make cloud DISAGREE with the token it reads, " +
		"and in this direction disagreement is platform sudo: pointed at \"hanzo\" it " +
		"admits every member of that org while IAM considers none of them SuperAdmin",
}, {
	fact:  "how to resolve a KMS-sealed secret from Deps",
	home:  "Deps.Secret (deps.go) — Deps is what HAS the KMS",
	local: regexp.MustCompile(`func kmsGetter\(`),
	owns:  nil,
	cost: "it was written twice, byte for byte, in apps/company and apps/compliance — " +
		"two homes for one helper, each free to change its nil-KMS behaviour alone",
}, {
	// NO ROW FOR "render a brand id". It was written and withdrawn, and the reason
	// is the boundary of this whole file.
	//
	// The rule's shape — upper-case the first rune — is a generic string operation.
	// Matching it flagged five files and four were right to do it: an account name,
	// a sentence, a generic word defaulting to "Other", a GraphQL type name. Only
	// apps/trust was a brand (now cloud.BrandDisplay).
	//
	// A guard at 4-in-5 false positives does not get obeyed, it gets deleted, and it
	// takes the true rows with it. What separates a brand title from capitalising a
	// word is what the string MEANS, and meaning is not a regexp. So the rows here
	// are only the ones whose shape is unambiguous on its face: an env var name, a
	// function name, a specific header read, a specific call.
	fact:  "the caller's address",
	home:  "clientip.ClientIP (zip) / clientip.ClientIPOf (net/http)",
	local: regexp.MustCompile(`Header\.Get\("X-Forwarded-For"\)`),
	owns:  []string{"clientip/clientip.go"},
	cost: "the copy read the LEFT-MOST forwarded entry — the one value in the chain a " +
		"caller writes for itself — into the billing and audit column, which is how one " +
		"host becomes a million clients and defeats every per-IP limit keyed on it",
}, {
	fact:  "which IAM signs my token",
	home:  "brand.Issuer — the leaf BOTH hosts can reach (package cloud and the light cmd/cloud, which does not link it)",
	local: regexp.MustCompile(`"CLOUD_IAM_ISSUER".*IssuerFor\(`),
	// Reading the pinned value into a field is CORRECT — config.go does exactly
	// that and then resolves through issuerFor. What is a second answer is the
	// env read COMBINED with the brand fallback inline, which is the shape that
	// skipped the trim. So the row matches the combination, not the read.
	owns: []string{"brand/brand.go", "iamurl.go"},
	cost: "the second spelling did not trim, and an OIDC issuer is compared as a literal " +
		"string — a pinned \"https://hanzo.id/\" and a derived \"https://hanzo.id\" are two " +
		"issuers, so a token stamped by one fails validation against the other",
}, {
	fact:  "whether this process may serve, given the console anti-forgery key",
	home:  "cloud.keyed at compose time (intent.go), cloud.Intended per request, account own for the minter",
	local: regexp.MustCompile(`account\.Shared\(\)|accountapp\.Shared\(\)`),
	owns:  nil,
	cost: "eleven apps asked it in Mount and refused their WHOLE surface — including " +
		"public reads that answer no token at all — for a key one branch of one control " +
		"reads, and being lazy they refused in the child on first request, where no probe " +
		"could see it",
}}

func TestOneFactHasOneHome(t *testing.T) {
	for _, c := range canons {
		found := map[string]int{}
		err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/testdata/") {
				return nil
			}
			for _, own := range c.owns {
				if strings.HasSuffix(path, own) {
					return nil
				}
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, line := range strings.Split(string(b), "\n") {
				s := strings.TrimSpace(line)
				if strings.HasPrefix(s, "//") || !c.local.MatchString(s) {
					continue
				}
				found[path]++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		for path, n := range found {
			t.Errorf("%s answers %s for itself (%d line(s)).\n  it lives at: %s\n  a second answer costs: %s",
				path, c.fact, n, c.home, c.cost)
		}
	}
}
