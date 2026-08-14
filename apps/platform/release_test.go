package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func testService() *cloud.Service[state] {
	return &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}
}

// nextVersion is the pure floor: max(floor, git…, container…) + 1 PATCH, never a
// major/minor jump; unparseable tags ignored; a pushed container tag that outruns the
// git tags is honoured (phantom-tag prevention).
func TestNextVersion(t *testing.T) {
	cases := []struct {
		name string
		git  []string
		cont []string
		want string
	}{
		{"floor only", nil, nil, "1.786.1"},
		{"git max", []string{"v1.786.42", "v1.700.0", "latest", "sha-abc"}, nil, "1.786.43"},
		{"container beats git (phantom prevention)", []string{"v1.786.42"}, []string{"1.786.43", "1.786", "latest"}, "1.786.44"},
		{"git beats container", []string{"v1.786.50"}, []string{"1.786.43"}, "1.786.51"},
		{"patch-bumps the true max, never minor/major", []string{"v2.0.0"}, []string{"1.786.99"}, "2.0.1"},
		{"unparseable ignored", []string{"garbage", "v", "1.2"}, []string{"nope"}, "1.786.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextVersion(tc.git, tc.cont, releaseFloor)
			if err != nil {
				t.Fatalf("nextVersion: %v", err)
			}
			if got != tc.want {
				t.Fatalf("git=%v cont=%v: want %s, got %s", tc.git, tc.cont, tc.want, got)
			}
		})
	}
}

// The ordering invariant: build → smoke → tag → pin, stopping at the FIRST
// failure — so a failure at build or smoke NEVER reaches the tag seam. No tag without
// a proven image.
func TestReleasePlan_Ordering(t *testing.T) {
	mk := func(failAt releaseStep, calls *[]string) releasePlan {
		step := func(name string, self releaseStep) func(context.Context) error {
			return func(context.Context) error {
				*calls = append(*calls, name)
				if self == failAt {
					return fmt.Errorf("%s failed", name)
				}
				return nil
			}
		}
		return releasePlan{
			build: step("built", stepBuilt),
			smoke: step("smoked", stepSmoked),
			tag:   step("tagged", stepTagged),
			pin:   step("pinned", stepPinned),
		}
	}

	// Happy path: all four run in order, reached = pinned.
	var calls []string
	reached, err := mk(stepNone, &calls).run(context.Background())
	if err != nil || reached != stepPinned {
		t.Fatalf("happy: reached=%v err=%v", reached, err)
	}
	if got := strings.Join(calls, ","); got != "built,smoked,tagged,pinned" {
		t.Fatalf("happy order: %s", got)
	}

	// Build fails: nothing else runs, NO tag, reached = none.
	calls = nil
	reached, err = mk(stepBuilt, &calls).run(context.Background())
	if err == nil || reached != stepNone {
		t.Fatalf("build-fail: reached=%v err=%v", reached, err)
	}
	if slices.Contains(calls, "tagged") {
		t.Fatalf("build-fail MINTED A TAG WITHOUT A PROVEN IMAGE: %v", calls)
	}

	// Smoke fails: build ran (image pushed), tag/pin did NOT, reached = built.
	calls = nil
	reached, err = mk(stepSmoked, &calls).run(context.Background())
	if err == nil || reached != stepBuilt {
		t.Fatalf("smoke-fail: reached=%v err=%v", reached, err)
	}
	if slices.Contains(calls, "tagged") || slices.Contains(calls, "pinned") {
		t.Fatalf("smoke-fail reached tag/pin (phantom tag): %v", calls)
	}

	// Tag fails: build+smoke ran, the pin did NOT move, reached = smoked.
	calls = nil
	reached, err = mk(stepTagged, &calls).run(context.Background())
	if err == nil || reached != stepSmoked {
		t.Fatalf("tag-fail: reached=%v err=%v", reached, err)
	}
	if slices.Contains(calls, "pinned") {
		t.Fatalf("tag-fail reached pin: %v", calls)
	}
}

// The real smoke step: launchSmokeJob creates a Job that runs the EXACT pushed image
// under the sh wrapper gated on "listening", with the production-representative boot
// env and the GHCR pull secret — proven over the fake dynamic client.
func TestLaunchSmokeJob_CreatesJob(t *testing.T) {
	k := fakeK8s()
	image := "ghcr.io/hanzoai/cloud:v1.786.44"
	job, err := k.launchSmokeJob(context.Background(), image, "dGVzdC1rZXk=", "rel_abc")
	if err != nil {
		t.Fatalf("launchSmokeJob: %v", err)
	}
	obj, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), job, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get smoke job: %v", err)
	}
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(containers))
	}
	cm := containers[0].(map[string]any)
	if cm["image"] != image {
		t.Fatalf("smoke runs the WRONG image: %v (want %s)", cm["image"], image)
	}
	cmd, _, _ := unstructured.NestedStringSlice(cm, "command")
	if len(cmd) != 3 || cmd[0] != "/bin/sh" {
		t.Fatalf("want sh wrapper, got %v", cmd)
	}
	// The needle must be the line the binary WRITES. This asserted
	// `"message":"listening"` — which the zip transport has never logged, it logs
	// "zip listening" — so it held the smoke gate to a string that could not match
	// and the release could never pass smoke. A test that pins the wrong end of a
	// contract is how the gate stayed broken while looking covered.
	if !strings.Contains(cmd[2], `"message":"zip listening"`) {
		t.Fatalf("smoke script does not gate on the line zip logs: %s", cmd[2])
	}
	env, _, _ := unstructured.NestedSlice(cm, "env")
	got := map[string]string{}
	for _, e := range env {
		em := e.(map[string]any)
		got[em["name"].(string)], _ = em["value"].(string)
	}
	if got["CLOUD_ENV"] != "smoke" || got["CLOUD_DATA_DIR"] != "/data" || got["CLOUD_KMS_MASTER_KEY_REF"] == "" {
		t.Fatalf("smoke boot env wrong: %v", got)
	}
	// The secret must be one the build namespace actually holds. This named
	// `kaniko-ghcr`, which does not exist there, so every smoke fell back to an
	// anonymous pull — asserting the name proved only that the name was constant.
	pull, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "imagePullSecrets")
	if len(pull) != 1 || pull[0].(map[string]any)["name"] != buildPullSecret {
		t.Fatalf("smoke must pull with the build namespace's credential: %v", pull)
	}
}

// The smoke GATE reads the Job's terminal state (the same jobResult the reconciler
// uses): succeeded → nil, failed → error, and a missing Job past its deadline times
// out promptly rather than hanging.
func TestWaitForJob(t *testing.T) {
	k := fakeK8s()
	seed := func(name, field string) {
		job := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": name, "namespace": k.buildNS},
			"status":   map[string]any{field: int64(1)},
		}}
		if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	seed("ok", "succeeded")
	seed("bad", "failed")

	if err := k.waitForJob(context.Background(), "ok", time.Minute); err != nil {
		t.Fatalf("succeeded job: want nil, got %v", err)
	}
	if err := k.waitForJob(context.Background(), "bad", time.Minute); err == nil {
		t.Fatalf("failed job: want error, got nil")
	}
	if err := k.waitForJob(context.Background(), "ghost", 0); err == nil {
		t.Fatalf("missing job past deadline: want timeout error, got nil")
	}
}

// releaseAPI stands in for both upstreams the version compute reads: GitHub (git
// tags) and the OCI registry (published image tags, token + paginated tags/list).
// pages are served in order and the Link header chains them, exactly as GHCR does.
type releaseAPI struct {
	gitTags []string
	pages   [][]string
	regCode int // non-200 to make the registry fail
	hits    int // registry tags/list requests served
}

func (a *releaseAPI) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/hanzoai/cloud/tags":
			out := make([]map[string]string, 0, len(a.gitTags))
			for _, tag := range a.gitTags {
				out = append(out, map[string]string{"name": tag})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon-pull"})
		case r.URL.Path == "/v2/hanzoai/cloud/tags/list":
			if a.regCode != 0 {
				w.WriteHeader(a.regCode)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "denied"})
				return
			}
			page := a.hits
			a.hits++
			if page < len(a.pages)-1 {
				w.Header().Set("Link", fmt.Sprintf(`</v2/hanzoai/cloud/tags/list?last=%d&n=1000>; rel="next"`, page))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": a.pages[page]})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(swapAPIBase(srv.URL))
	t.Cleanup(swapRegistryBase(srv.URL))
}

// version-compute wiring over the REAL upstream shapes: it folds the repo's git tags
// with the registry's published tags and bumps the max.
func TestComputeReleaseVersion(t *testing.T) {
	t.Setenv("GH_PAT", "test-token")
	api := &releaseAPI{
		gitTags: []string{"v1.786.42", "v1.700.0"},
		pages:   [][]string{{"v1.786.43", "1.786", "latest"}},
	}
	api.start(t)

	got, err := computeReleaseVersion(testService(), context.Background(), releaseRepoSlug)
	if err != nil {
		t.Fatalf("computeReleaseVersion: %v", err)
	}
	if got != "1.786.44" { // published 1.786.43 beats git 1.786.42 → next patch.
		t.Fatalf("want 1.786.44 (published-max+1), got %s", got)
	}
}

// REGRESSION (shipped, 2026-07-25): the published-tag read answered 403 — the PAT
// lacked `read:packages` — and the failure was swallowed as a warning, so the version
// was computed from git tags alone. Git had stalled at 1.786.42 while the registry had
// reached 1.786.99, and the "next" release came out BELOW a published image, aimed at
// overwriting it. A partial view has no maximum: the compute must fail, not guess.
func TestComputeReleaseVersion_RefusesPartialView(t *testing.T) {
	t.Setenv("GH_PAT", "test-token")
	api := &releaseAPI{gitTags: []string{"v1.786.42"}, regCode: http.StatusForbidden}
	api.start(t)

	got, err := computeReleaseVersion(testService(), context.Background(), releaseRepoSlug)
	if err == nil {
		t.Fatalf("published-tag read failed but compute returned %q — a version below a pushed image", got)
	}
	if !strings.Contains(err.Error(), "published image tags") {
		t.Fatalf("want an error naming the unreadable list, got %v", err)
	}
}

// The registry pages its tag list (GHCR caps at 1000) in insertion order, so the
// NEWEST versions are on the LAST page. Reading page one alone reports a stale max —
// the same unsound answer the 403 produced, by a different route.
func TestPublishedTags_FollowsPaginationToTheNewest(t *testing.T) {
	api := &releaseAPI{pages: [][]string{
		{"v1.786.1", "v1.786.2"},
		{"v1.786.3", "latest"},
		{"v1.801.213"}, // the newest tag lives here, and only here
	}}
	api.start(t)

	tags, err := publishedTags(context.Background())
	if err != nil {
		t.Fatalf("publishedTags: %v", err)
	}
	if api.hits != 3 {
		t.Fatalf("want all 3 pages fetched, got %d", api.hits)
	}
	next, err := nextVersion(nil, tags, releaseFloor)
	if err != nil {
		t.Fatalf("nextVersion: %v", err)
	}
	if next != "1.801.214" {
		t.Fatalf("want 1.801.214 (last page's max + 1), got %s", next)
	}
}

// githubJSON returns the STATUS for a non-2xx instead of trying to decode GitHub's
// error object into the success shape — the masking that made a 403 read as a decode
// bug. Every caller's `if code != …` policy depends on reaching this.
func TestGitHubJSON_NonSuccessReportsStatusNotADecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Resource not accessible"})
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()

	var out []struct{ Name string }
	code, err := githubJSON(testService(), context.Background(), http.MethodGet, "/repos/x/y/tags", "tok", nil, &out)
	if err != nil {
		t.Fatalf("want the status surfaced with no error, got %v", err)
	}
	if code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", code)
	}
}

// The receipt: tagRelease POSTs refs/tags/<v> at the pinned sha with the bearer token.
// tagRelease VERIFIES the claim that claimReleaseVersion already took; it no
// longer creates anything. The distinction is the whole fix — a tag minted at the
// end is a receipt for a number two lanes may both have spent, a tag taken at the
// start is the thing that stops the second lane.
func TestTagRelease_VerifiesTheClaim(t *testing.T) {
	t.Setenv("GH_PAT", "test-token")
	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	for _, tc := range []struct {
		name    string
		status  int
		refSHA  string
		wantErr bool
	}{
		{"claim still stands", http.StatusOK, sha, false},
		{"receipt deleted under us", http.StatusNotFound, "", true},
		{"claim overwritten by another commit", http.StatusOK, "cafebabecafebabecafebabecafebabecafebabe", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var method, path, auth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
				if tc.status != http.StatusOK {
					w.WriteHeader(tc.status)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": tc.refSHA}})
			}))
			defer srv.Close()
			defer swapAPIBase(srv.URL)()

			err := tagRelease(testService(), context.Background(), releaseRepoSlug, sha, "v1.786.44")
			if tc.wantErr != (err != nil) {
				t.Fatalf("tagRelease err=%v, wantErr=%v", err, tc.wantErr)
			}
			// A verification that POSTs is a verification that MUTATES.
			if method != http.MethodGet {
				t.Fatalf("tagRelease used %s; it must only READ the ref", method)
			}
			if path != "/repos/hanzoai/cloud/git/ref/tags/v1.786.44" {
				t.Fatalf("read the wrong ref path: %q", path)
			}
			if auth != "Bearer test-token" {
				t.Fatalf("auth header wrong: %q", auth)
			}
		})
	}
}

// THE RACE, REPRODUCED. Two commits ask for a version at the same moment; the
// registry and the git tag list both still say the same number is next. Exactly
// one may end up owning it, and the other must walk forward rather than build.
func TestClaimReleaseVersion_IsExclusive(t *testing.T) {
	t.Setenv("GH_PAT", "test-token")
	mine := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	theirs := "cafebabecafebabecafebabecafebabecafebabe"

	// held maps an already-claimed tag to the commit holding it.
	newSrv := func(held map[string]string, claimed *[]string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			// The two universes computeReleaseVersion reads. Floor them low so the
			// candidate is deterministic.
			case r.URL.Path == "/repos/hanzoai/cloud/tags":
				_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "v1.786.0"}})
			case r.Method == http.MethodPost && r.URL.Path == "/repos/hanzoai/cloud/git/refs":
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				tag := strings.TrimPrefix(body["ref"], "refs/tags/")
				if _, taken := held[tag]; taken {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "Reference already exists"})
					return
				}
				*claimed = append(*claimed, tag)
				held[tag] = body["sha"]
				w.WriteHeader(http.StatusCreated)
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/hanzoai/cloud/git/ref/tags/"):
				tag := strings.TrimPrefix(r.URL.Path, "/repos/hanzoai/cloud/git/ref/tags/")
				sha, ok := held[tag]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha}})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	}

	t.Run("a free number is claimed", func(t *testing.T) {
		var claimed []string
		srv := newSrv(map[string]string{}, &claimed)
		defer srv.Close()
		defer swapAPIBase(srv.URL)()
		v, err := claimFrom(testService(), context.Background(), releaseRepoSlug, mine, "1.786.1")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if v != "1.786.1" {
			t.Fatalf("claimed %q, want the first free number 1.786.1", v)
		}
		if len(claimed) != 1 || claimed[0] != "v"+v {
			t.Fatalf("claimed %v but returned %q", claimed, v)
		}
	})

	t.Run("a number another commit holds is skipped, not overwritten", func(t *testing.T) {
		var claimed []string
		// The loser arrives second: the next two numbers are already gone.
		held := map[string]string{"v1.786.1": theirs, "v1.786.2": theirs}
		srv := newSrv(held, &claimed)
		defer srv.Close()
		defer swapAPIBase(srv.URL)()
		v, err := claimFrom(testService(), context.Background(), releaseRepoSlug, mine, "1.786.1")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if v != "1.786.3" {
			t.Fatalf("walked to %q, want 1.786.3 — a held number must never be reused", v)
		}
		if held["v1.786.1"] != theirs || held["v1.786.2"] != theirs {
			t.Fatal("claiming overwrote a version another commit held")
		}
	})

	t.Run("our own claim is a resume, not a collision", func(t *testing.T) {
		var claimed []string
		held := map[string]string{"v1.786.1": mine}
		srv := newSrv(held, &claimed)
		defer srv.Close()
		defer swapAPIBase(srv.URL)()
		v, err := claimFrom(testService(), context.Background(), releaseRepoSlug, mine, "1.786.1")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if v != "1.786.1" {
			t.Fatalf("resumed as %q, want 1.786.1 — a re-run must reuse its own number", v)
		}
		if len(claimed) != 0 {
			t.Fatalf("a resume must mint nothing, minted %v", claimed)
		}
	})
}

// Fail-closed: the tag seam refuses when its KMS-provisioned token is unset — no
// half-published release against an anonymous request.
func TestReleaseSeams_FailClosedWithoutTokens(t *testing.T) {
	t.Setenv("GH_PAT", "")
	s := testService()
	if err := tagRelease(s, context.Background(), releaseRepoSlug, "sha", "v1.0.0"); err == nil {
		t.Fatal("tagRelease with no GH_PAT: want fail-closed error")
	}
}

// The rollout's fail-honestly property is proven against the writer that actually
// deploys — see pin_test.go TestRolloutFailsWhenThePinCannotMove. This file's own
// version tested the CR patch, which never had a CR to write to.

// swapAPIBase points the release seams at url and returns a restore func.
func swapAPIBase(url string) func() {
	prev := githubAPIBase
	githubAPIBase = url
	return func() { githubAPIBase = prev }
}

func swapRegistryBase(url string) func() {
	prev := registryBase
	registryBase = url
	return func() { registryBase = prev }
}
