package forge

// github.go is which host a repository lives on, when it is not only here.
//
// Most of the estate sits on the forge as PULL MIRRORS of GitHub, and a mirror
// is a cache: it lags its origin, nothing pushed to it reaches there, and an
// issue filed on it is filed where nobody looks. So a repository's HOME is read
// from the forge's own word on it, and every write that must land where people
// work — a branch, a pull request, an issue — goes to the home. Two surfaces ask
// (a coding run and the board), and this is the one answer both read.

import (
	"net/url"
	"regexp"
	"strings"
)

// ghName is GitHub's shape for an account or repository name.
var ghName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Home is the GitHub repository r mirrors, when it mirrors one.
//
// The forge's metadata decides and nothing else: a pull mirror of github.com,
// or an advance-only copy apps/sync imported from github.com (where the
// upstream account and repository are joined at the first `_` in r.Name). A
// repository that is neither is its own home.
func (r Repo) Home() (owner, name string, ok bool) {
	if !r.Mirror {
		if !strings.EqualFold(strings.TrimSpace(r.Description), "synced from github.com") {
			return "", "", false
		}
		owner, name, ok = strings.Cut(strings.TrimSpace(r.Name), "_")
		if !ok || !ghName.MatchString(owner) || !ghName.MatchString(name) {
			return "", "", false
		}
		return owner, name, true
	}
	u, err := url.Parse(strings.TrimSpace(r.Origin))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, name = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !ghName.MatchString(owner) || !ghName.MatchString(name) {
		return "", "", false
	}
	return owner, name, true
}

// GitHub reads a reference that names a repository on GitHub itself — one the
// forge holds no copy of, so no forge ACL can answer for it: GitHub's own
// address (`github.com/owner/name`, with or without a scheme), or a bare
// `owner/name` that is not in the org's forge namespace — whether its owner is
// another account, or the org has no namespace on the forge at all, which is
// every customer whose GitHub org shares their Hanzo org's name and who has no
// workspace yet. Everything else is the forge's.
//
// ns is the org's namespace on this forge — [Owner]'s for the estate, the
// [Workspace] the platform made for anyone else — and empty when it has none.
func GitHub(ns, org, ref string) (owner, name string, ok bool) {
	s := strings.TrimSpace(ref)
	for _, scheme := range []string{"https://", "http://"} {
		if len(s) > len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			s = s[len(scheme):]
			break
		}
	}
	parts := strings.Split(strings.TrimSuffix(strings.Trim(s, "/"), ".git"), "/")
	switch {
	case len(parts) == 3 && (strings.EqualFold(parts[0], "github.com") || strings.EqualFold(parts[0], "www.github.com")):
		owner, name = parts[1], parts[2]
	case len(parts) == 2 && !namespaced(ns, org, parts[0]):
		owner, name = parts[0], parts[1]
	default:
		return "", "", false
	}
	if !ghName.MatchString(owner) || !ghName.MatchString(name) {
		return "", "", false
	}
	return owner, name, true
}

// namespaced reports whether owner is the org's own namespace on this forge, as
// a person spells it: the namespace itself, or the org's name. An org with no
// namespace has none.
func namespaced(ns, org, owner string) bool {
	if ns == "" {
		return false
	}
	return strings.EqualFold(owner, ns) || strings.EqualFold(owner, org)
}
