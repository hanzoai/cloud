// Package remote reads what can be known about a git remote from its URL alone.
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
