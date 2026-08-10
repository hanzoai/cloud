// release.go — native release semantics on the /v1/runner build path.
//
// This is the in-cloud port of .github/workflows/release.yml: cloud self-publishes
// ghcr.io/hanzoai/cloud with the SAME invariant the workflow exists to enforce —
//
//	a git tag v<X.Y.Z> exists  ⇔  an image ghcr.io/hanzoai/cloud:v<X.Y.Z> was
//	pushed AND booted to "listening" in the smoke test.
//
// The tag is CLAIMED FIRST, and that is the load-bearing detail. The order is
// main push → claim version → build → smoke → verify claim → pin.
//
// It used to be … → build → smoke → MINT tag → pin, on the reasoning that a tag
// should be a receipt for a proven image rather than a trigger for a build that
// might fail. That reasoning is right about what a tag MEANS and wrong about what
// a version IS. A version is a shared, exclusive resource: this lane and
// .hanzo/workflows/cicd.yml both publish ghcr.io/hanzoai/cloud, both computed
// max+1 by READING the registry, and a read reserves nothing. Both then pushed —
// and a ghcr tag is MUTABLE, so the second push replaced the first's bytes under
// a name the first believed it owned (v1.801.361 at 04:40:53, v1.801.410 at
// 08:17:12, the replacement carrying no revision label at all). The loser learned
// this at the tag step, three quarters of the way through, having already
// corrupted the winner's image — which the winner went on to smoke, pin and ship.
//
// Creating refs/tags/v<N> is the only operation available here that the server
// performs as a COMPARE-AND-SWAP, so it is the only thing that can allocate.
// Claiming first costs a hole in the numbering when a build fails — a tag with no
// image — and that is the cheap direction: a hole is inert and visible (pin.sh
// refuses a tag that does not resolve), while a reused number is invisible and
// serves the wrong bytes.
//
// The whole pipeline is four injectable seams (releasePlan) run in strict order
// (run), so the ordering invariant is enforced by construction and unit-tested
// hermetically, while each concrete step (a k8s Job for build/smoke, a GitHub API
// call for the tag, a git push for the pin) is wired once in releaseFor.

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

const (
	// releaseImage is the ONE image cloud self-publishes; releaseRepoSlug/URL name
	// its source; releaseFloor is the version floor for the first release ever
	// (mirrors release.yml).
	releaseImage    = "ghcr.io/hanzoai/cloud"
	releaseRepoSlug = "hanzoai/cloud"
	releaseRepoURL  = "https://github.com/hanzoai/cloud"
	releaseFloor    = "1.786.0"
	// releaseServiceName is cloud's own name in the fleet inventory — the basename
	// of its declaration at universe charts/app/values/hanzo/cloud.yaml, which is
	// the file the release pins (pin.go).
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
	stepPinned
)

func (s releaseStep) String() string {
	switch s {
	case stepBuilt:
		return "built"
	case stepSmoked:
		return "smoked"
	case stepTagged:
		return "tagged"
	case stepPinned:
		return "pinned"
	default:
		return "none"
	}
}

// releasePlan is the release as four seams. run executes them in strict order and
// STOPS at the first failure, returning the highest step that fully succeeded. The
// invariant enforced by construction: the pin is reached ONLY after build, smoke
// AND the tag verification returned nil, so nothing can go live that did not
// build, boot, and still hold the version it was allocated.
type releasePlan struct {
	build func(context.Context) error
	smoke func(context.Context) error
	tag   func(context.Context) error
	pin   func(context.Context) error
}

func (p releasePlan) run(ctx context.Context) (releaseStep, error) {
	steps := []struct {
		step releaseStep
		fn   func(context.Context) error
	}{
		{stepBuilt, p.build},
		{stepSmoked, p.smoke},
		{stepTagged, p.tag},
		{stepPinned, p.pin},
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
// build → smoke → tag → pin (releasePlan.run). It returns 202 immediately with
// the computed version and image; the pipeline outlives the request (like
// buildFromPush). Because the tag is minted only after a proven image, a failure at
// build or smoke leaves NO tag and universe is never told of a phantom version.
func startRelease(s *cloud.Service[state], ctx context.Context, req runnerBuildReq) (*runnerBuildResp, error) {
	ref := firstNonEmpty(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	// A repo is a CLONE URL, and an unparseable one is refused here rather than
	// deep in the detached pipeline. Otherwise a bare name ("cloud" for
	// "https://github.com/hanzoai/cloud") answers 202 with an image tag, launches
	// nothing, and only says so in a log line nobody is reading — the caller is
	// told a release is in flight that never started.
	repo := strings.TrimSpace(req.Repo)
	if repo != "" {
		if u, err := url.Parse(repo); err != nil || u.Scheme == "" || u.Host == "" {
			return nil, zip.ErrBadRequest("repo must be a clone URL such as https://github.com/hanzoai/cloud; omit it to release " + releaseRepoURL)
		}
	}
	bldID, image, err := launchRelease(s, ctx, ref, repo, strings.TrimSpace(req.Dockerfile))
	if err != nil {
		return nil, err
	}
	return &runnerBuildResp{
		BuildJobID: bldID, Status: "releasing", RunnerPool: "32g", Image: image,
	}, nil
}

// ReleaseState is what a release id can be asked about. A 202 hands back an id,
// so the id has to mean something after the request returns — otherwise a release
// that dies in the detached pipeline is indistinguishable from one still running.
type ReleaseState struct {
	// ID is the build id returned by the 202.
	ID string `json:"id"`
	// Image is the tag the release publishes on success.
	Image string `json:"image"`
	// Version is that tag without the leading "v".
	Version string `json:"version"`
	// SHA is the commit the release pinned.
	SHA string `json:"sha"`
	// Status is "releasing", "released" or "failed".
	Status string `json:"status"`
	// Reached is the last pipeline step completed: built, smoked, tagged, pinned.
	Reached string `json:"reached,omitempty"`
	// Error is why it stopped, when it failed.
	Error string `json:"error,omitempty"`
	// StartedAt / EndedAt are unix seconds.
	StartedAt int64 `json:"startedAt"`
	EndedAt   int64 `json:"endedAt,omitempty"`
}

// releases keeps the last few outcomes in memory. A release is a minutes-long
// operation on a single-writer path, so a bounded map is the whole requirement —
// and an in-memory record honestly disappears on restart rather than pretending
// to be a durable history the pipeline does not keep.
var releases = struct {
	sync.Mutex
	byID  map[string]*ReleaseState
	order []string
}{byID: map[string]*ReleaseState{}}

const releasesKept = 20

func recordRelease(st *ReleaseState) {
	releases.Lock()
	defer releases.Unlock()
	if _, ok := releases.byID[st.ID]; !ok {
		releases.order = append(releases.order, st.ID)
		for len(releases.order) > releasesKept {
			delete(releases.byID, releases.order[0])
			releases.order = releases.order[1:]
		}
	}
	releases.byID[st.ID] = st
}

// ReleaseByID returns a recorded release. found=false once it has aged out.
func ReleaseByID(id string) (ReleaseState, bool) {
	releases.Lock()
	defer releases.Unlock()
	st, ok := releases.byID[strings.TrimSpace(id)]
	if !ok {
		return ReleaseState{}, false
	}
	return *st, true
}

// Releases returns the recorded releases, newest first.
func Releases() []ReleaseState {
	releases.Lock()
	defer releases.Unlock()
	out := make([]ReleaseState, 0, len(releases.order))
	for _, v := range slices.Backward(releases.order) {
		if st, ok := releases.byID[v]; ok {
			out = append(out, *st)
		}
	}
	return out
}

// releasing is the in-flight guard. A release is a fabric-wide operation that
// mints the next version from the tags that already exist, so two overlapping
// runs would compute the SAME version and race to publish it. Only one at a
// time; a second trigger is refused rather than queued, because by the time the
// first finishes its commit is already the newer one.
var releasing atomic.Bool

// launchRelease pins the commit, computes the next version, and starts the
// DETACHED build → smoke → tag → pin pipeline, returning the build id and the
// image it will publish. It is the ONE release entry point: the /v1/runner HTTP
// path and the push trigger both come through here, so a release cut by either
// is the same pipeline with the same guards. Errors are zip errors so the HTTP
// caller can return them directly.
func launchRelease(s *cloud.Service[state], ctx context.Context, ref, repo, dockerfile string) (string, string, error) {
	if !releasing.CompareAndSwap(false, true) {
		return "", "", zip.Errorf(http.StatusConflict, "a release is already in flight")
	}
	ok := false
	defer func() {
		// Hand the flag to the goroutine on success; clear it on every early exit.
		if !ok {
			releasing.Store(false)
		}
	}()

	sha, err := resolveCommit(s, ctx, releaseRepoSlug, ref)
	if err != nil {
		return "", "", zip.Errorf(http.StatusBadGateway, "resolve %s: %v", ref, err)
	}
	version, err := claimReleaseVersion(s, ctx, releaseRepoSlug, sha)
	if err != nil {
		return "", "", zip.Errorf(http.StatusBadGateway, "claim release version: %v", err)
	}
	tag := "v" + version
	image := releaseImage + ":" + tag
	bldID, err := genID("rel")
	if err != nil {
		return "", "", zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	repoURL := firstNonEmpty(repo, releaseRepoURL)
	plan := releaseFor(s, repoURL, sha, image, tag, firstNonEmpty(dockerfile, "Dockerfile"), bldID)

	state := &ReleaseState{
		ID: bldID, Image: image, Version: version, SHA: sha,
		Status: "releasing", StartedAt: time.Now().Unix(),
	}
	recordRelease(state)

	detached := context.WithoutCancel(ctx)
	go func() {
		defer releasing.Store(false)
		reached, rerr := plan.run(detached)
		done := &ReleaseState{
			ID: bldID, Image: image, Version: version, SHA: sha,
			Reached: reached.String(), StartedAt: state.StartedAt, EndedAt: time.Now().Unix(),
		}
		if rerr != nil {
			done.Status, done.Error = "failed", rerr.Error()
			recordRelease(done)
			s.Log.Error("release failed", "version", version, "image", image, "reached", reached.String(), "err", rerr)
			return
		}
		done.Status = "released"
		recordRelease(done)
		s.Log.Info("release published", "version", version, "image", image, "sha", sha)
	}()
	ok = true

	s.Log.Info("release started", "version", version, "image", image, "repo", repoURL, "sha", sha)
	return bldID, image, nil
}

// releaseFor assembles the production pipeline for one version. Each seam is the REAL
// action — a k8s Job for build and smoke, a GitHub API call for the tag, a git
// push for the pin —
// wired once here so run stays a pure ordering. build launches the ONE privileged
// direct-build core (launchDirectBuild, the same /v1/runner uses) and waits for the
// image to be pushed; smoke boots that pushed image and waits for "listening"; tag
// mints the receipt; pin moves the value in universe that makes the image live.
func releaseFor(s *cloud.Service[state], repoURL, sha, image, tag, dockerfile, bldID string) releasePlan {
	return releasePlan{
		build: func(ctx context.Context) error {
			job, err := s.State.k8s.launchDirectBuild(ctx, platformBuildOrg, repoURL, sha, image, dockerfile, bldID, nil)
			if err != nil {
				return fmt.Errorf("launch build: %w", err)
			}
			if err := s.State.k8s.waitForJob(ctx, job, buildDeadline); err != nil {
				return fmt.Errorf("build: %w", err)
			}
			return nil
		},
		smoke: func(ctx context.Context) error { return smokeImage(s, ctx, image, bldID) },
		tag:   func(ctx context.Context) error { return tagRelease(s, ctx, releaseRepoSlug, sha, tag) },
		pin:   func(ctx context.Context) error { return rolloutRelease(s, ctx, image, sha) },
	}
}

// rolloutRelease rolls the proven image live. It is the release pipeline's final
// step, reached only AFTER the tag receipt is minted (build + smoke passed).
//
// ONE WRITER: move image.tag in universe's charts/app/values/hanzo/cloud.yaml
// (pin.go pinUniverse) and let cd.hanzo.ai reconcile it. That scalar IS what runs —
// the fleet ApplicationSet's git generator reads those files, and nothing else
// makes an image live.
//
// It used to patch an operator hanzo.ai/v1 App CR instead. The CRD kind exists, but
// there is no `cloud` CR: cloud is reconciled by cd.hanzo.ai from the values file,
// not by the operator. So the patch had nothing to write to, every release failed
// here with the image built, smoked and tagged but NOT live, and v1.801.335 had to
// be pinned by hand. Two beliefs about what makes an image live disagreed in the
// source; the values file wins, because it is the one the cluster reads.
//
// The failure is reported honestly rather than tolerated: if the pin cannot move,
// the image is built, smoke-passed and tagged but NOT live, and a release that says
// otherwise is worse than one that fails.
func rolloutRelease(s *cloud.Service[state], ctx context.Context, image, sha string) error {
	if err := pinUniverse(s, ctx, releaseServiceName, image, sha); err != nil {
		return fmt.Errorf("pin %s in universe (image %s is tagged but NOT live): %w", releaseServiceName, image, err)
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

// claimReleaseVersion allocates the next version AND takes ownership of it in one
// act, before anything is built.
//
// computeReleaseVersion alone cannot allocate anything. It READS two tag universes
// and returns max+1, and a read reserves nothing: two lanes reading the same
// registry seconds apart both get the same answer, and both believe it is theirs.
// This lane and .hanzo/workflows/cicd.yml are exactly those two lanes — both
// publish ghcr.io/hanzoai/cloud, both computed max+1, and both then pushed. A ghcr
// tag is MUTABLE, so the second push REPLACED the first's bytes under a name the
// first had already been told it owned: v1.801.361 at 04:40:53, v1.801.410 at
// 08:17:12. The loser only found out at the tag step, three quarters of the way
// through a pipeline, long after it had overwritten the winner's image — which the
// winner went on to smoke, pin and ship.
//
// Creating refs/tags/v<N> is the one operation available here that the server
// performs as a COMPARE-AND-SWAP: 201 if the ref did not exist, 422 if it did,
// decided under GitHub's lock rather than in our head. So the claim is the
// allocation. A number that cannot be claimed was never ours to build.
//
// The retry bumps the patch LOCALLY rather than re-running computeReleaseVersion.
// A freshly claimed-but-unbuilt tag is invisible to both of that function's
// sources — it has no image yet, and gitTags reads only the first 100 of ~1700
// tags — so recomputing would return the same number, collide again, and burn all
// ten attempts without ever advancing. Counting up from the number we already have
// always terminates.
func claimReleaseVersion(s *cloud.Service[state], ctx context.Context, repo, sha string) (string, error) {
	tok := ghToken()
	if tok == "" {
		return "", fmt.Errorf("no GH_PAT configured — a version cannot be claimed, and an unclaimed version must not be built")
	}
	start, err := computeReleaseVersion(s, ctx, repo)
	if err != nil {
		return "", err
	}
	return claimFrom(s, ctx, repo, sha, start)
}

// claimFrom is the exclusive-allocation loop, separated from how the starting
// number was discovered. computeReleaseVersion reads the registry and the git tag
// list — the world — while this walks upward taking the first number the server
// will grant. Splitting them is what lets the RACE be tested at all: the
// behaviour worth proving is "two commits never hold one version", and that has
// nothing to do with where counting began.
func claimFrom(s *cloud.Service[state], ctx context.Context, repo, sha, start string) (string, error) {
	tok := ghToken()
	if tok == "" {
		return "", fmt.Errorf("no GH_PAT configured — a version cannot be claimed, and an unclaimed version must not be built")
	}
	v, ok := parseSemver(start)
	if !ok {
		return "", fmt.Errorf("computed version %q is not semver", start)
	}
	for range 10 {
		version := v.String()
		tag := "v" + version
		code, err := githubJSON(s, ctx, http.MethodPost, "/repos/"+repo+"/git/refs", tok,
			map[string]string{"ref": "refs/tags/" + tag, "sha": sha}, nil)
		if err != nil {
			return "", fmt.Errorf("claim %s: %w", tag, err)
		}
		if code == http.StatusCreated {
			s.Log.Info("release version claimed before build (compare-and-swap)", "repo", repo, "tag", tag, "sha", sha)
			return version, nil
		}
		if code != http.StatusUnprocessableEntity {
			return "", fmt.Errorf("claim %s: status %d", tag, code)
		}
		// Held. By this same commit — a re-run of a release that already claimed
		// its number — or by a different one, which is a genuine collision and
		// means the number belongs to somebody else.
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if _, gerr := githubJSON(s, ctx, http.MethodGet, "/repos/"+repo+"/git/ref/tags/"+tag, tok, nil, &ref); gerr == nil && ref.Object.SHA == sha {
			s.Log.Info("release version already claimed at this commit — resuming", "repo", repo, "tag", tag, "sha", sha)
			return version, nil
		}
		s.Log.Info("release version is held by another commit — taking the next", "repo", repo, "tag", tag, "heldBy", ref.Object.SHA)
		v.patch++
	}
	return "", fmt.Errorf("could not claim a version in 10 attempts starting at %s", start)
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

// ── receipt seam (GitHub API) ────────────────────────────────────────────────

// tagRelease mints the git tag on repo at sha via the GitHub refs API — the native
// equivalent of release.yml's `git tag && git push`. Called ONLY after build + smoke
// pass, so the tag is a RECEIPT for a proven image, never a build trigger. A 422 (ref
// already exists) is surfaced as a collision, exactly release.yml's guard against
// minting a number a concurrent run already took.
// tagRelease no longer MINTS the tag — claimReleaseVersion did that before the
// build, because minting it here was the bug. A tag created at the end is a
// receipt for work already done; a tag created at the start is a RESERVATION, and
// only the second one prevents a second lane from spending the same number. What
// is left at this position is the assertion that the reservation still stands, and
// still names this commit, before the pin makes it production's problem.
func tagRelease(s *cloud.Service[state], ctx context.Context, repo, sha, tag string) error {
	tok := ghToken()
	if tok == "" {
		return fmt.Errorf("no GH_PAT configured to verify the tag receipt")
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	code, err := githubJSON(s, ctx, http.MethodGet, "/repos/"+repo+"/git/ref/tags/"+tag, tok, nil, &ref)
	if err != nil {
		return fmt.Errorf("read tag %s: %w", tag, err)
	}
	if code != http.StatusOK {
		return fmt.Errorf("tag %s was claimed by this release but reads back status %d — refusing to pin a release whose receipt is gone", tag, code)
	}
	if ref.Object.SHA != sha {
		return fmt.Errorf("tag %s now names %s, not %s — the claim was overwritten; nothing may be pinned", tag, ref.Object.SHA, sha)
	}
	s.Log.Info("release tag verified (claimed before build, still names this commit)", "repo", repo, "tag", tag, "sha", sha)
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
// single outbound seam the release path uses — DRY across list, resolve and tag.
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

// selfReleaseList is the platform's own release runs as a list answers them.
// Distinct from /v1/releases, which lists a TENANT's deployments.
type selfReleaseList struct {
	// Data is the recorded release runs, newest first.
	Data []ReleaseState `json:"data"`
}

// selfReleaseRef addresses one self-publish release by the id its 202 returned.
// It is not `releaseRef`: that name already belongs to the git ref whose merges
// publish the next version (push.go), and two things that mean different things do
// not share a name.
type selfReleaseRef struct {
	// ID is the build id the release trigger answered with, from the path.
	ID string `json:"id"`
}

// listSelfReleases lists the self-publish releases this process has run.
//
// It lists the platform's own release runs with their current state, so a release
// that answered 202 with an id can be followed to its end. SuperAdmin only — this
// is the platform's own publishing record, not a tenant surface.
//
// The record lives in THIS process's memory, so it covers the releases this
// instance started and does not survive a restart.
func (o ops) listSelfReleases(ctx context.Context, _ *noInput) (*selfReleaseList, error) {
	c, err := o.request(ctx)
	if err != nil {
		return nil, cloud.Super.Refusal()
	}
	if err := mayRelease(c); err != nil {
		return nil, err
	}
	return &selfReleaseList{Data: Releases()}, nil
}

// getSelfRelease returns one self-publish release by the id its 202 returned.
//
// It returns the state of one release run — which is the whole reason the trigger
// answers with an id, because without this a release that died in the detached
// pipeline would look exactly like one still in flight. SuperAdmin only.
//
// A 404 means the id is unknown OR has aged out of this process's in-memory record.
// That is the honest answer either way: the process genuinely cannot tell the two
// apart.
func (o ops) getSelfRelease(ctx context.Context, in *selfReleaseRef) (*ReleaseState, error) {
	c, err := o.request(ctx)
	if err != nil {
		return nil, cloud.Super.Refusal()
	}
	if err := mayRelease(c); err != nil {
		return nil, err
	}
	st, ok := ReleaseByID(in.ID)
	if !ok {
		return nil, zip.ErrNotFound("no such release in this process's record")
	}
	return &st, nil
}

// mayRelease is the ONE authority question this surface asks: may this caller act
// on the platform's own release? Cutting one (runner.go) and reading one (above)
// take the same answer from the same function, so the 202 can never hand back an id
// its caller may not ask about, and the two can never drift.
//
// THE SCOPE IS cloud.Super, and nothing is conjoined to it. releaseImage is
// ghcr.io/hanzoai/cloud — the binary every service in every org runs — so a release
// is an act against SHARED platform state: the tag it publishes is what iam, kms,
// gateway and every customer app roll onto at the next reconcile, in every tenant.
// That is what cloud.Super names (gate.go), and SuperAdmin ⟺ `owner == "admin"` is
// the one platform predicate the estate gates on.
//
// OWNING THE REGISTRY NAMESPACE IS NOT THAT PREDICATE. imageInOrgRegistry bounds an
// ordinary push (runner.go) precisely because a push lands ONE tenant's artifact in
// the namespace that tenant owns; a release lands OURS on everyone, so no property
// of the caller's own org can be what admits it. Nor is the org-scoped `isAdmin`
// bit a weaker spelling of platform authority: it is SELF-SERVICE — an org's own
// admin sets it on a member of THEIR org — so admitting "admin of the org that owns
// hanzoai" delegated the fleet's binary to whoever the `hanzo` tenant enrols, an
// authority the platform does not administer. A gate whose far side can enrol its
// own callers is not a gate.
//
// Validated is part of cloud.Super, which is what refuses the MACHINE path in the
// same expression rather than a second one: PLATFORM_BUILD_CALLBACK_TOKEN mints no
// principal, so a leaked build token may enqueue an ordinary build and can never
// cut a release.
func mayRelease(c *zip.Ctx) error {
	if !cloud.Super.Admits(cloud.AuthorityOf(c)) {
		return zip.ErrForbidden("a release of " + releaseImage + " requires SuperAdmin")
	}
	return nil
}
