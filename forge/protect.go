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
// and it may FORCE push under the matching force-push allowlist. So a key is
// refused only when BOTH are closed: push is off (or whitelisted without keys)
// AND force push is off (or allowlisted without keys). A rule that blocked the
// ordinary push while leaving force push open would let a run rewrite the branch
// anyway, which is the outcome the protection exists to prevent.
// [deployKeyRefused] is that predicate and nothing else — it is read off the
// rule the forge returns rather than inferred from what we asked for, because
// what protects a branch is the rule that is THERE.
//
// A protected branch also refuses deletion and force-push outright
// (hook_pre_receive.go:190-225), which restores the other two properties the old
// per-ref grant had.
//
// # What this does NOT cover
//
// TAGS, and they are the bigger of the two residuals rather than the smaller.
// This fork publishes no tag-protection API — there is no
// /repos/{owner}/{repo}/tags/protection route in routers/api/v1/api.go — so a
// run's key can create and MOVE tags. That is not inert: Actions reads workflow
// files from the PUSHED COMMIT (services/actions/notifier_helper.go), so
// on:push:tags, on:create and on:release fire, and in a release model where a
// tag names the image a deployment pins, a movable tag is a supply-chain
// primitive rather than a mislabelled commit. Closing it needs a change in the
// fork (a deploy-key guard in the tag half of the pre-receive path, mirroring
// the branch one) and cannot be done from here.
//
// BRANCHES THIS DID NOT NAME. [Client.Protect] covers the default branch and the
// conventional release lines, so a repository with long-lived branches under
// another name — a colleague's working branch, an environment branch — leaves
// those force-pushable and deletable by a run's key. The bound that remains is
// the human's own push right: a run reaches only repositories its actor could
// already write.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	// THE RULE IS FETCHED BY NAME, not found by scanning a list.
	//
	// The list is paginated, so a repository with more rules than a page would
	// have read as unprotected once the rule fell off the end — and a repository
	// with many rules is exactly the one somebody has protected carefully. By
	// name there is no page to fall off.
	//
	// It is an EXACT name. The forge matches GLOBS itself and re-implementing
	// that here would be a second, disagreeing matcher deciding a security
	// question; so a rule spelled `main*` or `**` is not read as protecting
	// `main`, and [Client.Missing] exists to say so out loud rather than leaving
	// an operator to guess why a repository they protected is still refused.
	ru, err := c.rule(ctx, owner, repo, r.Branch)
	if isMissing(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ru.deployKeyRefused(), nil
}

// rule reads ONE branch-protection rule by its exact name. The route is a
// wildcard, so a rule name containing a slash arrives whole.
func (c *Client) rule(ctx context.Context, owner, repo, name string) (rule, error) {
	var out rule
	// ESCAPED like every other segment here. git permits '#' in a ref name, and a
	// bare '#' is a URL FRAGMENT — never sent to the server — so a repository
	// whose default branch is `main#x` would be answered for `main` and read as
	// protected while `main#x` was open.
	err := c.Machine().do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+
		"/branch_protections/"+url.PathEscape(name), nil, &out)
	return out, err
}

// Missing describes what a repository is short of, for the operator who has to
// fix it: the branch that needs a rule, and the rule names that ARE there.
//
// It exists because the commonest failure here is not an attack, it is somebody
// protecting `**` or `main*` and then finding that runs are still refused. The
// refusal has to name the remedy, or the next hour goes into reading this file.
func (c *Client) Missing(ctx context.Context, owner, repo string) string {
	r, err := c.Machine().Repo(ctx, owner, repo)
	if err != nil {
		return ""
	}
	rules, err := c.rules(ctx, owner, repo)
	if err != nil {
		return fmt.Sprintf("no rule named %q", r.Branch)
	}
	if len(rules) == 0 {
		return fmt.Sprintf("no rule named %q, and the repository has none at all", r.Branch)
	}
	var names []string
	for _, ru := range rules {
		names = append(names, ru.pattern())
	}
	return fmt.Sprintf("no rule named exactly %q; the rules present are [%s] "+
		"(a glob is not read as protecting the branch — the rule must name it)",
		r.Branch, strings.Join(names, ", "))
}

// rules lists every branch-protection rule, paged. It is the DIAGNOSTIC read —
// [Client.Protected] decides by name — so a truncated answer here costs a worse
// error message and never a wrong verdict.
func (c *Client) rules(ctx context.Context, owner, repo string) ([]rule, error) {
	var all []rule
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections"
	for p := 1; p <= maxPages; p++ {
		var batch []rule
		q := url.Values{"limit": {strconv.Itoa(page)}, "page": {strconv.Itoa(p)}}
		if err := c.Machine().do(ctx, path, q, &batch); err != nil {
			return all, err
		}
		all = append(all, batch...)
		if len(batch) < page {
			break
		}
	}
	return all, nil
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
	// The default branch AND the release lines. A rule on the default branch alone
	// leaves release/1.2 and v2 force-pushable and deletable by a run's key, and
	// those are the branches a build actually ships from — the run would be
	// confined from the branch nobody deploys and free on the ones everybody does.
	//
	// The release patterns are GLOBS on purpose: unlike [Client.Protected], which
	// must decide by exact name, this is a rule being WRITTEN, and the forge is
	// the thing that will match it.
	//
	// `v[0-9]*` and not `v*`: the latter reads as "version" to whoever writes it
	// and as "validation", "vendor-bump" and "v2-spike" to the matcher, which
	// would make ordinary working branches pull-request-only in every repository
	// this creates. A digit after the v is what makes it a version.
	for _, name := range []string{branch, "release/**", "v[0-9]*"} {
		err := c.Machine().send(ctx, sendOpts{
			method: http.MethodPost,
			path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections",
			body: map[string]any{
				"rule_name": name,
				// No direct push, by anyone, key or human: the branch moves through
				// a pull request. This is also what refuses the run's key, via the
				// pre-receive predicate restated in deployKeyRefused.
				"enable_push":       false,
				"enable_force_push": false,
			},
		})
		// A rule that is already there is the state we wanted.
		if err != nil && !errors.Is(err, ErrExists) {
			return fmt.Errorf("forge: protect %s/%s (%s): %w", owner, repo, name, err)
		}
	}
	return nil
}
