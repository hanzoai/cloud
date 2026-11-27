package auto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
	"github.com/zap-proto/zip"
)

// post.go — the post step: an automation publishes what its agent wrote to the
// social accounts its org connected (apps/provider: x, linkedin, facebook).
//
// THE AGENT WRITES, THIS STEP PUBLISHES. The agent step's answer is the post; an
// automation that acts on its own posts it at once. One that asks first ends its
// run holding the post, in review, and its person or an admin of the org posts
// or discards it from the page (reviewRun) — the run is finished then, by that
// answer, not by a workflow left waiting.
//
// NOTHING IS FAKED. Each account answers for itself: a post that went is kept
// with its link, and one that did not with the platform's own words — an account
// not connected, a token the platform refused, an app the platform has not
// approved for posting. A run posts to each account at most once: what posted is
// recorded before the next account is tried, and a retried step skips it.

const (
	// postAction names the step: core.post in a flow's step tree.
	postAction = "post"
	// reviewWindow is how long a held post may wait for its review.
	reviewWindow = 24 * time.Hour
	// deliverWithin bounds posting a reviewed post to every account.
	deliverWithin = 5 * time.Minute
)

// outlet is one place an automation can post text: its name and the longest
// post it takes. The post is made by the provider app, which keeps the org's
// token (apps/provider social_post.go).
type outlet struct {
	name string
	// limit is the longest post the platform takes, as it counts (weight).
	limit int
}

// targets are where an automation posts, by the name its record uses. Instagram
// and TikTok are not among them: neither takes a text post (an image or a video
// is required), and the page says so beside them.
var targets = map[string]outlet{
	"x":        {name: "X", limit: 280},
	"linkedin": {name: "LinkedIn", limit: 3000},
	"facebook": {name: "Facebook", limit: 63206},
}

// PostResult is where one post went: its link, or why it did not go.
type PostResult struct {
	// To is the account's platform: x, linkedin or facebook.
	To string `json:"to"`
	// URL opens the post; absent when it did not go.
	URL string `json:"url,omitempty"`
	// Error is the platform's reason it did not go; absent when it did.
	Error string `json:"error,omitempty"`
}

// The provider app and IAM, as values so a test stands in for them. Production
// never reassigns them.
var (
	// sendPost asks the provider app, which keeps the org's token, to make one post.
	sendPost = func(ctx context.Context, org, to, text string) (*client.SocialPostOut, error) {
		return providerpeer.IntegrationsPost(cloud.For(ctx, org), &client.SocialPostIn{To: to, Text: text})
	}
	// adminOf reports whether a person administers org (IAM's answer): only an
	// admin's automation posts to the organization's accounts.
	adminOf = func(ctx context.Context, org, sub string) (bool, error) {
		got, err := iampeer.IAMRoles(zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org: org, User: sub}))
		if err != nil {
			return false, err
		}
		return got != nil && got.Admin, nil
	}
)

// weight is a post's length as its platform counts it. X counts most characters
// outside Latin, Greek and Cyrillic, and every emoji, as two; the rest count each
// character once.
func weight(to, text string) int {
	n := 0
	for _, r := range text {
		if to == "x" && !(r <= 0x10FF || (r >= 0x2000 && r <= 0x200D) || (r >= 0x2010 && r <= 0x201F) || (r >= 0x2032 && r <= 0x2037)) {
			n += 2
			continue
		}
		n++
	}
	return n
}

// postTargets checks an automation's accounts: known, each once, in a stable
// order. Nil for none.
func postTargets(to []string) ([]string, error) {
	var out []string
	for _, t := range to {
		t = strings.ToLower(strings.TrimSpace(t))
		if _, ok := targets[t]; !ok {
			return nil, fmt.Errorf("an automation posts text to x, linkedin or facebook, not %q", t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out, nil
}

// publish posts text to one target as org, through the provider app, and
// answers where it went or why it did not.
func publish(ctx context.Context, org, to, text string) PostResult {
	t := targets[to]
	out := PostResult{To: to}
	if n := weight(to, text); n > t.limit {
		out.Error = fmt.Sprintf("the post is %d characters and %s takes %d", n, t.name, t.limit)
		return out
	}
	got, err := sendPost(ctx, org, to, text)
	switch {
	case err != nil:
		out.Error = t.name + " could not be reached: " + err.Error()
	case got == nil:
		out.Error = t.name + ": the integrations app answered nothing"
	default:
		out.URL, out.Error = got.URL, got.Error
	}
	return out
}

// PostIn is the post activity's input: the run, its accounts and its text.
type PostIn struct {
	Owner string   `json:"owner"`
	RunID string   `json:"runId"`
	To    []string `json:"to"`
	Text  string   `json:"text"`
}

// HoldIn is the post a run wrote, kept on it.
type HoldIn struct {
	Owner string `json:"owner"`
	RunID string `json:"runId"`
	Text  string `json:"text"`
}

// HoldPostActivity keeps the post a run's agent wrote on the run.
func HoldPostActivity(ctx context.Context, in HoldIn) error {
	s := mounted
	if s == nil {
		return ErrNotMounted
	}
	return s.State.store.SetRunDraft(ctx, in.Owner, in.RunID, in.Text)
}

// NoteIn is the one line a run ends with.
type NoteIn struct {
	Owner   string `json:"owner"`
	RunID   string `json:"runId"`
	Summary string `json:"summary"`
}

// NoteRunActivity keeps the one line a run ends with.
func NoteRunActivity(ctx context.Context, in NoteIn) error {
	s := mounted
	if s == nil {
		return ErrNotMounted
	}
	return s.State.store.SetRunSummary(ctx, in.Owner, in.RunID, clip(in.Summary))
}

// PostActivity posts a run's text to each of its accounts, once each, and keeps
// where each went and a line on how it went.
func PostActivity(ctx context.Context, in PostIn) ([]PostResult, error) {
	s := mounted
	if s == nil {
		return nil, ErrNotMounted
	}
	return deliver(ctx, s, in)
}

// deliver is PostActivity's body, for the engine's step and a reviewed post alike.
func deliver(ctx context.Context, s *cloud.Service[state], in PostIn) ([]PostResult, error) {
	run, err := s.State.store.GetRun(ctx, in.Owner, in.RunID)
	if err != nil {
		return nil, err
	}
	// The accounts are the organization's: only an automation whose person
	// administers it posts to them, asked of IAM each time.
	if ok, err := adminOf(ctx, in.Owner, run.Person); err != nil || !ok {
		why := "the person this automation runs as is not an admin of the organization, so it does not post to the organization's accounts"
		if err != nil {
			why = "IAM could not say whether the person this automation runs as is an admin of the organization, so nothing was posted"
		}
		results := make([]PostResult, 0, len(in.To))
		for _, to := range in.To {
			results = append(results, PostResult{To: to, Error: why})
		}
		b, _ := json.Marshal(results)
		_ = s.State.store.SetRunPosts(ctx, in.Owner, in.RunID, string(b))
		return results, s.State.store.SetRunSummary(ctx, in.Owner, in.RunID, postSummary(results))
	}
	var done []PostResult
	_ = json.Unmarshal([]byte(run.Posts), &done)
	results := make([]PostResult, 0, len(in.To))
	for _, to := range in.To {
		if i := slices.IndexFunc(done, func(r PostResult) bool { return r.To == to && r.URL != "" }); i >= 0 {
			results = append(results, done[i])
			continue
		}
		results = append(results, publish(ctx, in.Owner, to, in.Text))
		b, _ := json.Marshal(results)
		if err := s.State.store.SetRunPosts(ctx, in.Owner, in.RunID, string(b)); err != nil {
			s.Log.Warn("auto: where a run posted was not recorded", "org", in.Owner, "run", in.RunID, "err", err)
		}
	}
	if err := s.State.store.SetRunSummary(ctx, in.Owner, in.RunID, postSummary(results)); err != nil {
		return results, err
	}
	return results, nil
}

// posted is the status a run ends with after posting: failed when no account
// took it.
func posted(results []PostResult) FlowRunStatus {
	if slices.ContainsFunc(results, func(r PostResult) bool { return r.URL != "" }) {
		return RunSucceeded
	}
	return RunFailed
}

// postSummary is the one line a posting run is remembered by.
func postSummary(results []PostResult) string {
	var went, not []string
	for _, r := range results {
		if r.URL != "" {
			went = append(went, targets[r.To].name)
		} else {
			not = append(not, r.Error)
		}
	}
	switch {
	case len(not) == 0:
		return clip("Posted to " + strings.Join(went, ", ") + ".")
	case len(went) == 0:
		return clip("Not posted: " + strings.Join(not, "; ") + ".")
	}
	return clip("Posted to " + strings.Join(went, ", ") + "; not posted: " + strings.Join(not, "; ") + ".")
}

// runPost is the post step inside FlowRunWorkflow: it takes the agent step's
// answer as the post and keeps it on the run, then posts it, or, for an
// automation that asks first, ends the run in review holding it (reviewRun). It
// answers the status the run ends with.
func runPost(ctx workflow.Context, in FlowRunInput, runID string, step FlowStep, outputs map[string]any) (map[string]any, FlowRunStatus, error) {
	agent, _ := outputs[agentAction].(map[string]any)
	text, _ := agent["answer"].(string)
	text = strings.TrimSpace(text)
	bkCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2.0, MaximumInterval: 10 * time.Second, MaximumAttempts: 5,
		},
	})
	if text == "" {
		_ = workflow.ExecuteActivity(bkCtx, NoteRunActivity, NoteIn{Owner: in.Owner, RunID: runID, Summary: "Not posted: the agent wrote nothing to post."}).Get(bkCtx, nil)
		return map[string]any{"posted": false}, RunFailed, errors.New("the agent wrote nothing to post")
	}
	if err := workflow.ExecuteActivity(bkCtx, HoldPostActivity, HoldIn{Owner: in.Owner, RunID: runID, Text: text}).Get(bkCtx, nil); err != nil {
		return nil, RunFailed, err
	}
	if boolInput(step.Input, "review") {
		_ = workflow.ExecuteActivity(bkCtx, NoteRunActivity, NoteIn{Owner: in.Owner, RunID: runID, Summary: "Waiting for your review: post it or discard it."}).Get(bkCtx, nil)
		return map[string]any{"held": true}, RunPaused, nil
	}
	// Posting is not retried by the engine: a retry after one account took the
	// post would post it there twice. publish asks a busy platform again itself.
	postCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: deliverWithin,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var results []PostResult
	if err := workflow.ExecuteActivity(postCtx, PostActivity, PostIn{Owner: in.Owner, RunID: runID, To: strsInput(step.Input, "to"), Text: text}).Get(postCtx, &results); err != nil {
		return nil, RunFailed, err
	}
	return map[string]any{"posts": results}, posted(results), nil
}

// strsInput reads a list of strings from a step's input.
func strsInput(in map[string]any, key string) []string {
	var out []string
	switch v := in[key].(type) {
	case []string:
		out = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
