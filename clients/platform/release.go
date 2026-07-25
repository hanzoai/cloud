// release.go — native release semantics on the /v1/runner build path.
//
// This is the in-cloud port of .github/workflows/release.yml: cloud self-publishes
// ghcr.io/hanzoai/cloud with the SAME invariant the workflow exists to enforce —
//
//	a git tag v<X.Y.Z> exists  ⇔  an image ghcr.io/hanzoai/cloud:v<X.Y.Z> was
//	pushed AND booted to "listening" in the smoke test.
//
// The tag is a RECEIPT for a proven image, minted only AFTER a successful push +
// smoke — never a trigger for a build that might fail. The order is inverted from
// the old tag-triggers-build design (which left phantom tags with no image behind
// them → ImagePullBackOff): main push → compute version → build → smoke → tag →
// notify, so any failure fails BEFORE the tag and leaves no receipt.
//
// The whole pipeline is four injectable seams (releasePlan) run in strict order
// (run), so the ordering invariant is enforced by construction and unit-tested
// hermetically, while each concrete step (a k8s Job for build/smoke, a GitHub API
// call for tag/notify) is wired once in releaseFor.
package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

const (
	// releaseImage is the ONE image cloud self-publishes; releaseRepoSlug/URL name
	// its source; releaseFloor is the version floor for the first release ever
	// (mirrors release.yml). universeRepo receives the image-update dispatch.
	releaseImage    = "ghcr.io/hanzoai/cloud"
	releaseRepoSlug = "hanzoai/cloud"
	releaseRepoURL  = "https://github.com/hanzoai/cloud"
	releaseFloor    = "1.786.0"
	universeRepo    = "hanzoai/universe"
	// releaseServiceName is the operator Service CR metadata.name for cloud's own
	// self-publish (crs/cloud.yaml) — the target of both the native CR rollout and
	// the image-update mirror, so the two never name different CRs.
	releaseServiceName = "cloud"
)

// githubAPIBase is the GitHub REST root. It is a var (not a const) ONLY so tests can
// point the release seams at an httptest server; production always uses the real API.
// The future home is git.hanzo.ai — but until it hosts cloud's own repo, GitHub
// remains the tag/list/dispatch home and release.yml must stay as the proven path.
var githubAPIBase = "https://api.github.com"

// ── version compute (pure) ───────────────────────────────────────────────────

type semver struct{ major, minor, patch int }

// parseSemver accepts "X.Y.Z" or "vX.Y.Z" with non-negative integer parts; anything
// else (latest, sha-…, the major_minor "1.786", empty) reports ok=false and is ignored.
func parseSemver(s string) (semver, bool) {
	p := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(p) != 3 {
		return semver{}, false
	}
	var v semver
	var err error
	if v.major, err = atoiNonNeg(p[0]); err != nil {
		return semver{}, false
	}
	if v.minor, err = atoiNonNeg(p[1]); err != nil {
		return semver{}, false
	}
	if v.patch, err = atoiNonNeg(p[2]); err != nil {
		return semver{}, false
	}
	return v, true
}

func atoiNonNeg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a non-negative int: %q", s)
	}
	return n, nil
}

func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v semver) String() string {
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
}

// nextVersion is the monotonic PATCH bump over the union of the git tags and the
// already-published image tags, floored at floor. Pure — the caller injects both
// lists so it is hermetic. It mirrors release.yml's version step exactly:
// max(floor, git…, image…) + 1 patch, never a major/minor jump. Folding in the image
// tags means a number that already has a PUSHED image (even one whose run died before
// tagging, or whose smoke failed after push) is never reused — the phantom-tag
// prevention, and the reason computeReleaseVersion refuses to run on a partial list.
func nextVersion(gitTags, imageTags []string, floor string) (string, error) {
	max, ok := parseSemver(floor)
	if !ok {
		return "", fmt.Errorf("invalid version floor %q", floor)
	}
	for _, t := range append(append([]string{}, gitTags...), imageTags...) {
		if v, ok := parseSemver(t); ok && max.less(v) {
			max = v
		}
	}
	max.patch++
	return max.String(), nil
}

// ── ordered pipeline (the state machine) ─────────────────────────────────────

type releaseStep int

const (
	stepNone releaseStep = iota
	stepBuilt
	stepSmoked
	stepTagged
	stepNotified
)

func (s releaseStep) String() string {
	switch s {
	case stepBuilt:
		return "built"
	case stepSmoked:
		return "smoked"
	case stepTagged:
		return "tagged"
	case stepNotified:
		return "notified"
	default:
		return "none"
	}
}

// releasePlan is the release as four seams. run executes them in strict order and
// STOPS at the first failure, returning the highest step that fully succeeded. The
// invariant enforced by construction: the tag seam is reached ONLY after build AND
// smoke returned nil, so a git tag can never exist without a built, pushed and
// smoke-passed image; notify runs only after the tag is minted.
type releasePlan struct {
	build  func(context.Context) error
	smoke  func(context.Context) error
	tag    func(context.Context) error
	notify func(context.Context) error
}

func (p releasePlan) run(ctx context.Context) (releaseStep, error) {
	steps := []struct {
		step releaseStep
		fn   func(context.Context) error
	}{
		{stepBuilt, p.build},
		{stepSmoked, p.smoke},
		{stepTagged, p.tag},
		{stepNotified, p.notify},
	}
	reached := stepNone
	for _, s := range steps {
		if err := s.fn(ctx); err != nil {
			return reached, fmt.Errorf("release stopped before %s: %w", s.step, err)
		}
		reached = s.step
	}
	return reached, nil
}

// ── /v1/runner hook ──────────────────────────────────────────────────────────

// startRelease drives native release semantics for cloud's self-publish on
// /v1/runner: pin the commit, compute the next version, then in a DETACHED goroutine
// build → smoke → tag → notify (releasePlan.run). It returns 202 immediately with
// the computed version and image; the pipeline outlives the request (like
// buildFromPush). Because the tag is minted only after a proven image, a failure at
// build or smoke leaves NO tag and universe is never told of a phantom version.
func startRelease(s *cloud.Service[state], c *zip.Ctx, req runnerBuildReq) error {
	ref := firstNonEmpty(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	sha, err := resolveCommit(s, c.Context(), releaseRepoSlug, ref)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "resolve %s: %v", ref, err)
	}
	version, err := computeReleaseVersion(s, c.Context(), releaseRepoSlug)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "compute release version: %v", err)
	}
	tag := "v" + version
	image := releaseImage + ":" + tag
	repoURL := firstNonEmpty(strings.TrimSpace(req.Repo), releaseRepoURL)
	dockerfile := firstNonEmpty(strings.TrimSpace(req.Dockerfile), "Dockerfile")

	bldID, err := genID("rel")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	plan := releaseFor(s, repoURL, sha, image, tag, dockerfile, bldID)

	ctx := context.WithoutCancel(c.Context())
	go func() {
		reached, rerr := plan.run(ctx)
		if rerr != nil {
			s.Log.Error("release failed", "version", version, "image", image, "reached", reached.String(), "err", rerr)
			return
		}
		s.Log.Info("release published", "version", version, "image", image, "sha", sha)
	}()

	s.Log.Info("release started", "version", version, "image", image, "repo", repoURL, "sha", sha)
	return c.JSON(http.StatusAccepted, runnerBuildResp{
		BuildJobID: bldID, Status: "releasing", RunnerPool: "32g", Image: image,
	})
}

// releaseFor assembles the production pipeline for one version. Each seam is the REAL
// action — a k8s Job for build and smoke, a GitHub API call for tag and notify —
// wired once here so run stays a pure ordering. build launches the ONE privileged
// direct-build core (launchDirectBuild, the same /v1/runner uses) and waits for the
// image to be pushed; smoke boots that pushed image and waits for "listening"; tag
// mints the receipt; notify rolls it to universe.
func releaseFor(s *cloud.Service[state], repoURL, sha, image, tag, dockerfile, bldID string) releasePlan {
	return releasePlan{
		build: func(ctx context.Context) error {
			job, err := s.State.k8s.launchDirectBuild(ctx, repoURL, sha, image, dockerfile, bldID)
			if err != nil {
				return fmt.Errorf("launch build: %w", err)
			}
			if err := s.State.k8s.waitForJob(ctx, job, buildDeadline); err != nil {
				return fmt.Errorf("build: %w", err)
			}
			return nil
		},
		smoke:  func(ctx context.Context) error { return smokeImage(s, ctx, image, bldID) },
		tag:    func(ctx context.Context) error { return tagRelease(s, ctx, releaseRepoSlug, sha, tag) },
		notify: func(ctx context.Context) error { return rolloutRelease(s, ctx, image, sha) },
	}
}

// rolloutRelease rolls the proven image live. It is the release pipeline's final
// step, reached only AFTER the tag receipt is minted (build + smoke passed).
//
// PRIMARY — native CR rollout: patch the operator hanzo.ai/v1 Service CR's
// spec.image directly (cloud.OnServiceRelease → clients/paas releaseService), so
// the operator reconciles the Deployment. No ArgoCD, no repository_dispatch, no
// git round-trip — the direct-CR seam this closes.
//
// MIRROR — GitOps: also fire the image-update dispatch at universe
// (notifyUniverse) so any environment still reconciled by the git/ArgoCD pipeline
// stays in sync during the cutover.
//
// Best-effort composition: the step succeeds if EITHER path rolled the image, so a
// missing cloud-api CR-patch RBAC (native) or a missing dispatch token (GitOps)
// alone never fails a release that already produced a proven, tagged image.
func rolloutRelease(s *cloud.Service[state], ctx context.Context, image, sha string) error {
	var crErr error
	if cloud.ServiceReleaserRegistered() {
		crErr = cloud.OnServiceRelease(ctx, cloud.ServiceReleaseEvent{Service: releaseServiceName, Image: image, SHA: sha})
		if crErr == nil {
			s.Log.Info("release rolled out via operator CR patch (native)", "service", releaseServiceName, "image", image)
		} else {
			s.Log.Warn("native CR rollout failed; relying on the GitOps mirror", "service", releaseServiceName, "image", image, "err", crErr)
		}
	} else {
		crErr = fmt.Errorf("paas control plane not co-resident (no native CR rollout)")
	}

	nuErr := notifyUniverse(s, ctx, image, sha)
	if nuErr != nil {
		s.Log.Warn("universe image-update mirror failed", "image", image, "err", nuErr)
	}
	if crErr != nil && nuErr != nil {
		return fmt.Errorf("rollout failed on both paths: native=%v; gitops=%v", crErr, nuErr)
	}
	return nil
}

// smokeImage boots the just-built image in-cluster and waits for the smoke Job to
// pass — the native mirror of release.yml's smoke gate. A failed or timed-out Job is
// a smoke failure, which STOPS the pipeline before the tag.
func smokeImage(s *cloud.Service[state], ctx context.Context, image, bldID string) error {
	key, err := randKey()
	if err != nil {
		return fmt.Errorf("smoke key: %w", err)
	}
	job, err := s.State.k8s.launchSmokeJob(ctx, image, key, bldID)
	if err != nil {
		return fmt.Errorf("launch smoke: %w", err)
	}
	if err := s.State.k8s.waitForJob(ctx, job, smokeJobDeadline); err != nil {
		return fmt.Errorf("smoke: %w", err)
	}
	return nil
}

// randKey is a throwaway 32-byte base64 KMS master key for the smoke boot (no real
// secret) so the KMS plane mounts on its normal ready path exactly as prod does.
func randKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// ── version-compute seams ────────────────────────────────────────────────────

// computeReleaseVersion reads the two tag universes release.yml folds together — the
// repo's git tags and releaseImage's PUBLISHED tags — and returns the monotonic next
// patch.
//
// BOTH lists are required. Neither is best-effort, because the maximum of a PARTIAL
// view is not a maximum: with the published tags missing, the computation happily
// returns a number that already names a pushed image, and the release then overwrites
// it. That is not theory — it shipped. The published-tag call was a GitHub
// packages-API read needing a `read:packages` scope our PAT does not carry; it
// answered 403, the error was logged as a warning and swallowed, and the version was
// computed from git tags alone. Git tags had stalled at v1.801.209 while the registry
// (and production) had reached v1.801.213, so the next "release" computed v1.801.210
// — four versions BACKWARD, aimed at overwriting a published image. Enumerating both
// universes is the whole mechanism; if either cannot be read, there is no sound answer
// and the release must stop rather than guess.
func computeReleaseVersion(s *cloud.Service[state], ctx context.Context, repo string) (string, error) {
	git, err := gitTags(s, ctx, repo)
	if err != nil {
		return "", fmt.Errorf("list git tags: %w", err)
	}
	published, err := publishedTags(ctx)
	if err != nil {
		return "", fmt.Errorf("list published image tags: %w", err)
	}
	return nextVersion(git, published, releaseFloor)
}

func gitTags(s *cloud.Service[state], ctx context.Context, repo string) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
	}
	code, err := githubJSON(s, ctx, http.MethodGet, "/repos/"+repo+"/tags?per_page=100", ghToken(), nil, &out)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("git tags: status %d", code)
	}
	names := make([]string, 0, len(out))
	for _, t := range out {
		names = append(names, t.Name)
	}
	return names, nil
}

// ── published-tag enumeration (the registry answers) ─────────────────────────

// registryBase is the OCI registry root for releaseImage. A var (not a const) ONLY so
// tests can point it at an httptest server; production always uses the real registry.
var registryBase = "https://ghcr.io"

// registryPageMax bounds Link-header pagination. Reaching it is an ERROR, never a
// silent truncation — a capped list is a partial view, and a partial view is exactly
// the unsound maximum this whole seam exists to prevent.
const registryPageMax = 64

// publishedTags lists every tag currently published for releaseImage.
//
// It asks the REGISTRY, not GitHub's package-metadata API. The registry is the
// authority on the question actually being asked — "which tags exist, and which would
// an overwrite clobber" — it is what `docker pull` resolves against, and its pull
// scope is anonymous, so enumeration no longer depends on a PAT carrying a
// `read:packages` scope ours does not have.
//
// Pagination is not optional. The registry caps a page (GHCR: 1000 tags) and returns
// them in insertion order, so the NEWEST versions are on the LAST page — reading page
// one alone reports a stale maximum, the same unsound answer by a different route.
func publishedTags(ctx context.Context) ([]string, error) {
	host, repo, ok := strings.Cut(releaseImage, "/")
	if !ok {
		return nil, fmt.Errorf("release image %q names no repository", releaseImage)
	}
	token, err := registryPullToken(ctx, host, repo)
	if err != nil {
		return nil, err
	}
	var tags []string
	next := "/v2/" + repo + "/tags/list?n=1000"
	for page := 0; next != ""; page++ {
		if page == registryPageMax {
			return nil, fmt.Errorf("tag list exceeded %d pages: refusing a truncated view", registryPageMax)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		link, err := registryGet(ctx, registryBase+next, token, &body)
		if err != nil {
			return nil, err
		}
		tags = append(tags, body.Tags...)
		next = nextLink(link)
	}
	return tags, nil
}

// registryPullToken exchanges nothing for a pull-scoped bearer — the anonymous half of
// the Docker registry token flow, all a public image's tag list requires.
func registryPullToken(ctx context.Context, host, repo string) (string, error) {
	var body struct {
		Token string `json:"token"`
	}
	u := registryBase + "/token?service=" + url.QueryEscape(host) +
		"&scope=" + url.QueryEscape("repository:"+repo+":pull")
	if _, err := registryGet(ctx, u, "", &body); err != nil {
		return "", fmt.Errorf("registry pull token: %w", err)
	}
	if body.Token == "" {
		return "", fmt.Errorf("registry pull token: empty")
	}
	return body.Token, nil
}

// registryGet performs one registry read, decoding into out and returning the Link
// header that carries the next page. A non-200 is an error here (unlike githubJSON,
// whose callers each apply their own status policy): every registry read on this path
// is an enumeration that must be complete or fail.
func registryGet(ctx context.Context, endpoint, token string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := releaseHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", endpoint, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return resp.Header.Get("Link"), nil
}

// nextLink returns the rel="next" target of an RFC-8288 Link header, or "" when the
// page is the last — the loop's terminating condition.
func nextLink(header string) string {
	for _, field := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(field), ";")
		target := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, p := range parts[1:] {
			if strings.EqualFold(strings.TrimSpace(p), `rel="next"`) {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

// resolveCommit pins a ref (branch/tag/full sha) to a full commit SHA via the GitHub
// API, so the whole release — the build, and the tag that receipts it — targets ONE
// immutable commit. A 40-hex ref is already a commit and returned as-is.
func resolveCommit(s *cloud.Service[state], ctx context.Context, repo, ref string) (string, error) {
	if isHex40(ref) {
		return ref, nil
	}
	var out struct {
		SHA string `json:"sha"`
	}
	code, err := githubJSON(s, ctx, http.MethodGet, "/repos/"+repo+"/commits/"+ref, ghToken(), nil, &out)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK || out.SHA == "" {
		return "", fmt.Errorf("resolve %s@%s: status %d", repo, ref, code)
	}
	return out.SHA, nil
}

func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// ── receipt + notify seams (GitHub API) ──────────────────────────────────────

// tagRelease mints the git tag on repo at sha via the GitHub refs API — the native
// equivalent of release.yml's `git tag && git push`. Called ONLY after build + smoke
// pass, so the tag is a RECEIPT for a proven image, never a build trigger. A 422 (ref
// already exists) is surfaced as a collision, exactly release.yml's guard against
// minting a number a concurrent run already took.
func tagRelease(s *cloud.Service[state], ctx context.Context, repo, sha, tag string) error {
	tok := ghToken()
	if tok == "" {
		return fmt.Errorf("no GH_PAT configured for tag receipt")
	}
	code, err := githubJSON(s, ctx, http.MethodPost, "/repos/"+repo+"/git/refs", tok,
		map[string]string{"ref": "refs/tags/" + tag, "sha": sha}, nil)
	if err != nil {
		return err
	}
	if code == http.StatusUnprocessableEntity {
		return fmt.Errorf("tag %s already exists (collision)", tag)
	}
	if code != http.StatusCreated {
		return fmt.Errorf("create tag %s: status %d", tag, code)
	}
	s.Log.Info("release tag minted (receipt for a pushed, smoke-passed image)", "repo", repo, "tag", tag, "sha", sha)
	return nil
}

// notifyUniverse fires the image-update repository_dispatch at hanzoai/universe so the
// GitOps pipeline rolls the proven image — the SAME image-update contract every
// service uses (release.yml's notify-universe). Runs ONLY after the tag is minted, so
// universe is never asked to deploy a phantom tag. Token is UNIVERSE_DISPATCH_TOKEN
// from env (KMS-provisioned); fail closed if unset.
func notifyUniverse(s *cloud.Service[state], ctx context.Context, image, sha string) error {
	tok := getenv("UNIVERSE_DISPATCH_TOKEN", "")
	if tok == "" {
		return fmt.Errorf("no UNIVERSE_DISPATCH_TOKEN configured")
	}
	body := map[string]any{
		"event_type": "image-update",
		"client_payload": map[string]string{
			"service": "cloud",
			"image":   image,
			"sha":     sha,
			"env":     "all",
		},
	}
	code, err := githubJSON(s, ctx, http.MethodPost, "/repos/"+universeRepo+"/dispatches", tok, body, nil)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent {
		return fmt.Errorf("notify universe: status %d", code)
	}
	s.Log.Info("universe notified (image-update)", "image", image, "sha", sha)
	return nil
}

// ── shared GitHub seam ───────────────────────────────────────────────────────

// ghToken is the GitHub PAT for the release seams (list tags, resolve commit, mint
// tag). GH_PAT — the admin:org + write:packages token release.yml uses — read from
// env (KMS-provisioned). Empty ⇒ the dependent step fails closed.
func ghToken() string { return getenv("GH_PAT", "") }

// releaseHTTP is the one client for the release seams — a bounded timeout so a hung
// GitHub call can never wedge the pipeline goroutine.
var releaseHTTP = &http.Client{Timeout: 30 * time.Second}

// githubJSON performs one GitHub REST call: it marshals body (if non-nil), sets the
// bearer + JSON headers, and decodes the response into out (if non-nil), returning
// the status code so each caller applies its own success/collision policy. It is the
// single outbound seam the release path uses — DRY across list, resolve, tag, notify.
func githubJSON(s *cloud.Service[state], ctx context.Context, method, path, token string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, githubAPIBase+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := releaseHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// Decode ONLY a success body. GitHub answers a non-2xx with an error object
	// ({"message":…}), never the success shape, so decoding one into out yields a
	// bogus unmarshal error that MASKS the status the caller's policy is written
	// against — that is how a 403 on the packages API read as a decode bug and went
	// unnoticed through four releases. Draining instead keeps the seam's contract
	// (return the status, let each caller apply its own success/collision policy)
	// and makes every `if code != …` below reachable for the first time.
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		if derr := json.NewDecoder(resp.Body).Decode(out); derr != nil && derr != io.EOF {
			return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, path, derr)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}
