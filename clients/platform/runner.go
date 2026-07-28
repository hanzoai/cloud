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
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// runnerBuildReq mirrors the CLI BuildReq (cli/platform.go). repo + image are
// required; the rest are optional build knobs.
type runnerBuildReq struct {
	Repo         string `json:"repo"`
	SHA          string `json:"sha"`
	Image        string `json:"image"`
	Branch       string `json:"branch,omitempty"`
	Ref          string `json:"ref,omitempty"`
	Dockerfile   string `json:"dockerfile,omitempty"`
	Context      string `json:"context,omitempty"`
	DockerTarget string `json:"dockerTarget,omitempty"`
	OS           string `json:"os,omitempty"`
	Arch         string `json:"arch,omitempty"`
	OrgID        string `json:"organizationId,omitempty"`
	// Release requests native release semantics for cloud's self-publish: compute
	// the next version, build+push ghcr.io/hanzoai/cloud, smoke it, then tag (the
	// receipt) and notify universe. It owns its output image (release.go).
	Release bool `json:"release,omitempty"`

	// Binaries selects the ARTIFACT lane (artifact.go): build what the repo's
	// hanzo.yml `binaries:` block declares — a Go binary, an npm tarball, a Rust
	// binary — and publish it to hanzoai/s3 instead of pushing an image. It is the
	// same recipe hanzoai/ci reads, sent verbatim, so `image` is meaningless here
	// and must be absent. Bucket/Tag mirror hanzo.yml's `bucket:` and the tag the
	// GitHub lane publishes under, so both front doors write ONE index at ONE URL.
	Binaries []binarySpec `json:"binaries,omitempty"`
	Bucket   string       `json:"bucket,omitempty"`
	Tag      string       `json:"tag,omitempty"`
}

// runnerBuildResp is the 202 acceptance (matches the CLI BuildJob). Index is the
// artifact lane's output — the binaries.json a host reads — where Image is the
// image lane's; a build produces exactly one of the two.
type runnerBuildResp struct {
	BuildJobID string `json:"buildJobId"`
	Status     string `json:"status"`
	RunnerPool string `json:"runnerPool"`
	Image      string `json:"image,omitempty"`
	Target     string `json:"target,omitempty"`
	Index      string `json:"index,omitempty"`
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
	for _, owned := range orgRegistryNamespaces[strings.ToLower(strings.TrimSpace(org))] {
		if ns == owned {
			return true
		}
	}
	return false
}

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
	for _, owned := range orgRegistryNamespaces[strings.ToLower(strings.TrimSpace(org))] {
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

// runnerBuild serves POST /v1/runner — enqueue a native, privileged build.
func runnerBuild(s *cloud.Service[state], c *zip.Ctx) error {
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
		return zip.ErrForbidden("invalid build token")
	}

	var req runnerBuildReq
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("decode build request: " + err.Error())
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Image = strings.TrimSpace(req.Image)

	// Release self-publishes ghcr.io/hanzoai/cloud (compute version → build → smoke
	// → tag → notify) — a fabric operation reserved to the MACHINE token. An
	// interactive login, even an admin, never cuts a cloud release.
	if req.Release {
		if !viaToken {
			return zip.ErrForbidden("release builds require the platform build token")
		}
		return startRelease(s, c, req)
	}

	ref := firstNonEmpty(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	if len(req.Binaries) > 0 {
		return runnerArtifactBuild(s, c, req, ref, viaIAM)
	}
	if req.Repo == "" || req.Image == "" {
		return zip.ErrBadRequest("repo and image are required")
	}
	// Validate the output image as a single, well-formed OCI ref BEFORE any
	// registry/authz decision reads it — so imageRegistryNamespace parses a clean
	// ref and a comma/space/`=` can never inject a BuildKit `--output` exporter
	// attribute (M1). launchDirectBuild re-validates at the k8s choke point
	// (validateBuildInputs); this is the early, user-facing 400.
	if _, err := validateImageRef(req.Image); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if !imageAllowed(req.Image) {
		return zip.ErrForbidden("image must push to an owned registry (ghcr.io/{hanzoai,luxfi,zooai}/*)")
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
			return zip.ErrForbidden("image registry-org must match your organization")
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
			return zip.ErrForbidden("organizationId must be your own org")
		}
	}

	bldID, err := genID("bld")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	jobName, err := s.State.k8s.launchDirectBuild(c.Context(), req.Repo, ref, req.Image, strings.TrimSpace(req.Dockerfile), bldID)
	if err != nil {
		return zip.Errorf(deployErrStatus(err), "launch build: %v", err)
	}

	// Record the build (org "platform" — a fabric-owned direct build, not
	// tenant-scoped). Best-effort: a record miss must not fail a launched build.
	now := time.Now().Unix()
	b := Build{ID: bldID, Org: firstNonEmpty(buildOrg, platformBuildOrg), Status: "queued", Image: req.Image, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(c.Context(), b); err != nil {
		s.Log.Warn("runner build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner build launched", "job", jobName, "image", req.Image, "ref", ref, "repo", req.Repo)

	return c.JSON(http.StatusAccepted, runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Image: req.Image, Target: strings.TrimSpace(req.DockerTarget),
	})
}

// runnerArtifactBuild serves the ARTIFACT lane of POST /v1/runner: build what the
// repo's hanzo.yml `binaries:` declares and publish it, rather than push an image.
// Auth is already settled by the caller; the bounds this lane adds are its own:
// the repo URL (the same allowlisted-git-host validator the image lane uses), the
// recipe (binarySpec.validate), and — on the IAM path — the forge owner, which
// must be one the caller's org owns.
func runnerArtifactBuild(s *cloud.Service[state], c *zip.Ctx, req runnerBuildReq, ref string, viaIAM bool) error {
	if strings.TrimSpace(req.Image) != "" {
		return zip.ErrBadRequest("a build produces binaries or an image, never both")
	}
	if len(req.Binaries) > maxArtifactBinaries {
		return zip.Errorf(http.StatusBadRequest, "at most %d binaries per build", maxArtifactBinaries)
	}
	repoURL, err := validateRepoURL(req.Repo)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if _, err := validateGitRef(ref); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	for i := range req.Binaries {
		if err := req.Binaries[i].validate(); err != nil {
			return zip.ErrBadRequest(err.Error())
		}
	}
	// The publish path segment. Defaults to the pinned ref (a tag publishes at
	// its tag, a commit at its sha — both immutable); a branch ref carries a '/'
	// and would nest the layout, so it must be named explicitly.
	tag := firstNonEmpty(strings.TrimSpace(req.Tag), ref)
	if !artifactNameRE.MatchString(tag) {
		return zip.ErrBadRequest("tag must be a flat version/commit segment (set `tag` when building a branch)")
	}
	bucket := firstNonEmpty(strings.TrimSpace(req.Bucket), defaultArtifactBucket)
	if !bucketRE.MatchString(bucket) {
		return zip.ErrBadRequest("bucket must be a valid object-store bucket name")
	}
	// Same H1 confinement the image lane applies to a registry namespace: an IAM
	// org-admin publishes only its own brand's repos. The machine token is
	// fabric-trusted (it is how a native push and cloud's own release publish).
	if viaIAM && !principal.IsSuperAdmin(c) {
		callerOrg, _ := principal.Org(c)
		if !repoOwnerInOrg(repoURL, callerOrg) {
			return zip.ErrForbidden("repo owner must match your organization")
		}
	}

	bldID, err := genID("bld")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	slug := repoSlug(repoURL)
	base := artifactBase(bucket, slug, tag)
	jobName, err := s.State.k8s.launchArtifactBuild(c.Context(), repoURL, ref, tag, base, artifactPutBase(bucket, slug, tag), req.Binaries, bldID)
	if err != nil {
		return zip.Errorf(deployErrStatus(err), "launch build: %v", err)
	}

	// The build row records the INDEX as the output, the way the image lane
	// records the pushed ref: it is the one URL the artifact is reached by.
	index := base + "/binaries.json"
	now := time.Now().Unix()
	b := Build{ID: bldID, Org: platformBuildOrg, Status: "queued", Image: index, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(c.Context(), b); err != nil {
		s.Log.Warn("runner artifact build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner artifact build launched", "job", jobName, "index", index, "ref", ref, "repo", repoURL, "binaries", len(req.Binaries))

	return c.JSON(http.StatusAccepted, runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Index: index,
	})
}
