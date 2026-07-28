package catalog

// origin.go answers the one question the catalog could not: what is this thing
// TO YOU.
//
// Everything the fleet has built arrives here as one flat list, and in it a
// curated starter, a stranger's remix and somebody else's paid UI kit render
// identically. They are different nouns, and a browser that cannot tell them
// apart is not a catalog, it is a pile:
//
//	template     OUR curated starter — the thing you fork FROM. Finite, quality
//	             gated, each with a live demo. It is a repo in the templates org,
//	             or the deployed demo of one of the curated gallery's entries.
//	community    somebody BUILT this. Forks and remixes: the apps orgs, the
//	             auto-publish org every public fork lands in, and every live site
//	             that is not one of our starters' demos. Unbounded and
//	             user-owned. OUR seeded example apps are in here too.
//	third-party  somebody ELSE's work. Carried only with an upstream credit;
//	             never shown as ours and never handed out with a fork button.
//	product      our own software — the fleet's repos. Not a starter to fork,
//	             not a demo somebody built. The rest of what we ship.
//
// # Derived, never tagged
//
// Origin comes from where the thing LIVES and what its owner DECLARED: which
// GitHub org the repo sits in, whether the live slug is one of the curated
// gallery's own, whether a fork parent or an upstream credit was recorded. A
// field an operator types is a field that is wrong by the second week; those
// three facts cannot drift, because they are the same facts the fork flow and
// the sites edge already run on.
//
// # Orthogonal to Official
//
// Origin says which LANE a row belongs in. Official (projects.Project.Official,
// admin-gated) says WHOSE work it is. They are deliberately two fields: our
// seeded examples are community entries that carry Official, and folding that
// into one "official-example" value would make the two unaskable separately —
// while "show me community apps that are NOT ours" is the entire point of
// having a community lane at all.
import (
	"strings"

	"github.com/hanzoai/cloud/clients/projects"
	"github.com/hanzoai/cloud/clients/templates"
)

// The four origins. A row always has exactly one: "nobody said" is the state
// this file exists to abolish, so Entry.Origin is not omitempty either.
const (
	// OriginTemplate is one of OUR curated starters — the thing you fork FROM.
	OriginTemplate = "template"
	// OriginCommunity is something somebody BUILT. Ours carry Official.
	OriginCommunity = "community"
	// OriginThirdParty is somebody ELSE's work, shown only with its credit.
	OriginThirdParty = "third-party"
	// OriginProduct is our own software: the fleet's repos.
	OriginProduct = "product"
)

// curated is the ONE curated-gallery door (clients/templates owns the embedded
// catalog and its variants). A package var for the same reason the corpus's two
// other sources are: the derivation is testable without the embedded catalog.
var curated = templates.List

// starters is every slug a demo of one of our curated starters can deploy under.
// It is built FORWARD from the gallery rather than matched backward against a
// name pattern, so a new template puts its demo in the templates lane with
// nothing else to remember — and a community app can never fall in by spelling.
//
// Three shapes, because those are the three a project slug is derived in
// (clients/projects fork.go seedFrom):
//
//	<slug>             the template's own name
//	<slug>-<variant>   one of the shapes it ships in (format / page / theme)
//	<slug>-template    the name a reserved subdomain forces it to deploy under
func starters() map[string]bool {
	cat, err := curated()
	if err != nil {
		return nil // no gallery ⇒ no starter claims; every demo reads as community
	}
	out := make(map[string]bool, len(cat)*2)
	for _, t := range cat {
		out[t.Slug] = true
		out[t.Slug+"-template"] = true
		for _, v := range t.Variants {
			out[t.Slug+"-"+v.ID] = true
		}
	}
	return out
}

// siteOrigin files one live site. Order is precedence, strongest fact first: a
// declared credit is a statement that the work is somebody else's, recorded
// lineage is proof somebody built it from something, and a curated slug is the
// gallery's own demo. What is left is an app on the platform — which is what
// the community lane IS.
func siteOrigin(s projects.LiveSite, starter map[string]bool) string {
	switch {
	case s.Upstream != "":
		return OriginThirdParty
	case s.ForkedFrom != "":
		return OriginCommunity
	case starter[strings.ToLower(s.Slug)]:
		return OriginTemplate
	default:
		return OriginCommunity
	}
}
