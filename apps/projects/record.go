package projects

// record.go — an estate site keeps what it served, so a bad publish can be
// undone by serving an earlier release again.
//
// A site published through a deployment (bin/site's open → upload → complete, an
// archive upload, a sandbox build) serves its files IN PLACE: the edge reads
// `<namespace>/<slug>/` and every publish writes into that same prefix. So what a
// publish replaced stopped existing the moment it began, and no release
// (release.go) was ever cut that a rollback could point the site at.
//
// So once a deployment of an estate org's site goes live, the files the site
// serves are KEPT as a release: the same immutable, content-addressed value
// promote makes, copied server side into `<namespace>/.releases/<slug>/<id>/` and
// recorded LAST, and the deployment is linked to it (deployments.release_id).
// Rolling back is then what it already is for a release: activate an earlier one
// (activate, release.go), one atomic flip of the serving pointer, the host
// claimed and the edge purged. The next deployment takes the pointer back, as a
// deployment always has.
//
// WHICH SITES. An estate org's (forge.Owner: the platform's own orgs, whose sites
// their CI publishes). A customer's site is billed for what its org stores, and a
// kept copy of every publish would multiply that bill without being asked for; a
// customer keeps the releases it promotes itself.
//
// OFF THE PUBLISH'S PATH. The copy runs after the deployment is live, so a slow
// store never holds a completion, and a keep that fails is logged and leaves the
// site exactly as it was. Each copy keeps the object's own metadata (the content
// type, encoding and cache policy its publisher wrote) and is conditional on the
// ETag listed, so a kept release holds what was served or is not recorded at all.
// Kept to keptReleases per site by the same retention promote applies.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/object"
)

const (
	// keptReleases is how many releases of a site retention leaves: the one the
	// site serves and two before it, enough to roll back past two publishes that
	// went wrong in a row.
	keptReleases = 3
	// keptFiles and keptBytes bound one kept release. They are wider than one
	// upload's caps (maxFiles, maxTotalBytes) because a site published file by
	// file grows past those: hanzo.ai serves about ten thousand files.
	keptFiles = 20000
	keptBytes = 4 << 30
	// keepWait bounds one keep: the listing, the copy and the record.
	keepWait = 10 * time.Minute
	// keptHistory is the most deployments a site's revisions answer.
	keptHistory = 20
)

// keepsReleases reports whether org's sites are kept as releases when they go
// live: the estate's orgs, whose sites the platform publishes from its own CI.
func keepsReleases(org string) bool {
	_, err := forge.Owner(org)
	return err == nil
}

// keeper runs the keeps in flight, and lets Shutdown wait for them, since a keep
// writes the store.
type keeper struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (k *keeper) add() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return false
	}
	k.wg.Add(1)
	return true
}

// wait stops new keeps and waits for the running ones to end.
func (k *keeper) wait() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.closed = true
	k.mu.Unlock()
	k.wg.Wait()
}

// keep cuts what site p serves, now that deployment d made it live, into a
// release, in the background. It does nothing for a site no estate org owns.
func keep(s *cloud.Service[state], org string, p Project, d Deployment) {
	k := s.State.keeping
	if k == nil || !keepsReleases(org) || !s.State.blob.configured() || !k.add() {
		return
	}
	life := s.State.life
	if life == nil {
		life = context.Background()
	}
	s.Go("projects.keep", []any{"org", org, "slug", p.Slug, "deployment", d.ID}, func() {
		defer k.wg.Done()
		ctx, cancel := context.WithTimeout(life, keepWait)
		defer cancel()
		r, err := keepLive(s, ctx, org, p, d)
		if err != nil {
			s.Log.Warn("site not kept as a release: it cannot be rolled back to this publish",
				"org", org, "slug", p.Slug, "deployment", d.ID, "version", d.Version, "err", err)
			return
		}
		s.Log.Info("site kept as a release", "org", org, "slug", p.Slug, "deployment", d.ID,
			"version", d.Version, "release", r.ID, "objects", r.Objects, "bytes", r.Bytes)
	})
}

// keepLive is one keep: list the live prefix into a manifest, copy it to the
// release its digest names unless that release is already kept, record it, link
// the deployment to it, and apply retention.
func keepLive(s *cloud.Service[state], ctx context.Context, org string, p Project, d Deployment) (Release, error) {
	cli, bucket := s.State.blob.store, s.State.blob.bucket
	src := sitePrefix(p.Namespace, p.Slug)
	objs, total, err := scanSource(ctx, cli, bucket, src, keptFiles, keptBytes)
	if err != nil {
		return Release{}, err
	}
	id := releaseID(objs)
	r, err := s.State.store.GetRelease(ctx, org, p.Slug, id)
	switch {
	case errors.Is(err, errNotFound):
		dst := releasePrefix(p.Namespace, p.Slug, id)
		if err := copyKept(ctx, cli, bucket, src, dst, objs); err != nil {
			return Release{}, err
		}
		r = Release{
			ID: id, Org: org, Slug: p.Slug, Prefix: dst, Source: "deployment " + d.ID,
			Objects: len(objs), Bytes: total, CreatedAt: time.Now().Unix(),
		}
		if err := s.State.store.PutRelease(ctx, r); err != nil {
			return Release{}, err
		}
	case err != nil:
		return Release{}, err
	}
	if err := s.State.store.Kept(ctx, org, p.ID, d.ID, id); err != nil {
		return Release{}, err
	}
	pruneReleases(s, ctx, cli, org, p.Slug, keptReleases)
	refresh(s, org)
	return r, nil
}

// copyKept server-side-copies a manifest from src to dst, 32 at a time. Unlike
// copyRelease it keeps each object's metadata as its publisher wrote it, because
// the release serves those same objects again: a precompressed asset keeps its
// encoding, a Markdown twin its type. Each copy is conditional on the ETag the
// manifest was digested from, so a file that changed mid-copy fails the keep
// rather than being kept half old.
func copyKept(ctx context.Context, cli object.Store, bucket, src, dst string, objs []releaseObject) error {
	etag := make(map[string]string, len(objs))
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		etag[o.rel] = o.etag
		keys = append(keys, o.rel)
	}
	_, err := fan(ctx, keys, func(ctx context.Context, k string) error {
		info, err := cli.Copy(ctx,
			object.Ref{Bucket: bucket, Key: dst + "/" + k},
			object.Ref{Bucket: bucket, Key: src + "/" + k},
			object.Copy{IfMatch: etag[k]})
		if err != nil {
			if object.Conflict(err) {
				return errSourceChanged
			}
			return err
		}
		// A copied object keeps a single-part ETag; a multipart one's ETag is not
		// its content's, and a store may answer a new one for the copy.
		if want := etag[k]; info.ETag != "" && !strings.Contains(want, "-") && info.ETag != want {
			return errSourceChanged
		}
		return nil
	})
	return err
}

// ── the plane: a site as a deployed application ──────────────────────────────

// exposeKept publishes the two ops the deploy console reads a site and rolls one
// back through. Called from Mount.
func exposeKept() {
	p := cloud.Plane()
	p.Post("/projects/site/apps", planeSiteApps,
		zip.WithOperationID(client.ProjectsSiteApps),
		zip.WithSummary("The caller's sites as deployed applications, and what each can be rolled back to"))
	p.Post("/projects/site/rollback", planeSiteRollback,
		zip.WithOperationID(client.ProjectsSiteRollback),
		zip.WithSummary("Serve one of a site's kept releases again"))
}

// planeSiteApps answers the caller's sites that have served something, or the
// one site in.Slug names with what it can be rolled back to. Another org's slug
// is a 404, as every site read here answers it.
func planeSiteApps(ctx context.Context, in *client.SiteAppsIn) (*client.SiteApps, error) {
	s, org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	if slug := slugOf(in.Slug); slug != "" {
		p, err := s.State.store.GetProject(ctx, org, slug)
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("site not found")
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "get site: %v", err)
		}
		app, err := siteApp(s, ctx, org, p)
		if err != nil {
			return nil, err
		}
		return &client.SiteApps{Sites: []client.SiteApp{app}}, nil
	}
	ps, err := s.State.store.ListProjects(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list sites: %v", err)
	}
	live, err := s.State.store.LiveDeploys(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list sites: %v", err)
	}
	out := &client.SiteApps{Sites: []client.SiteApp{}}
	for _, p := range ps {
		if p.CurrentDeploy == "" && p.CurrentRelease == "" {
			continue // never served anything, so nothing to deploy or roll back
		}
		l := live[p.Slug]
		app := siteView(p, l.Release)
		if app.Revision == l.Release {
			app.Version, app.Commit = l.Version, l.Commit
		}
		out.Sites = append(out.Sites, app)
	}
	return out, nil
}

// siteApp is one site with its history: the deployments kept as a release that
// retention still holds, newest first.
func siteApp(s *cloud.Service[state], ctx context.Context, org string, p Project) (client.SiteApp, error) {
	kept, err := s.State.store.KeptReleases(ctx, org, p, keptHistory)
	if err != nil {
		return client.SiteApp{}, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	liveRelease := ""
	for _, k := range kept {
		if k.Deploy == p.CurrentDeploy {
			liveRelease = k.Release.ID
			break
		}
	}
	app := siteView(p, liveRelease)
	app.History = make([]client.SiteRevision, 0, len(kept))
	for _, k := range kept {
		app.History = append(app.History, client.SiteRevision{
			Release: k.Release.ID, Deploy: k.Deploy, Version: k.Version, Commit: k.Commit,
			At: k.At, Objects: k.Release.Objects, Bytes: k.Release.Bytes,
		})
		if app.Version == 0 && k.Release.ID == app.Revision {
			app.Version, app.Commit = k.Version, k.Commit
		}
	}
	return app, nil
}

// siteView is what a site serves: the release its pointer names, else the one
// its live deployment was kept as (liveRelease, "" when it was not).
func siteView(p Project, liveRelease string) client.SiteApp {
	rev := p.CurrentRelease
	if rev == "" {
		rev = liveRelease
	}
	return client.SiteApp{
		Slug: p.Slug, Status: p.Status, LiveURL: p.LiveURL, Revision: rev, UpdatedAt: p.UpdatedAt,
	}
}

// planeSiteRollback serves one of a site's kept releases again: the activation a
// release's own route makes (activate, release.go), so the two cannot differ about
// what a rollback is. The release must be one of this site's and not the one it
// serves; one retention reclaimed is 410.
func planeSiteRollback(ctx context.Context, in *client.SiteRollbackIn) (*client.SiteApp, error) {
	s, org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.State.store.GetProject(ctx, org, slugOf(in.Slug))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("site not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get site: %v", err)
	}
	id := strings.TrimSpace(in.Release)
	if !releaseIDRE.MatchString(id) {
		return nil, zip.ErrNotFound("release not found")
	}
	now, err := siteApp(s, ctx, org, p)
	if err != nil {
		return nil, err
	}
	if id == now.Revision {
		return nil, zip.ErrConflict(p.Slug + " already serves " + id + "; name an earlier release from its history")
	}
	if _, err := activate(s, ctx, org, p, id); err != nil {
		return nil, activateErr(err)
	}
	s.Log.Info("site rolled back", "org", org, "slug", p.Slug, "from", now.Revision, "to", id,
		"actor", cloud.Who(ctx).User)
	if p, err = s.State.store.GetProject(ctx, org, p.Slug); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get site: %v", err)
	}
	app, err := siteApp(s, ctx, org, p)
	if err != nil {
		return nil, err
	}
	return &app, nil
}
