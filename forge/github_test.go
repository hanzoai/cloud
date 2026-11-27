package forge

// github_test.go pins where a repository lives when it is not only here: the
// home a mirror names, and the references that name GitHub itself.

import "testing"

func TestHome(t *testing.T) {
	for name, c := range map[string]struct {
		repo        Repo
		owner, name string
		ok          bool
	}{
		"a mirror of github is homed there": {Repo{Mirror: true, Origin: "https://github.com/hanzo-apps/hanzo.ai"}, "hanzo-apps", "hanzo.ai", true},
		"a synced github copy is homed":     {Repo{Name: "valerypetrov_clickhouse", Description: "synced from github.com"}, "valerypetrov", "clickhouse", true},
		"a synced gitlab copy is not":       {Repo{Name: "acme_api", Description: "synced from gitlab.com"}, "", "", false},
		"a synced copy without account":     {Repo{Name: "_api", Description: "synced from github.com"}, "", "", false},
		"a .git suffix is not the name":     {Repo{Mirror: true, Origin: "https://github.com/hanzo-apps/site.git"}, "hanzo-apps", "site", true},
		"the live forge's own answer":       {Repo{Mirror: true, Origin: "https://github.com/hanzoai/hanzo.ai.git"}, "hanzoai", "hanzo.ai", true},
		"the host is matched without case":  {Repo{Mirror: true, Origin: "https://GitHub.com/acme/api/"}, "acme", "api", true},
		"a credential is not an address":    {Repo{Mirror: true, Origin: "https://x:y@github.com/acme/api"}, "acme", "api", true},
		"a forge-native repo is its own":    {Repo{Origin: "https://github.com/acme/api"}, "", "", false},
		"a mirror naming no origin":         {Repo{Mirror: true}, "", "", false},
		"a mirror of another host":          {Repo{Mirror: true, Origin: "https://gitlab.com/acme/api"}, "", "", false},
		"a lookalike host":                  {Repo{Mirror: true, Origin: "https://github.com.evil.example/acme/api"}, "", "", false},
		"a path that is not owner/name":     {Repo{Mirror: true, Origin: "https://github.com/acme/api/tree/main"}, "", "", false},
		"an scp-style address":              {Repo{Mirror: true, Origin: "git@github.com:acme/api.git"}, "", "", false},
		"a name that is not a name":         {Repo{Mirror: true, Origin: "https://github.com/-acme/api"}, "", "", false},
	} {
		o, n, ok := c.repo.Home()
		if ok != c.ok || o != c.owner || n != c.name {
			t.Errorf("%s: Home = (%q, %q, %v), want (%q, %q, %v)", name, o, n, ok, c.owner, c.name, c.ok)
		}
	}
}

func TestGitHub(t *testing.T) {
	for _, c := range []struct {
		ns, org, ref string
		owner, name  string
		ok           bool
	}{
		{"hanzoai", "hanzo", "github.com/hanzo-apps/site", "hanzo-apps", "site", true},
		{"hanzoai", "hanzo", "https://github.com/hanzoai/cloud.git", "hanzoai", "cloud", true},
		{"hanzoai", "hanzo", "HTTPS://WWW.GitHub.com/octocat/hello/", "octocat", "hello", true},
		{"hanzoai", "hanzo", "octocat/hello", "octocat", "hello", true},
		// The org's forge namespace is the forge's, however it is spelled.
		{"hanzoai", "hanzo", "hanzoai/cloud", "", "", false},
		{"hanzoai", "hanzo", "hanzo/cloud", "", "", false},
		{"hanzoai", "hanzo", "cloud", "", "", false},
		{"hanzoai", "hanzo", "hanzo.ai", "", "", false},
		{"hanzoai", "hanzo", "https://git.hanzo.ai/hanzoai/cloud", "", "", false},
		// An org with no forge namespace has nothing but GitHub, even under its own name.
		{"", "acme", "acme/widgets", "acme", "widgets", true},
		// An org whose workspace the platform made reads it the same way the estate
		// reads its own: by the namespace's name or the org's, and nothing else.
		{"acme-2", "acme", "acme-2/widgets", "", "", false},
		{"acme-2", "acme", "acme/widgets", "", "", false},
		{"acme-2", "acme", "github.com/acme/widgets", "acme", "widgets", true},
		{"acme-2", "acme", "hanzoai/cloud", "hanzoai", "cloud", true},
		// Another host is never GitHub, and a name GitHub would refuse is nothing.
		{"hanzoai", "hanzo", "gitlab.com/acme/api", "", "", false},
		{"hanzoai", "hanzo", "github.com.evil.example/acme/api", "", "", false},
		{"hanzoai", "hanzo", "github.com/-acme/api", "", "", false},
		{"hanzoai", "hanzo", "github.com/acme/api/tree/main", "", "", false},
		{"hanzoai", "hanzo", "github.com/acme/..", "", "", false},
	} {
		o, n, ok := GitHub(c.ns, c.org, c.ref)
		if ok != c.ok || o != c.owner || n != c.name {
			t.Errorf("GitHub(%q, %q, %q) = (%q, %q, %v), want (%q, %q, %v)", c.ns, c.org, c.ref, o, n, ok, c.owner, c.name, c.ok)
		}
	}
}
