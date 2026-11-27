package git

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
)

// collect_rules.go — which package a repository's root manifest names, and
// which packages belong to the Hanzo estate (#456). Pure: no request, no store.

var (
	npmName    = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	pypiName   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	cratesName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
)

// estateGitHubOrgs is the canonical list of public GitHub organizations in the
// Hanzo, Lux, Zoo, Zen, Pars and Bootnode estate.
var estateGitHubOrgs = []string{
	"hanzoai",
	"hanzo-js",
	"hanzo-apps",
	"hanzo-community",
	"hanzoui",
	"hanzoskill",
	"hanzokms",
	"hanzozt",
	"luxfi",
	"luxdefi",
	"zooai",
	"zoolabs",
	"zenlm",
	"parsnetwork",
	"bootnode",
}

// estateOrgSet is every lowercased GitHub org recognized as owned by the estate,
// including private/umbrella orgs that packages may cite in repository metadata.
var estateOrgSet = map[string]bool{
	"hanzoai":         true,
	"hanzo-js":        true,
	"hanzo-apps":      true,
	"hanzo-community": true,
	"hanzo-inc":       true,
	"hanzoui":         true,
	"hanzogui":        true,
	"hanzoskill":      true,
	"hanzokms":        true,
	"hanzozt":         true,
	"luxfi":           true,
	"luxdefi":         true,
	"zooai":           true,
	"zoolabs":         true,
	"zenlm":           true,
	"parsnetwork":     true,
	"bootnode":        true,
}

// estateNpmScopes are the npm scopes owned by the estate (lowercased, with @).
var estateNpmScopes = map[string]bool{
	"@hanzo":       true,
	"@hanzogui":    true,
	"@hanzoai":     true,
	"@hanzo-js":    true,
	"@hanzo-apps":  true,
	"@hanzoui":     true,
	"@hanzoskill":  true,
	"@hanzokms":    true,
	"@hanzozt":     true,
	"@luxfi":       true,
	"@luxdefi":     true,
	"@zooai":       true,
	"@zoolabs":     true,
	"@zenlm":       true,
	"@parsnetwork": true,
	"@bootnode":    true,
}

// estateNpmSearchScopes are the npm scopes queried directly against the npm
// registry search API so monorepo workspace packages are always discovered.
var estateNpmSearchScopes = []string{
	"hanzo",
	"hanzogui",
	"hanzoai",
	"luxfi",
	"luxdefi",
	"zooai",
	"zoolabs",
	"zenlm",
	"parsnetwork",
	"bootnode",
}

// estateCratesSearchPrefixes are the prefixes queried against crates.io search.
var estateCratesSearchPrefixes = []string{
	"hanzo",
	"luxfi",
	"lux-",
	"zen-",
	"zenlm",
	"zooai",
}

// estatePyPISeeds are well-known Hanzo estate PyPI package names probed during
// discovery so monorepo Python packages without root manifests are collected.
var estatePyPISeeds = []string{
	"hanzo",
	"hanzoai",
	"hanzo-ai",
	"hanzo-mcp",
	"hanzo-sdk",
	"hanzo-cli",
	"hanzo-dev",
	"hanzo-agents",
	"hanzo-tools",
	"hanzo-memory",
	"hanzo-network",
	"hanzo-aci",
	"hanzo-sandbox",
	"hanzo-flow",
	"hanzo-ml",
	"hanzo-chat",
	"zenlm",
	"zen-sdk",
	"luxfi",
	"lux-sdk",
	"zooai",
	"zoolabs",
}

// estatePackagePrefixes are unscoped package name prefixes (after normalizing
// '_' and '.' to '-') that belong to the Hanzo estate.
var estatePackagePrefixes = []string{
	"hanzo",
	"luxfi",
	"luxdefi",
	"lux-",
	"zooai",
	"zoolabs",
	"zenlm",
	"zen-",
	"pars-",
	"parsnetwork",
	"bootnode",
}

// isEstateOrg reports whether org is a Hanzo estate GitHub organization.
func isEstateOrg(org string) bool {
	return estateOrgSet[strings.ToLower(strings.TrimSpace(org))]
}

// hasEstatePrefix reports whether an unscoped package name starts with a
// recognized Hanzo estate prefix.
func hasEstatePrefix(name string) bool {
	norm := strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(strings.TrimSpace(name)))
	if norm == "" {
		return false
	}
	for _, pfx := range estatePackagePrefixes {
		if strings.HasPrefix(norm, pfx) {
			return true
		}
	}
	return false
}

// isEstateScopedPackage reports whether name is an npm scoped package under an
// estate scope (e.g. @hanzo/ui, @hanzogui/core, @luxfi/sdk).
func isEstateScopedPackage(eco, name string) bool {
	if eco != "" && eco != "npm" {
		return false
	}
	n := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(n, "@") {
		return false
	}
	scope, pkg, ok := strings.Cut(n, "/")
	return ok && pkg != "" && estateNpmScopes[scope]
}

// isEstatePackageName reports whether a package's scope or prefix belongs to
// the Hanzo estate.
func isEstatePackageName(eco, name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if eco == "dockerhub" {
		ns, _, _ := strings.Cut(n, "/")
		return isEstateOrg(ns) || hasEstatePrefix(ns)
	}
	if strings.HasPrefix(n, "@") {
		return isEstateScopedPackage(eco, n)
	}
	return hasEstatePrefix(n)
}

// extractGitHubOwner extracts owner and repo from any GitHub repository URL
// spelling commonly found in package manifests and registry metadata:
// https/http/ssh/git/git+https/git+ssh URLs (including monorepo subpaths),
// scp-style git@github.com:owner/repo.git, github:owner/repo, or
// github.com/owner/repo.
func extractGitHubOwner(raw string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "git+")
	var host, path string
	switch {
	case strings.HasPrefix(strings.ToLower(s), "github:"):
		host = "github.com"
		path = s[len("github:"):]
	case strings.HasPrefix(s, "git@github.com:"):
		host = "github.com"
		path = strings.TrimPrefix(s, "git@github.com:")
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", "", false
		}
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git" {
			return "", "", false
		}
		host, path = u.Host, u.Path
	default:
		if strings.Contains(s, "@") {
			return "", "", false
		}
		host, path, _ = strings.Cut(s, "/")
	}
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if host != "github.com" {
		return "", "", false
	}
	// Strip query or fragment if attached to raw path.
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	o := parts[0]
	r := strings.TrimSuffix(parts[1], ".git")
	if !repoSegment.MatchString(o) || !repoSegment.MatchString(r) {
		return "", "", false
	}
	return o, r, true
}

// isEstateRepoURL reports whether raw points to a GitHub repository owned by a
// Hanzo estate organization.
func isEstateRepoURL(raw string) bool {
	owner, _, ok := extractGitHubOwner(raw)
	return ok && isEstateOrg(owner)
}

// isNonEstateRepoURL reports whether raw explicitly points to a repository on a
// third-party GitHub org or another git host (GitLab, Bitbucket, Codeberg).
func isNonEstateRepoURL(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if owner, _, ok := extractGitHubOwner(s); ok {
		return !isEstateOrg(owner)
	}
	s = strings.TrimPrefix(s, "git+")
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return false
		}
		host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		switch host {
		case "gitlab.com", "bitbucket.org", "codeberg.org", "sr.ht", "git.sr.ht":
			return true
		}
	}
	return false
}

// estateUpstreamFromURL normalizes raw into "github.com/<owner>/<repo>" when
// raw points to a Hanzo estate GitHub repository.
func estateUpstreamFromURL(raw string) string {
	owner, repo, ok := extractGitHubOwner(raw)
	if !ok || !isEstateOrg(owner) {
		return ""
	}
	return "github.com/" + strings.ToLower(owner) + "/" + strings.ToLower(repo)
}

// isOwnedPackage reports whether a package should be attributed to the Hanzo
// estate. A package is owned when:
//   - its repository URL explicitly points to a Hanzo estate GitHub org, OR
//   - it has an explicit Hanzo estate npm scope (@hanzo/*, @hanzogui/*, etc.), OR
//   - its name starts with a Hanzo estate prefix AND its repository URL does not
//     explicitly point to a third-party GitHub org or external git host.
func isOwnedPackage(eco, name, repoURL string) bool {
	if isEstateRepoURL(repoURL) {
		return true
	}
	if isEstateScopedPackage(eco, name) {
		return true
	}
	if isEstatePackageName(eco, name) {
		return !isNonEstateRepoURL(repoURL)
	}
	return false
}

// estateOrgForPackage returns the lowercased org attribution for a package: its
// GitHub upstream owner when known, otherwise the canonical estate org implied
// by its scope or prefix.
func estateOrgForPackage(eco, name, upstream string) string {
	if o := upstreamOwner(upstream); o != "" {
		return o
	}
	n := strings.ToLower(strings.TrimSpace(name))
	if eco == "dockerhub" {
		ns, _, _ := strings.Cut(n, "/")
		return ns
	}
	if strings.HasPrefix(n, "@") {
		scope, _, _ := strings.Cut(n, "/")
		switch scope {
		case "@hanzo", "@hanzoai":
			return "hanzoai"
		default:
			return strings.TrimPrefix(scope, "@")
		}
	}
	norm := strings.NewReplacer("_", "-", ".", "-").Replace(n)
	switch {
	case strings.HasPrefix(norm, "hanzogui"):
		return "hanzogui"
	case strings.HasPrefix(norm, "hanzoui"):
		return "hanzoui"
	case strings.HasPrefix(norm, "hanzoskill"):
		return "hanzoskill"
	case strings.HasPrefix(norm, "hanzokms"):
		return "hanzokms"
	case strings.HasPrefix(norm, "hanzozt"):
		return "hanzozt"
	case strings.HasPrefix(norm, "hanzo"):
		return "hanzoai"
	case strings.HasPrefix(norm, "luxdefi"):
		return "luxdefi"
	case strings.HasPrefix(norm, "luxfi"), strings.HasPrefix(norm, "lux-"):
		return "luxfi"
	case strings.HasPrefix(norm, "zoolabs"):
		return "zoolabs"
	case strings.HasPrefix(norm, "zooai"):
		return "zooai"
	case strings.HasPrefix(norm, "zenlm"), strings.HasPrefix(norm, "zen-"):
		return "zenlm"
	case strings.HasPrefix(norm, "parsnetwork"), strings.HasPrefix(norm, "pars-"):
		return "parsnetwork"
	case strings.HasPrefix(norm, "bootnode"):
		return "bootnode"
	}
	return ""
}

// npmRepoField decodes package.json / npm registry "repository", which may be
// either a string ("owner/repo", "https://github.com/...") or {"url": "..."}.
type npmRepoField struct {
	URL string
}

func (r *npmRepoField) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		r.URL = normalizeNpmRepoShorthand(s)
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &obj) == nil {
		r.URL = normalizeNpmRepoShorthand(obj.URL)
		return nil
	}
	return nil
}

// normalizeNpmRepoShorthand expands npm's "owner/repo" shorthand to a GitHub
// URL so extractGitHubOwner and isNonEstateRepoURL recognize third-party forks.
func normalizeNpmRepoShorthand(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.Contains(s, "://") || strings.Contains(s, ":") || strings.Contains(s, "@") {
		return s
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) == 2 && repoSegment.MatchString(parts[0]) && repoSegment.MatchString(parts[1]) {
		return "https://github.com/" + parts[0] + "/" + parts[1]
	}
	return s
}

// manifestPackages are the Hanzo-estate registry packages the manifests at a
// repository's root name, as {ecosystem, name} refs with no upstream:
// package.json on npm, pyproject.toml and setup.cfg on PyPI, Cargo.toml on
// crates. Third-party packages (e.g. forked repositories like postcss, cookie,
// express), private npm packages, unpublished crates and malformed names are
// refused.
func manifestPackages(files []client.GitHubFile) []packageRef {
	var out []packageRef
	add := func(eco, name, repoURL string) {
		if !isOwnedPackage(eco, name, repoURL) {
			return
		}
		if !slices.ContainsFunc(out, func(p packageRef) bool { return p.Ecosystem == eco && p.Name == name }) {
			out = append(out, packageRef{Ecosystem: eco, Name: name})
		}
	}
	for _, f := range files {
		switch f.Path {
		case "package.json":
			var p struct {
				Name       string       `json:"name"`
				Private    bool         `json:"private"`
				Repository npmRepoField `json:"repository"`
			}
			if json.Unmarshal([]byte(f.Text), &p) == nil && !p.Private && len(p.Name) <= maxNameLen && npmName.MatchString(p.Name) {
				add("npm", p.Name, p.Repository.URL)
			}
		case "pyproject.toml":
			name := tomlString(f.Text, "project", "name")
			if name == "" {
				name = tomlString(f.Text, "tool.poetry", "name")
			}
			if pypiName.MatchString(name) {
				repoURL := pyprojectRepoURL(f.Text)
				add("pypi", name, repoURL)
			}
		case "setup.cfg":
			if name := iniString(f.Text, "metadata", "name"); pypiName.MatchString(name) {
				repoURL := iniString(f.Text, "metadata", "url")
				add("pypi", name, repoURL)
			}
		case "Cargo.toml":
			if tomlBool(f.Text, "package", "publish") != "false" {
				if name := tomlString(f.Text, "package", "name"); cratesName.MatchString(name) {
					repoURL := tomlString(f.Text, "package", "repository")
					if repoURL == "" {
						repoURL = tomlString(f.Text, "package", "homepage")
					}
					add("crates", name, repoURL)
				}
			}
		}
	}
	return out
}

// pyprojectRepoURL extracts a repository URL from [project.urls] or
// [tool.poetry] in pyproject.toml, preferring repository/source links.
func pyprojectRepoURL(text string) string {
	for _, k := range []string{"Repository", "repository", "Source", "source", "GitHub", "github", "Homepage", "homepage"} {
		if v := tomlString(text, "project.urls", k); v != "" {
			if _, _, ok := extractGitHubOwner(v); ok {
				return v
			}
		}
	}
	for _, k := range []string{"repository", "homepage"} {
		if v := tomlString(text, "tool.poetry", k); v != "" {
			return v
		}
	}
	return ""
}

// tomlValue is the raw text after `key =` in `[section]`, comments and
// surrounding space trimmed off the unquoted forms, or "" when the table or key
// is absent. It reads the one shape a manifest names itself in — a key on its
// own line — and nothing else of TOML.
func tomlValue(text, section, key string) string {
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "[["):
			in = false
		case strings.HasPrefix(line, "["):
			head, _, _ := strings.Cut(strings.TrimPrefix(line, "["), "]")
			in = strings.ReplaceAll(strings.TrimSpace(head), " ", "") == section
		case in:
			k, v, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(k) == key {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// tomlString is tomlValue for a quoted string, "" when it is not one.
func tomlString(text, section, key string) string {
	v := tomlValue(text, section, key)
	if len(v) < 2 || (v[0] != '"' && v[0] != '\'') {
		return ""
	}
	end := strings.IndexByte(v[1:], v[0])
	if end < 0 {
		return ""
	}
	return v[1 : 1+end]
}

// tomlBool is tomlValue for a bare word, comment cut off.
func tomlBool(text, section, key string) string {
	v, _, _ := strings.Cut(tomlValue(text, section, key), "#")
	return strings.TrimSpace(v)
}

// iniString is the value of `key = value` in `[section]` of an INI file.
func iniString(text, section, key string) string {
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			head, _, _ := strings.Cut(strings.TrimPrefix(line, "["), "]")
			in = strings.TrimSpace(head) == section
		case in:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				k, v, ok = strings.Cut(line, ":")
			}
			if ok && strings.TrimSpace(k) == key {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
