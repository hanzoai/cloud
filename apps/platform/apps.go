// apps.go — /v1/platform/apps: deploy through the ONE delivery plane.
//
// An app here is a values file in universe reconciled by cd.hanzo.ai. Declaring
// one is the whole of deploying it, so this surface is exactly three moves:
// build the repository into an image, write the declaration that names it, and
// report what the delivery plane has done with it.
//
// ── how this differs from its two siblings, so nobody builds a fourth ───────
//
//	/v1/platform/apps                    THIS. Declarations in universe git,
//	                                     reconciled by cd.hanzo.ai. The one
//	                                     delivery plane.
//	/v1/platform/projects/:project/apps  The operator lane: a Service CR applied
//	                                     directly to the cluster by this process's
//	                                     own reconciler. A SECOND deployer, and it
//	                                     predates the decision that cd.hanzo.ai is
//	                                     the only one. It should converge onto this
//	                                     surface; until it does, both exist and
//	                                     this comment is how a reader knows which
//	                                     is which.
//	/v1/platform/fleet                   OBSERVATION, not delivery: the running
//	                                     workloads of the platform's own tier and
//	                                     where they have drifted from what they
//	                                     declare. It deploys nothing.
//
// Static sites are NOT here. /v1/project/sites already serves them (apps/projects,
// S3-backed), and bucket listing is already /v1/s3/buckets (apps/s3). Adding
// either name under this prefix would be a second address for one fact.
//
// ── the two things a caller may never choose ────────────────────────────────
//
// The DIRECTORY, because the ApplicationSet derives the AppProject fence from it
// (declare.go). And the IMAGE REPOSITORY, because a declaration is what the
// cluster pulls: a caller who could name it could point a namespace it is allowed
// to write at an image it is not allowed to build. Both are derived here from the
// validated principal and neither is a field of the request.
//
// An org is its name, so the org IS the directory, the namespace and the fence
// — one value. `org` on a request is therefore not a placement field but an
// ACT-AS: a SuperAdmin may name any org, including a reserved one; everyone else
// gets their own and naming another is refused, not silently downgraded.

package platform

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// appsRoutes registers the delivery surface. It takes BOTH services because the
// two halves read different planes: declarations come from git (the platform
// service, which holds the KMS client a declared secret is sealed with) and
// reconciliation comes from the cluster (the fleet service, which holds the
// dynamic client). Joining them is this surface's whole job, so it holds both
// rather than either half growing a copy of the other's client.
//
// The prefix is spelled whole here, in the file that registers under it, so the
// leaves below compose under it and cmd/zipdoc files their prose at that path.
func appsRoutes(app cloud.Router, s *cloud.Service[state], fs *cloud.Service[fleetState]) {

	d := delivery{s: s, fs: fs}

	g := app.Group("/v1/platform")
	// The WRITE stays raw and says why at delivery_wire_test.go: cloud.Guard
	// answers before anything is read, while a typed op decodes the body first — so
	// an anonymous caller sending a malformed declaration would be told its JSON is
	// bad rather than that it may not declare. Error precedence is wire.
	//
	// EVERY MEMBER MAY DEPLOY, and where it lands is the difference: an org admin
	// commits to universe main and CD applies it, anyone else opens a review
	// branch and deploys nothing until it is merged. declareApp decides which.
	g.Raw(http.MethodPost, "/apps", cloud.Guard(cloud.Member, cloud.Handle(s, declareApp)))

	// The READS are typed ops. They hold BOTH services through one receiver, for
	// the reason the closures did: a global set in Mount is nil in every process
	// that did not run this Mount, and a board reading nil renders an empty
	// delivery plane rather than an unreadable one.
	g.Get("/apps", d.listDeclarations)
	g.Get("/apps/:app", d.getDeclaration)
	g.Get("/apps/:app/cd", d.getReconciliation)
	g.Get("/cd", d.listReconciliations)

	// Projects (project.go): the declarations grouped by the project each names,
	// and the writes that move a declaration's partOf. Every one gates inside, so
	// MCP and the CLI meet the same admin check a route does.
	g.Get("/projects", d.listProjects)
	g.Get("/projects/:project", d.getProject)
	g.Post("/projects", d.createProject, zip.WithStatus(http.StatusCreated))
	g.Put("/projects/:project", d.renameProject)
	g.Delete("/projects/:project", d.deleteProject)
	g.Put("/apps/:app/project", d.moveApp)

	// Sync: what a sync of one app would change, and the sync itself — the deploy
	// step every manual Application waits on. SuperAdmin, like CD's own RBAC.
	g.Get("/apps/:app/diff", d.getDiff)
	g.Post("/apps/:app/sync", d.syncApp, zip.WithStatus(http.StatusAccepted))
}

// ── the request ──────────────────────────────────────────────────────────────

// declareReq is one deploy.
//
// There is no `namespace`, no `project` and no `image` field, and their absence
// is the security property: see the header. `tier` is the one lever over
// placement and it is SuperAdmin-only.
type declareReq struct {
	// Repo is what to build: a GitHub repository as `owner/name` or
	// `https://github.com/owner/name` (or as this deployment's own git address
	// naming an owner other than the caller's org, which is how the builder sends
	// one), or another https clone URL. A GitHub
	// repository must be one the caller may see — their own connected GitHub, or
	// the org's installations for an org admin — and is 404 otherwise; any other
	// URL is an org admin's to build. Required unless this is a release of an
	// image a previous call already built (Tag set, Build false).
	Repo string `json:"repo,omitempty"`
	// Ref is the branch, tag or sha to build. Defaults to the GitHub
	// repository's default branch, and to main for any other URL.
	Ref string `json:"ref,omitempty"`
	// Dockerfile is a path relative to the repository root. Empty selects the
	// zero-config pack frontend, which detects the project's own build.
	Dockerfile string `json:"dockerfile,omitempty"`

	// Name is the app: the Helm release name, the file's basename, and every
	// object name the chart renders. Defaults to the repository's basename.
	// A DNS-1123 label — it is a Kubernetes object name, not a display string.
	Name string `json:"name,omitempty"`
	// Host is the public hostname. Defaults to <name>.<sites host>, and the name
	// is then claimed in the one namespace app and site names share: a name
	// another org, app or site holds makes the app <name>-<org> (numbered from 2
	// when that is taken too), and the answer's notice says so. A host named here
	// that is taken is 409. A non-SuperAdmin may name no other
	// host under the sites host; a custom domain is claimed and verified through
	// /v1/platform/projects/.../domains first, because serving a host is not the
	// same as owning it.
	Host string `json:"host,omitempty"`
	// Env is the container environment, written on CREATE only. Changing it later
	// is an edit to the declaration, which is a reviewed file — see checkDeclared.
	Env []declareEnv `json:"env,omitempty"`
	// Port is the container port. Defaults to 3000.
	Port int `json:"port,omitempty"`
	// Replicas defaults to 1.
	Replicas int `json:"replicas,omitempty"`
	// PartOf is the project the app belongs to, written as the file's `partOf`.
	// It defaults to the app's own name — a project of one — on create. On a
	// release of an existing app it must match what the file says, or be omitted:
	// moving an app between projects is PUT /v1/platform/apps/{app}/project.
	PartOf string `json:"partOf,omitempty"`

	// Mode is where the declaration is pushed: "branch" opens a review and
	// deploys NOTHING, "commit" writes universe main and CD reconciles it. An org
	// admin commits unless they ask for a branch; anyone else always opens a
	// review, and asking for "commit" is refused rather than downgraded.
	Mode string `json:"mode,omitempty"`
	// Tag names an already-built image to declare. Set it to RELEASE a build that
	// a previous call produced; omit it and this call builds one.
	Tag string `json:"tag,omitempty"`
	// Build launches a build. It defaults to true when Tag is empty and false
	// when it is set, so neither the deploy case nor the release case has to say
	// so — stating it is only for the case that wants the other.
	Build *bool `json:"build,omitempty"`

	// Org is who this app belongs to, which is also its directory, its namespace
	// and its fence. It defaults to the caller's own org; naming ANOTHER is an
	// act-as and is SuperAdmin only. A reserved org — the platform's own
	// namespace family — is SuperAdmin only for the same reason, on read as well
	// as on write.
	Org string `json:"org,omitempty"`
}

// builds reports whether this deploy launches a build: Build when it is stated,
// otherwise exactly when no Tag names an image already built.
func (r declareReq) builds() bool {
	if r.Build != nil {
		return *r.Build
	}
	return r.Tag == ""
}

// buildRef is the build this call launched, if it launched one.
type buildRef struct {
	ID     string `json:"id"`
	Job    string `json:"job"`
	Image  string `json:"image"`
	Status string `json:"status"` // "building" — it is asynchronous and always is
	// Repo is the clone URL that was built: a GitHub repository resolved to
	// where it lives now.
	Repo string `json:"repo"`
	// Ref is the branch, tag or sha that was built.
	Ref string `json:"ref"`
}

// declareResp is what one deploy did. All three parts are reported separately
// because they can disagree: a build can be running while the declaration that
// names its output already sits on a branch waiting for it.
type declareResp struct {
	App         Declaration   `json:"app"`
	Build       *buildRef     `json:"build,omitempty"`
	Declaration declareResult `json:"declaration"`
	// Notice says what the caller may not expect. To a deploy whose name was
	// taken: the name and host the app has instead. To a member's deploy, which
	// opens a review: that it deploys nothing until merged, when its branch is
	// removed, and that its public env values are cleartext on that branch.
	// Absent on an admin's deploy under the name it asked for.
	Notice string `json:"notice,omitempty"`
}

// ── deploy ───────────────────────────────────────────────────────────────────

func declareApp(s *cloud.Service[state], c *zip.Ctx) error {
	var req declareReq
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	super := principal.Super(c)
	// ADMIN is what decides where a deploy lands: an org admin (or platform sudo)
	// writes main, everyone else a review. It is read off the validated principal
	// and never off the body.
	admin := super || principal.IsOrgAdmin(c)

	dir, err := resolveOrg(org, req.Org, super)
	if err != nil {
		return err
	}
	// A repository in the org's own code workspace is published as its
	// project's site, built in a sandbox — never by the lane below (site.go).
	if handled, err := declareSite(c, org, dir, req); handled {
		return err
	}
	mode, err := declareModeFor(req.Mode, admin)
	if err != nil {
		return err
	}
	// The source is resolved BEFORE the name, which defaults to the repository's
	// own: a GitHub repository answers under the name it has now.
	src, err := resolveSource(c, org, dir, req.Repo, req.Ref, admin)
	if err != nil {
		return err
	}
	req.Repo, req.Ref = src.url, src.ref
	// A PRIVATE GITHUB REPOSITORY'S BUILD IS HANDED A TOKEN THAT READS IT (#766).
	// A customer's build holds no fabric credential (solveSecrets hands those to
	// the estate's builds only), so it is minted one of its own, as the caller,
	// NOW: a caller GitHub will not mint for is refused before a name is claimed
	// or a review reserved, not minutes later at the clone. Every way out of this
	// call that does not launch the build revokes it; a launched build's revoker
	// owns it from then on (launchDeclareBuild).
	var checkout *gitCheckout
	if src.private && req.builds() && laneOf(dir, cloud.Admin.Admits(cloud.AuthorityOf(c))) == customer {
		if checkout, err = mintCheckout(cloud.As(c, ""), src.owner, src.repo); err != nil {
			return err
		}
		defer func() {
			if checkout != nil {
				_ = revokeCheckout(context.WithoutCancel(c.Context()), checkout)
			}
		}()
	}
	name, err := declareName(req)
	if err != nil {
		return err
	}
	partOf := strings.TrimSpace(req.PartOf)
	if partOf != "" && !appNameRE.MatchString(partOf) {
		return zip.ErrBadRequest("partOf must be a DNS-1123 label")
	}
	// A deploy that named no host takes the first of its name and the org's own
	// spellings of it that is free, or already this app's (#769): "Add to project"
	// names the app after the repository, and a repository called `site` is not a
	// reason to refuse it. A host the caller NAMED is theirs to have or not.
	asked := name
	if strings.TrimSpace(req.Host) == "" {
		if name, err = ownName(s, c.Context(), dir, name); err != nil {
			return err
		}
	}
	host, err := declareHost(s, name, req.Host, super)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.Host) != "" && host == defaultHost(s, name) {
		if err := claim(s, c.Context(), dir, name, holder(name)); err != nil {
			return err
		}
	}
	build := req.builds()
	if !build && req.Tag == "" {
		return zip.ErrBadRequest("build is false and no tag is given — nothing names an image to declare")
	}
	if build && strings.TrimSpace(req.Repo) == "" {
		return zip.ErrBadRequest("repo is required to build")
	}
	if !admin {
		// A MEMBER'S DEPLOY IS A REVIEW, so nothing it carries may take effect
		// before the review does. A sealed env value would: the seal writes the
		// running app's own KMS record now, ahead of any merge. Public values ride
		// the reviewed file; a secret is set by an admin.
		for _, e := range req.Env {
			if !e.Public {
				return zip.ErrForbidden("env " + e.Name + " would be sealed into the running app's secrets before review; " +
					"a member's deploy carries public configuration only, and an org admin sets secrets")
			}
		}
		// A MEMBER'S DEPLOY IS A BUILD OF A REPOSITORY THEY CAN SEE, and nothing
		// wider. Releasing an image built earlier names no source they were
		// checked against, and it opens a review branch with no build behind it.
		if !build {
			return zip.ErrForbidden("a member deploys by building a repository; releasing an existing image is an org admin's")
		}
	}
	// A MEMBER'S DEPLOY IS BOUNDED BY WHO ASKED, not only by the org: a rate and a
	// cap on the review branches they hold (review.go). It names its person, so a
	// principal that names none is not a member here.
	member := ""
	if !admin {
		if member = strings.TrimSpace(c.User()); member == "" {
			return principal.Refused(c)
		}
	}

	port := req.Port
	if port == 0 {
		port = declarePort
	}
	if port < 1 || port > 65535 {
		return zip.ErrBadRequest("port must be 1..65535")
	}
	replicas := req.Replicas
	if replicas == 0 {
		replicas = 1
	}
	if replicas < 0 || replicas > 100 {
		return zip.ErrBadRequest("replicas must be 0..100")
	}
	for _, e := range req.Env {
		if strings.TrimSpace(e.Name) == "" {
			return zip.ErrBadRequest("every env entry needs a name")
		}
	}
	// SPLIT BEFORE ANYTHING IS RENDERED. A values file is committed to universe
	// git, so a credential written into one is cleartext in git HISTORY forever,
	// readable by everyone who can read the repository and unrecoverable by
	// deletion. That is strictly worse than the database case the operator lane
	// had, and it is why this split is here rather than deeper: nothing that
	// could reach render() ever holds credential material.
	plain, secretKeys, err := splitSecretEnv(s, c, dir, name, req.Env)
	if err != nil {
		return err
	}
	// The caller's OWN assertion of what may be written in the clear, recorded
	// straight from the request and never re-derived from what the split
	// produced — see declareSpec.Public.
	var public []string
	for _, e := range req.Env {
		if e.Public {
			public = append(public, e.Name)
		}
	}

	spec := declareSpec{
		Name:       name,
		Org:        dir,
		PartOf:     partOf,
		Repository: declareRepository(s, dir, name),
		Tag:        req.Tag,
		Hosts:      []string{host},
		Env:        plain,
		SecretKeys: secretKeys,
		Public:     public,
		Port:       port,
		Replicas:   replicas,
		// A declaration this API writes is CD-automated: it carries no operator
		// App CR, so nothing else writes its objects — the exact condition the
		// ApplicationSet names for enabling it. On a branch it is inert either
		// way; on main it is what makes the merge mean something.
		Automated: true,
		Origin:    src.origin,
	}

	resp := declareResp{}
	if name != asked {
		resp.Notice = renamedNotice(s, asked, name)
	}
	id := genID("bld")
	if member != "" {
		// The review is reserved BEFORE the build, so the bounds hold for the thing
		// that costs — a member's build — and not only for the branch after it. It
		// names the branch declare pushes to: this build's tag is its last segment.
		held := review{Branch: declareBranch(dir, name, id), Org: dir, User: member, App: name, Tag: id}
		if err := reserveMember(s, c.Context(), held); err != nil {
			return err
		}
		resp.Notice = strings.TrimSpace(resp.Notice + " " + memberNotice(public))
		defer func() {
			if resp.Build == nil {
				// Nothing was built, so nothing will be pushed: give the place back.
				if err := s.State.store.releaseReview(context.WithoutCancel(c.Context()), dir, held.Branch); err != nil {
					s.Log.Warn("release a member's review", "org", dir, "err", err)
				}
			}
		}()
	}
	if build {
		b, err := launchDeclareBuild(s, c, req, dir, spec.Repository, name, id, member, checkout)
		if err != nil {
			return err
		}
		checkout = nil // the launched build's revoker holds it now
		resp.Build = b
		spec.Tag = b.ID
		// AN ADMIN'S DEPLOY COMMITS THE TAG ITS OWN BUILD WILL PUSH. Main is
		// written now, naming an image that exists once the build lands, and CD's
		// pull retries until it does — so the deploy is one call, as the org admin
		// asked for. What that trades is stated plainly: until the build is green
		// the new pods cannot pull, a rolling update keeps the old ones serving,
		// and a build that FAILS leaves main naming an image that will never exist
		// until the next deploy moves the tag. A release of an image built earlier
		// (Tag given) still proves it pullable before main is pointed at it.
		spec.Building = mode == modeCommit
	}
	if !isTag(spec.Tag) {
		return zip.ErrBadRequest("tag must be a valid image tag: [A-Za-z0-9_][A-Za-z0-9._-]{0,127}")
	}

	res, err := declare(s, c.Context(), spec, mode)
	if err != nil {
		// The build (if any) is already launched and its record stands. Saying so
		// matters: a caller that reads "failed" and retries would otherwise build
		// twice for one deploy.
		return zip.Errorf(universeStatus(err), "declare %s/%s: %v", dir, name, err)
	}
	resp.App, resp.Declaration = res.Declaration, res
	return c.JSON(http.StatusAccepted, resp)
}

// launchDeclareBuild runs the repository through the SAME privileged BuildKit
// lane POST /v1/build drives — launchDirectBuild, the choke point where the clone,
// the GitHub read credential and the job spec are decided once for every build —
// and records it the way that entry does, so it is listed beside every other
// build. The output image is the one derived above, never a caller's string.
//
// It does not go through build() itself, and the reason is the registry bound, not
// a preference: build() confines a pushed image to a namespace the CREDENTIAL's
// org owns (imageInOrgRegistry), which only the registry brands have, while a
// declaration's image is <prefix>/<org>/<app> — derived here and injective, so no
// org can derive another's. Routed through build(), every customer org's deploy
// would be refused.
//
// checkout, when set, is the token declareApp minted for a PRIVATE GitHub
// repository built on the customer lane (checkout.go): it reads that repository
// and nothing else. The build is handed it, and once the build is launched a
// revoker ends it when the Job is done. A build that does not launch leaves it
// to the caller to revoke. The estate lane keeps its standing credential
// (githubRead) and is handed none.
func launchDeclareBuild(s *cloud.Service[state], c *zip.Ctx, req declareReq, org, repository, name, id, member string, checkout *gitCheckout) (*buildRef, error) {
	image := repository + ":" + id
	url, dockerfile, ref, image, err := validateBuildInputs(req.Repo, req.Dockerfile, req.Ref, image)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if ref == "" {
		ref = "main"
	}
	if !imageAllowed(image) {
		return nil, zip.ErrForbidden("the derived image is not in a registry this deployment may push to: " + image)
	}
	// Charged to the ORG that asked, not to the fabric pool: the concurrency
	// ceiling is per-org, so one org looping deploys can only exhaust its own
	// share instead of locking every other org out of building (red F3). A
	// member's build waits behind its org's admins' (dispatch.go).
	//
	// Built on the estate lane only when org is an estate org AND the caller
	// administers it (laneOf); a member's build is a customer's wherever it lands.
	l := laneOf(org, cloud.Admin.Admits(cloud.AuthorityOf(c)))
	job, err := s.State.k8s.launchCheckedBuild(c.Context(), l, org, member, url, ref, image, dockerfile, "", "", id, checkout, nil, nil)
	if err != nil {
		s.Log.Error("declare build failed to launch", "app", name, "err", err)
		if errors.Is(err, errNoCheckout) {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "could not launch the build: %v", err)
		}
		return nil, zip.Errorf(deployErrStatus(err), "could not launch the build: %v", err)
	}
	if checkout != nil {
		go s.State.k8s.revokeWhenDone(job, checkout, s.Log.Warn)
	}
	// Recorded under the org the way POST /v1/build records its own, so a deploy's
	// build is listed and read back beside every other. Best-effort: a record miss
	// must not fail a launched build.
	now := time.Now().Unix()
	if err := s.State.store.InsertBuild(c.Context(), Build{
		ID: id, Org: org, Status: "queued", Image: image, JobName: job, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		s.Log.Warn("declare build record insert failed (build already launched)", "job", job, "err", err)
	}
	return &buildRef{ID: id, Job: job, Image: image, Status: "building", Repo: url, Ref: ref}, nil
}

// buildFrom is what a deploy builds: the clone URL, the ref, and the address the
// declaration records it came from. A GitHub repository also names where it lives
// and whether it is private, which decides whether its build is handed a read
// token (checkout.go).
type buildFrom struct {
	url, ref, origin string
	owner, repo      string
	private          bool
}

// ghName is GitHub's shape for an account or repository name.
var ghName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// githubRepoOf reads a GitHub repository out of what a caller typed:
// `owner/name`, or `https://github.com/owner/name` with or without `.git`.
// isGitHub is true whenever the text NAMES GitHub, so a malformed GitHub address
// is refused rather than falling through to be cloned as some other URL.
func githubRepoOf(repo string) (owner, name string, isGitHub bool, err error) {
	s := strings.TrimSpace(repo)
	rest, hosted := "", false
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			rest, hosted = s[len(prefix):], true
			break
		}
	}
	if !hosted {
		if strings.Contains(s, "://") || strings.HasPrefix(s, "/") || strings.Count(s, "/") != 1 {
			return "", "", false, nil
		}
		rest = s
	}
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	owner, name, ok := strings.Cut(rest, "/")
	if !ok || !ghName.MatchString(owner) || !ghName.MatchString(name) {
		return "", "", true, zip.ErrBadRequest("repo names GitHub but not a repository: want owner/name")
	}
	return owner, name, true, nil
}

// ownGitAddress reads owner and name out of a clone address on this deployment's
// own git surface, https://<cloud.Domain()>/v1/git/<owner>/<name>[.git], and
// false for anything else: another host, a port, a credential, a query, or the
// three-segment project form.
func ownGitAddress(raw string) (owner, name string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" ||
		!strings.EqualFold(u.Hostname(), cloud.Domain()) {
		return "", "", false
	}
	rest, found := strings.CutPrefix(u.Path, "/v1/git/")
	if !found {
		return "", "", false
	}
	owner, name, found = strings.Cut(strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git"), "/")
	if !found || strings.Contains(name, "/") || !ghName.MatchString(owner) || !ghName.MatchString(name) {
		return "", "", false
	}
	return owner, name, true
}

// resolveSource decides what a deploy builds, and whether this caller may.
//
// A GITHUB REPOSITORY is looked up in the caller's own view — the one the
// repository listing answers from: their connected GitHub, else the org's
// installations for an org admin — and is 404 when it is not there, whether it
// exists or not. It is then built at the name GitHub gives it NOW, from its
// default branch unless a ref was named.
//
// THIS DEPLOYMENT'S OWN GIT ADDRESS NAMING ANOTHER OWNER is a GitHub repository
// too (#766). The builder (hanzoai/build) sends a GitHub row as
// `https://<api>/v1/git/<owner>/<name>.git`, but the first segment there is a
// HANZO org: for a GitHub owner that is not the caller's org it names nothing the
// build can fetch, and the build failed minutes later with "not found". It is read
// as the GitHub repository it spells, through the same lookup, so it is never
// wider than `owner/name`. The org's own address is left as it was.
//
// ANY OTHER URL is an org admin's. The build Job clones with the fabric's own
// credentials (the forge's, and the GitHub read token for the repositories it is
// bound to), so a member naming an address they cannot read would be reading it
// through the build — the lookup above is what stands between a member and that
// for GitHub, and nothing stands there for an arbitrary URL.
func resolveSource(c *zip.Ctx, org, dir, repo, ref string, admin bool) (buildFrom, error) {
	repo, ref = strings.TrimSpace(repo), strings.TrimSpace(ref)
	if repo == "" {
		return buildFrom{ref: ref}, nil
	}
	owner, name, isGitHub, err := githubRepoOf(repo)
	if err != nil {
		return buildFrom{}, err
	}
	if o, n, ok := ownGitAddress(repo); ok && !strings.EqualFold(o, org) && !strings.EqualFold(o, dir) {
		owner, name, isGitHub = o, n, true
	}
	if !isGitHub {
		if !admin {
			return buildFrom{}, zip.ErrForbidden(
				"a member deploys a GitHub repository their own GitHub reaches; building any other source is an org admin's")
		}
		return buildFrom{url: repo, ref: ref, origin: repo}, nil
	}
	ctx, cancel := context.WithTimeout(cloud.As(c, ""), grantBudget)
	defer cancel()
	got, err := providerpeer.IntegrationsGitHubRepo(ctx, &client.GitHubRepoIn{Owner: owner, Repo: name})
	if err != nil {
		return buildFrom{}, githubRefusal(owner, name, err)
	}
	if got == nil || !ghName.MatchString(got.Owner) || !ghName.MatchString(got.Repo) {
		return buildFrom{}, zip.Errorf(http.StatusBadGateway, "the GitHub lookup named no repository")
	}
	home := "https://github.com/" + got.Owner + "/" + got.Repo
	return buildFrom{url: home + ".git", ref: cmp.Or(ref, got.Base), origin: home,
		owner: got.Owner, repo: got.Repo, private: got.Private}, nil
}

// githubRefusal keeps the status the lookup answered with: a repository outside
// the caller's view is 404 to them, and a deployment that cannot ask GitHub is
// 503, never either of those dressed as the other.
func githubRefusal(owner, name string, err error) error {
	var he *zip.HTTPError
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return zip.Errorf(http.StatusServiceUnavailable, "GitHub is not reachable from this deployment")
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		return zip.ErrNotFound(fmt.Sprintf("GitHub repository %s/%s not found among the repositories you can deploy", owner, name))
	case errors.As(err, &he) && he.Status < 500:
		return zip.Errorf(he.Status, "GitHub repository %s/%s: %s", owner, name, he.Error())
	}
	return zip.Errorf(http.StatusBadGateway, "look up GitHub repository %s/%s: %v", owner, name, err)
}

// ── read ─────────────────────────────────────────────────────────────────────

// declaredResp is the board: what this org declares, and what CD did with it.
type declaredResp struct {
	// Org is the directory read — the caller's own, or another when a SuperAdmin
	// asked to act as it.
	Org  string     `json:"org"`
	Apps []declared `json:"apps"`
	// Unreadable are the org's values files that do not parse, so they are not
	// among apps. Absent when every file parsed.
	Unreadable []unreadableFile `json:"unreadable,omitempty"`
	CD         *unreadable      `json:"cdUnavailable,omitempty"`
}

// declared is one app: the declaration, and the reconciliation of it. `cd` is
// null when the delivery plane has no Application for this declaration — which
// is the normal state of a declaration that only exists on a branch.
type declared struct {
	Declaration
	CD *CDApp `json:"cd"`
}

// unreadable says WHY the reconciliation half of the board is missing, so a
// plane that could not be read never renders as "no app has been reconciled".
type unreadable struct {
	// Reason is why: the cause for a SuperAdmin, and which plane for anyone else.
	Reason string `json:"reason"`
}

// cdResp is the delivery plane as this caller may observe it.
type cdResp struct {
	Applications []CDApp `json:"applications"`
}

// splitSecretEnv seals every value into KMS EXCEPT the ones the caller declared
// public, and returns the public entries plus the sealed names.
//
// ★ SEAL BY DEFAULT. The operator lane classifies by shape because its output is
// a database column it can rewrite. This lane's output is a commit, and a commit
// cannot be unpublished — so the question is not "how good is the classifier"
// but "which way does it fail". Every shape rule fails both ways; seal-by-default
// chooses the direction. A miss here means an operator cannot read back a config
// value. A miss the other way means a password is in git history forever.
//
// It also ends the arms race. PGPASSWORD (libpq”'s own variable, one token to any
// splitter), *_PW, a symbol-rich password (stronger passwords were MORE likely to
// be published), a KUBECONFIG with base64 key material and no PEM armour, a
// base32 MFA seed — each slipped the classifier for a different reason, and each
// fix would have been a new special case. There is no case to miss when the
// default is to seal.
func splitSecretEnv(s *cloud.Service[state], c *zip.Ctx, org, app string, env []declareEnv) ([]declareEnv, []string, error) {
	var plain []declareEnv
	var keys []string
	// Stamp org onto the KMS plane context: declareApp is a raw *zip.Ctx handler,
	// so c.Context() carries no zip.Caller and KMSPeer would otherwise reach kms
	// with an empty org (falling back to the custodian brand org and refusing
	// every other tenant's orgs/<org>/platform/... ref with 403).
	kmsCtx := cloud.As(c, org)
	for _, e := range env {
		if e.Public {
			// Explicitly public — but a caller may not publish a credential by
			// asserting it is not one. The shape check survives HERE and only
			// here, where it can refuse and never permit: as a backstop on the
			// one path that writes cleartext to git, not as the thing that
			// decides what a secret is.
			if mustSeal(e.Name, e.Value) {
				return nil, nil, zip.ErrBadRequest("env " + e.Name +
					" is marked public but its value looks like a credential — refusing to write it to git; unmark it and it will be sealed")
			}
			plain = append(plain, e)
			continue
		}
		if strings.TrimSpace(e.Value) == "" {
			// Nothing to seal and nothing to leak. A named-but-empty secret would
			// otherwise mint a reference to a KMS record that does not exist.
			return nil, nil, zip.ErrBadRequest("env " + e.Name + " carries no value; mark it public if it is empty configuration")
		}
		if s.KMS == nil {
			return nil, nil, zip.Errorf(http.StatusServiceUnavailable,
				"env %s is a credential and this deployment has no KMS to seal it into — refusing to write it to git", e.Name)
		}
		if err := s.KMS.PutSecret(kmsCtx, kmsSecretRef(org, app, e.Name), []byte(e.Value)); err != nil {
			// The error names the KEY only. A 5xx body must never echo the value.
			s.Log.Error("seal declare secret", "org", org, "app", app, "key", e.Name, "err", err)
			return nil, nil, zip.Errorf(http.StatusBadGateway, "could not seal %s into KMS; nothing was written", e.Name)
		}
		keys = append(keys, e.Name)
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		// The tenant needs a KMS identity for the operator to authenticate with,
		// or the reference resolves to nothing and the pod waits on a Secret that
		// never arrives. Best-effort, exactly as the operator lane treats it: the
		// declaration is still correct, the sync reports pending.
		ensureTenantKMSAuth(s, kmsCtx, org)
	}
	return plain, keys, nil
}

// ── derivation (the security spine) ──────────────────────────────────────────

// resolveOrg resolves the values DIRECTORY — which is also the destination
// namespace and the AppProject fence, because an org is its name and that name
// is all three.
//
// TWO REFUSALS, and they are different questions. Naming ANOTHER org is an
// act-as and belongs to platform sudo. Naming a RESERVED org is reaching into
// the platform's own namespace family — the control plane, the delivery plane,
// the brands, the reserved admin org — and belongs there too, even when it IS
// the caller's own org, because a caller whose IAM org is `kube-system` is not
// thereby the owner of Kubernetes.
//
// Both refuse rather than downgrade. Quietly substituting the caller's own org
// for one it asked for would make an escape attempt indistinguishable from a
// normal request, in the logs and in the response.
func resolveOrg(own, asked string, super bool) (string, error) {
	dir := namespace.Sanitize(own)
	if dir == "" {
		return "", zip.ErrForbidden("the caller's organization does not resolve to a name")
	}
	if a := strings.TrimSpace(asked); a != "" {
		// A directory is taken as named; anything else is an IAM org name and is
		// folded. The board answers directories, and folding one again is not the
		// identity — namespace.Sanitize re-suffixes a name shaped like its own
		// output — so a folded org handed its own directory back would 403.
		want := a
		if checkOrg(a) != nil {
			want = namespace.Sanitize(a)
		}
		if want == "" {
			return "", zip.ErrBadRequest("org does not resolve to a name")
		}
		if want != dir && !super {
			return "", zip.ErrForbidden("acting as another organization requires SuperAdmin")
		}
		dir = want
	}
	if reserved(dir) && !super {
		return "", zip.ErrForbidden(dir +
			" is the platform's own: its fence admits every namespace and cluster-scoped RBAC, so declaring there requires SuperAdmin")
	}
	if err := checkOrg(dir); err != nil {
		return "", zip.ErrBadRequest(err.Error())
	}
	return dir, nil
}

// declareRepository derives the image a declaration may name: <prefix>/<org>/<app>,
// for every org alike.
//
// It is INJECTIVE, which is the property that matters: org and app are both
// DNS-1123 labels and neither can contain the '/' that separates them, so the
// (org, app) pair is uniquely recoverable from the ref and two orgs can never
// derive one repository. That is what stops one org pushing an image another
// org's declaration would pull — the same guarantee k8sClient.buildImageRef
// carries, reached without prefixing anything onto the org's name.
func declareRepository(s *cloud.Service[state], org, name string) string {
	prefix := s.State.k8s.imagePrefix
	if prefix == "" {
		prefix = defaultBuildImagePrefix
	}
	return strings.TrimRight(prefix, "/") + "/" + org + "/" + name
}

// declareName resolves the app name: the caller's, or the repository's basename.
// It is a Kubernetes object name (the Helm release, the Deployment, the Service,
// the Ingress), so it is a DNS-1123 label and nothing else.
func declareName(req declareReq) (string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = repoBase(req.Repo)
	}
	name = strings.ToLower(name)
	if !slugRE.MatchString(name) {
		return "", zip.ErrBadRequest("name must be a DNS-1123 label of at most 40 characters: " + slugRE.String())
	}
	return name, nil
}

// repoBase is the repository's own name — the last path segment, without .git.
func repoBase(repo string) string {
	u := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(repo), "/"), ".git")
	if u == "" {
		return ""
	}
	return path.Base(u)
}

// declareHost resolves the public hostname.
//
// A non-SuperAdmin gets the app's own name and nothing else under the sites
// host: serving a host is not the same as owning it, and the claim-and-verify
// flow for a custom domain already exists at
// /v1/platform/projects/:project/apps/:app/domains. A deploy endpoint that
// accepted an arbitrary host would be a way around it.
func declareHost(s *cloud.Service[state], name, host string, super bool) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		d := defaultHost(s, name)
		if !hostname(d) {
			return "", zip.ErrInternal("this app's name does not resolve to a hostname")
		}
		return d, nil
	}
	if !hostname(host) {
		return "", zip.ErrBadRequest("host is not a valid hostname")
	}
	if super {
		return host, nil
	}
	if host == defaultHost(s, name) {
		return host, nil
	}
	if underSitesApex(s, host) {
		return "", zip.ErrForbidden("a host under " + s.State.sitesHost + " is an app's own name; this app's is " + defaultHost(s, name))
	}
	return "", zip.ErrForbidden(
		"a custom domain is claimed and verified before it is served — add " + host +
			" through the app's domains surface first, then declare it")
}

// declareModeFor is where a deploy lands for this caller. An org admin commits
// to universe main unless they ask for a branch. Anyone else opens a review, and
// naming "commit" is REFUSED rather than downgraded: a caller who asked for main
// and quietly got a branch would read "deployed" where nothing was.
func declareModeFor(m string, admin bool) (declareMode, error) {
	mode, err := declareModeOf(m)
	switch {
	case err != nil:
		return "", err
	case strings.TrimSpace(m) == "" && admin:
		return modeCommit, nil
	case mode == modeCommit && !admin:
		return "", zip.ErrForbidden("writing universe main is an org admin's; omit mode and this deploy opens a review")
	}
	return mode, nil
}

// declareModeOf reads a mode as written, "branch" when absent — the default of
// every universe write that is not a deploy (the project edits).
func declareModeOf(m string) (declareMode, error) {
	switch strings.TrimSpace(m) {
	case "", string(modeBranch):
		return modeBranch, nil
	case string(modeCommit):
		return modeCommit, nil
	default:
		return "", zip.ErrBadRequest(`mode must be "branch" (opens a review and deploys nothing) or "commit" (writes main)`)
	}
}

// hostname is a conservative DNS name check: labels of alphanumerics and
// hyphens, at least two of them, nothing longer than the DNS limits.
func hostname(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			ch := l[i]
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

// isTag holds an OCI tag to its own grammar. The tag reaches a declaration the
// cluster pulls, so it is checked here as well as by validateImageRef on the
// build path — the release case (Tag given, no build) does not pass through that.
func isTag(t string) bool {
	if t == "" || len(t) > 128 {
		return false
	}
	c := t[0]
	if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
		return false
	}
	for i := 1; i < len(t); i++ {
		c := t[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// ── prose ────────────────────────────────────────────────────────────────────

func init() {
	// The body and the answer, declared: the route is raw (delivery_wire_test.go
	// says why), so without this the document names an address and nothing about
	// what it takes or what it answers.
	spec.Register("/v1/platform/apps", http.MethodPost, declareReq{}, declareResp{})
	spec.Describe("/v1/platform/apps", http.MethodPost,
		"Deploy an app through cd.hanzo.ai",
		"Builds a git repository into an image and writes the declaration that names it — a values "+
			"file in `hanzoai/universe` under `charts/app/values/<namespace>/<name>.yaml`, which the "+
			"`fleet` ApplicationSet renders as one Application. That file IS the deployment: nothing "+
			"else has to be applied.\n\n"+
			"`repo` is a GitHub repository — `owner/name` or `https://github.com/owner/name` — or another "+
			"https clone URL. A GitHub repository must be one the caller may see: their own connected "+
			"GitHub, or the installations their org holds for an org admin (the same set "+
			"`GET /v1/provider/github/repos` lists); anything else is 404. It is built at the name GitHub "+
			"gives it now, from `ref`, which defaults to its default branch. This deployment's own git "+
			"address naming an owner other than the caller's org (`https://<api host>/v1/git/<owner>/<name>.git`, "+
			"as the builder sends a GitHub repository) is read as that GitHub repository. A private GitHub "+
			"repository built for an org outside the estate is handed a token that reads that one repository, "+
			"for an hour at most, minted as the caller: their own GitHub must push to it, or they administer the "+
			"org whose installation reaches it. A caller GitHub mints no token for is refused before anything is "+
			"built. Any other URL is an org admin's to build.\n\n"+
			"`partOf` names the project the app belongs to, and defaults to the app's own name.\n\n"+
			"Every member of the org may deploy; WHERE it lands is the difference. An org admin commits "+
			"to universe main and CD applies it — in one call, naming the tag its own build will push, "+
			"which CD pulls once the build lands. Anyone else opens a review: the declaration is pushed "+
			"to `deploy/<namespace>/<name>/<tag>`, `declaration.review` is the link that opens the pull "+
			"request, and nothing deploys until it is merged. `mode` may say `branch` to make an admin's "+
			"deploy a review too; a non-admin naming `commit` is refused, never downgraded.\n\n"+
			"A member's deploy is bounded by who asked: ten deploys an hour and five open review branches. "+
			"Past a bound it answers 429. A review branch is deleted once main declares its tag, or "+
			"after seven days unmerged; `notice` says so, and that the public `env` values the branch "+
			"carries are cleartext in universe, where deleting the branch does not unpublish them.\n\n"+
			"A release of "+
			"an image an earlier call built (`tag` given) is proven pullable before main is pointed at "+
			"it — a declaration naming an image the registry cannot serve is an ImagePullBackOff with no "+
			"rollback path.\n\n"+
			"Omit `tag` to build; give it to declare an image an earlier call already built, which is "+
			"how a green build is released without rebuilding it. That release is an org admin's: a "+
			"member's deploy always builds, and carries public `env` only — a sealed value would write "+
			"the running app's secrets before the review is merged, so a member naming one is refused.\n\n"+
			"An org is its name: the values DIRECTORY, the destination NAMESPACE and the AppProject "+
			"FENCE are all `<org>`, and the image is `<registry>/<org>/<app>`. None of them is a "+
			"request field — the directory decides what CD admits the sync under and the repository "+
			"decides what the cluster pulls, so a caller who could name either could reach outside "+
			"its own org.\n\n"+
			"`org` is an ACT-AS, not a placement field: it defaults to the caller's own, and naming "+
			"another requires SuperAdmin. So does naming a RESERVED org — the platform's own namespace "+
			"family (the brands and their environments, the control and delivery planes, `admin`) — "+
			"even when it is the caller's own, because an IAM org named `kube-system` does not own "+
			"Kubernetes. Both refuse rather than downgrade, so an escape attempt is never "+
			"indistinguishable from a normal request.\n\n"+
			"The app answers at `<name>.<sites host>`, and its name is claimed in the one namespace app and "+
			"site names share, first come first served across every org. When another org, app or site holds "+
			"it, the app takes the first free one of `<name>-<org>`, `<name>-<org>-2` … `-5` instead, and "+
			"`notice` says so; the same call made again lands on the same app. A `host` named in the request "+
			"that is taken is 409. Any other host under the sites host is refused, and a custom domain is "+
			"claimed and verified first.")

	spec.Describe("/v1/platform/apps", http.MethodGet,
		"What this organization has declared, and what CD did with it",
		"Returns the declarations in the caller's own org directory, each joined with the Hanzo CD "+
			"Application reconciling it — sync verdict, health, the universe commit last applied. "+
			"`cd` is null for a declaration the delivery plane has no Application for, which is the "+
			"normal state of one that exists only on a branch.\n\n"+
			"If the delivery plane cannot be read, the declarations are still returned and "+
			"`cdUnavailable` says why. An unreadable plane never renders as \"nothing has been "+
			"reconciled\".")

	spec.Describe("/v1/platform/apps/:app", http.MethodGet,
		"One declaration",
		"The values file for one app as git declares it: image repository and tag, hosts, replicas, "+
			"and whether CD is automated on it. 404 when this organization declares no such app.")

	spec.Describe("/v1/platform/apps/:app/cd", http.MethodGet,
		"One app's reconciliation",
		"The Hanzo CD Application for one declaration, on its own — the poll a deploy view makes "+
			"while it waits, without re-reading the whole inventory. 404 while the declaration exists "+
			"only on a branch, because the generator reads main.")

	spec.Describe("/v1/platform/cd", http.MethodGet,
		"The delivery plane",
		"Every Hanzo CD Application this caller may observe, with its sync verdict, health, the "+
			"universe revision last applied, and whether automation and self-heal are on. A "+
			"SuperAdmin sees the fleet; an org admin sees only Applications whose destination "+
			"namespace IS its own organization, and never a reserved one.\n\n"+
			"A cluster with no CD installed answers an empty plane. A plane that cannot be READ "+
			"answers 503 and says why — the two are opposite facts and never share a shape.")
}
