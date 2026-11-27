package platform

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	cloud "github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var retryBuildSeq atomic.Int64

const fetch524 = "error: failed to solve: failed to read dockerfile: failed to fetch remote https://git.hanzo.ai/hanzo-inc/cloud.git: git stderr: fatal: unable to access 'https://git.hanzo.ai/hanzo-inc/cloud.git/': The requested URL returned error: 524 : exit status 128"

// retryHarness is a platform with one door build whose Job failed `ago` ago.
func retryHarness(t *testing.T, ago time.Duration, mutate func(*Build)) (*cloud.Service[state], Build) {
	t.Helper()
	store, err := openStore(t.TempDir(), testMaster, testKMS)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	k := fakeK8s()
	s := &cloud.Service[state]{Log: luxlog.New("test"), Brand: "hanzo", State: state{k8s: k, store: store}}
	ctx := context.Background()

	buildID := fmt.Sprintf("bld_retrybuild%d", retryBuildSeq.Add(1))
	req := buildRequest{Lane: estate, Repo: "https://github.com/hanzoai/app", Ref: "main", Image: "ghcr.io/hanzoai/app:v1", Dockerfile: "Dockerfile", Platforms: []string{"linux/amd64"}}
	job, err := k.launchDirectBuild(ctx, req.Lane, "hanzo", "", req.Repo, req.Ref, req.Image, req.Dockerfile, "", "", buildID, nil, req.Platforms)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	jobs := k.dyn.Resource(jobsGVR).Namespace(k.buildNS)
	obj, err := jobs.Get(ctx, job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "failed")
	_ = unstructured.SetNestedSlice(obj.Object, []any{map[string]any{
		"type": "Failed", "status": "True", "lastTransitionTime": time.Now().Add(-ago).UTC().Format(time.RFC3339),
	}}, "status", "conditions")
	if _, err := jobs.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	b := Build{ID: buildID, Org: "hanzo", Status: "building", Image: req.Image, JobName: job, Platforms: req.Platforms, Request: req.encode(), CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	if mutate != nil {
		mutate(&b)
	}
	if err := store.InsertBuild(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return s, b
}

// A SOURCE FETCH THE FORGE REFUSED IS ASKED AGAIN, once the wait for it has passed: the failed Job
// is removed and the next tick launches the build again under the same Job name, from the request
// the door kept.
func TestAFailedSourceFetchIsAskedAgain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, b := retryHarness(t, time.Hour, nil)

		if !retryFetch(s, ctx, b, fetch524) {
			t.Fatal("a 524 from the forge was not retried")
		}
		got, err := s.State.store.GetBuild(ctx, "hanzo", b.ID)
		if err != nil || got.Attempt != 1 || got.Status == "failed" {
			t.Fatalf("row after the retry decision: %+v (%v)", got, err)
		}
		if jobExists(s.State.k8s, b.JobName) {
			t.Fatal("the failed Job was left, so the build cannot be launched again under its name")
		}

		reconcileDirectBuilds(s, ctx)
		if !jobExists(s.State.k8s, b.JobName) {
			t.Fatal("the build was not launched again")
		}
		if done, _, err := s.State.k8s.jobResult(ctx, b.JobName); err != nil || done {
			t.Fatalf("the new Job is %v done (%v), want a fresh one", done, err)
		}
	})
}

// A fetch that failed a moment ago waits out its backoff, and what is not a fetch the forge
// refused, a fan-out, a build with no kept request and a build out of attempts all fail as before.
func TestOnlyATransientFetchOfARetryableBuildIsRetried(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()

		s, b := retryHarness(t, time.Second, nil)
		if !retryFetch(s, ctx, b, fetch524) || !jobExists(s.State.k8s, b.JobName) {
			t.Fatal("a fetch that failed a second ago was not left to wait")
		}
		if got, _ := s.State.store.GetBuild(ctx, "hanzo", b.ID); got.Attempt != 0 {
			t.Fatalf("attempt %d, want 0 while it waits", got.Attempt)
		}

		for name, tc := range map[string]struct {
			reason string
			mutate func(*Build)
		}{
			"a build error":    {"error: failed to solve: process \"go build\" did not complete successfully: exit code: 1", nil},
			"a fan-out":        {fetch524, func(b *Build) { b.Platforms = []string{"linux/amd64", "linux/arm64"} }},
			"no kept request":  {fetch524, func(b *Build) { b.Request = "" }},
			"out of attempts":  {fetch524, func(b *Build) { b.Attempt = len(fetchBackoff) }},
			"a missing branch": {"error: failed to solve: failed to fetch remote https://git.hanzo.ai/x.git: fatal: couldn't find remote ref nope", nil},
		} {
			s, b := retryHarness(t, time.Hour, tc.mutate)
			if retryFetch(s, ctx, b, tc.reason) {
				t.Errorf("%s was retried", name)
			}
		}
	})
}
