// runner.go — POST /v1/platform/runner: the native, privileged build endpoint.
//
// This is the no-GitHub-builders build trigger that `hanzo build` and the
// git-push-to-deploy hook call. It replaces the old /v1/arcd surface: one native
// build API on the runner fabric.
//
// It differs from the tenant path (/v1/platform/.../deploy, which FORCES a
// per-tenant image ref): a /v1/platform/runner build is PRIVILEGED — the caller supplies
// the output image — so it is bounded three ways:
//   - a credential: one that NAMES an organization, or the shared build-callback
//     token compared in constant time,
//   - an image-ref allowlist restricted to the org registries we own, so a leaked
//     token can never push to an arbitrary registry, and
//   - on a credential that names an organization, that organization's own
//     namespace — so an ordinary build reaches one brand rather than all of them.

package platform

import (
	"cmp"
	"context"
	"crypto/subtle"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
)

// runnerBuildReq mirrors the CLI BuildReq (cli/platform.go). repo + image are
// required; the rest are optional build knobs.
//
// THERE IS NO ORGANIZATION FIELD. A build belongs to the organization its
// credential names (runnerOrg), and an attribution a caller can write is worth
// nothing — checking one merely moves the mistake to whoever forgets the check
// next. Deleting it leaves nothing to check and nothing to forget.
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
	// Args are --build-arg values. They are what lets several images off ONE
	// Dockerfile mean different things — the sandbox classes are three entries
	// differing only by STAGE. Validated at the k8s choke point, with VERSION and
	// REVISION taking precedence: those are receipts the builder derives from the
	// tag and the commit, and a caller that could overwrite them could make an
	// image lie about which commit it is.
	Args map[string]string `json:"args,omitempty" url:"-"`
	// OS is the target operating system for the artifact lane.
	OS string `json:"os,omitempty" url:"-"`
	// Arch is the target architecture for the artifact lane.
	Arch string `json:"arch,omitempty" url:"-"`
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
	// BuildJobID is the queued build's id, and what its progress is read by.
	BuildJobID string `json:"buildJobId"`
	// Status is `queued` — the build was accepted and has not finished.
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
// oci.hanzo.ai and registry.hanzo.ai are ONE store behind one Traefik router,
// not two registries: naming the canonical host here grants no reach the
// deprecated alias did not already have, and omitting it refused the very name
// the fleet is told to write.
var ownedRegistryHosts = []string{"oci.hanzo.ai", "registry.hanzo.ai", "ghcr.io"}

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
	return slices.Contains(ownedBy(org), ns)
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
// ghcr.io/hanzoai/cloud among them — the binary the whole fleet runs. A fold
// applied to one side of a comparison is not a normalization, it is a collision,
// and here the collision IS a cross-tenant privilege grant.
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
	want := environ.Or("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	if want == "" {
		return false
	}
	got := stripBearer(c.Header("Authorization"))
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// runnerOrg resolves the ORGANIZATION a build is authorized for — read off the
// caller's credential and nothing the caller stated. It answers "" when the request
// carries no org-scoped identity at all, which is the only case the shared
// build-callback token has to cover.
//
// It reads ONLY the principal.* accessors — the output of the ONE identity verifier
// — which read authority headers SanitizeIdentity strips on ingress and re-mints
// solely from a signature-verified token, so the signal is unforgeable off-gateway.
//
// TWO KINDS OF CREDENTIAL NAME AN ORG, and each is entitled differently because
// they are different things:
//
//   - A PERSON must ADMINISTER the org (or hold platform sudo). Publishing into a
//     brand's registry is not a plain member's to do, so a login is necessary and
//     not sufficient.
//   - An APPLICATION acting as itself IS the organization's own machine identity.
//     Obtaining its token requires that application's client secret, and its org is
//     the application's own owner, which it cannot choose: IAM mints an app token no
//     membership set, so the org-switch admits nothing. It holds no admin scope and
//     needs none — the act is a purpose, and the purpose is bounded below by the
//     namespace that org owns.
//
// The second is what lets a build carry an organization at all. Asking every caller
// for the admin bit asked a question no non-interactive credential can answer:
// authz.Claims.OrgAdmin refuses every machine by construction — correctly, since an
// app is issued for a purpose and not handed an org's self-service surface — so the
// only credential that still reached this door from a pipeline was the fabric's
// shared token, which names no org and is therefore wider than any single build.
func runnerOrg(c *zip.Ctx) string {
	org, ok := principal.Org(c) // composes principal.Validated
	if !ok {
		return ""
	}
	if principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c) || principal.IsApp(c) {
		return org
	}
	return ""
}

// runnerBuild triggers a native build — an image, or the binaries a repo declares.
//
// The fabric's own build trigger, and what `hanzo build` and git-push-to-deploy
// call. It answers 202 with the build job id: a queued build, not a pushed
// artifact.
//
// Two lanes, and a build is exactly one of them. The IMAGE lane takes `repo` and
// the output `image` and launches a BuildKit Job that pushes it. The ARTIFACT lane
// takes `binaries` — the same recipe the repo's hanzo.yml declares — and publishes
// to object storage instead; it must carry no `image`, because a build produces
// binaries or an image, never both.
//
// PRIVILEGED, and A BUILD BELONGS TO THE ORGANIZATION ITS CREDENTIAL NAMES. Two
// credentials, never a third:
//
//   - one that NAMES an organization — a person who administers it (the `hanzo
//     build` path, so one IAM login authorizes a build with no separate build
//     token), or that organization's own machine identity (the pipeline path). The
//     build is attributed to that org and confined to what it owns.
//   - the shared build-callback token, compared in constant time. It names NO
//     organization, which is both why the fabric's own release can publish across
//     brands with it and why anything that CAN name one is read first.
//
// Both are bounded by the owned-registry allowlist. The org path is bounded again,
// by the org: the image's registry namespace must be one that organization owns, so
// it publishes into its own brand and can never overwrite another's through the
// shared push credential. The same confinement applies to the artifact lane's repo
// owner. There is no request field naming an organization — the attribution is read
// off the credential, so there is nothing for a caller to write it with.
//
// The output image is parsed and validated as a single well-formed OCI ref before
// any authorization decision reads it, so a crafted ref cannot smuggle a
// build-exporter attribute past the check.
func (o ops) runnerBuild(ctx context.Context, body *runnerBuildReq) (*runnerBuildResp, error) {
	s := o.s
	// The request itself, not a tenant: this route resolves the organization from
	// the credential below and accepts a credential that names none, so asking the
	// seam for a tenant here would refuse the fabric's own build before the route
	// could decide.
	c, err := o.request(ctx)
	if err != nil {
		return nil, err
	}
	req := *body
	// The organization this build belongs to, read off the credential (runnerOrg).
	// Empty means the caller named none, and the shared build-callback token is then
	// the only thing that can authorize the build.
	//
	// THE CREDENTIAL THAT NAMES AN ORG IS READ FIRST, and that order is the rule
	// rather than a preference: a caller presenting a real organization is attributed
	// to it and confined to it, whether or not an ambient shared secret also rode
	// along. Reading the token first discarded the org the caller had proved in
	// favour of a secret that proves none — so a request carrying both got
	// fabric-wide latitude across every brand's registry, and the build recorded
	// nobody. An ambient secret must not be able to promote an identity out of its
	// own tenant.
	org := runnerOrg(c)
	if org == "" && !runnerTokenOK(c) {
		return nil, zip.ErrForbidden("invalid build token")
	}

	req.Repo = strings.TrimSpace(req.Repo)
	req.Image = strings.TrimSpace(req.Image)

	// EVERY BUILD HERE IS A TENANT'S BUILD, and the allowlist below is what bounds
	// it: an ordinary build publishes ONE tenant's artifact into the namespace that
	// tenant owns, so the organization the credential names is the right bound.
	//
	// ghcr.io/hanzoai/cloud — the binary every service in every org runs — is not
	// published from here at all. Its version numbers are ordered by one branch of
	// one repository, and the compare-and-swap that allocates one runs in
	// .hanzo/workflows/cicd.yml, beside the commit it is numbering. A second
	// allocator reading the same registry cannot reserve anything the first one
	// honours, so there is one.
	ref := cmp.Or(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	if len(req.Binaries) > 0 {
		return runnerArtifactBuild(s, ctx, c, req, ref, org)
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

	// H1 — bind the image's registry namespace to the org the CREDENTIAL names.
	// imageAllowed proved the image targets a registry the fabric owns; this proves
	// that organization owns the namespace, so it publishes into its own brand and
	// can never overwrite another brand's image through the shared push credential.
	// A real platform SuperAdmin may cross (disabled in prod). The shared token
	// names no organization, so there is none to confine it to — that, and not a
	// wider allowlist, is the whole of its extra latitude.
	if org != "" && !principal.IsSuperAdmin(c) && !imageInOrgRegistry(req.Image, org) {
		return nil, zip.ErrForbidden("image registry-org must match your organization")
	}

	bldID := genID("bld")

	jobName, err := s.State.k8s.launchDirectBuild(ctx, platformBuildOrg, req.Repo, ref, req.Image, strings.TrimSpace(req.Dockerfile), bldID, req.Args)
	if err != nil {
		return nil, zip.Errorf(deployErrStatus(err), "launch build: %v", err)
	}

	// Record the build under the organization its credential named, and under
	// "platform" when it named none (the fabric's own build). Best-effort: a record
	// miss must not fail a launched build.
	now := time.Now().Unix()
	b := Build{ID: bldID, Org: cmp.Or(org, platformBuildOrg), Status: "queued", Image: req.Image, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(ctx, b); err != nil {
		s.Log.Warn("runner build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner build launched", "job", jobName, "image", req.Image, "ref", ref, "repo", req.Repo)

	return &runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Image: req.Image, Target: strings.TrimSpace(req.DockerTarget),
	}, nil
}

// runnerArtifactBuild serves the ARTIFACT lane of POST /v1/platform/runner: build what the
// repo's hanzo.yml `binaries:` declares and publish it, rather than push an image.
// Auth is already settled by the caller, which hands this lane the organization the
// credential named ("" for the fabric's own token). The bounds this lane adds are
// its own: the repo URL (the same allowlisted-git-host validator the image lane
// uses), the recipe (binarySpec.validate), and the forge owner, which must be one
// that organization owns.
func runnerArtifactBuild(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, req runnerBuildReq, ref, org string) (*runnerBuildResp, error) {
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
	if _, err := validateBuildRef(ref); err != nil {
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
	tag := cmp.Or(strings.TrimSpace(req.Tag), ref)
	if !artifactNameRE.MatchString(tag) {
		return nil, zip.ErrBadRequest("tag must be a flat version/commit segment (set `tag` when building a branch)")
	}
	bucket := cmp.Or(strings.TrimSpace(req.Bucket), defaultArtifactBucket)
	if !bucketRE.MatchString(bucket) {
		return nil, zip.ErrBadRequest("bucket must be a valid object-store bucket name")
	}
	// Same H1 confinement the image lane applies to a registry namespace: an
	// organization publishes artifacts only for the forge owner it owns. The shared
	// token names no organization, so there is none to confine it to.
	if org != "" && !principal.IsSuperAdmin(c) && !repoOwnerInOrg(repoURL, org) {
		return nil, zip.ErrForbidden("repo owner must match your organization")
	}

	bldID := genID("bld")
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
	b := Build{ID: bldID, Org: cmp.Or(org, platformBuildOrg), Status: "queued", Image: index, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(ctx, b); err != nil {
		s.Log.Warn("runner artifact build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner artifact build launched", "job", jobName, "index", index, "ref", ref, "repo", repoURL, "binaries", len(req.Binaries))

	return &runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Index: index,
	}, nil
}
