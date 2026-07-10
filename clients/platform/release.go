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
// already-pushed container tags, floored at floor. Pure — the caller injects both
// lists so it is hermetic. It mirrors release.yml's version step exactly:
// max(floor, git…, container…) + 1 patch, never a major/minor jump. Folding in the
// container tags means a number that already has a PUSHED image (even one whose run
// died before tagging, or whose smoke failed after push) is never reused — the
// phantom-tag prevention.
func nextVersion(gitTags, containerTags []string, floor string) (string, error) {
	max, ok := parseSemver(floor)
	if !ok {
		return "", fmt.Errorf("invalid version floor %q", floor)
	}
	for _, t := range append(append([]string{}, gitTags...), containerTags...) {
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
		notify: func(ctx context.Context) error { return notifyUniverse(s, ctx, image, sha) },
	}
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

// ── version-compute seams (GitHub API) ───────────────────────────────────────

// computeReleaseVersion reads the two tag universes release.yml folds together — the
// repo's git tags and ghcr.io/hanzoai/cloud's pushed container tags — and returns the
// monotonic next patch. Container tags are best-effort: if that call fails we still
// bump over the git tags (never below a pushed number we could read).
func computeReleaseVersion(s *cloud.Service[state], ctx context.Context, repo string) (string, error) {
	git, err := gitTags(s, ctx, repo)
	if err != nil {
		return "", fmt.Errorf("list git tags: %w", err)
	}
	cont, err := containerTags(s, ctx)
	if err != nil {
		s.Log.Warn("release: list container tags failed (bumping over git tags only)", "err", err)
	}
	return nextVersion(git, cont, releaseFloor)
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

func containerTags(s *cloud.Service[state], ctx context.Context) ([]string, error) {
	var out []struct {
		Metadata struct {
			Container struct {
				Tags []string `json:"tags"`
			} `json:"container"`
		} `json:"metadata"`
	}
	code, err := githubJSON(s, ctx, http.MethodGet, "/orgs/hanzoai/packages/container/cloud/versions?per_page=100", ghToken(), nil, &out)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("container versions: status %d", code)
	}
	var tags []string
	for _, v := range out {
		tags = append(tags, v.Metadata.Container.Tags...)
	}
	return tags, nil
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
	if out != nil {
		if derr := json.NewDecoder(resp.Body).Decode(out); derr != nil && derr != io.EOF {
			return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, path, derr)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}
