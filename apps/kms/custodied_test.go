// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The custody set is only as good as its coverage: an app that starts sealing a
// tenant's credential under a subtree nobody listed hands it to every member
// again. This reads every ref the fleet spells and holds each org subtree it
// names to the set — custodied, or named below as something else.
//
// The four spellings the apps use: "orgs/" + org + "/<subtree>…",
// OrgPath(org, "<subtree>", …), "orgs/%s/<subtree>…", and a literal org's
// "orgs/<org>/<subtree>/…". Two builders it cannot read carry their own test:
// apps/wallet's keyRef and apps/provisioning's one subtree per kind.
var (
	sealConcat  = regexp.MustCompile(`"orgs/"\s*\+\s*([A-Za-z_][\w.]*)\s*\+\s*"/([a-z0-9-]+)`)
	sealPath    = regexp.MustCompile(`OrgPath\(\s*[^,()"]+,\s*"([a-z0-9-]+)"`)
	sealFormat  = regexp.MustCompile(`"orgs/%s/([a-z0-9-]+)`)
	sealLiteral = regexp.MustCompile(`"orgs/[a-z0-9-]+/([a-z0-9-]+)/`)
)

// notCustody is every org subtree the sweep finds that is NOT sealed on a
// tenant's behalf, and why.
var notCustody = map[string]string{
	// The platform org's own material, read by its charts and CI under the
	// grants declared for them (forge/source.go, apps/task, apps/seo, plugin/s3).
	"deploy":   "the deployment's, under the platform org",
	"seo":      "the deployment's, under the platform org",
	"hanzo-s3": "the deployment's, under the platform org",
	// apps/commerce reads the platform's own payment-rail secrets at
	// orgs/hanzo/wire/<NAME> (secrets_env.go), falling back to commerce-secrets
	// and the environment. Only the literal platform org is ever spelled there.
	"wire": "the deployment's, under the platform org",
	// apps/content keys an object in S3 with this shape; it is not a KMS ref.
	"output": "an object-store key",
}

// fleetSources is every Go source the fleet builds from, by its path beneath
// the repository root: what the sweeps below read.
func fleetSources(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "testdata", "node_modules", ".git", "webui", "e2e":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "zipdoc_gen.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEveryOrgSubtreeAnAppSealsIsCustodied(t *testing.T) {
	found := map[string][]string{}
	for path, src := range fleetSources(t) {
		for _, m := range sealConcat.FindAllStringSubmatch(src, -1) {
			found[m[2]] = append(found[m[2]], path)
		}
		for _, re := range []*regexp.Regexp{sealPath, sealFormat, sealLiteral} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				found[m[1]] = append(found[m[1]], path)
			}
		}
	}
	// The sweep must see the apps red named, or it is reading the wrong tree.
	for _, want := range []string{"tools-mcp", "environment", "providers", "platform", "validators", "integrations"} {
		if _, ok := found[want]; !ok {
			t.Fatalf("the sweep did not find %q; it is not reading the apps", want)
		}
	}
	var missing []string
	for sub, where := range found {
		if _, ok := custodied[sub]; ok {
			continue
		}
		if _, ok := notCustody[sub]; ok {
			continue
		}
		slices.Sort(where)
		missing = append(missing, sub+" ("+strings.Join(slices.Compact(where), ", ")+")")
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Fatalf("an app seals under an org subtree the custody set does not name; add it to custodied (custody.go) or say why it is not:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// A SYNC subtree is custodied only BENEATH its root (custody.go): a key at
// orgs/<org>/platform/<KEY> or orgs/<org>/validators/<KEY> is the deployment's
// own and opens over REST as any hand-kept value does. An app that sealed a
// credential there would hand it to every member again, and nothing above would
// notice — the subtree is in the set. So every spelling of a sync subtree in the
// fleet reaches at least two segments beneath it, a folder and then the key, or
// is named in folders with the test that pins what it seals.
//
// The four spellings are the sweep's, read to the end of the expression: for a
// concatenation, a format and a literal every '/' after the subtree is a segment
// boundary, and OrgPath names a folder, so the key it is handed is one more. The
// expression ends at the first ',', ')' or ';' — a shorter reading can only fail
// a ref that is deep enough, never pass one that is not.
var syncSpelling = func() *regexp.Regexp {
	var names []string
	for sub, h := range custodied {
		if h.sync {
			names = append(names, regexp.QuoteMeta(sub))
		}
	}
	slices.Sort(names)
	return regexp.MustCompile(`("orgs/"\s*\+\s*[A-Za-z_][\w.]*\s*\+\s*"/|"orgs/%s/|"orgs/[a-z0-9-]+/|OrgPath\(\s*[^,()"]+,\s*")(` +
		strings.Join(names, "|") + `)\b([^,);]*)`)
}()

// folders are the spellings of a sync subtree that name a FOLDER beneath its root
// rather than a key, keyed by file and line, and the test in the owning app that
// pins every ref sealed under it. A line that changes fails here until it is
// looked at again.
var folders = map[string]string{
	`apps/validator/validators.go: return "orgs/" + org + "/validators/" + strconv.FormatUint(tokenID, 10)`: "a slot's folder: keygen.go seals <folder>/<artifact>, " +
		"held to custody by apps/validator TestEveryStakingArtifactIsCustodied",
}

func TestNoAppSealsAtASyncSubtreesRoot(t *testing.T) {
	seen := map[string]bool{}
	var shallow []string
	for path, src := range fleetSources(t) {
		for _, line := range strings.Split(src, "\n") {
			for _, m := range syncSpelling.FindAllStringSubmatch(line, -1) {
				depth := strings.Count(m[3], "/")
				if strings.HasPrefix(m[1], "OrgPath") {
					depth++
				}
				if depth >= 2 {
					continue
				}
				at := path + ": " + strings.TrimSpace(line)
				if _, ok := folders[at]; ok {
					seen[at] = true
					continue
				}
				shallow = append(shallow, at)
			}
		}
	}
	slices.Sort(shallow)
	if len(shallow) > 0 {
		t.Fatalf("a ref names a key at a sync subtree's root, which is not custodied: seal it beneath a folder "+
			"(orgs/<org>/<subtree>/<folder>/<key>), or name the folder in folders with the test that pins its refs:\n  %s",
			strings.Join(shallow, "\n  "))
	}
	for at := range folders {
		if !seen[at] {
			t.Errorf("folders names %q, which the sweep no longer finds; drop it or correct it", at)
		}
	}
}

// The depth reading itself, on the shapes it has to tell apart.
func TestSyncSpellingDepth(t *testing.T) {
	for line, want := range map[string]int{
		`return "orgs/" + org + "/platform/" + appSlug + "/" + key`:               2,
		`return "orgs/" + org + "/validators/" + strconv.FormatUint(tokenID, 10)`: 1,
		`ref := "orgs/" + org + "/platform/" + key`:                               1,
		`kms.PutSecret(ctx, "orgs/"+org+"/platform/"+app+"/"+k, v)`:               2,
		`fmt.Sprintf("orgs/%s/platform/%s", org, key)`:                            1,
		`fmt.Sprintf("orgs/%s/validators/%s/%s", org, id, key)`:                   2,
		`"orgs/acme/platform/ROOT_KEY"`:                                           1,
		`OrgPath(org, "platform")`:                                                1,
		`OrgPath(org, "platform/"+app)`:                                           2,
	} {
		m := syncSpelling.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s: not read as a sync subtree's spelling", line)
			continue
		}
		depth := strings.Count(m[3], "/")
		if strings.HasPrefix(m[1], "OrgPath") {
			depth++
		}
		if depth != want {
			t.Errorf("%s: depth %d, want %d", line, depth, want)
		}
	}
	if syncSpelling.MatchString(`"orgs/" + org + "/platforms/" + key`) {
		t.Error("a subtree that merely begins with a sync subtree's name was read as one")
	}
}
