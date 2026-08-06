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
// Static sites are NOT here. /v1/platform/sites already serves them (apps/projects,
// S3-backed), and bucket listing is already /v1/s3/buckets (apps/storage). Adding
// either name under this prefix would be a second address for one fact.
//
// ── the two things a caller may never choose ────────────────────────────────
//
// The DIRECTORY, because the ApplicationSet derives the AppProject fence from it
// (declare.go). And the IMAGE REPOSITORY, because a declaration is what the
// cluster pulls: a caller who could name it could point a namespace it is allowed
// to write at an image it is not allowed to build. Both are derived here from the
// validated principal and neither is a field of the request.

package platform

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// appsRoutes registers the delivery surface. It takes BOTH services because the
// two halves read different planes: declarations come from git (the platform
// service, which holds the KMS client the universe token is read with) and
// reconciliation comes from the cluster (the fleet service, which holds the
// dynamic client). Joining them is this surface's whole job, so it holds both
// rather than either half growing a copy of the other's client.
//
// FLAT paths, not a Group — for the reason fleet.go states: `Group("/v1/platform/
// apps").Get("")` composes to a literal trailing slash, and that slash is what the
// OpenAPI emitter publishes, so every generated SDK would call an address the
// manifest prefix does not name.
func appsRoutes(app cloud.Router, s *cloud.Service[state], fs *cloud.Service[fleetState]) {
	app.Post("/v1/platform/apps", cloud.Guard(cloud.Admin, cloud.Handle(s, declareApp)))
	app.Get("/v1/platform/apps/:app", cloud.Guard(cloud.Admin, cloud.Handle(s, getDeclared)))
	app.Get("/v1/platform/cd", cloud.Guard(cloud.Admin, cloud.Handle(fs, listCD)))
	app.Get("/v1/platform/ci", cloud.Guard(cloud.Admin, cloud.Handle(s, listCI)))

	// The two joining routes hold BOTH services, closed over here. Not a package
	// global reached at request time: a global set in Mount is nil in every
	// process that did not run this Mount, and a board reading nil renders an
	// empty delivery plane rather than an unreadable one.
	app.Get("/v1/platform/apps", cloud.Guard(cloud.Admin, func(c *zip.Ctx) error {
		return listDeclared(s, fs, c)
	}))
	app.Get("/v1/platform/apps/:app/cd", cloud.Guard(cloud.Admin, func(c *zip.Ctx) error {
		return getDeclaredCD(s, fs, c)
	}))
}

// ── the request ──────────────────────────────────────────────────────────────

// declareReq is one deploy.
//
// There is no `namespace`, no `project` and no `image` field, and their absence
// is the security property: see the header. `tier` is the one lever over
// placement and it is SuperAdmin-only.
type declareReq struct {
	// Repo is the https clone URL to build. Required unless this is a release of
	// an image a previous call already built (Tag set, Build false).
	Repo string `json:"repo,omitempty"`
	// Ref is the branch, tag or sha to build. Defaults to main.
	Ref string `json:"ref,omitempty"`
	// Dockerfile is a path relative to the repository root. Empty selects the
	// zero-config pack frontend, which detects the project's own build.
	Dockerfile string `json:"dockerfile,omitempty"`

	// Name is the app: the Helm release name, the file's basename, and every
	// object name the chart renders. Defaults to the repository's basename.
	// A DNS-1123 label — it is a Kubernetes object name, not a display string.
	Name string `json:"name,omitempty"`
	// Host is the public hostname. Defaults to <name>.<org>.<sites host>. A
	// non-SuperAdmin may only name a host inside its own org's subtree; a custom
	// domain is claimed and verified through /v1/platform/projects/.../domains
	// first, because serving a host is not the same as owning it.
	Host string `json:"host,omitempty"`
	// Env is the container environment, written on CREATE only. Changing it later
	// is an edit to the declaration, which is a reviewed file — see checkDeclared.
	Env []declareEnv `json:"env,omitempty"`
	// Port is the container port. Defaults to 3000.
	Port int `json:"port,omitempty"`
	// Replicas defaults to 1.
	Replicas int `json:"replicas,omitempty"`

	// Mode is where the declaration is pushed: "branch" (default) opens a review
	// and deploys NOTHING, "commit" writes main and CD reconciles it.
	Mode string `json:"mode,omitempty"`
	// Tag names an already-built image to declare. Set it to RELEASE a build that
	// a previous call produced; omit it and this call builds one.
	Tag string `json:"tag,omitempty"`
	// Build launches a build. It defaults to true when Tag is empty and false
	// when it is set, so neither the deploy case nor the release case has to say
	// so — stating it is only for the case that wants the other.
	Build *bool `json:"build,omitempty"`

	// Tier places the declaration in the PLATFORM's own directory rather than the
	// caller's tenant directory. SuperAdmin only: that directory's AppProject
	// admits every namespace and cluster-scoped RBAC. Values: "" (tenant) or
	// "platform".
	Tier string `json:"tier,omitempty"`
}

// buildRef is the build this call launched, if it launched one.
type buildRef struct {
	ID     string `json:"id"`
	Job    string `json:"job"`
	Image  string `json:"image"`
	Status string `json:"status"` // "building" — it is asynchronous and always is
}

// declareResp is what one deploy did. All three parts are reported separately
// because they can disagree: a build can be running while the declaration that
// names its output already sits on a branch waiting for it.
type declareResp struct {
	App         Declaration   `json:"app"`
	Build       *buildRef     `json:"build,omitempty"`
	Declaration declareResult `json:"declaration"`
}

// ── deploy ───────────────────────────────────────────────────────────────────

func declareApp(s *cloud.Service[state], c *zip.Ctx) error {
	var req declareReq
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	super := principal.IsSuperAdmin(c)

	ns, err := declareNamespace(s, org, req.Tier, super)
	if err != nil {
		return err
	}
	name, err := declareName(req)
	if err != nil {
		return err
	}
	mode, err := declareModeOf(req.Mode)
	if err != nil {
		return err
	}
	host, err := declareHost(s, org, name, req.Host, super)
	if err != nil {
		return err
	}
	build := req.Tag == ""
	if req.Build != nil {
		build = *req.Build
	}
	if !build && req.Tag == "" {
		return zip.ErrBadRequest("build is false and no tag is given — nothing names an image to declare")
	}
	if build && strings.TrimSpace(req.Repo) == "" {
		return zip.ErrBadRequest("repo is required to build")
	}
	// Building AND committing to main in one call can never succeed, so it is
	// refused BEFORE a privileged build is spent on it: a commit proves the image
	// pullable, and the image this call would build does not exist until the Job
	// finishes. The two-step is the real flow and the error names it — build here,
	// then commit that tag once the build is green.
	if build && mode == modeCommit {
		return zip.ErrBadRequest(
			"mode=commit proves the image is pullable, and a build launched by this same call has not produced one yet — " +
				"deploy first (the default branch mode returns the build's tag), then commit that tag once the build is green")
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

	spec := declareSpec{
		Name:       name,
		Namespace:  ns,
		Repository: declareRepository(s, ns, org, name),
		Tag:        req.Tag,
		Hosts:      []string{host},
		Env:        req.Env,
		Port:       port,
		Replicas:   replicas,
		// A declaration this API writes is CD-automated: it carries no operator
		// App CR, so nothing else writes its objects — the exact condition the
		// ApplicationSet names for enabling it. On a branch it is inert either
		// way; on main it is what makes the merge mean something.
		Automated: true,
		Origin:    strings.TrimSpace(req.Repo),
	}

	resp := declareResp{}
	if build {
		b, err := launchDeclareBuild(s, c, req, spec.Repository, name)
		if err != nil {
			return err
		}
		resp.Build = b
		spec.Tag = b.ID
	}
	if !isTag(spec.Tag) {
		return zip.ErrBadRequest("tag must be a valid image tag: [A-Za-z0-9_][A-Za-z0-9._-]{0,127}")
	}

	res, err := declare(s, c.Context(), spec, mode)
	if err != nil {
		// The build (if any) is already launched and its record stands. Saying so
		// matters: a caller that reads "failed" and retries would otherwise build
		// twice for one deploy.
		return zip.Errorf(http.StatusBadGateway, "declare %s/%s: %v", ns, name, err)
	}
	resp.App, resp.Declaration = res.Declaration, res
	return c.JSON(http.StatusAccepted, resp)
}

// launchDeclareBuild runs the repository through the SAME privileged BuildKit
// lane /v1/runner drives — one build path, one set of validations, one job spec.
// The output image is the one derived above, never a caller's string.
func launchDeclareBuild(s *cloud.Service[state], c *zip.Ctx, req declareReq, repository, name string) (*buildRef, error) {
	id, err := genID("bld")
	if err != nil {
		return nil, zip.ErrInternal("could not mint a build id")
	}
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
	job, err := s.State.k8s.launchDirectBuild(c.Context(), url, ref, image, dockerfile, id)
	if err != nil {
		s.Log.Error("declare build failed to launch", "app", name, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "could not launch the build: %v", err)
	}
	return &buildRef{ID: id, Job: job, Image: image, Status: "building"}, nil
}

// ── read ─────────────────────────────────────────────────────────────────────

// declaredResp is the board: what this org declares, and what CD did with it.
type declaredResp struct {
	// Namespace is the directory read — the caller's tenant directory, or the
	// platform's when a SuperAdmin asked for it.
	Namespace string      `json:"namespace"`
	Apps      []declared  `json:"apps"`
	CD        *cdPlaneErr `json:"cdUnavailable,omitempty"`
}

// declared is one app: the declaration, and the reconciliation of it. `cd` is
// null when the delivery plane has no Application for this declaration — which
// is the normal state of a declaration that only exists on a branch.
type declared struct {
	Declaration
	CD *CDApp `json:"cd"`
}

// cdPlaneErr says WHY the reconciliation half of the board is missing, so an
// unreadable plane never renders as "no app has been reconciled".
type cdPlaneErr struct {
	Reason string `json:"reason"`
}

func listDeclared(s *cloud.Service[state], fs *cloud.Service[fleetState], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	ns, err := declareNamespace(s, org, c.Query("tier"), principal.IsSuperAdmin(c))
	if err != nil {
		return err
	}
	ds, err := declarations(s, c.Context(), ns)
	if err != nil {
		s.Log.Error("inventory read failed", "namespace", ns, "err", err)
		return zip.Errorf(http.StatusBadGateway, "could not read the declarations in %s: %v", ns, err)
	}
	out := declaredResp{Namespace: ns, Apps: make([]declared, 0, len(ds))}
	for _, d := range ds {
		out.Apps = append(out.Apps, declared{Declaration: d})
	}
	// The join is best-effort BY DESIGN and says so when it is missing: the
	// declarations ARE the answer to "what have I deployed", and refusing the
	// whole board because the cluster is unreadable would lose the half that is
	// readable. What must never happen is a silent null — hence cdUnavailable.
	apps, cdErr := cdApps(fs, c.Context(), requestPrincipal(c))
	if cdErr != nil {
		out.CD = &cdPlaneErr{Reason: cdErr.Error()}
	} else {
		by := map[string]*CDApp{}
		for i := range apps {
			by[apps[i].Name] = &apps[i]
		}
		for i := range out.Apps {
			out.Apps[i].CD = by[out.Apps[i].Application]
		}
	}
	return c.JSON(http.StatusOK, out)
}

func getDeclared(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := c.Param("app")
	if !slugRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	ns, err := declareNamespace(s, org, c.Query("tier"), principal.IsSuperAdmin(c))
	if err != nil {
		return err
	}
	ds, err := declarations(s, c.Context(), ns)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not read the declarations in %s: %v", ns, err)
	}
	for _, d := range ds {
		if d.Name == name {
			return c.JSON(http.StatusOK, d)
		}
	}
	return zip.ErrNotFound("no declaration for " + name + " in " + ns)
}

// getDeclaredCD answers one app's reconciliation alone — the poll a deploy UI
// makes while it waits, without re-reading the whole inventory each time.
func getDeclaredCD(s *cloud.Service[state], fs *cloud.Service[fleetState], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := c.Param("app")
	if !slugRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	ns, err := declareNamespace(s, org, c.Query("tier"), principal.IsSuperAdmin(c))
	if err != nil {
		return err
	}
	apps, err := cdApps(fs, c.Context(), requestPrincipal(c))
	if err != nil {
		return err
	}
	want := ns + "-" + name
	for i := range apps {
		if apps[i].Name == want {
			return c.JSON(http.StatusOK, apps[i])
		}
	}
	return zip.ErrNotFound("the delivery plane has no Application " + want +
		" — a declaration on a branch has none until the branch is merged")
}

// cdResp is the delivery plane as this caller may observe it.
type cdResp struct {
	Applications []CDApp `json:"applications"`
}

func listCD(fs *cloud.Service[fleetState], c *zip.Ctx) error {
	apps, err := cdApps(fs, c.Context(), requestPrincipal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, cdResp{Applications: apps})
}

// listCI is DECLARED AND NOT IMPLEMENTED, and answers so.
//
// The forge's Actions surface needs a Forgejo API client, and this deployment
// has none — the only outbound forge interaction anywhere in this binary is the
// git CLI over https with a KMS-held token (pin.go). Returning an empty list
// would be the estate's own worst bug shape: a surface that reports success and
// does nothing, indistinguishable from a forge with no runs. 501 names what is
// missing instead.
func listCI(s *cloud.Service[state], c *zip.Ctx) error {
	return zip.Errorf(http.StatusNotImplemented,
		"platform ci: continuous integration is not wired — this deployment has no forge API client, "+
			"and answering an empty run list would be indistinguishable from a forge with no runs")
}

// ── derivation (the security spine) ──────────────────────────────────────────

// declareNamespace resolves the values DIRECTORY, which is the destination
// namespace AND the AppProject fence. It is derived from the validated org and
// never from a path, a header or a body field.
//
// The default is the caller's tenant directory. `tier=platform` selects the
// brand's own directory, whose AppProject admits every namespace and
// cluster-scoped RBAC — so it is SuperAdmin only, and a non-super caller asking
// for it is refused rather than quietly given its tenant directory.
func declareNamespace(s *cloud.Service[state], org, tier string, super bool) (string, error) {
	switch strings.TrimSpace(tier) {
	case "", "tenant":
		slug := namespace.Sanitize(org)
		if slug == "" {
			return "", zip.ErrForbidden("the caller's organization does not resolve to a namespace")
		}
		return "tenant-" + slug, nil
	case "platform":
		if !super {
			return "", zip.ErrForbidden(
				"tier=platform writes the platform's own delivery directory, whose fence admits every namespace and cluster-scoped RBAC: SuperAdmin required")
		}
		brand := namespace.Sanitize(s.Brand)
		if brand == "" {
			return "", zip.ErrInternal("this deployment declares no brand namespace")
		}
		return brand, nil
	default:
		return "", zip.ErrBadRequest(`tier must be "" (the caller's tenant) or "platform"`)
	}
}

// declareRepository derives the image a declaration may name.
//
// Tenant apps land under tenant-<org>/<app>, which is the injective per-tenant
// path the build lane already uses (k8sClient.buildImageRef): distinct tenants
// always target distinct repositories, so one tenant can never push an image
// another's declaration would pull. The platform tier keeps the flat repository
// its own services already publish to.
func declareRepository(s *cloud.Service[state], ns, org, name string) string {
	prefix := s.State.k8s.imagePrefix
	if prefix == "" {
		prefix = defaultBuildImagePrefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if strings.HasPrefix(ns, "tenant-") {
		return fmt.Sprintf("%s/tenant-%s/%s", prefix, namespace.Sanitize(org), name)
	}
	return prefix + "/" + name
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
// A non-SuperAdmin gets its own org's subtree and nothing else: serving a host
// is not the same as owning it, and the claim-and-verify flow for a custom
// domain already exists at /v1/platform/projects/:project/apps/:app/domains. A
// deploy endpoint that accepted an arbitrary host would be a way around it.
func declareHost(s *cloud.Service[state], org, name, host string, super bool) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return defaultHost(s, org, name), nil
	}
	if !hostname(host) {
		return "", zip.ErrBadRequest("host is not a valid hostname")
	}
	if super {
		return host, nil
	}
	if host == defaultHost(s, org, name) || isOrgSubtreeHost(s, org, host) {
		return host, nil
	}
	if underSitesApex(s, host) {
		return "", zip.ErrForbidden("hosts under " + s.State.sitesHost + " belong to their own organization's subtree")
	}
	return "", zip.ErrForbidden(
		"a custom domain is claimed and verified before it is served — add " + host +
			" through the app's domains surface first, then declare it")
}

func declareModeOf(m string) (declareMode, error) {
	switch strings.TrimSpace(m) {
	case "", string(modeBranch):
		return modeBranch, nil
	case string(modeCommit):
		return modeCommit, nil
	default:
		return "", zip.ErrBadRequest(`mode must be "branch" (default; opens a review and deploys nothing) or "commit" (writes main)`)
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
	openapi.Describe("/v1/platform/apps", http.MethodPost,
		"Deploy an app through cd.hanzo.ai",
		"Builds a git repository into an image and writes the declaration that names it — a values "+
			"file in `hanzoai/universe` under `charts/app/values/<namespace>/<name>.yaml`, which the "+
			"`fleet` ApplicationSet renders as one Application. That file IS the deployment: nothing "+
			"else has to be applied.\n\n"+
			"`mode` decides whether anything can go live. The default, `branch`, pushes to "+
			"`deploy/<namespace>/<name>/<tag>` and returns a review URL; the generator reads main, so "+
			"a branch declaration deploys NOTHING and merging the review is the deliberate act. "+
			"`commit` writes main, and proves the image is pullable first — a declaration naming an "+
			"image the registry cannot serve is an ImagePullBackOff with no rollback path.\n\n"+
			"Omit `tag` to build; give it to declare an image an earlier call already built, which is "+
			"how a green build is released without rebuilding it.\n\n"+
			"The values DIRECTORY and the image REPOSITORY are derived from the caller's organization "+
			"and are not request fields. The directory decides the AppProject fence CD admits the sync "+
			"under, and the repository decides what the cluster pulls; a caller who could name either "+
			"could reach outside its own tenant. `tier=platform` places the declaration in the "+
			"platform's own directory and is SuperAdmin only. A host outside the caller's org subtree "+
			"is refused: claim and verify a custom domain first.")

	openapi.Describe("/v1/platform/apps", http.MethodGet,
		"What this organization has declared, and what CD did with it",
		"Returns the declarations in the caller's tenant directory, each joined with the Hanzo CD "+
			"Application reconciling it — sync verdict, health, the universe commit last applied. "+
			"`cd` is null for a declaration the delivery plane has no Application for, which is the "+
			"normal state of one that exists only on a branch.\n\n"+
			"If the delivery plane cannot be read, the declarations are still returned and "+
			"`cdUnavailable` says why. An unreadable plane never renders as \"nothing has been "+
			"reconciled\".")

	openapi.Describe("/v1/platform/apps/:app", http.MethodGet,
		"One declaration",
		"The values file for one app as git declares it: image repository and tag, hosts, replicas, "+
			"and whether CD is automated on it. 404 when this organization declares no such app.")

	openapi.Describe("/v1/platform/apps/:app/cd", http.MethodGet,
		"One app's reconciliation",
		"The Hanzo CD Application for one declaration, on its own — the poll a deploy view makes "+
			"while it waits, without re-reading the whole inventory. 404 while the declaration exists "+
			"only on a branch, because the generator reads main.")

	openapi.Describe("/v1/platform/cd", http.MethodGet,
		"The delivery plane",
		"Every Hanzo CD Application this caller may observe, with its sync verdict, health, the "+
			"universe revision last applied, and whether automation and self-heal are on. A "+
			"SuperAdmin sees the fleet; an org admin sees only Applications whose destination "+
			"namespace its own organization owns.\n\n"+
			"A cluster with no CD installed answers an empty plane. A plane that cannot be READ "+
			"answers 503 and says why — the two are opposite facts and never share a shape.")

	openapi.Describe("/v1/platform/ci", http.MethodGet,
		"Continuous integration (not wired)",
		"Answers 501. The forge's Actions runs need a Forgejo API client and this deployment has "+
			"none; an empty run list would be indistinguishable from a forge with no runs.")
}
