// Package ossfunding resolves an OSS package (by PURL) to a payout target —
// where the money for that package should go. It is the "who do we pay?" half
// of the payout system, orthogonal to the "how much?" half (ossattr).
//
// Resolution is best-effort and layered, cheapest/most-certain first:
//
//  1. PURL structure: most Go/npm PURLs encode a GitHub (or GitLab) owner/repo
//     in their name (pkg:golang/github.com/gin-gonic/gin -> github owner
//     "gin-gonic"). That owner is a strong default target — GitHub Sponsors
//     and a repo FUNDING.yml are keyed by owner/repo.
//  2. FUNDING.yml lookup (network): fetch the repo's .github/FUNDING.yml to get
//     the exact funding platforms the maintainer declared (github sponsors,
//     open collective, ko-fi, custom). This is the authoritative target.
//  3. Registry maintainers (network): for ecosystems without a VCS coordinate
//     in the PURL (some npm/pypi), the package registry's maintainer record.
//
// When nothing resolves, the result is Unresolved -> the accrual is HELD (its
// money waits in a held pool, never lost) until a later resolution pass.
//
// This package's pure core (FromPURL) is offline and deterministic — it derives
// the candidate target from the PURL alone. The network enrichment (FUNDING.yml
// / registry) is behind the Resolver interface so the accrual path never blocks
// on I/O and tests run without a network.
package ossfunding

import (
	"strings"
)

// Kind enumerates the supported funding rails (the money-out targets).
type Kind string

const (
	KindGitHubSponsors Kind = "github_sponsors"
	KindOpenCollective Kind = "opencollective"
	KindKoFi           Kind = "kofi"
	KindCustom         Kind = "custom" // a maintainer-declared URL
	KindUnresolved     Kind = "unresolved"
)

// Target is a resolved payout destination for a package.
type Target struct {
	Kind Kind `json:"kind"`
	// Handle is the rail-specific identifier:
	//   github_sponsors -> the sponsorable login (user/org), e.g. "gin-gonic"
	//   opencollective  -> the collective slug
	//   kofi            -> the ko-fi handle
	//   custom          -> the full URL
	Handle string `json:"handle"`
	// Repo is the owner/repo the target was derived from, for audit.
	Repo string `json:"repo,omitempty"`
	// Source records how this was resolved: "purl" | "funding.yml" | "registry".
	Source string `json:"source"`
}

// Unresolved is the held-pool sentinel.
func Unresolved() Target { return Target{Kind: KindUnresolved} }

// String renders a target as "kind:handle" — the snapshot stored on accrual
// lines (OSSAccrual.FundingTarget).
func (t Target) String() string {
	if t.Kind == KindUnresolved || t.Handle == "" {
		return string(KindUnresolved)
	}
	return string(t.Kind) + ":" + t.Handle
}

// FromPURL is the pure, offline resolver: it derives a candidate funding target
// from the PURL's embedded VCS coordinate. For PURLs whose name is a
// github.com/<owner>/<repo> path (the common case for Go modules, and many
// npm/others), it returns a GitHub Sponsors target for <owner> with Source
// "purl". When no VCS owner can be extracted it returns Unresolved.
//
// Examples:
//
//	pkg:golang/github.com/gin-gonic/gin@v1.12.0      -> github_sponsors:gin-gonic
//	pkg:golang/golang.org/x/sys@v0.20.0              -> github_sponsors:golang  (x/* -> golang org)
//	pkg:npm/react@18.3.1                             -> unresolved (no VCS in PURL)
//	pkg:golang/github.com/google/uuid@v1.6.0         -> github_sponsors:google
func FromPURL(purl string) Target {
	owner, repo := vcsCoordinate(purl)
	if owner == "" {
		return Unresolved()
	}
	return Target{
		Kind:   KindGitHubSponsors,
		Handle: owner,
		Repo:   repo,
		Source: "purl",
	}
}

// vcsCoordinate extracts (owner, owner/repo) from a PURL whose name embeds a
// known VCS host path. Returns empty owner when none is found.
func vcsCoordinate(purl string) (owner, repo string) {
	// Strip "pkg:<type>/" prefix and "@version" suffix to get the bare name.
	name := purl
	if i := strings.Index(name, "/"); strings.HasPrefix(name, "pkg:") && i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimSpace(name)

	switch {
	case strings.HasPrefix(name, "github.com/"):
		parts := strings.SplitN(strings.TrimPrefix(name, "github.com/"), "/", 3)
		if len(parts) >= 2 {
			return parts[0], parts[0] + "/" + parts[1]
		}
	case strings.HasPrefix(name, "gitlab.com/"):
		parts := strings.SplitN(strings.TrimPrefix(name, "gitlab.com/"), "/", 3)
		if len(parts) >= 2 {
			return parts[0], parts[0] + "/" + parts[1]
		}
	case strings.HasPrefix(name, "golang.org/x/"):
		// The Go sub-repos (golang.org/x/...) are maintained by the "golang" org.
		rest := strings.TrimPrefix(name, "golang.org/x/")
		sub := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			sub = rest[:i]
		}
		return "golang", "golang/" + sub
	}
	return "", ""
}

// Resolver enriches a PURL-derived candidate with authoritative funding info
// (FUNDING.yml, registry maintainers). Implementations may perform network I/O
// and SHOULD be called OUT of the request hot path (e.g. a periodic resolution
// pass over held accruals), never blocking usage recording.
type Resolver interface {
	// Resolve returns the best target for the PURL. It must never error fatally:
	// on any failure it returns Unresolved so the amount is held, not dropped.
	Resolve(purl string) Target
}

// PURLResolver is the always-available offline resolver: it only uses FromPURL.
// It is the default the accrual engine uses inline (deterministic, no network),
// so accruals get a candidate target immediately; a richer network Resolver can
// run later over held lines.
type PURLResolver struct{}

func (PURLResolver) Resolve(purl string) Target { return FromPURL(purl) }
