package forge

// protect.go is where a run's confinement actually lives.
//
// # Why it has to be here and not in the orchestrator
//
// The credential this package mints is per-REPOSITORY. The one it replaced was
// per-REF: the embedded server read the ref out of the grant and refused any
// command naming another, so a run could not touch main even in the repository
// it was working in. Forgejo has no per-ref credential — token scopes are
// categories and a deploy key is a repository — so that confinement cannot be
// carried by the credential any more.
//
// The orchestrator's rules do not replace it. A run pushes SSH to Forgejo, which
// never sees our refspec: `git push origin HEAD:refs/heads/main --force` in one
// tool call reaches the forge with none of our code in the path. And reading the
// branch tip AFTERWARDS is a check on whether to file a pull request, not
// containment — by the time it runs, the push has landed.
//
// So the confinement has to be a fact the FORGE enforces, and that is a
// protected branch. Measured against this fork's own pre-receive
// (routers/private/hook_pre_receive.go:270-285), a deploy key may push a
// protected branch exactly when
//
//	CanPush && (!EnableWhitelist || WhitelistDeployKeys)
//
// so it is refused when push is off, or when a whitelist is on and deploy keys
// are not in it. [deployKeyRefused] is that predicate and nothing else — it is
// read off the rule the forge returns rather than inferred from what we asked
// for, because what protects a branch is the rule that is THERE.
//
// A protected branch also refuses deletion and force-push outright
// (hook_pre_receive.go:190-225), which restores the other two properties the old
// per-ref grant had.
//
// # What this does NOT cover
//
// TAGS. This fork publishes no tag-protection API — there is no
// /repos/{owner}/{repo}/tags/protection route in routers/api/v1/api.go — so a
// run's key can still create and move tags. It is a smaller hole than a branch
// (a tag runs no workflow on a push-triggered pipeline) but it is a real one,
// and it cannot be closed from here.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// ErrOpen means the repository does not refuse a deploy key on its default
// branch, so a run's credential would be able to rewrite it.
var ErrOpen = errors.New("forge: the default branch does not refuse a run's key")

// rule is one branch-protection rule, in the fields that decide whether a deploy
// key may write.
type rule struct {
	Name string `json:"rule_name"`
	// Branch is the deprecated spelling the forge still answers with on older
	// rules; a rule created before rule_name existed carries only this.
	Branch string `json:"branch_name"`

	Push      bool `json:"enable_push"`
	PushList  bool `json:"enable_push_whitelist"`
	PushKeys  bool `json:"push_whitelist_deploy_keys"`
	Force     bool `json:"enable_force_push"`
	ForceList bool `json:"enable_force_push_allowlist"`
	ForceKeys bool `json:"force_push_allowlist_deploy_keys"`
}

// pattern is the branch or glob this rule governs.
func (r rule) pattern() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Branch
}

// deployKeyRefused is the forge's own rule, restated: may a deploy key write a
// branch this rule governs?
//
// Both halves matter. A rule that blocks an ordinary push but allows a force
// push would let a run rewrite the branch anyway, which is the outcome the
// protection exists to prevent — so the answer is "refused" only when it is
// refused both ways.
func (r rule) deployKeyRefused() bool {
	push := r.Push && (!r.PushList || r.PushKeys)
	force := r.Force && (!r.ForceList || r.ForceKeys)
	return !push && !force
}

// Protected reports whether org/repo's DEFAULT branch refuses a run's key.
//
// It reads the rules the forge is actually holding, so a repository somebody
// unprotected by hand reads as open here even though this package once
// protected it. That is the point: the question is about the forge's current
// state, not about our intent.
func (c *Client) Protected(ctx context.Context, owner, repo string) (bool, error) {
	r, err := c.Machine().Repo(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	if r.Branch == "" {
		return false, fmt.Errorf("forge: %s/%s names no default branch", owner, repo)
	}
	rules, err := c.rules(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	for _, ru := range rules {
		// An EXACT match on the default branch, not a glob evaluation. The forge
		// matches globs itself and re-implementing that here would be a second,
		// disagreeing matcher; an exact rule is the one thing we can assert about
		// without owning the pattern language.
		if ru.pattern() == r.Branch && ru.deployKeyRefused() {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) rules(ctx context.Context, owner, repo string) ([]rule, error) {
	var out []rule
	err := c.Machine().do(ctx,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/branch_protections", nil, &out)
	return out, err
}

// Protect makes org/repo's default branch refuse every direct write, so the
// only way into it is a reviewed pull request.
//
// It is applied to repositories THIS PACKAGE CREATES (see [Client.Ensure]) and
// nowhere else. Applying it to a repository a team already works in would change
// that team's workflow as a side effect of somebody starting an agent run, which
// is not a decision this code gets to make — an existing repository that does
// not refuse a key is REFUSED a grant instead ([Client.Grant]), so the operator
// makes that change deliberately or not at all.
//
// enable_push false rather than a deploy-key-only whitelist: a whitelist that
// excluded keys would have to enumerate the humans allowed to push, which is a
// list that drifts the first time somebody joins. Refusing every direct push to
// the default branch of a repository made for agent work needs no list and
// cannot drift.
func (c *Client) Protect(ctx context.Context, owner, repo, branch string) error {
	if branch == "" {
		return fmt.Errorf("forge: protect %s/%s: no branch", owner, repo)
	}
	err := c.Machine().send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections",
		body: map[string]any{
			"rule_name": branch,
			// No direct push, by anyone, key or human: the branch moves through a
			// pull request. This is also what refuses the run's key, via the
			// pre-receive predicate restated in deployKeyRefused.
			"enable_push":       false,
			"enable_force_push": false,
		},
	})
	// A rule that is already there is the state we wanted.
	if err != nil && !errors.Is(err, ErrExists) {
		return fmt.Errorf("forge: protect %s/%s: %w", owner, repo, err)
	}
	return nil
}
