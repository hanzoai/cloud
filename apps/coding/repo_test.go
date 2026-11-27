package coding

// repo_test.go — a reference becomes one of the org's repositories, or a refusal
// that names the nearest ones. Never a repository nobody has.

import (
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/forge"
)

// Every way a person or a model writes a repository reads as one, and nothing
// that could carry a path, a `..` or a flag reads as anything.
func TestAReferenceIsARepositoryOrNothing(t *testing.T) {
	for ref, want := range map[string][2]string{
		"hanzo.ai":                                {"", "hanzo.ai"},
		"https://hanzo.ai":                        {"", "hanzo.ai"},
		"HTTPS://hanzo.ai/":                       {"", "hanzo.ai"},
		"hanzoai/hanzo.ai":                        {"hanzoai", "hanzo.ai"},
		"hanzoai/cloud.git":                       {"hanzoai", "cloud"},
		"https://git.hanzo.ai/hanzoai/hanzo.ai":   {"hanzoai", "hanzo.ai"},
		"  https://git.hanzo.ai/hanzoai/cloud/  ": {"hanzoai", "cloud"},
	} {
		owner, name, err := reference(ref)
		if err != nil || owner != want[0] || name != want[1] {
			t.Errorf("reference(%q) = %q, %q, %v; want %q, %q", ref, owner, name, err, want[0], want[1])
		}
	}
	for _, ref := range []string{"", "../other/api", "a/../b", "-upload-pack", "a b", "git@git.hanzo.ai:hanzoai/x", "hanzoai//x"} {
		if _, _, err := reference(ref); err == nil {
			t.Errorf("reference(%q) was accepted; it can carry more than a name", ref)
		}
	}
}

// An owner is this org's namespace or the org itself, and nothing else.
func TestOnlyTheOrgsOwnNamespaceIsOurs(t *testing.T) {
	owner, err := forge.Owner("hanzo")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"", owner, strings.ToUpper(owner), "hanzo"} {
		if !ours(estate("hanzo"), "hanzo", o) {
			t.Errorf("%q is hanzo's own namespace and was refused", o)
		}
	}
	for _, o := range []string{"hanzo-apps", "lux", "acme"} {
		if ours(estate("hanzo"), "hanzo", o) {
			t.Errorf("%q is not hanzo's namespace and was accepted", o)
		}
	}
	if ours(forge.Workspace{}, "acme", "anything") {
		t.Error("an org with no namespace on the forge owns every name")
	}
	// A workspace the platform made is the org's by its own name and the
	// namespace's, and by nothing else — the estate's least of all.
	made := forge.Workspace{Owner: "acme-2", Made: true}
	for _, o := range []string{"", "acme", "acme-2", "ACME-2"} {
		if !ours(made, "acme", o) {
			t.Errorf("%q is acme's own workspace and was refused", o)
		}
	}
	for _, o := range []string{"hanzoai", "acme-3", "hanzo"} {
		if ours(made, "acme", o) {
			t.Errorf("%q is not acme's workspace and was accepted", o)
		}
	}
}

// The name wins; else the one repository serving the site; else nothing, and the
// nearest names — the same list for the same question.
func TestPickFindsTheRepositoryARequestMeans(t *testing.T) {
	repos := []forge.Repo{
		{Name: "cloud"},
		{Name: "Hanzo.ai"},
		{Name: "hanzo.ai-docs", Website: "https://docs.hanzo.ai"},
		{Name: "site", Website: "https://www.zoo.ngo/"},
		{Name: "lux-web", Website: "https://lux.network"},
		{Name: "lux-app", Website: "http://lux.network/app"},
	}
	for want, tc := range map[string]struct {
		got  string
		near []string
	}{
		"hanzo.ai":        {"Hanzo.ai", nil},
		"CLOUD":           {"cloud", nil},
		"zoo.ngo":         {"site", nil},
		"docs.hanzo.ai":   {"hanzo.ai-docs", nil},
		"lux.network":     {"", []string{"lux-app", "lux-web"}},
		"hanzo":           {"", []string{"Hanzo.ai", "hanzo.ai-docs"}},
		"nothing-like-it": {"", nil},
	} {
		got, near := pick(want, repos)
		if got != tc.got || strings.Join(near, ",") != strings.Join(tc.near, ",") {
			t.Errorf("pick(%q) = %q %v; want %q %v", want, got, near, tc.got, tc.near)
		}
	}
}

// Imported GitHub repositories in a platform-made workspace are mirrored as
// `<owner>_<repo>` while projects carry a hyphenated slug (`<owner>-<repo>`)
// and surfaces may send only the repo basename (`Sroute`). pickFor resolves
// unambiguous mirrors and project-tagged matches without guessing across
// ambiguous duplicates or estate workspaces.
func TestPickForResolvesMirroredProjectRepos(t *testing.T) {
	single := []forge.Repo{{Name: "cssprod_sroute"}}
	multi := []forge.Repo{{Name: "cssprod_sroute"}, {Name: "other_sroute"}}
	hyphenMulti := []forge.Repo{{Name: "my-org_my-app"}, {Name: "other-org_my-app"}}

	for _, tc := range []struct {
		desc    string
		name    string
		project string
		made    bool
		repos   []forge.Repo
		got     string
		near    []string
	}{
		{"project slug + basename resolves mirror", "Sroute", "cssprod-sroute", true, single, "cssprod_sroute", nil},
		{"unique mirror in made workspace resolves basename", "Sroute", "", true, single, "cssprod_sroute", nil},
		{"project slug disambiguates two mirrors", "Sroute", "cssprod-sroute", true, multi, "cssprod_sroute", nil},
		{"multi-hyphen owner/repo disambiguates by project slug", "my-app", "my-org-my-app", true, hyphenMulti, "my-org_my-app", nil},
		{"ambiguous mirrors without project refuse with both", "Sroute", "", true, multi, "", []string{"cssprod_sroute", "other_sroute"}},
		{"mismatched repo name does not hijack project repo", "unrelated", "cssprod-sroute", true, single, "", nil},
		{"estate workspace does not suffix-match without project", "Sroute", "", false, single, "", []string{"cssprod_sroute"}},
	} {
		got, near := pickFor(tc.name, tc.project, tc.made, tc.repos)
		if got != tc.got || strings.Join(near, ",") != strings.Join(tc.near, ",") {
			t.Errorf("%s: pickFor(%q, %q, %v) = %q %v; want %q %v",
				tc.desc, tc.name, tc.project, tc.made, got, near, tc.got, tc.near)
		}
	}
}

// A NAME WHOSE CODE IS ON GITHUB means the private edition there, however the
// org's namespace is spelled, and never the forge's copy; the open-source
// edition is reached by its own address. Other names, and other orgs, are the
// forge's as before.
func TestHomeIsThePrivateEdition(t *testing.T) {
	for _, c := range []struct {
		org, ref, want string
	}{
		{"hanzo", "cloud", "hanzo-inc/cloud"},
		{"hanzo", "Cloud", "hanzo-inc/cloud"},
		{"hanzo", "cloud.Git", "hanzo-inc/cloud"},
		{"hanzo", "hanzo/cloud", "hanzo-inc/cloud"},
		{"hanzo", "https://api.hanzo.ai/v1/git/hanzo/cloud", "hanzo-inc/cloud"},
		{"hanzo", "universe", "hanzo-inc/universe"},
		{"hanzo", "hanzoai/cloud", ""},
		{"hanzo", "https://git.hanzo.ai/hanzoai/cloud", ""},
		{"hanzo", "Hanzo/cloud", "hanzo-inc/cloud"},
		{"hanzo", "HANZO/cloud", "hanzo-inc/cloud"},
		{"hanzo", "docs", ""},
		{"acme", "cloud", ""},
	} {
		owner, name, err := reference(c.ref)
		if err != nil {
			t.Fatalf("reference(%q): %v", c.ref, err)
		}
		o, n, ok := home(c.org, owner, name)
		if got := o + "/" + n; ok != (c.want != "") || ok && got != c.want {
			t.Fatalf("home for %q in %s = %q, %v; want %q", c.ref, c.org, got, ok, c.want)
		}
	}
}

// A PLACEHOLDER IN THE ESTATE IS REFUSED, naming the address that reaches real
// code; a project's one-README repository in a made workspace is not.
func TestVacantRefusesAPlaceholder(t *testing.T) {
	estate := forge.Workspace{Owner: "hanzoai"}
	for _, c := range []struct {
		name string
		ws   forge.Workspace
		r    forge.Repo
		bad  bool
	}{
		{"an early run's placeholder", estate, forge.Repo{Name: "cloud", Description: placeholder}, true},
		{"an empty repository", estate, forge.Repo{Name: "x", Empty: true}, true},
		{"a repository with code", estate, forge.Repo{Name: "mcp", Description: "Hanzo MCP"}, false},
		{"a made workspace's new project", forge.Workspace{Owner: "acme-1", Made: true}, forge.Repo{Name: "site", Description: placeholder}, false},
	} {
		err := vacant(c.ws, c.r)
		if (err != nil) != c.bad {
			t.Fatalf("%s: vacant = %v", c.name, err)
		}
		if err != nil && !errors.Is(err, errVacant) {
			t.Fatalf("%s: an empty repository must be refused as final (errVacant), never sent on to GitHub: %v", c.name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "github.com/<owner>/<name>") {
			t.Fatalf("%s: the refusal names no way to the code: %v", c.name, err)
		}
	}
}
