// retry.go — a build whose source the forge would not give is asked again.
//
// A build clones its repository from the forge, and a forge that is busy answers 5xx or not at
// all: git.hanzo.ai's own source fetch failed with a 524 and took a release's build with it. That
// is not the build's fault and asking again is the whole remedy, so a failure whose reason is a
// transient fetch error is retried up to maxFetchRetries times, each after a longer wait.
//
// The door keeps what it was asked (Build.Request) so the build can be launched again under its
// own Job name once the failed Job is gone. Only a single-platform build is retried: a fan-out's
// halves each have a Job and an index to join, and a build that carries a checkout credential
// has none stored.

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hanzoai/cloud/sdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fetchBackoff is how long a failed fetch waits before the build is asked again, by how many
// times it has been; its length is how many times that is.
var fetchBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// transientFetch is a source fetch that failed for the forge's sake: a 5xx, a timeout, a
// connection the forge dropped.
var transientFetch = regexp.MustCompile(`failed to fetch remote.*(error: 5\d\d|timed out|Connection (reset|refused|timed out)|Could not resolve host|early EOF)`)

// buildRequest is what the door was asked, as much as asking again needs. Nothing in it is a
// credential: those are decided at launch, from the org and the repository.
type buildRequest struct {
	Lane       lane
	Repo, Ref  string
	Image      string
	Dockerfile string
	Target     string
	Dir        string
	Args       map[string]string
	Platforms  []string
	Tags       []string
}

func (r buildRequest) encode() string {
	out, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(out)
}

// retryFetch reports whether a failed build is being asked again: it is waiting out its
// backoff, or its failed Job has just been removed so the next tick launches it again.
func retryFetch(s *cloud.Service[state], ctx context.Context, b Build, reason string) bool {
	if b.Request == "" || len(b.Platforms) > 1 || b.Attempt >= len(fetchBackoff) || !transientFetch.MatchString(reason) {
		return false
	}
	failed, err := s.State.k8s.jobFailedAt(ctx, b.JobName)
	if err != nil {
		return true // cannot say when it failed; the deadline decides
	}
	if time.Since(failed) < fetchBackoff[b.Attempt] {
		return true
	}
	if err := s.State.k8s.deleteJob(ctx, b.JobName); err != nil {
		s.Log.Warn("reconcile: remove a failed build's Job to ask again", "build", b.ID, "err", err)
		return true
	}
	b.Attempt++
	b.Reason, b.UpdatedAt = fmt.Sprintf("attempt %d of %d failed, asking again: %s", b.Attempt, len(fetchBackoff)+1, reason), time.Now().Unix()
	if err := s.State.store.UpdateBuild(ctx, b); err != nil {
		s.Log.Warn("reconcile: record a build asked again", "build", b.ID, "err", err)
	}
	s.Log.Info("build source fetch failed; asking again", "build", b.ID, "attempt", b.Attempt, "reason", reason)
	return true
}

// askAgain launches a build whose Job was removed to retry it, from the request the door kept.
func askAgain(s *cloud.Service[state], ctx context.Context, b Build) {
	var r buildRequest
	if err := json.Unmarshal([]byte(b.Request), &r); err != nil {
		s.Log.Warn("reconcile: a build's kept request is unreadable", "build", b.ID, "err", err)
		return
	}
	_, err := s.State.k8s.launchDirectBuild(ctx, r.Lane, b.Org, "", r.Repo, r.Ref, r.Image, r.Dockerfile, r.Target, r.Dir, b.ID, r.Args, r.Platforms, r.Tags...)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		s.Log.Warn("reconcile: ask a build again", "build", b.ID, "err", err)
	}
}

// jobFailedAt is when a Job failed.
func (k *k8sClient) jobFailedAt(ctx context.Context, name string) (time.Time, error) {
	job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return time.Time{}, err
	}
	conds, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Failed" && m["status"] == "True" {
			if t, err := time.Parse(time.RFC3339, fmt.Sprint(m["lastTransitionTime"])); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, errors.New("the Job records no failure time")
}

// deleteJob removes a build Job and its pods.
func (k *k8sClient) deleteJob(ctx context.Context, name string) error {
	policy := metav1.DeletePropagationBackground
	err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
