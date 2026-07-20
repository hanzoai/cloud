package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// The ordering invariant: build → smoke → tag → notify, stopping at the FIRST
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
			build:  step("built", stepBuilt),
			smoke:  step("smoked", stepSmoked),
			tag:    step("tagged", stepTagged),
			notify: step("notified", stepNotified),
		}
	}

	// Happy path: all four run in order, reached = notified.
	var calls []string
	reached, err := mk(stepNone, &calls).run(context.Background())
	if err != nil || reached != stepNotified {
		t.Fatalf("happy: reached=%v err=%v", reached, err)
	}
	if got := strings.Join(calls, ","); got != "built,smoked,tagged,notified" {
		t.Fatalf("happy order: %s", got)
	}

	// Build fails: nothing else runs, NO tag, reached = none.
	calls = nil
	reached, err = mk(stepBuilt, &calls).run(context.Background())
	if err == nil || reached != stepNone {
		t.Fatalf("build-fail: reached=%v err=%v", reached, err)
	}
	if contains(calls, "tagged") {
		t.Fatalf("build-fail MINTED A TAG WITHOUT A PROVEN IMAGE: %v", calls)
	}

	// Smoke fails: build ran (image pushed), tag/notify did NOT, reached = built.
	calls = nil
	reached, err = mk(stepSmoked, &calls).run(context.Background())
	if err == nil || reached != stepBuilt {
		t.Fatalf("smoke-fail: reached=%v err=%v", reached, err)
	}
	if contains(calls, "tagged") || contains(calls, "notified") {
		t.Fatalf("smoke-fail reached tag/notify (phantom tag): %v", calls)
	}

	// Tag fails: build+smoke ran, notify did NOT, reached = smoked.
	calls = nil
	reached, err = mk(stepTagged, &calls).run(context.Background())
	if err == nil || reached != stepSmoked {
		t.Fatalf("tag-fail: reached=%v err=%v", reached, err)
	}
	if contains(calls, "notified") {
		t.Fatalf("tag-fail reached notify: %v", calls)
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
	if !strings.Contains(cmd[2], `"message":"listening"`) {
		t.Fatalf("smoke script does not gate on \"listening\": %s", cmd[2])
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
	pull, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "imagePullSecrets")
	if len(pull) != 1 || pull[0].(map[string]any)["name"] != "kaniko-ghcr" {
		t.Fatalf("smoke must pull the image from GHCR: %v", pull)
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

// version-compute wiring over the REAL GitHub response shapes: it folds the repo's
// git tags with ghcr.io/hanzoai/cloud's pushed container tags and bumps the max.
func TestComputeReleaseVersion(t *testing.T) {
	t.Setenv("GH_PAT", "test-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/hanzoai/cloud/tags":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "v1.786.42"}, {"name": "v1.700.0"}})
		case "/orgs/hanzoai/packages/container/cloud/versions":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"metadata": map[string]any{"container": map[string]any{"tags": []string{"v1.786.43", "1.786", "latest"}}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()

	got, err := computeReleaseVersion(testService(), context.Background(), releaseRepoSlug)
	if err != nil {
		t.Fatalf("computeReleaseVersion: %v", err)
	}
	if got != "1.786.44" { // container 1.786.43 beats git 1.786.42 → next patch.
		t.Fatalf("want 1.786.44 (container-max+1), got %s", got)
	}
}

// The receipt: tagRelease POSTs refs/tags/<v> at the pinned sha with the bearer token.
func TestTagRelease_RefPath(t *testing.T) {
	t.Setenv("GH_PAT", "test-token")
	var gotBody map[string]string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/hanzoai/cloud/git/refs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()

	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := tagRelease(testService(), context.Background(), releaseRepoSlug, sha, "v1.786.44"); err != nil {
		t.Fatalf("tagRelease: %v", err)
	}
	if gotBody["ref"] != "refs/tags/v1.786.44" || gotBody["sha"] != sha {
		t.Fatalf("tag body wrong: %v", gotBody)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("auth header wrong: %q", gotAuth)
	}
}

// The universe contract: image-update dispatch with {service,image,sha,env} — the
// SAME payload release.yml's notify-universe fires.
func TestNotifyUniverse_Payload(t *testing.T) {
	t.Setenv("UNIVERSE_DISPATCH_TOKEN", "disp-token")
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/hanzoai/universe/dispatches" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()

	img := "ghcr.io/hanzoai/cloud:v1.786.44"
	if err := notifyUniverse(testService(), context.Background(), img, "deadbeef"); err != nil {
		t.Fatalf("notifyUniverse: %v", err)
	}
	if got["event_type"] != "image-update" {
		t.Fatalf("event_type: %v", got["event_type"])
	}
	cp, _ := got["client_payload"].(map[string]any)
	if cp["service"] != "cloud" || cp["image"] != img || cp["env"] != "all" || cp["sha"] != "deadbeef" {
		t.Fatalf("client_payload wrong: %v", cp)
	}
}

// Fail-closed: the tag + notify seams refuse when their KMS-provisioned token is
// unset — no half-published release against an anonymous request.
func TestReleaseSeams_FailClosedWithoutTokens(t *testing.T) {
	t.Setenv("GH_PAT", "")
	t.Setenv("UNIVERSE_DISPATCH_TOKEN", "")
	s := testService()
	if err := tagRelease(s, context.Background(), releaseRepoSlug, "sha", "v1.0.0"); err == nil {
		t.Fatal("tagRelease with no GH_PAT: want fail-closed error")
	}
	if err := notifyUniverse(s, context.Background(), "img", "sha"); err == nil {
		t.Fatal("notifyUniverse with no token: want fail-closed error")
	}
}

// swapAPIBase points the release seams at url and returns a restore func.
func swapAPIBase(url string) func() {
	prev := githubAPIBase
	githubAPIBase = url
	return func() { githubAPIBase = prev }
}
