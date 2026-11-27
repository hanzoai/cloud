package projects

// link_rpc.go — a repository the caller's org linked, as one of its projects.
//
// An org links a repository through the sync app (/v1/sync): one repository, or
// a whole GitHub account, which declares a link for every repository it holds.
// The link is the sync app's and the project store this one's, so the sync app
// asks here, over the plane, when it declares one. The answer is a project row —
// the row GET /v1/projects lists for hanzo.ai and the platform alike — so a
// linked repository is a project, and there is no second list of them.
//
// One project per repository: a project that already builds from it is the
// answer, whoever made it.
//
// A linked project gets no published source at link time ([born]). The linked
// repository IS its source, and an account link declares hundreds at once, so a
// community copy each would be hundreds of empty repositories nobody asked for.
// Its first update gives it one ([share]), as for every project, and the
// generated-site path ([ensureProject]) makes its rows the same way.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/remote"
	"github.com/hanzoai/cloud/sdk"
)

// exposeLink publishes the link on the internal plane. Called from Mount.
func exposeLink() {
	cloud.Plane().Post("/projects/link", planeLink,
		zip.WithOperationID(client.ProjectsLink),
		zip.WithSummary("Make a repository the caller's org linked one of its projects"))
}

// planeLink answers the CALLER's project that builds from in.Repo, making it
// when there is none.
//
// The org is the caller's plane identity and never an argument. A new project is
// named for the repository, `<account>/<name>`, under the first slug of
// `<account>-<name>`, `-2`, … the org does not hold, in `draft`, and PUBLIC for
// the reason [planeCreate]'s is: private is paid for by a request, and a plane
// call has none.
func planeLink(ctx context.Context, in *client.LinkIn) (*client.ProjectOut, error) {
	org := cloud.Who(ctx).Org
	if org == "" || !orgPathSafe(org) {
		return nil, zip.ErrForbidden("projects link: org required")
	}
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, zip.ErrInternal("projects: this process does not own the project store")
	}
	repo, account, name, err := linked(in.Repo)
	if err != nil {
		return nil, err
	}
	switch p, err := s.State.store.ProjectByRepo(ctx, org, repo); {
	case err == nil:
		return &client.ProjectOut{ID: p.ID, Slug: p.Slug}, nil
	case !errors.Is(err, errNotFound):
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	full := account + "/" + name
	for n := 1; n <= copyTries; n++ {
		body := projectsCreate{Name: full, Slug: Slug(account+"-"+name, n)}
		if body.Slug == "" {
			continue
		}
		body.Repo.URL = repo
		p, err := draft(s, org, body)
		if err != nil {
			return nil, err
		}
		p.Visibility = Public
		switch err := insert(s, ctx, &p, nil); {
		case err == nil:
			return &client.ProjectOut{ID: p.ID, Slug: p.Slug}, nil
		case !errors.Is(err, errConflict) && !errors.Is(err, errHostTaken):
			if he := (*zip.HTTPError)(nil); errors.As(err, &he) {
				return nil, err
			}
			return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
	}
	return nil, zip.Errorf(http.StatusConflict, "every name for %q is taken", full)
}

// linked reads a repository's https clone address: the address with any
// credential dropped, and the account and repository it names.
func linked(raw string) (repo, account, name string, err error) {
	u, perr := url.Parse(remote.Clean(raw))
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", "", zip.ErrBadRequest("repo must be an https clone address")
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git"), "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", "", "", zip.ErrBadRequest("repo must name an account and a repository")
	}
	return u.String(), parts[len(parts)-2], parts[len(parts)-1], nil
}
