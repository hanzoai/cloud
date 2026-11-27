package triage

import (
	"slices"
	"strings"

	"github.com/hanzoai/cloud/apps/event"
)

// Config is everything an owner sets in admin. The zero value does nothing: no
// repository is routed, no class has a threshold, and the kill switch is off.
type Config struct {
	// Enabled is the kill switch. Off, faults are still counted and nothing is
	// filed, started or merged.
	Enabled bool `json:"enabled"`
	// Actor is the IAM subject every autonomous act is done and audited as.
	Actor string `json:"actor"`
	// Label is the issue label that hands an issue to a run.
	Label string `json:"label"`
	// Threshold is, per fault class (server, dependency, run, cron, client), how
	// many occurrences within how many minutes file an issue. Count 0 files none.
	Threshold map[string]Limit `json:"threshold"`
	// Runs bounds the runs themselves.
	Runs Runs `json:"runs"`
	// Repos routes a fault to the repository that owns it, first match wins.
	Repos []Repo `json:"repos"`
}

// Limit is a rate: Count occurrences within Minutes.
type Limit struct {
	Count   int `json:"count"`
	Minutes int `json:"minutes"`
}

// Runs bounds the autonomous runs.
type Runs struct {
	// Seconds is the budget of one run.
	Seconds int `json:"seconds"`
	// PerDay is how many runs one org's day may start.
	PerDay int `json:"perDay"`
	// Attempts is how many runs one issue may have, a regression included.
	Attempts int `json:"attempts"`
}

// Repo is one repository triage works on and what it may do there.
type Repo struct {
	// Org is the Hanzo org whose GitHub installation reaches the repository, and
	// the org whose faults route to it.
	Org string `json:"org"`
	// Repo is owner/name.
	Repo string `json:"repo"`
	// Match routes a fault here when any entry holds.
	Match []Match `json:"match"`
	// Auto starts a run for an issue of this repository; off, issues are filed and
	// left to people.
	Auto bool `json:"auto"`
	// Merge lets a green, low-risk pull request of a run merge itself.
	Merge bool `json:"merge"`
	// Concurrent is how many runs this repository may have at once.
	Concurrent int `json:"concurrent"`
	// Required are the checks that must read success on a pull request's head. None
	// named: every check on it must, and at least one must exist.
	Required []string `json:"required"`
}

// Match names a fault by one of its facts: its service, its product, or its
// capability (the first segment of its route after /v1/, or its class for a run
// or a job).
type Match struct {
	Service    string `json:"service,omitempty"`
	Product    string `json:"product,omitempty"`
	Capability string `json:"capability,omitempty"`
}

// route is the repository a fault belongs to.
func (c Config) route(f event.Fault) (Repo, bool) {
	for _, r := range c.Repos {
		if r.Org == f.Org && slices.ContainsFunc(r.Match, func(m Match) bool { return m.holds(f) }) {
			return r, true
		}
	}
	return Repo{}, false
}

// repo is the repository an issue is on, when triage works on it.
func (c Config) repo(org, name string) (Repo, bool) {
	i := slices.IndexFunc(c.Repos, func(r Repo) bool { return r.Org == org && strings.EqualFold(r.Repo, name) })
	if i < 0 {
		return Repo{}, false
	}
	return c.Repos[i], true
}

func (m Match) holds(f event.Fault) bool {
	switch {
	case m.Service != "":
		return m.Service == f.Service
	case m.Product != "":
		return m.Product == f.Product
	case m.Capability != "":
		return m.Capability == capability(f)
	}
	return false
}

// capability is what a fault is routed by besides its service: the product its
// route names, or its kind for a run or a job.
func capability(f event.Fault) string {
	if rest, ok := strings.CutPrefix(f.Path, "/v1/"); ok {
		rest = strings.TrimPrefix(rest, "admin/")
		name, _, _ := strings.Cut(rest, "/")
		return name
	}
	return f.Attributes["fault.class"]
}
