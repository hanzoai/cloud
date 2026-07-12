// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// EntryInput addresses one cron entry by ConfigMap name. It is the ONLY
// thing a schedule embeds — the activity resolves the current ConfigMap at
// fire time.
type EntryInput struct {
	Name string `json:"name"`
}

// PokeResult / JobResult surface run outcomes in the workflow result (what
// the Tasks console shows on the execution).
type (
	PokeResult struct {
		URL    string `json:"url"`
		Status int    `json:"status"`
	}
	JobResult struct {
		Job string `json:"job"`
	}
)

// PokeWorkflow runs one HTTP poke entry: a single retried activity. The
// endpoints behind pokes (billing sweep, pricing sync) are idempotent, so a
// couple of retries inside one tick are safe; across ticks the next fire is
// the retry.
func PokeWorkflow(ctx workflow.Context, in EntryInput) (PokeResult, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 10 * time.Second, BackoffCoefficient: 2.0,
			MaximumInterval: time.Minute, MaximumAttempts: 3,
		},
	})
	var out PokeResult
	err := workflow.ExecuteActivity(actCtx, PokeActivity, in).Get(actCtx, &out)
	return out, err
}

// JobWorkflow runs one k8s Job entry: a single UNretried activity (a failed
// backup Job must fail loudly in the console, not silently re-run; the next
// cron tick is the retry — the CronJob restartPolicy/backoffLimit inside the
// Job spec still governs pod-level retries).
func JobWorkflow(ctx workflow.Context, in EntryInput) (JobResult, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Hour,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var out JobResult
	err := workflow.ExecuteActivity(actCtx, RunJobActivity, in).Get(actCtx, &out)
	return out, err
}

// ReconcileWorkflow converges schedules onto the ConfigMap set (see
// reconcile in cron.go) as a single retried activity.
func ReconcileWorkflow(ctx workflow.Context) error {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2.0,
			MaximumInterval: 30 * time.Second, MaximumAttempts: 3,
		},
	})
	return workflow.ExecuteActivity(actCtx, ReconcileActivity).Get(actCtx, nil)
}

// pokeSpec is the poke.json payload of a poke entry. bearerEnv names an env
// var on THIS process (KMS-synced) whose value becomes the Authorization
// bearer — the ConfigMap never carries a secret.
type pokeSpec struct {
	URL       string `json:"url"`
	Method    string `json:"method,omitempty"`    // default POST
	BearerEnv string `json:"bearerEnv,omitempty"` // env var holding the bearer token
	Timeout   string `json:"timeout,omitempty"`   // Go duration, default 10m
}

// httpDo is swappable for tests; production is a plain client (the per-poke
// timeout comes from the request context).
var httpDo = http.DefaultClient.Do

// PokeActivity resolves the entry's current poke.json and performs the HTTP
// call. Non-2xx is an error so the run fails visibly in the console.
func PokeActivity(ctx context.Context, in EntryInput) (PokeResult, error) {
	e, err := activityKube().getEntry(ctx, in.Name)
	if err != nil {
		return PokeResult{}, err
	}
	if e.Kind != kindPoke {
		return PokeResult{}, fmt.Errorf("cron entry %q is not a poke", in.Name)
	}
	p := e.Poke
	method := p.Method
	if method == "" {
		method = http.MethodPost
	}
	timeout := 10 * time.Minute
	if p.Timeout != "" {
		if d, err := time.ParseDuration(p.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, p.URL, strings.NewReader("{}"))
	if err != nil {
		return PokeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.BearerEnv != "" {
		tok := os.Getenv(p.BearerEnv)
		if tok == "" {
			return PokeResult{}, fmt.Errorf("poke %q: bearer env %s is empty", in.Name, p.BearerEnv)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpDo(req)
	if err != nil {
		return PokeResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return PokeResult{}, fmt.Errorf("poke %q: %s %s → %d: %s", in.Name, method, p.URL, resp.StatusCode, body)
	}
	return PokeResult{URL: p.URL, Status: resp.StatusCode}, nil
}

// RunJobActivity resolves the entry's current job.yaml, enforces Forbid
// concurrency (an active run of the same entry skips this tick, matching the
// retired CronJobs' concurrencyPolicy), creates the Job, and blocks until it
// finishes. A failed Job fails the activity — and so the run in the console.
func RunJobActivity(ctx context.Context, in EntryInput) (JobResult, error) {
	k := activityKube()
	e, err := k.getEntry(ctx, in.Name)
	if err != nil {
		return JobResult{}, err
	}
	if e.Kind != kindJob {
		return JobResult{}, fmt.Errorf("cron entry %q is not a job", in.Name)
	}
	active, err := k.hasActiveJob(ctx, in.Name)
	if err != nil {
		return JobResult{}, err
	}
	if active {
		// Forbid: the previous run is still going; this tick is a no-op.
		return JobResult{Job: "(skipped: previous run still active)"}, nil
	}
	jobName, err := k.createJob(ctx, in.Name, e.JobYAML)
	if err != nil {
		return JobResult{}, err
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return JobResult{Job: jobName}, ctx.Err()
		case <-t.C:
			done, succeeded, err := k.jobDone(ctx, jobName)
			if err != nil {
				return JobResult{Job: jobName}, err
			}
			if !done {
				continue
			}
			if !succeeded {
				return JobResult{Job: jobName}, fmt.Errorf("job %s failed", jobName)
			}
			return JobResult{Job: jobName}, nil
		}
	}
}

// ReconcileActivity is the scheduled half of reconcile (boot runs it inline).
func ReconcileActivity(ctx context.Context) error {
	eng := currentEngine()
	if eng == nil {
		return fmt.Errorf("tasks engine not ready")
	}
	return reconcile(ctx, eng.View(org()), activityKube(), activityLog())
}
