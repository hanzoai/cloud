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

// ErrNoPusher means no team was found that may write the repository, so there
// is nobody to put on the whitelist. It is a REFUSAL rather than a fallback to
// an empty list, because an empty whitelist refuses everyone — the humans who
// own the repository included.
var ErrNoPusher = errors.New("forge: no team may push this repository")

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

	// Who may push, and force-push, when the lists are on. These are the only
	// fields a caller states — everything above is read back from what a rule
	// IS — and Protect fills both lists with the same principals, so the people
	// who could write the branch before the rule can still do exactly what they
	// did.
	PushTeams  []string `json:"push_whitelist_teams,omitempty"`
	PushUsers  []string `json:"push_whitelist_usernames,omitempty"`
	ForceTeams []string `json:"force_push_allowlist_teams,omitempty"`
	ForceUsers []string `json:"force_push_allowlist_usernames,omitempty"`
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

// rules lists every branch-protection rule, paged. [Client.Protected] decides by
// name, so for it this is the DIAGNOSTIC read. [Client.monotone] reads it to learn
// whether its rule is already there; a truncated answer costs that path a refused
// create, never a weaker rule.
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

// monotone makes every branch of owner/repo move ONE WAY: a push may add
// commits to it, and nothing may rewrite or delete it.
//
// It is what a synced repository is FOR. [Client.Protect] is the wrong rule here
// — it refuses every direct push, including the sync's own, so the repository
// could never receive the branch it exists to receive. This one leaves the push
// open and closes the two operations that lose history, which is exactly the
// property the advance already keeps on its side of the wire.
//
// And that is why it belongs on the SERVER. apps/sync never sends a forcing
// refspec, and it never deletes; but that is one client's discipline, and the
// repository holds a canonical copy that a colleague with a clone, a run's key,
// or a future caller of this package could rewrite in one command. A rule the
// forge enforces needs nobody to keep being careful.
//
// `**` because it is EVERY branch: a rule on the default branch alone leaves the
// release lines and everybody's working branches rewritable, and a mirror has no
// branch it would be reasonable to lose.
//
// Deliberately NOT the exact-name rule [Client.Protected] reads. That question
// is "may a run's deploy key write here", the answer for a synced repository is
// no, and a glob is not read as protecting a branch — so these repositories stay
// outside [Client.Grant], which is where they belong.
//
// The rule is READ before it is written. The forge answers a create for a rule it
// already holds with 403 — the status it also uses for a refusal — so a create
// cannot tell "already there" from "not allowed", and every sync of an existing
// repository stopped at this step. Present and right is done; present and weaker
// is corrected in place; absent is created.
func (c *Client) monotone(ctx context.Context, owner, repo string) error {
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections"
	rules, err := c.Machine().rules(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("forge: keep %s/%s advance-only: %w", owner, repo, err)
	}
	// Push YES — an upstream's commits have to be able to land. Force NO, and a
	// protected branch also refuses deletion (hook_pre_receive.go:190-225). Those
	// are the two ways a ref stops being a record of what happened.
	for _, ru := range rules {
		if ru.pattern() != "**" {
			continue
		}
		if ru.Push && !ru.Force {
			return nil
		}
		err := c.Machine().send(ctx, sendOpts{
			method: http.MethodPatch,
			path:   base + "/" + url.PathEscape("**"),
			body:   map[string]any{"enable_push": true, "enable_force_push": false},
		})
		if err != nil {
			return fmt.Errorf("forge: keep %s/%s advance-only: %w", owner, repo, err)
		}
		return nil
	}
	err = c.Machine().send(ctx, sendOpts{
		method: http.MethodPost,
		path:   base,
		body:   map[string]any{"rule_name": "**", "enable_push": true, "enable_force_push": false},
	})
	// A rule made between the read and this write is the state we wanted.
	if err != nil && !errors.Is(err, ErrExists) {
		return fmt.Errorf("forge: keep %s/%s advance-only: %w", owner, repo, err)
	}
	return nil
}

// Protect makes org/repo's default branch refuse a RUN'S KEY while leaving the
// humans who own the repository able to push it directly.
//
// # The boundary is the branch, not the credential
//
// A coding run needs write access — it clones, commits and pushes the branch it
// works on, and a read-only key would make it useless. What it must never do is
// move the DEFAULT branch, because that is the branch CI builds and deployments
// pin, and on repositories carrying Actions workflows on self-hosted runners a
// push to it is code execution rather than a commit. So the run's key writes
// work branches freely and is refused on main; changes land through a pull
// request; and a human's own push to main is untouched.
//
// # Why this is not enable_push:false
//
// It was. Refusing EVERY direct push — key or human — needed no list of
// principals and could not drift, and that was its whole appeal. It also made
// main pull-request-only for every repository this touched, which is a change
// to how the people who own it work, imposed as a side effect of somebody
// starting an agent run.
//
// The objection to a whitelist was that it "would have to enumerate the humans
// allowed to push, which is a list that drifts the first time somebody joins".
// That is true of a list of PEOPLE and false of a list of TEAMS: a team is a
// stable name, and who is in it is IAM's answer, kept where org membership is
// already kept rather than copied into a branch rule. Adding a maintainer
// changes nothing here.
//
// # Fail closed
//
// The rule is only written once a team that may write has actually been
// resolved. A whitelist with nothing in it does not mean "everyone" — it means
// NOBODY pushes main, including the people who own the repository, which is a
// worse outcome than refusing the run. An unresolvable owner is an error, and
// no rule is written.
func (c *Client) Protect(ctx context.Context, owner, repo, branch string) error {
	if branch == "" {
		return fmt.Errorf("forge: protect %s/%s: no branch", owner, repo)
	}
	w, err := c.writers(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("forge: protect %s/%s: cannot resolve who may push: %w", owner, repo, err)
	}
	if w.empty() {
		return fmt.Errorf("%w: %s/%s: nobody holds write, so a whitelist would refuse everyone", ErrNoPusher, owner, repo)
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
		// THE LISTS ARE WHAT REFUSE THE KEY. deployKeyRefused reads
		// `Push && (!PushList || PushKeys)` and the same for force, so both ON
		// with lists that do not name deploy keys evaluates to refused both
		// ways — the verdict enable_push:false gave, reached without taking
		// anything from a person.
		//
		// Force-push is kept for the same principals rather than switched off,
		// because the requirement is that existing human access survives the
		// rule unchanged. Before it, anyone with write could force-push; after
		// it, the same people can and a key cannot. The `sync` actor that keeps
		// a forge copy current is one of those people, and a rule that took its
		// force-push away would stall the copy the first time GitHub history
		// moved under it.
		want := rule{Name: name,
			Push: true, PushList: true, PushTeams: w.Teams, PushUsers: w.Users,
			Force: true, ForceList: true, ForceTeams: w.Teams, ForceUsers: w.Users}
		if !want.deployKeyRefused() {
			// Unreachable by construction, and asserted anyway: this is the one
			// place a rule is composed, and the predicate is the whole point of
			// composing it that way.
			return fmt.Errorf("forge: protect %s/%s (%s): the rule would admit a run's key", owner, repo, name)
		}
		err := c.Machine().send(ctx, sendOpts{
			method: http.MethodPost,
			path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections",
			// THE RULE IS WRITTEN AS THE VALUE IT IS READ AS.
			//
			// This was a map[string]any whose keys restated the struct's json
			// tags by hand, so one rule was described twice — once to write and
			// once to read — and the two were free to disagree. They already
			// nearly did: deployKeyRefused reasons over the STRUCT, and the only
			// thing making its verdict true of what was actually sent was six
			// string literals matching six tags.
			//
			// Sent as a rule, the predicate below is about the same value the
			// forge is being handed, and a renamed field cannot mean one thing
			// to the writer and another to the reader.
			body: want,
		})
		// A rule that is already there is the state we wanted.
		if err != nil && !errors.Is(err, ErrExists) {
			return fmt.Errorf("forge: protect %s/%s (%s): %w", owner, repo, name, err)
		}
	}
	return nil
}

// principals is who may write a repository: teams the forge says hold write,
// and people who hold it directly as collaborators.
type principals struct {
	Teams []string
	Users []string
}

func (p principals) empty() bool { return len(p.Teams) == 0 && len(p.Users) == 0 }

// writers is everyone who may WRITE org/repo today, read from the forge.
//
// Teams are the common case and the reason a whitelist does not drift: a team
// is a stable name, and who is in it is IAM's answer, materialized in the
// forge namespace [Owner] maps the org to. Adding a maintainer never touches
// the rule.
//
// Direct collaborators are read as well, because a rule that named only teams
// would quietly take the default branch away from anybody granted write one
// repository at a time — and the requirement is that nobody who could push
// before the rule is refused after it. Read and none are not candidates: a
// principal the forge would refuse anyway does not belong on a push list.
func (c *Client) writers(ctx context.Context, owner, repo string) (principals, error) {
	m := c.Machine()
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	var teams []struct {
		Name       string `json:"name"`
		Permission string `json:"permission"`
	}
	if err := m.send(ctx, sendOpts{method: http.MethodGet, path: base + "/teams", out: &teams}); err != nil {
		return principals{}, err
	}
	var p principals
	for _, t := range teams {
		if writes(t.Permission) && strings.TrimSpace(t.Name) != "" {
			p.Teams = append(p.Teams, strings.TrimSpace(t.Name))
		}
	}
	var people []struct {
		Login string `json:"login"`
	}
	if err := m.send(ctx, sendOpts{method: http.MethodGet, path: base + "/collaborators?limit=50", out: &people}); err != nil {
		return principals{}, err
	}
	for _, u := range people {
		login := strings.TrimSpace(u.Login)
		if login == "" {
			continue
		}
		var perm struct {
			Permission string `json:"permission"`
		}
		if err := m.send(ctx, sendOpts{method: http.MethodGet,
			path: base + "/collaborators/" + url.PathEscape(login) + "/permission", out: &perm}); err != nil {
			return principals{}, err
		}
		if writes(perm.Permission) {
			p.Users = append(p.Users, login)
		}
	}
	return p, nil
}

func writes(permission string) bool {
	switch strings.ToLower(permission) {
	case "write", "admin", "owner":
		return true
	}
	return false
}

// Reconcile brings an EXISTING repository up to the rule a new one is born
// with, and reports whether it had to change anything.
//
// It exists because "born protected" only ever reached repositories created
// after it was written. Everything older — hanzoai/cloud among them — carries
// no rule at all, so [Client.Grant] refuses every run against them, forever,
// and the refusal names a remedy nobody could apply without this.
//
// IT ONLY EVER FILLS A VACUUM. A repository already refusing a key is left
// exactly as it is, however it got there. A repository carrying ANY rule is not
// touched either, even one that admits a key: somebody decided that
// repository's policy, and the two ways writing over it goes wrong are both
// real. A weak rule (force-push open, or deploy keys whitelisted) was somebody's
// choice to make and is theirs to change. A GLOB rule such as `**` protects the
// branch in the forge's eyes but not in [Client.Protected]'s, and adding an
// exact rule for the branch would OUTRANK it — loosening a pull-request-only
// default branch into a pushable one to let an agent in. Both are refused with
// the remedy named, and the operator decides.
//
// What remains is precisely the case this exists for: a repository with no
// rules at all, created before "born protected" — hanzoai/cloud among them.
// There is no policy there to override, only the absence of one.
func (c *Client) Reconcile(ctx context.Context, owner, repo string) (changed bool, err error) {
	guarded, err := c.Protected(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	if guarded {
		return false, nil // already refuses a run's key, however it got there
	}
	present, err := c.rules(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	if len(present) > 0 {
		return false, fmt.Errorf("%w: %s/%s: %s", ErrOpen, owner, repo, c.Missing(ctx, owner, repo))
	}
	r, err := c.Writable(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	if err := c.Protect(ctx, owner, repo, r.Branch); err != nil {
		return false, err
	}
	// THE VERDICT IS A READ-BACK. A 2xx says the forge accepted the rule; only
	// asking it afterwards says the branch now refuses a key, and that is the
	// property every run downstream depends on.
	guarded, err = c.Protected(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	if !guarded {
		return false, fmt.Errorf("%w: %s/%s: the rule was accepted and the branch still takes a key: %s",
			ErrOpen, owner, repo, c.Missing(ctx, owner, repo))
	}
	return true, nil
}
