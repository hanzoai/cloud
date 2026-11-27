package projects

// build.go — a site built from its own repository, in a sandbox.
//
// A project whose source is in a workspace the platform made for its org
// (workspace.go) is built where every tenant's code runs: a sandbox the agent
// app's runner leases. This app keeps the site. It opens the deployment, hands
// the runner a grant that writes this one site's prefix and nothing else
// (grant.go), and ends the deployment when the runner says how the build went.
//
//	projects_site_open    a deployment, and the grant to upload it with
//	projects_site_close   live with the keys uploaded, or failed with why
//	projects_site_builds  the org's sandbox builds, for the platform's board
//
// Each is the CALLER's org's, read off the call. The repository a build is of is
// checked against the org's own workspace record, never taken from the project
// row alone: a row names whatever repository its last write gave it, and only
// the record says which namespace is the org's.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// sourceBuild is the Deployment.Source of a site a sandbox built.
const sourceBuild = "build"

// siteBuildLimit is the most rows the builds board is answered.
const siteBuildLimit = 50

// exposeSite publishes the three ops on the internal plane. Called from Mount.
func exposeSite() {
	p := cloud.Plane()
	p.Post("/projects/site/open", planeSiteOpen,
		zip.WithOperationID(client.ProjectsSiteOpen),
		zip.WithSummary("Open a deployment of one of the caller's projects, built from its own repository"))
	p.Post("/projects/site/close", planeSiteClose,
		zip.WithOperationID(client.ProjectsSiteClose),
		zip.WithSummary("End a sandbox-built deployment: live with its keys, or failed"))
	p.Post("/projects/site/builds", planeSiteBuilds,
		zip.WithOperationID(client.ProjectsSiteBuilds),
		zip.WithSummary("The caller's sandbox-built deployments, newest first"))
}

// buildWait bounds asking the agent app to start a build. The build runs after
// the answer, so this is the admission and nothing more.
const buildWait = 15 * time.Second

// build asks the agent app to build org's project slug from its repository in a
// sandbox and put it on the project's site. The org rides the call, stated on a
// context of its own because the project is org's whoever the request was. The
// person who asked rides it too (cloud.Detach): the build's sandbox is leased as
// theirs and paid from their wallet, and a call naming only the tenant names no
// wallet, so its lease was refused and the build never ran.
func build(c *zip.Ctx, org, slug string) error {
	ctx, cancel := context.WithTimeout(plane.For(cloud.Detach(context.Background(), c), org), buildWait)
	defer cancel()
	_, err := plane.Call[client.PublishIn, client.Publishing](ctx, agentpeer.App, client.AgentsCodingPublish,
		&client.PublishIn{Project: slug})
	return err
}

// buildLife is how long a sandbox build may be open. Past its grant's life
// (grant.go) it cannot upload a byte, so a deployment still building then has
// lost its publisher — the process that ran it ended, or its ending never
// arrived — and nothing else will end it.
const buildLife = grantTTL + 5*time.Minute

// buildSweep is how often abandoned builds are looked for.
const buildSweep = time.Minute

// watchBuilds ends abandoned builds every buildSweep until life ends.
func watchBuilds(s *cloud.Service[state], life context.Context) {
	t := time.NewTicker(buildSweep)
	defer t.Stop()
	for {
		select {
		case <-life.Done():
			return
		case <-t.C:
			if n, err := expireBuilds(s, life, time.Now()); err != nil && life.Err() == nil {
				s.Log.Warn("abandoned builds were not ended", "ended", n, "err", err)
			}
		}
	}
}

// expireBuilds ends every sandbox build opened more than buildLife before now as
// failed, saying why, the way a build that reports its failure is ended: a
// project that never served moves from building to error, and a live one keeps
// serving. It answers how many it ended.
func expireBuilds(s *cloud.Service[state], ctx context.Context, now time.Time) (int, error) {
	stale, err := s.State.store.StaleBuilds(ctx, now.Add(-buildLife).Unix())
	if err != nil {
		return 0, err
	}
	why := fmt.Sprintf("the build did not finish within %d minutes; publish it again", int(buildLife/time.Minute))
	ended := 0
	var failed []error
	for _, b := range stale {
		if ctx.Err() != nil {
			return ended, ctx.Err()
		}
		// One build that cannot be ended holds up none of the others.
		p, err := s.State.store.GetProject(ctx, b.Deployment.Org, b.Slug)
		if err == nil {
			_, err = land(s, ctx, b.Deployment.Org, p, b.Deployment, ending{message: why})
		}
		if err != nil {
			failed = append(failed, fmt.Errorf("%s/%s %s: %w", b.Deployment.Org, b.Slug, b.Deployment.ID, err))
			continue
		}
		ended++
	}
	return ended, errors.Join(failed...)
}

// planeOrg is the caller's org on a plane op, refused when it is not one this
// store can key a site under.
func planeOrg(ctx context.Context) (*cloud.Service[state], string, error) {
	org := cloud.Who(ctx).Org
	if org == "" || !orgPathSafe(org) {
		return nil, "", zip.ErrForbidden("projects: org required")
	}
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, "", zip.ErrInternal("projects: this process does not own the project store")
	}
	return s, org, nil
}

// planeSiteOpen opens a deployment of the caller's project, to be built from the
// project's repository in a sandbox, and answers the grant to upload it with.
//
// Refused before anything is written: a project that is not the caller's, one
// whose repository is not in the org's own made workspace, a named repository
// that is not the project's, storage that cannot mint a grant, and an org past
// the free storage tier that cannot pay a month of what it holds. The deployment
// itself is free.
//
// A live project STAYS live while it builds. The edge serves only a live
// project, so marking it building would take the site down for the length of
// every rebuild; the deployment row carries the build's state instead. A
// project that has never served moves to building, and the builder shows that.
func planeSiteOpen(ctx context.Context, in *client.SiteOpenIn) (*client.SiteOpened, error) {
	s, org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := loadProject(s, ctx, org, slugOf(in.Slug))
	if err != nil {
		return nil, err
	}
	owner, repo, err := source(s, ctx, org, p)
	if err != nil {
		return nil, err
	}
	if in.Owner != "" || in.Repo != "" {
		if !strings.EqualFold(in.Owner, owner) || !strings.EqualFold(in.Repo, repo) {
			return nil, zip.ErrForbidden("projects: " + p.Slug + " is built from its own repository, not " + in.Owner + "/" + in.Repo)
		}
	}
	if !s.State.blob.configured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage not configured")
	}
	if err := gateStorage(s, ctx, org, "", false); err != nil {
		return nil, cloud.Denied(err)
	}
	now := time.Now()
	version, err := s.State.store.NextVersion(ctx, p.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "version: %v", err)
	}
	d := Deployment{
		ID: genID("dep"), ProjectID: p.ID, Org: org, Version: version, Status: "building",
		Source: sourceBuild, Bucket: s.State.blob.bucket, Prefix: sitePrefix(p.Namespace, p.Slug),
		CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}
	if err := s.State.store.InsertDeployment(ctx, d); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}
	grant, gErr := mintGrant(ctx, s.State.blob, d.Prefix, now)
	if gErr != nil || grant == nil {
		// A sandbox has no other way to write the site, so a deployment it cannot
		// upload is ended here rather than left building.
		why := "uploads are not configured on this deployment"
		if gErr != nil {
			why = gErr.Error()
		}
		d.Status, d.Message, d.UpdatedAt = "error", why, time.Now().Unix()
		_ = s.State.store.UpdateDeployment(ctx, d)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "projects: no upload grant: %s", why)
	}
	if p.Status != "live" {
		p.Status, p.UpdatedAt = "building", now.Unix()
		if err := s.State.store.UpdateProject(ctx, p); err != nil {
			s.Log.Warn("set building failed (continuing)", "slug", p.Slug, "err", err)
		}
	}
	emitProjectLifecycle(ctx, cloud.LifecycleBuildStarted, org, p, d, "building "+p.Slug)
	return &client.SiteOpened{
		ID: d.ID, Owner: owner, Repo: repo, Branch: cmp.Or(strings.TrimSpace(p.RepoBranch), "main"),
		Upload: client.SiteUpload{
			URL: grant.URL, Fields: client.Fields(grant.Fields), Prefix: grant.Prefix,
			MaxBytes: grant.MaxBytes, ExpiresAt: grant.ExpiresAt,
		},
	}, nil
}

// planeSiteClose ends a deployment projects_site_open opened. A live ending claims
// the host, reconciles the prefix against the keys uploaded, and re-measures the
// org's storage; a failed one leaves a site that is serving alone. Neither is
// charged.
//
// Only a sandbox build that is still building can be ended here, so a CI
// deployment, an upload, or one already ended is not this op's to move.
func planeSiteClose(ctx context.Context, in *client.SiteCloseIn) (*client.SiteClosed, error) {
	s, org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := loadProject(s, ctx, org, slugOf(in.Slug))
	if err != nil {
		return nil, err
	}
	d, err := s.State.store.GetDeployment(ctx, org, p.ID, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	if d.Source != sourceBuild || d.Status != "building" {
		return nil, zip.ErrConflict("projects: deployment " + d.ID + " is not a build in progress")
	}
	d, err = land(s, ctx, org, p, d, ending{
		live: in.Live, commit: in.Commit, message: in.Message,
		files: in.Files, bytes: in.Bytes, keys: in.Keys,
	})
	if err != nil {
		return nil, err
	}
	return &client.SiteClosed{Status: d.Status, LiveURL: d.LiveURL, Message: d.Message}, nil
}

// planeSiteBuilds lists the caller's sandbox builds, newest first.
func planeSiteBuilds(ctx context.Context, in *client.SiteBuildsIn) (*client.SiteBuilds, error) {
	s, org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > siteBuildLimit {
		limit = siteBuildLimit
	}
	rows, err := s.State.store.ListBuilds(ctx, org, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list builds: %v", err)
	}
	out := &client.SiteBuilds{Builds: make([]client.SiteBuild, 0, len(rows))}
	for _, r := range rows {
		out.Builds = append(out.Builds, client.SiteBuild{
			ID: r.Deployment.ID, Project: r.Slug, Repo: r.Repo, Commit: r.Deployment.Commit,
			Status: r.Deployment.Status, Message: r.Deployment.Message,
			CreatedAt: r.Deployment.CreatedAt, UpdatedAt: r.Deployment.UpdatedAt,
		})
	}
	return out, nil
}

// source is the repository project p builds from, owner/name, when that is a
// repository in the workspace the platform made for org — and a refusal
// otherwise.
//
// The estate's orgs build their sites in CI, and nothing here changes that. For
// every other org the namespace is the one the record names, read here from the
// record and never from the row: a project whose repository was pointed at
// another namespace, on this forge or any other, has no sandbox build.
func source(s *cloud.Service[state], ctx context.Context, org string, p Project) (owner, repo string, err error) {
	if _, err := forge.Owner(org); err == nil {
		return "", "", zip.ErrForbidden("projects: this organization's sites are built by its own CI")
	}
	ns, err := s.State.store.book().Read(ctx, org)
	if err != nil {
		return "", "", zip.Errorf(http.StatusInternalServerError, "read workspace: %v", err)
	}
	if ns == "" {
		return "", "", zip.ErrConflict("projects: this organization has no code workspace yet")
	}
	m, err := machine(s, ctx)
	if err != nil {
		return "", "", zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	owner, repo, ok := forgeRepo(p.RepoURL, m.Host())
	if !ok {
		if account, name, linked := forgeRepo(p.RepoURL, githubHost); linked {
			return imported(ctx, m, ns, p.Slug, account, name)
		}
	}
	if !ok || !strings.EqualFold(owner, ns) {
		return "", "", zip.ErrConflict("projects: " + p.Slug + "'s source is not in this organization's code workspace")
	}
	return ns, repo, nil
}

// githubHost is where a project linked to its GitHub repository points.
const githubHost = "github.com"

// imported is the copy of a GitHub repository that an import landed in org's own
// workspace (apps/provider githubImport), which is what a project linked to that
// repository builds from.
//
// An import records the project with the repository's GitHub address (adopt
// links it before the copy exists) and lands the code at
// forge.Imported(account, name) in the org's namespace. The builder publishes
// that copy, so a project that names GitHub is built from it. Nothing outside
// the namespace this org's record names is ever read: the name is derived, the
// namespace is the record's, and the copy must already be there. A repository
// that was never imported builds nothing.
func imported(ctx context.Context, m *forge.Client, ns, slug, account, name string) (owner, repo string, err error) {
	repo = forge.Imported(account, name)
	if !slugPart(repo) {
		return "", "", zip.ErrConflict("projects: " + slug + "'s source is not in this organization's code workspace")
	}
	if _, err := m.Repo(ctx, ns, repo); err != nil {
		if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrUnknownActor) {
			return "", "", zip.ErrConflict("projects: " + slug + " is linked to github.com/" + account + "/" + name +
				", which has not been imported into this organization's code workspace yet; import it, then publish")
		}
		return "", "", zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	return ns, repo, nil
}

// forgeRepo reads owner and name out of a clone address on host — https, no
// credential, exactly two path segments, `.git` optional — and false for any
// other address.
func forgeRepo(raw, host string) (owner, repo string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.EqualFold(u.Host, host) {
		return "", "", false
	}
	owner, repo, ok = strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	repo = strings.TrimSuffix(repo, ".git")
	if !ok || !slugPart(owner) || !slugPart(repo) {
		return "", "", false
	}
	return owner, repo, true
}

// slugPart is one forge name: letters, digits, '.', '_' and '-', not led by a
// dot, and never a traversal.
func slugPart(s string) bool {
	if s == "" || len(s) > 100 || s[0] == '.' || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
