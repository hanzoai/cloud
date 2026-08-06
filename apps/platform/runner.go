// runner.go — POST /v1/runner: the native, privileged build endpoint.
//
// This is the no-GitHub-builders build trigger that `hanzo build`, the
// git-push-to-deploy hook, and cloud's own self-release all call. It replaces
// the old /v1/arcd surface: one native build API on the runner fabric.
//
// It differs from the tenant path (/v1/platform/.../deploy, which FORCES a
// per-tenant image ref): a /v1/runner build is PRIVILEGED — the caller supplies
// the output image — so it is gated two ways:
//   - a shared build-callback token (constant-time), and
//   - an image-ref allowlist restricted to the org registries we own,
//
// so a leaked token can never push to an arbitrary registry.

package platform

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// runnerBuildReq mirrors the CLI BuildReq (cli/platform.go). repo + image are
// required; the rest are optional build knobs.
//
// Every field carries `url:"-"`. This is a PRIVILEGED build trigger, and zip's
// binder fills an In field from the query string as well as the body: without the
// opt-out `?image=…` would choose the pushed artifact, and the whole authorization
// argument below reads req.Image.
type runnerBuildReq struct {
	// Repo is the repository clone URL to build. Required on the image lane.
	Repo string `json:"repo" url:"-"`
	// SHA is the commit to pin; it wins over Ref and Branch.
	SHA string `json:"sha" url:"-"`
	// Image is the output image ref to push. Required on the image lane, and it
	// must target a registry namespace the caller's org owns.
	Image string `json:"image" url:"-"`
	// Branch is the branch to build when no SHA or Ref is given.
	Branch string `json:"branch,omitempty" url:"-"`
	// Ref is the git ref to build when no SHA is given.
	Ref string `json:"ref,omitempty" url:"-"`
	// Dockerfile is the path to build from; empty uses the zero-config frontend.
	Dockerfile string `json:"dockerfile,omitempty" url:"-"`
	// Context is the build context path within the repo.
	Context string `json:"context,omitempty" url:"-"`
	// DockerTarget is the multi-stage build target to stop at.
	DockerTarget string `json:"dockerTarget,omitempty" url:"-"`
	// OS is the target operating system for the artifact lane.
	OS string `json:"os,omitempty" url:"-"`
	// Arch is the target architecture for the artifact lane.
	Arch string `json:"arch,omitempty" url:"-"`
	// OrgID attributes the build to an org. On the IAM path it defaults to the
	// caller's own validated org, and a foreign one is refused unless the caller
	// is a platform SuperAdmin.
	OrgID string `json:"organizationId,omitempty" url:"-"`
	// Release requests native release semantics for cloud's self-publish: compute
	// the next version, build+push ghcr.io/hanzoai/cloud, smoke it, then tag (the
	// receipt) and notify universe. It owns its output image (release.go), and it
	// takes SuperAdmin.
	Release bool `json:"release,omitempty" url:"-"`

	// Binaries selects the ARTIFACT lane (artifact.go): build what the repo's
	// hanzo.yml `binaries:` block declares — a Go binary, an npm tarball, a Rust
	// binary — and publish it to hanzoai/s3 instead of pushing an image. It is the
	// same recipe hanzoai/ci reads, sent verbatim, so `image` is meaningless here
	// and must be absent.
	Binaries []binarySpec `json:"binaries,omitempty" url:"-"`
	// Bucket mirrors hanzo.yml's `bucket:` — where the artifact lane publishes.
	Bucket string `json:"bucket,omitempty" url:"-"`
	// Tag is the publish path segment, so both front doors write ONE index at ONE
	// URL. It defaults to the pinned ref, and must be named explicitly for a
	// branch.
	Tag string `json:"tag,omitempty" url:"-"`
}

// runnerBuildResp is the 202 acceptance (matches the CLI BuildJob). Index is the
// artifact lane's output — the binaries.json a host reads — where Image is the
// image lane's; a build produces exactly one of the two.
type runnerBuildResp struct {
	// BuildJobID is the queued build's id, and what a release is followed by.
	BuildJobID string `json:"buildJobId"`
	// Status is `queued` for an ordinary build, `releasing` for a self-publish.
	Status string `json:"status"`
	// RunnerPool is the runner class the build was placed on.
	RunnerPool string `json:"runnerPool"`
	// Image is the ref the image lane will push.
	Image string `json:"image,omitempty"`
	// Target is the multi-stage build target, echoed back.
	Target string `json:"target,omitempty"`
	// Index is the binaries.json URL the artifact lane will publish.
	Index string `json:"index,omitempty"`
}

// ownedRegistryHosts are the registry hosts the fabric operates. An image on any
// other host is NEVER allowed on the privileged build path. registry.hanzo.ai is
// the self-hosted fleet registry (the native CI/CD home); ghcr stays during the
// migration as the public mirror.
var ownedRegistryHosts = []string{"registry.hanzo.ai", "ghcr.io"}

// orgRegistryNamespaces maps an IAM org (the validated `owner` claim) to the
// registry namespace(s) that org OWNS. Only the three brands that own a registry
// appear (hanzo→hanzoai, lux→luxfi, zoo→zooai); an org absent here owns NO push
// target, so an org-admin of it is refused on the IAM build path (fail-closed) —
// nobody pushes to a brand they do not own. The UNION of the values is the set of
// namespaces the fabric owns (ownedNamespaces), which the machine-token (fabric)
// path may push to freely and a real SuperAdmin may cross into.
//
// This is the ONE place the org→registry trust is expressed, and it is the fix
// for H1: imageAllowed proves an image targets an owned registry, but only
// imageInOrgRegistry proves the CALLER owns that namespace — without the second
// check any org-admin could overwrite another brand's production image via the
// shared push credential.
var orgRegistryNamespaces = map[string][]string{
	"hanzo": {"hanzoai"},
	"lux":   {"luxfi"},
	"zoo":   {"zooai"},
}

// ownedNamespaces is the set of registry namespaces the fabric owns — the union
// of every org's namespaces. Derived once from orgRegistryNamespaces so there is
// no second hand-maintained list to drift.
var ownedNamespaces = func() map[string]bool {
	m := map[string]bool{}
	for _, nss := range orgRegistryNamespaces {
		for _, ns := range nss {
			m[ns] = true
		}
	}
	return m
}()

// imageRegistryNamespace returns the owned-registry namespace an image pushes to
// (ghcr.io/luxfi/x → "luxfi", registry.hanzo.ai/hanzoai/y → "hanzoai") and
// ok=false when the image is not on an owned host, has no namespace/repo split, or
// carries an empty repo. The parse is strict: <owned-host>/<namespace>/<repo…>.
// It assumes the image has already passed validateImageRef (a single clean OCI
// ref), so the host and namespace are the first two '/'-separated components.
func imageRegistryNamespace(image string) (string, bool) {
	for _, h := range ownedRegistryHosts {
		prefix := h + "/"
		if !strings.HasPrefix(image, prefix) {
			continue
		}
		rest := image[len(prefix):]
		i := strings.IndexByte(rest, '/')
		if i <= 0 || i+1 >= len(rest) {
			return "", false // no namespace, or nothing after it (no repo)
		}
		return rest[:i], true
	}
	return "", false
}

// imageAllowed reports whether image targets a registry namespace the fabric owns
// (the OUTER bound shared by the machine and IAM paths). It does NOT bind the
// namespace to any caller — imageInOrgRegistry does that on the IAM path.
func imageAllowed(image string) bool {
	ns, ok := imageRegistryNamespace(image)
	return ok && ownedNamespaces[ns]
}

// imageInOrgRegistry reports whether image pushes to a namespace the given org
// OWNS (H1). Used to confine an IAM org-admin to its own brand's registry: a
// hanzo admin may push ghcr.io/hanzoai/* but never ghcr.io/luxfi/*. A caller whose
// org owns no namespace (not one of the three registry brands) always fails here.
func imageInOrgRegistry(image, org string) bool {
	ns, ok := imageRegistryNamespace(image)
	if !ok {
		return false
	}
	for _, owned := range ownedBy(org) {
		if ns == owned {
			return true
		}
	}
	return false
}

// ownedBy is the ONE lookup of an org's registry namespaces, keyed by the VERBATIM
// validated IAM owner — the same value principal.Org returns, trimmed and nothing
// else.
//
// Both halves of an authorization comparison must be the same value. This lookup
// folded the org (strings.ToLower) while principal.Org returns it verbatim and
// never lowercases, precisely because "acme" and "ACME" are DISTINCT IAM tenants
// (principal.go, and TestMembershipMatchIsByteExact pins it). So a tenant who
// self-serves an org named `Hanzo` — a different owner from `hanzo`, whose own
// RoleOwner makes IsOrgAdmin true inside it — folded onto the `hanzo` key and
// claimed the `hanzoai` namespace: push over another brand's production images,
// and past the release gate onto ghcr.io/hanzoai/cloud, the binary the whole fleet
// runs. A fold applied to one side of a comparison is not a normalization, it is a
// collision, and here the collision IS a cross-tenant privilege grant.
//
// The keys are therefore the real IAM owners (brand.Default is "hanzo"); a brand
// whose owner is spelled otherwise is named here as it is spelled there.
func ownedBy(org string) []string { return orgRegistryNamespaces[strings.TrimSpace(org)] }

// repoOwnerInOrg is imageInOrgRegistry for the ARTIFACT lane: it reports whether
// the repo being built belongs to a forge owner the caller's org owns. It reads
// the SAME orgRegistryNamespaces map because a brand's registry namespace and its
// forge owner are one name (hanzo→hanzoai owns both ghcr.io/hanzoai/* and
// github.com/hanzoai/*) — so a hanzo admin publishes artifacts for hanzoai repos
// and never for luxfi's, exactly as it can never push a luxfi image.
func repoOwnerInOrg(repoURL, org string) bool {
	slug := repoSlug(repoURL)
	owner, _, ok := strings.Cut(slug, "/")
	if !ok || owner == "" {
		return false
	}
	for _, owned := range ownedBy(org) {
		if strings.EqualFold(owner, owned) {
			return true
		}
	}
	return false
}

// stripBearer returns the token from an "Authorization: Bearer <tok>" header.
func stripBearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return h
}

// runnerTokenOK reports whether the request carries the shared build-callback
// token, compared in constant time. An UNSET server secret can never match (a
// zero-length secret would otherwise ConstantTimeCompare-equal a zero-length
// header) — the token path simply does not authorize, and the endpoint stays
// available via the IAM path rather than ever accepting an empty credential.
func runnerTokenOK(c *zip.Ctx) bool {
	want := strings.TrimSpace(getenv("PLATFORM_BUILD_CALLBACK_TOKEN", ""))
	if want == "" {
		return false
	}
	got := stripBearer(c.Header("Authorization"))
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// runnerIAMAdmin reports whether the request carries a validated IAM principal
// that is an admin (of its own org via the IAM `isAdmin` bit, or a platform
// SuperAdmin) within a resolvable org. It reads ONLY the principal.* accessors —
// the output of the ONE identity verifier — which read authority headers that
// SanitizeIdentity strips on ingress and re-mints solely from a signature-verified
// JWT, so the signal is unforgeable off-gateway. A validated but non-admin member
// is refused here; the owned-registry allowlist still bounds the image either way.
func runnerIAMAdmin(c *zip.Ctx) bool {
	if !principal.Validated(c) {
		return false
	}
	if _, ok := principal.Org(c); !ok {
		return false
	}
	return principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)
}

// runnerBuild triggers a native build — an image, or the binaries a repo declares.
//
// The fabric's own build trigger, and what `hanzo build`, git-push-to-deploy and
// cloud's own self-release all call. It answers 202 with the build job id: a queued
// build, not a pushed artifact.
//
// Two lanes, and a build is exactly one of them. The IMAGE lane takes `repo` and
// the output `image` and launches a BuildKit Job that pushes it. The ARTIFACT lane
// takes `binaries` — the same recipe the repo's hanzo.yml declares — and publishes
// to object storage instead; it must carry no `image`, because a build produces
// binaries or an image, never both. `release: true` is the third mode: cloud
// self-publishing its own image, version computed, built, smoke-tested, tagged and
// announced.
//
// PRIVILEGED, with exactly two credentials and never a third: the shared
// build-callback token compared in constant time — the machine path, which a user
// never holds — or a validated IAM principal who is an ADMIN of their org, which is
// the `hanzo build` user path and means one IAM login authorizes a build with no
// separate build token. A plain member is refused.
//
// Both paths are bounded the same way: the output must push to a registry the
// fabric owns, and on the IAM path the image's registry namespace must MATCH the
// caller's own validated org — so an org admin can only publish into their own
// brand and can never overwrite another's through the shared push credential. The
// same confinement applies to the artifact lane's repo owner.
//
// `release: true` is the exception, and takes SUPERADMIN. It publishes the
// platform's own image — the binary the whole fleet runs — so what it lands reaches
// every org at the next reconcile, and no role inside the caller's own org can
// authorize that. An org admin is refused however the registry namespace lines up,
// and the build token, which carries no identity at all, may enqueue an ordinary
// build but never a release.
//
// The output image is parsed and validated as a single well-formed OCI ref before
// any authorization decision reads it, so a crafted ref cannot smuggle a
// build-exporter attribute past the check.
func (o ops) runnerBuild(ctx context.Context, body *runnerBuildReq) (*runnerBuildResp, error) {
	s := o.s
	// The request itself, not a tenant: this route authorizes on a shared
	// credential or on platform authority, and a machine caller carries no org at
	// all — so asking for one would refuse the very path this endpoint exists for.
	c, err := o.request(ctx)
	if err != nil {
		return nil, err
	}
	req := *body
	// Auth — ONE of two credentials, never a third:
	//   (1) the shared build-callback token (constant-time): the MACHINE path
	//       (git-push-to-deploy, cloud self-release, the operator). A user never
	//       holds it. Or
	//   (2) a validated IAM principal who is an admin (the IAM `isAdmin` bit, or a
	//       platform SuperAdmin): the `hanzo build` USER path, so ONE IAM login
	//       authorizes a build with no separate build token. principal.Validated is
	//       true ONLY when the identity boundary (the gateway / cloud's own
	//       SanitizeIdentity) minted X-User-Id from a signature-verified JWT and
	//       re-minted X-User-IsOrgAdmin from its `isAdmin` claim — every authority
	//       header is STRIPPED on ingress and re-injected only from validated
	//       claims, so an off-gateway forge cannot fake it, and a plain member
	//       (no admin bit) is refused.
	// Both paths are bounded by the SAME owned-registry allowlist below, so neither
	// can push outside the registries we own.
	viaToken := runnerTokenOK(c)
	viaIAM := !viaToken && runnerIAMAdmin(c)
	if !viaToken && !viaIAM {
		return nil, zip.ErrForbidden("invalid build token")
	}

	req.Repo = strings.TrimSpace(req.Repo)
	req.Image = strings.TrimSpace(req.Image)

	// Release self-publishes the platform's own image (compute version → build →
	// smoke → tag → notify), and that is PLATFORM authority, not the authority an
	// ordinary build takes. The two lanes part company here and nowhere else.
	//
	// An ordinary build below publishes ONE tenant's artifact into the namespace
	// that tenant owns, so the caller's own org bounds it (imageInOrgRegistry) and
	// admin of that org is the right role. A release publishes releaseImage —
	// ghcr.io/hanzoai/cloud, the binary every service in every org runs — so what it
	// lands is OURS, on everyone, at the next reconcile. No property of the caller's
	// own org can admit an act with that reach, which is why the gate is mayRelease
	// (release.go) and reads cloud.Super alone.
	//
	// The whole decision is that one call, including the MACHINE path: cloud.Super
	// requires a validated principal and PLATFORM_BUILD_CALLBACK_TOKEN mints none,
	// so a leaked build token still enqueues an ordinary build and still cannot cut
	// a release — one expression, not a second rule standing beside it.
	//
	// The image is taken from the CONSTANT releaseImage, never from the request:
	// launchRelease publishes releaseImage regardless of what req.Image says, so
	// reading the request here would decide against a value the caller chooses.
	if req.Release {
		if err := mayRelease(c); err != nil {
			return nil, err
		}
		return startRelease(s, ctx, req)
	}

	ref := firstNonEmpty(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	if len(req.Binaries) > 0 {
		return runnerArtifactBuild(s, ctx, c, req, ref, viaIAM)
	}
	if req.Repo == "" || req.Image == "" {
		return nil, zip.ErrBadRequest("repo and image are required")
	}
	// Validate the output image as a single, well-formed OCI ref BEFORE any
	// registry/authz decision reads it — so imageRegistryNamespace parses a clean
	// ref and a comma/space/`=` can never inject a BuildKit `--output` exporter
	// attribute (M1). launchDirectBuild re-validates at the k8s choke point
	// (validateBuildInputs); this is the early, user-facing 400.
	if _, err := validateImageRef(req.Image); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if !imageAllowed(req.Image) {
		return nil, zip.ErrForbidden("image must push to an owned registry (ghcr.io/{hanzoai,luxfi,zooai}/*)")
	}

	// H1 — bind the image's registry-org to the caller's VALIDATED org on the IAM
	// path. imageAllowed proved the image targets an owned registry; this proves
	// the CALLER owns that registry namespace, so an org-admin can only push into
	// its own brand and can never overwrite another brand's image via the shared
	// push credential. A real platform SuperAdmin may cross (disabled in prod); the
	// machine-token path is fabric-trusted and keeps full owned-registry latitude
	// (it is how cloud self-releases ghcr.io/hanzoai/cloud and the operator builds).
	if viaIAM && !principal.IsSuperAdmin(c) {
		callerOrg, _ := principal.Org(c)
		if !imageInOrgRegistry(req.Image, callerOrg) {
			return nil, zip.ErrForbidden("image registry-org must match your organization")
		}
	}

	// Attribute the build to an org. On the IAM path the org is the caller's
	// VALIDATED org, never a client-named one: default organizationId to it, and
	// refuse a foreign org unless the caller is a platform SuperAdmin (who may act
	// cross-org). The machine-token path keeps its explicit organizationId.
	buildOrg := strings.TrimSpace(req.OrgID)
	if viaIAM {
		callerOrg, _ := principal.Org(c)
		switch {
		case buildOrg == "":
			buildOrg = callerOrg
		case buildOrg != callerOrg && !principal.IsSuperAdmin(c):
			return nil, zip.ErrForbidden("organizationId must be your own org")
		}
	}

	bldID, err := genID("bld")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	jobName, err := s.State.k8s.launchDirectBuild(ctx, platformBuildOrg, req.Repo, ref, req.Image, strings.TrimSpace(req.Dockerfile), bldID)
	if err != nil {
		return nil, zip.Errorf(deployErrStatus(err), "launch build: %v", err)
	}

	// Record the build (org "platform" — a fabric-owned direct build, not
	// tenant-scoped). Best-effort: a record miss must not fail a launched build.
	now := time.Now().Unix()
	b := Build{ID: bldID, Org: firstNonEmpty(buildOrg, platformBuildOrg), Status: "queued", Image: req.Image, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(ctx, b); err != nil {
		s.Log.Warn("runner build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner build launched", "job", jobName, "image", req.Image, "ref", ref, "repo", req.Repo)

	return &runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Image: req.Image, Target: strings.TrimSpace(req.DockerTarget),
	}, nil
}

// runnerArtifactBuild serves the ARTIFACT lane of POST /v1/runner: build what the
// repo's hanzo.yml `binaries:` declares and publish it, rather than push an image.
// Auth is already settled by the caller; the bounds this lane adds are its own:
// the repo URL (the same allowlisted-git-host validator the image lane uses), the
// recipe (binarySpec.validate), and — on the IAM path — the forge owner, which
// must be one the caller's org owns.
func runnerArtifactBuild(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, req runnerBuildReq, ref string, viaIAM bool) (*runnerBuildResp, error) {
	if strings.TrimSpace(req.Image) != "" {
		return nil, zip.ErrBadRequest("a build produces binaries or an image, never both")
	}
	if len(req.Binaries) > maxArtifactBinaries {
		return nil, zip.Errorf(http.StatusBadRequest, "at most %d binaries per build", maxArtifactBinaries)
	}
	repoURL, err := validateRepoURL(req.Repo)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if _, err := validateGitRef(ref); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	for i := range req.Binaries {
		if err := req.Binaries[i].validate(); err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
	}
	// The publish path segment. Defaults to the pinned ref (a tag publishes at
	// its tag, a commit at its sha — both immutable); a branch ref carries a '/'
	// and would nest the layout, so it must be named explicitly.
	tag := firstNonEmpty(strings.TrimSpace(req.Tag), ref)
	if !artifactNameRE.MatchString(tag) {
		return nil, zip.ErrBadRequest("tag must be a flat version/commit segment (set `tag` when building a branch)")
	}
	bucket := firstNonEmpty(strings.TrimSpace(req.Bucket), defaultArtifactBucket)
	if !bucketRE.MatchString(bucket) {
		return nil, zip.ErrBadRequest("bucket must be a valid object-store bucket name")
	}
	// Same H1 confinement the image lane applies to a registry namespace: an IAM
	// org-admin publishes only its own brand's repos. The machine token is
	// fabric-trusted (it is how a native push and cloud's own release publish).
	if viaIAM && !principal.IsSuperAdmin(c) {
		callerOrg, _ := principal.Org(c)
		if !repoOwnerInOrg(repoURL, callerOrg) {
			return nil, zip.ErrForbidden("repo owner must match your organization")
		}
	}

	bldID, err := genID("bld")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	slug := repoSlug(repoURL)
	base := artifactBase(bucket, slug, tag)
	jobName, err := s.State.k8s.launchArtifactBuild(ctx, repoURL, ref, tag, base, artifactPutBase(bucket, slug, tag), req.Binaries, bldID)
	if err != nil {
		return nil, zip.Errorf(deployErrStatus(err), "launch build: %v", err)
	}

	// The build row records the INDEX as the output, the way the image lane
	// records the pushed ref: it is the one URL the artifact is reached by.
	index := base + "/binaries.json"
	now := time.Now().Unix()
	b := Build{ID: bldID, Org: platformBuildOrg, Status: "queued", Image: index, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(ctx, b); err != nil {
		s.Log.Warn("runner artifact build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner artifact build launched", "job", jobName, "index", index, "ref", ref, "repo", repoURL, "binaries", len(req.Binaries))

	return &runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Index: index,
	}, nil
}
