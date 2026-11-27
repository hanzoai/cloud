package remote

import "strings"

// Provider names the forge a clone URL points at — github, gitlab, bitbucket, or
// `git` for anything else. Empty for an empty URL: no URL is not "some other
// forge". It is a LABEL for display, and it is asked here rather than derived per
// caller so one repository URL never carries two names across two screens.
func Provider(raw string) string {
	r := strings.ToLower(raw)
	switch {
	case r == "":
		return ""
	case strings.Contains(r, "github.com"):
		return "github"
	case strings.Contains(r, "gitlab"):
		return "gitlab"
	case strings.Contains(r, "bitbucket"):
		return "bitbucket"
	default:
		return "git"
	}
}

// Clean returns a clone URL with its credential removed.
//
// A credential rides in a URL's userinfo (RFC 3986 §3.2.1). Over http(s) all of
// it is secret — GitHub's "x-access-token:<token>@", GitLab's "oauth2:<token>@",
// a bare token standing as the user — so all of it goes. Over any other scheme
// the user names an account (ssh://git@host) and stays; only a password goes. A
// scp-style remote (git@host:owner/repo) keeps its user by the same rule.
//
// Access to a private repository belongs to the org's provider link, which mints
// short-lived tokens on demand; a stored or echoed URL never carries one.
func Clean(raw string) string {
	s := strings.TrimSpace(raw)
	i := strings.Index(s, "://")
	if i < 0 {
		at := strings.LastIndexByte(s, '@')
		if at < 0 {
			return s
		}
		user, _, secret := strings.Cut(s[:at], ":")
		if !secret {
			return s
		}
		return user + s[at:]
	}
	scheme, rest := s[:i+3], s[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	at := strings.LastIndexByte(rest[:end], '@')
	if at < 0 {
		return s
	}
	switch strings.ToLower(scheme) {
	case "http://", "https://":
		return scheme + rest[at+1:]
	}
	user, _, _ := strings.Cut(rest[:at], ":")
	return scheme + user + rest[at:]
}

// Host names the host a clone URL points at, lowercased and without a port or
// a user: `github.com` for https://github.com/zooai/ngo and for
// git@github.com:zooai/ngo.git alike. Empty for a URL with no host.
func Host(raw string) string {
	s := Clean(raw)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexAny(s, "/?#"); j >= 0 {
			s = s[:j]
		}
	} else if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	} else {
		return ""
	}
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		s = s[at+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// Owner names the account a clone URL's repository lives under — the first path
// segment, lowercased: `zooai` for https://github.com/zooai/ngo.git and for
// git@github.com:zooai/ngo.git alike. Empty when the URL names no account and
// repository, so a host alone or a bare name owns nothing.
func Owner(raw string) string {
	s := Clean(raw)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		j := strings.IndexByte(s, '/')
		if j < 0 {
			return ""
		}
		s = s[j+1:]
	} else if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:]
	} else {
		return ""
	}
	owner, repo, ok := strings.Cut(strings.Trim(s, "/"), "/")
	if !ok || owner == "" || repo == "" {
		return ""
	}
	return strings.ToLower(owner)
}
