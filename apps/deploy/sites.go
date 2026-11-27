package deploy

// sites.go — the third kind an application name resolves to: a site.
//
// A site is published to the projects app's object store and served by the
// sites edge; no App CR and no CD Application describes it, so until now this
// console could neither list one nor say what it served. It is read, and rolled
// back, through the projects app over the internal plane, for the CALLER (their
// own principal; a SuperAdmin's in the org they act in), so the projects app's
// own rule decides which sites exist for them: their org's, and no other.
//
// A site answers to `site-<slug>`. Site slugs and CD Application names share a
// spelling (hanzo-studio is both), so a bare slug could not say which it meant;
// the prefix makes the site the only thing the name can be. An App CR or CD
// Application literally named `site-…` still wins, as an App CR wins over a CD
// Application (resolve.go).
//
// A site's revisions are the releases its live deployments were kept as
// (apps/projects record.go), so syncing one to an earlier revision serves that
// release again: a rollback (dashboard.go sync).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/sdk/principal"
)

const (
	// siteName prefixes a site's application name.
	siteName = "site-"
	// siteWait bounds one read of the projects app; a rollback moves a pointer
	// and purges the edge, so it is given no longer.
	siteWait = 15 * time.Second
)

// The projects app's two answers, as variables so a test can stand in for it.
var (
	askSites        = projectpeer.ProjectsSiteApps
	askSiteRollback = projectpeer.ProjectsSiteRollback
)

// siteApps lists the sites of the caller's org as applications. A projects app
// that cannot answer leaves them out, logged, rather than failing a list that
// also carries the caller's other applications.
func (o ops) siteApps(ctx context.Context, sc scope) []argoApp {
	if sc.home() == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(onBehalf(ctx), siteWait)
	defer cancel()
	out, err := askSites(ctx, &client.SiteAppsIn{})
	if err != nil {
		o.s.Log.Warn("deploy: sites left out of the applications list", "org", sc.home(), "err", err)
		return nil
	}
	apps := make([]argoApp, 0, len(out.Sites))
	for i := range out.Sites {
		apps = append(apps, projectSite(sc.home(), &out.Sites[i]))
	}
	return apps
}

// site reads one site of the caller's org with its history, nil when the org has
// no site of that slug.
func (o ops) site(ctx context.Context, slug string) (*client.SiteApp, error) {
	ctx, cancel := context.WithTimeout(onBehalf(ctx), siteWait)
	defer cancel()
	out, err := askSites(ctx, &client.SiteAppsIn{Slug: slug})
	var he *zip.HTTPError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		return nil, nil
	case err != nil:
		return nil, siteErr(err)
	case len(out.Sites) != 1:
		return nil, nil
	}
	return &out.Sites[0], nil
}

// rollbackSite serves one of the site's kept releases again, through the
// projects app, and answers the site as it now is.
func (o ops) rollbackSite(ctx context.Context, t target, revision string) (*argoApp, error) {
	call, cancel := context.WithTimeout(onBehalf(ctx), siteWait)
	defer cancel()
	got, err := askSiteRollback(call, &client.SiteRollbackIn{Slug: t.site.Slug, Release: revision})
	if err != nil {
		return nil, siteErr(err)
	}
	user, _ := consoleUser(ctx)
	actor, org := consoleActor(ctx, user)
	o.s.Log.Info("site rolled back", "site", t.org+"/"+t.site.Slug, "from", t.site.Revision,
		"to", got.Revision, "actor", actor, "org", org)
	app := projectSite(t.org, got)
	return &app, nil
}

// siteErr keeps the projects app's own refusals (404, 409, 410) and states any
// other failure as the console's 503, naming what could not be asked.
func siteErr(err error) error {
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
		return err
	}
	return zip.Errorf(http.StatusServiceUnavailable, "deploy: the sites could not be read: %v", err)
}

// projectSite is one site as an argocd Application: named site-<slug>, in its
// org, healthy while it serves, synced at the release it serves, and — on a
// one-site read — with the releases it can be rolled back to as its history.
func projectSite(org string, st *client.SiteApp) argoApp {
	app := argoApp{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: argoMeta{
			Name:      siteName + st.Slug,
			Namespace: org,
			Labels:    map[string]string{orgLabel: org, "hanzo.ai/site": st.Slug},
		},
		Spec: argoSpec{
			Destination: argoDestination{Server: "https://kubernetes.default.svc", Namespace: org},
			Project:     principal.DefaultProject,
		},
		Status: argoStatus{
			Sync:      argoSyncStatus{Status: siteSync(st), Revision: st.Revision},
			Health:    argoHealth{Status: siteHealth(st.Status), Message: st.LiveURL},
			Resources: []argoResourceStatus{},
		},
	}
	if st.UpdatedAt > 0 {
		app.Status.ReconciledAt = time.Unix(st.UpdatedAt, 0).UTC().Format(time.RFC3339)
	}
	// argocd orders a history oldest first; the projects app answers newest first.
	for i := len(st.History) - 1; i >= 0; i-- {
		h := st.History[i]
		app.Status.History = append(app.Status.History, argoHistory{
			ID: int64(h.Version), Revision: h.Release,
			DeployedAt: time.Unix(h.At, 0).UTC().Format(time.RFC3339),
		})
	}
	return app
}

// siteTree is a site's ApplicationTree: empty, since a site is files in the
// object store and owns no cluster object.
func siteTree() argoTree {
	return argoTree{Nodes: []argoNode{}, OrphanedNodes: []argoNode{}, Hosts: []any{}}
}

// siteSync is Synced when the site serves a known release, and Unknown when its
// files were not kept, so there is no revision to have reached.
func siteSync(st *client.SiteApp) string {
	if st.Revision == "" {
		return "Unknown"
	}
	return "Synced"
}

// siteHealth maps a site's status onto the argocd health vocabulary.
func siteHealth(status string) string {
	switch status {
	case "live":
		return "Healthy"
	case "building":
		return "Progressing"
	case "error":
		return "Degraded"
	case "":
		return "Unknown"
	}
	return "Missing"
}

// siteRevisionMetadata describes one revision of a site: the deployment that went
// live with it, from the site's history. A revision the history does not hold is
// echoed back with the site's own time, as for any other kind.
func siteRevisionMetadata(st *client.SiteApp, revision string) argoRevisionMetadata {
	if revision == "" || revision == "HEAD" {
		revision = st.Revision
	}
	for _, h := range st.History {
		if h.Release == revision {
			msg := fmt.Sprintf("release %s, kept from deployment %s (v%d)", h.Release, h.Deploy, h.Version)
			if h.Commit != "" {
				msg += " at commit " + h.Commit
			}
			return argoRevisionMetadata{Date: time.Unix(h.At, 0).UTC().Format(time.RFC3339), Message: msg}
		}
	}
	if len(revision) > maxRevisionLen {
		revision = revision[:maxRevisionLen]
	}
	return argoRevisionMetadata{Date: time.Unix(st.UpdatedAt, 0).UTC().Format(time.RFC3339), Message: strings.TrimSpace(revision)}
}
