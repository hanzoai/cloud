package platform

// site.go — a deploy of a repository in the org's own code workspace.
//
// An org the estate's table does not name keeps its code in a namespace the
// platform made for it on the forge (forge/workspace.go). A deploy naming a
// repository there is not built by the BuildKit lane: that lane runs on the
// estate's CI pool with the forge's own read credential, and tenant code does
// not run there. It is published instead the way every such project is — built
// from its repository in a sandbox by the agent app and served at the project's
// own address by the project app — and the builds board lists it beside the
// org's other builds.
//
// A forge address in any other namespace is not the org's, and is refused
// before anything is built: the org is the caller's validated one, and only its
// own record names its workspace.

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// siteCall bounds one call to the project or agent app on this path.
const siteCall = 20 * time.Second

// forgeAddress reads owner and name out of an https clone address on the forge
// host, and false for anything else: another host, a credential, a query, or a
// path that is not exactly owner/name.
func forgeAddress(raw string) (owner, name string, ok bool) {
	if forgeHost == "" {
		return "", "", false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, forgeHost) {
		return "", "", false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", true
	}
	owner, name, found := strings.Cut(strings.Trim(u.Path, "/"), "/")
	name = strings.TrimSuffix(name, ".git")
	if !found || strings.Contains(name, "/") || !ghName.MatchString(owner) || !ghName.MatchString(name) {
		return "", "", true
	}
	return owner, name, true
}

// workspaceOf is org's made workspace, as the project app records it; an empty
// Owner when org has none.
func workspaceOf(org string) (forge.Workspace, error) {
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), org), siteCall)
	defer cancel()
	out, err := projectpeer.ProjectsWorkspace(ctx, &client.WorkspaceIn{})
	if err != nil {
		return forge.Workspace{}, err
	}
	if out == nil {
		return forge.Workspace{}, nil
	}
	return forge.Answered(out.Owner, out.Made)
}

// declareSite answers a deploy whose repository is on the forge, for an org
// the estate's table does not name, and reports whether it did. Every other
// deploy — the estate's own, and any repository that is not on the forge — goes
// on exactly as it did.
//
// org is the caller's validated IAM org and dir the directory the deploy is
// for, which differs from org's own only for a SuperAdmin acting as another. The
// workspace record is keyed by the IAM org, so an act-as is not published here;
// and it may not hand a customer's workspace to the lane below either, so an
// act-as naming a forge namespace that is not the estate's is refused.
func declareSite(c *zip.Ctx, org, dir string, req declareReq) (bool, error) {
	owner, name, onForge := forgeAddress(req.Repo)
	if !onForge {
		return false, nil
	}
	if dir != namespace.Sanitize(org) {
		if _, err := forge.Org(owner); err == nil {
			return false, nil
		}
		return true, zip.ErrForbidden("a repository in an organization's own code workspace is published by that organization")
	}
	if _, err := forge.Owner(org); err == nil {
		return false, nil
	}
	ws, err := workspaceOf(org)
	if err != nil {
		return true, zip.Errorf(http.StatusServiceUnavailable, "this organization's code workspace could not be read: %v", err)
	}
	if owner == "" || !ws.Made || !strings.EqualFold(owner, ws.Owner) {
		return true, zip.ErrNotFound("repository not found among this organization's repositories")
	}
	if strings.TrimSpace(req.Dockerfile) != "" || len(req.Env) > 0 || req.Port != 0 || req.Replicas != 0 || strings.TrimSpace(req.Host) != "" {
		return true, zip.ErrBadRequest("a repository in this organization's workspace is published as its project's site: " +
			"it is built by its own package scripts and served at the project's address, so dockerfile, env, port, replicas and host do not apply")
	}
	project := cmp.Or(strings.TrimSpace(req.PartOf), strings.TrimSpace(req.Name), name)
	if !appNameRE.MatchString(project) {
		return true, zip.ErrBadRequest("partOf must name the project, a DNS-1123 label")
	}
	build := req.Tag == ""
	if req.Build != nil {
		build = *req.Build
	}
	if !build {
		return true, shipped(c, org, project, req)
	}
	// The person publishing rides the call (cloud.Detach): the build's sandbox is
	// leased as theirs and paid from their wallet, and a call naming only the
	// tenant names no wallet, so its lease was refused and the build never ran.
	ctx, cancel := context.WithTimeout(plane.For(cloud.Detach(context.Background(), c), org), siteCall)
	defer cancel()
	pub, err := plane.Call[client.PublishIn, client.Publishing](ctx, agentpeer.App, client.AgentsCodingPublish,
		&client.PublishIn{Project: project, Owner: ws.Owner, Repo: name, Ref: strings.TrimSpace(req.Ref)})
	if err != nil {
		return true, err
	}
	return true, c.JSON(http.StatusAccepted, declareResp{
		App: Declaration{Name: project, Org: org, PartOf: project, Hosts: []string{}},
		Build: &buildRef{
			ID: pub.Deployment, Status: pub.Status,
			Repo: "https://" + forgeHost + "/" + ws.Owner + "/" + name + ".git", Ref: pub.Ref,
		},
		Declaration: declareResult{Mode: modeCommit, Ref: pub.Ref, Live: true},
		Notice:      "Building " + project + " from " + pub.Ref + " in a sandbox; it goes live at the project's address when the build finishes.",
	})
}

// shipped answers the release of a build this path made: the site went live
// when its build finished, so there is nothing to write — only the build's
// state to say.
func shipped(c *zip.Ctx, org, project string, req declareReq) error {
	b, err := siteBuild(org, strings.TrimSpace(req.Tag))
	if err != nil {
		return err
	}
	if b == nil || b.Project != project {
		return zip.ErrNotFound("no build " + req.Tag + " of " + project)
	}
	switch b.Status {
	case "live":
		return c.JSON(http.StatusAccepted, declareResp{
			App:         Declaration{Name: project, Org: org, PartOf: project, Hosts: []string{}},
			Declaration: declareResult{Mode: modeCommit, Live: true},
			Notice:      project + " went live when build " + b.ID + " finished; there is nothing more to ship.",
		})
	case "error":
		return zip.ErrConflict("build " + b.ID + " failed: " + cmp.Or(b.Message, "no reason given"))
	}
	return zip.ErrConflict("build " + b.ID + " is still building")
}

// siteBuild is one of org's sandbox builds by id, nil when it has none.
func siteBuild(org, id string) (*client.SiteBuild, error) {
	all, err := siteBuilds(org)
	if err != nil {
		return nil, err
	}
	for _, b := range all {
		if b.ID == id {
			return &b, nil
		}
	}
	return nil, nil
}

// siteBuilds is org's sandbox builds, newest first. The estate has none, and
// neither has a deployment that does not run the project app, which is where
// every one of them is recorded.
func siteBuilds(org string) ([]client.SiteBuild, error) {
	if _, err := forge.Owner(org); err == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), org), siteCall)
	defer cancel()
	out, err := projectpeer.ProjectsSiteBuilds(ctx, &client.SiteBuildsIn{})
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil, nil
	case err != nil:
		return nil, zip.Errorf(http.StatusBadGateway, "this organization's site builds could not be read: %v", err)
	}
	return out.Builds, nil
}

// siteRow is a sandbox build as the builds board shows every build: building,
// succeeded or failed.
func siteRow(b client.SiteBuild) buildRow {
	status := "building"
	switch b.Status {
	case "live":
		status = "succeeded"
	case "error":
		status = "failed"
	}
	return buildRow{
		ID: b.ID, Repo: b.Repo, Commit: shortCommit(b.Commit), Status: status,
		StartedAt: text.Unix(b.CreatedAt), Duration: buildDuration(status, b.CreatedAt, b.UpdatedAt),
	}
}
