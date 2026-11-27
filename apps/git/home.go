package git

// home.go — a repository whose home is the forge.
//
// git.hanzo.ai is the one home of a repository, and it names one by owner and
// name: hanzo-inc/cloud. Every op here that takes a repository reads that
// coordinate the same way ([place]): a name with an owner is the forge's
// repository, read from the forge as the caller; a bare name is a repository
// this app holds, or one in the workspace the platform made for the org, as it
// always was. What a repository ran, and what its runners are, are read from the
// forge the same way (actions.go), because the forge schedules every run of a
// repository it holds.
//
// Two controls decide what a caller reads, and neither backs the other up
// ([forgeFor]). The NAMESPACE must be one the caller's org owns: one the forge's
// closed table names for it (forge.Org — hanzo owns hanzoai, hanzo-inc and the
// estate's other namespaces), or the workspace the platform made for it. And the
// read is the caller's own forge login, so the forge's ACL decides what comes
// back. A SuperAdmin may name any namespace; the forge's ACL still applies.

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
	"golang.org/x/sync/errgroup"
)

// place splits a repository argument into the forge owner and name it
// addresses; ok is false for a bare name, which is a repository this app holds.
func place(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(normalizeName(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

// forgeFor is the forge as the caller, for repositories under the owners named:
// refused 403, before the forge is asked, when one is not a namespace the
// caller's org owns.
func forgeFor(ctx context.Context, t tenant, owners ...string) (*forge.Client, error) {
	if !principal.SuperFrom(ctx) {
		for _, owner := range owners {
			if !owns(t.org, owner) {
				return nil, zip.ErrForbidden(owner + " is not a forge namespace of your org")
			}
		}
	}
	return forgeAs(ctx)
}

// owns reports whether the IAM org owns the forge namespace owner: by the
// forge's closed table, or as the workspace the platform made for it.
func owns(org, owner string) bool {
	if o, err := forge.Org(owner); err == nil {
		return strings.EqualFold(o, org)
	}
	home, err := forgeHome(org)
	return err == nil && strings.EqualFold(home, owner)
}

// namespaces are the forge namespaces the org owns, its home first: the
// closed table's, or the one workspace the platform made for it.
func namespaces(org string) []string {
	home, err := forgeHome(org)
	if err != nil {
		return nil
	}
	out := []string{home}
	for _, ns := range forge.Namespaces(org) {
		if !slices.Contains(out, ns) {
			out = append(out, ns)
		}
	}
	return out
}

// homeView is a forge repository in this surface's shape, named by the
// coordinate every other op takes: owner/name.
func homeView(org string, r forge.Repo, branches []string) repoView {
	v := forgeView(org, r, branches)
	if r.FullName != "" {
		v.Name = r.FullName
	}
	return v
}

// forgeQuery narrows the forge's repository list.
type forgeQuery struct {
	// Owner lists one forge namespace of the caller's org, such as hanzo-inc.
	// Empty lists every namespace the org owns.
	Owner string `query:"owner" json:"owner,omitempty"`
}

// namespaceReads bounds the namespaces one list reads at once.
const namespaceReads = 4

// listHome returns the repositories on the forge (git.hanzo.ai) the caller can
// read, in every forge namespace their org owns or in the one named, most
// recently updated first.
//
// Each is named owner/name, the coordinate `repos get`, `runs`, `workflows` and
// the browse reads take. The read is the caller's own forge login, so a private
// repository they are not given on the forge is not listed. The repositories
// this app holds are `repos list`.
//
// Example: {"owner": "hanzo-inc"}
func (o ops) listHome(ctx context.Context, in *forgeQuery) (*repoList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	scope := namespaces(t.org)
	if owner := strings.TrimSpace(in.Owner); owner != "" {
		scope = []string{owner}
	}
	if len(scope) == 0 {
		return &repoList{Data: []repoView{}}, nil
	}
	m, err := forgeFor(ctx, t, scope...)
	if err != nil {
		return nil, err
	}
	parts := make([][]repoView, len(scope))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(namespaceReads)
	for i, owner := range scope {
		g.Go(func() error {
			repos, err := m.Repos(gctx, owner)
			if errors.Is(err, forge.ErrUnknownActor) {
				// A namespace this person is not given on the forge holds nothing
				// they can read; the other namespaces still answer.
				return nil
			}
			if err != nil {
				return madeRefusal(err, owner+" is no namespace on the forge")
			}
			for _, r := range repos {
				parts[i] = append(parts[i], homeView(t.org, r, nil))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := []repoView{}
	for _, p := range parts {
		out = append(out, p...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return &repoList{Data: out}, nil
}
