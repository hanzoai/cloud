package projects

// namespace.go — the first segment of a site's storage prefix.
//
// A site's objects live at `<namespace>/<slug>/` and its releases at
// `<namespace>/.releases/<slug>/`, and the namespace is the GitHub org its
// repository lives in: zoo.ngo, built from github.com/zooai/ngo, is kept under
// `zooai/ngo`. The IAM org that owns the project is a different fact — who pays,
// who may change it — and brand orgs like `zoo` own sites whose source lives in
// several GitHub orgs.
//
// THE ORG MUST HOLD THAT GITHUB ACCOUNT. A repository URL is the caller's to
// write, so a namespace read off it alone would let any org write under
// `hanzoai/` or `zooai/` — and an edge route that serves a prefix by path would
// serve it. The proof that an org speaks for a GitHub account is its connection
// to that account (provider: GitHub's own consent screen, or a SuperAdmin's
// claim), so the namespace is the repository's account when the org holds that
// account's connection, and the org's own name when it does not or when the
// project has no repository.
//
// NOR MAY IT BE ANOTHER ORG'S NAME. A GitHub login and an IAM org can share a
// name without sharing an owner — whoever registered github.com/zoo is not org
// zoo — and `zoo/` already holds zoo's sites. A namespace belongs to the org
// whose site has been kept there longest, so a GitHub org whose namespace holds
// another org's sites is refused the same way, and the site stays under its own
// org. Where no site is kept yet, the name of an org this store knows is refused.
// The order matters both ways: an org that later takes a GitHub org's name (or
// once kept one site under it) does not shut the namespace's holder out of it.
//
// It is decided when the repository is linked — at create, and when an update
// sends a repository — and stored with the project, so it never moves under a
// live site because a connection came or went. Moving a site is relinking it,
// and a move is refused unless it is safe to serve from (move):
//
//   - a site with releases does not move: their rows name the old prefix;
//   - a published site moves only onto a copy of itself — the new prefix's
//     index.html is the old one's, so it is never served empty;
//   - anything else enters a GitHub namespace only where nothing is kept for
//     its slug yet, so a hand-copied prefix an edge route serves cannot be
//     taken by claiming its slug.
//
// The objects are copied by whoever moves the site, and the edge routes that
// name the old prefix are switched after.

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/internal/remote"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// namespace answers the namespace a project of org linked to repo is kept under.
// Only a github.com repository has a GitHub org; any other host keeps the site
// under its org without asking.
// An integrations app that is deployed and cannot answer fails the link rather
// than choosing for it: a namespace chosen during an outage would outlive the
// outage. A deployment that runs no integrations app holds no connection at all,
// so every site there is kept under its org.
func namespace(s *cloud.Service[state], ctx context.Context, org, repo string) (string, error) {
	owner := remote.Owner(repo)
	if remote.Host(repo) != "github.com" || owner == "" || owner == org || !slugRE.MatchString(owner) || s.State.holds == nil {
		return org, nil
	}
	held, err := s.State.holds(ctx, org, owner)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return org, nil
	case err != nil:
		return "", zip.Errorf(http.StatusServiceUnavailable, "namespace: whether %s holds GitHub %s could not be read: %v", org, owner, err)
	case !held:
		return org, nil
	}
	first, err := s.State.store.Holder(ctx, owner)
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "namespace: %v", err)
	}
	if first != "" {
		if first == org {
			return owner, nil
		}
		return org, nil
	}
	other, err := s.State.store.Keeps(ctx, owner)
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "namespace: %v", err)
	}
	if other {
		return org, nil
	}
	return owner, nil
}

// holdsGitHub asks the integrations app, as org, whether org holds the GitHub
// account. Org-held only: a member's personal link is not the org's word.
func holdsGitHub(ctx context.Context, org, account string) (bool, error) {
	c, err := providerpeer.IntegrationsConnection(plane.For(ctx, org),
		&client.ConnectionIn{Provider: "github", Account: account})
	if err != nil {
		return false, fmt.Errorf("integrations: %w", err)
	}
	// The label too: an integrations app that does not read `account` answers
	// for whichever account the org holds, and that is not this one.
	return c.Connected && strings.EqualFold(c.AccountLabel, account), nil
}

// relink decides p's namespace again for the repository it now names, having
// named was. A repository in another account (or on another host) is a new
// link: the namespace it decides is p's, or the update is refused. The same
// repository sent again — the whole body a console saves — moves p only where
// move allows it, which is how a site already copied to its GitHub namespace is
// moved there; otherwise p stays where it is, and an integrations app that
// cannot answer does not fail an edit that changed no link.
func relink(s *cloud.Service[state], ctx context.Context, p *Project, was string) error {
	same := remote.Host(was) == remote.Host(p.RepoURL) && remote.Owner(was) == remote.Owner(p.RepoURL)
	ns, err := namespace(s, ctx, p.Org, p.RepoURL)
	if err != nil {
		if same {
			s.Log.Warn("namespace not decided again (the site stays where it is)", "org", p.Org, "slug", p.Slug, "err", err)
			return nil
		}
		return err
	}
	if err := move(s, ctx, *p, ns); err != nil {
		// move's 409 is a move it refused as unsafe; anything else is one it
		// could not decide, and that is not swallowed.
		if he := (*zip.HTTPError)(nil); same && errors.As(err, &he) && he.Status == http.StatusConflict {
			return nil
		}
		return err
	}
	p.Namespace = ns
	return nil
}

// move refuses to put p under the namespace to unless that is safe to serve
// from (see the head of this file). p.Namespace is where it is kept now, empty
// for a project not yet created. A deployment with no sites bucket keeps
// nothing, so nothing can be refused there.
func move(s *cloud.Service[state], ctx context.Context, p Project, to string) error {
	if to == p.Namespace || !s.State.blob.configured() {
		return nil
	}
	if p.Namespace != "" {
		rels, err := s.State.store.ListReleases(ctx, p.Org, p.Slug, 1)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "namespace: %v", err)
		}
		if len(rels) > 0 {
			return zip.ErrConflict(p.Slug + " has releases, whose rows name " + releaseSpace(p.Namespace) + "/" + p.Slug + "; a site with releases does not move to " + to)
		}
	}
	cli := s.State.blob.store
	// No bucket yet keeps nothing anywhere.
	if ok, err := cli.Has(ctx, s.State.blob.bucket); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "namespace: %v", err)
	} else if !ok {
		return nil
	}
	// index is the ETag of the site's index.html under ns, "" when there is none.
	// An object the store gives no ETag reads as a different one, so it is never
	// taken for a copy.
	index := func(ns string) (string, bool, error) {
		st, err := cli.Stat(ctx, s.State.blob.bucket, sitePrefix(ns, p.Slug)+"/index.html")
		if object.Absent(err) {
			return "", false, nil
		}
		if err != nil {
			return "", false, zip.Errorf(http.StatusServiceUnavailable, "namespace: %v", err)
		}
		return st.ETag, true, nil
	}
	if p.Namespace != "" {
		was, published, err := index(p.Namespace)
		if err != nil {
			return err
		}
		if published {
			now, _, err := index(to)
			if err != nil {
				return err
			}
			if was == "" || now != was {
				return zip.ErrConflict(p.Slug + " is published at " + sitePrefix(p.Namespace, p.Slug) + "/; copy it to " + sitePrefix(to, p.Slug) + "/ before moving it there")
			}
			return nil
		}
	}
	if to == p.Org {
		return nil
	}
	for _, prefix := range []string{sitePrefix(to, p.Slug) + "/", releaseSpace(to) + "/" + p.Slug + "/"} {
		for _, err := range cli.List(ctx, s.State.blob.bucket, object.List{Prefix: prefix, Max: 1}) {
			if err != nil {
				return zip.Errorf(http.StatusServiceUnavailable, "namespace: %v", err)
			}
			return zip.ErrConflict(prefix + " already holds objects this project did not put there")
		}
	}
	return nil
}
