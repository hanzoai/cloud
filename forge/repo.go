package forge

// repo.go is the repository itself: whether it is there, whether this person may
// write it, and what its branches point at.
//
// These are the facts a run needs before it starts and after it finishes, and
// they are reads OF THE FORGE for the same reason everything else here is — the
// forge holds the refs, so it is the only thing that can say what a ref is. A
// second answer read from a copy is how a run files a pull request for a branch
// nobody can fetch.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Writable is the forge's own verdict that this client's ACTOR may push to
// org/repo, and it returns the repository so a caller does not read it twice.
//
// It is deliberately a read AS THE HUMAN. The alternative — checking a table
// here, or trusting that the caller already knows — makes this package's
// judgement the authority on a permission the forge owns, and the two disagree
// the moment somebody is removed from a team. Sudo means the answer comes from
// the forge's ACL applied to that person.
//
// A repository the actor cannot SEE is 404, which surfaces as [ErrUnknownActor]
// and is the same answer as one that does not exist. That is right: it declines
// to tell a caller that a private repository is there.
func (c *Client) Writable(ctx context.Context, owner, repo string) (Repo, error) {
	r, err := c.Repo(ctx, owner, repo)
	if err != nil {
		return Repo{}, err
	}
	if r.Perm == nil || !r.Perm.Push {
		// Named separately from "cannot see it": this person is on the repository
		// and may read it, and the thing they may not do is write. Collapsing the
		// two would tell a reader to ask for access they already have.
		return Repo{}, fmt.Errorf("forge: %s cannot push to %s/%s", c.actor, owner, repo)
	}
	if r.Archived {
		return Repo{}, fmt.Errorf("forge: %s/%s is archived", owner, repo)
	}
	return r, nil
}

// Ensure returns org/repo, creating it under the actor when it is not there.
//
// It exists because the two hosts were being migrated one repository at a time,
// so a repository that is real to a caller may simply not have arrived yet — and
// a run pointed at one that has not gets a 404 from `git clone`, minutes after
// it was admitted, with a session already open and a sandbox already leased.
//
// The repository is born WITH A COMMIT (auto_init). An empty repository clones
// successfully and then has no HEAD, so the run's first `rev-parse` fails and
// the failure reads as a broken sandbox rather than as an empty repository.
//
// A 409 is SUCCESS. Two runs admitted at once for a repository that does not
// exist yet both try to create it; one wins, and the loser wanted exactly what
// the winner made.
func (c *Client) Ensure(ctx context.Context, owner, repo, description string) (Repo, error) {
	if err := validSegment(owner); err != nil {
		return Repo{}, err
	}
	if err := validSegment(repo); err != nil {
		return Repo{}, fmt.Errorf("forge: repo: %w", err)
	}
	got, err := c.Writable(ctx, owner, repo)
	if err == nil {
		return got, nil
	}
	// Only absence justifies creating one. A repository that IS there and that
	// this actor may not write must stay a refusal — creating past it would let a
	// caller make a repository whose name is already taken by one they cannot see.
	if !isMissing(err) {
		return Repo{}, err
	}
	var made Repo
	cerr := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/orgs/" + url.PathEscape(owner) + "/repos",
		body: map[string]any{
			"name":           repo,
			"description":    description,
			"private":        true, // a repository is born closed; opening it is a decision
			"auto_init":      true,
			"default_branch": "main",
		},
		out: &made,
	})
	if cerr != nil && !errors.Is(cerr, ErrExists) {
		return Repo{}, fmt.Errorf("forge: create %s/%s: %w", owner, repo, cerr)
	}
	// Read it back either way: on the conflict path we have no body, and on the
	// create path the response carries no permissions — and the permission is
	// the thing the caller is about to rely on.
	out, err := c.Writable(ctx, owner, repo)
	if err != nil {
		return Repo{}, err
	}
	// BORN PROTECTED. A run's key is per-repository, so the only thing standing
	// between it and the default branch is a rule the forge enforces (protect.go).
	// Applying it at creation is the one moment it changes nobody's workflow, and
	// it is what lets [Client.Grant] refuse an unprotected repository without
	// refusing every new one.
	if perr := c.Protect(ctx, owner, repo, out.Branch); perr != nil {
		return Repo{}, perr
	}
	return out, nil
}

// EnsureOrg makes the organisation NAME exist, idempotently. It is how a
// namespace this deployment owns — the community catalogue every published
// project's source lives in (apps/projects visibility.go) — comes into being on
// the first publish rather than through a pre-flight nobody runs. A fresh
// deployment has no such org until this creates it.
//
// A read that finds it is the whole answer; only its absence justifies a create,
// and a 409 is the state asked for. The verdict is a READ-BACK, for the same
// reason [Client.Ensure]'s and [Client.SetPublic]'s are: a 2xx says the forge
// accepted the request, and the only statement that the namespace is now there
// to be written and walked is the forge finding it afterwards.
func (c *Client) EnsureOrg(ctx context.Context, name string) error {
	// A MACHINE-ONLY capability, stated the way [Client.Delete] states its own.
	// Creating a top-level organisation is a platform act with no human behind it;
	// a client acting As(a user) could otherwise mint arbitrary namespaces in that
	// user's name — squatting. As and Machine are mutually exclusive, so `!machine`
	// is exactly "not the deployment identity".
	if !c.machine {
		return fmt.Errorf("forge: EnsureOrg is the machine identity's; %q may not create an org", c.actor)
	}
	if err := validSegment(name); err != nil {
		return err
	}
	if _, err := c.get(ctx, "/orgs/"+url.PathEscape(name), nil, &struct{}{}); err == nil {
		return nil
	} else if !isMissing(err) {
		return err
	}
	cerr := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/orgs",
		body:   map[string]any{"username": name, "visibility": "public"},
	})
	if cerr != nil && !errors.Is(cerr, ErrExists) {
		return fmt.Errorf("forge: create org %s: %w", name, cerr)
	}
	if _, err := c.get(ctx, "/orgs/"+url.PathEscape(name), nil, &struct{}{}); err != nil {
		return fmt.Errorf("forge: confirm org %s: %w", name, err)
	}
	return nil
}

// CreateOrg makes the organisation NAME, PRIVATE, and refuses to adopt one that
// is there: a name that is taken answers [ErrExists] and nothing is touched.
//
// It is [Client.EnsureOrg]'s strict twin, for the namespace a tenant's code
// lives in ([Provision]). EnsureOrg reads a 409 as the state it wanted and makes
// the org public, which is right for the community catalogue this deployment
// owns and wrong here: adopting a name the estate or a stranger already holds is
// the cross-tenant write [Owner] is closed against, and a public namespace lists
// what should be seen by nobody.
//
// The forge answers a taken, reserved or disallowed name with 422 and a racing
// create can answer 409; both are ErrExists — this name cannot be ours. The
// verdict is a READ-BACK: the namespace must then be there and private, and one
// that came back otherwise is deleted before the refusal is returned, so a
// failure leaves nothing behind.
func (c *Client) CreateOrg(ctx context.Context, name string) error {
	if !c.machine {
		return fmt.Errorf("forge: CreateOrg is the machine identity's; %q may not create an org", c.actor)
	}
	if err := validSegment(name); err != nil {
		return err
	}
	err := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/orgs",
		body: map[string]any{
			"username":   name,
			"visibility": "private",
			// Nobody but an owner changes which teams reach a repository.
			"repo_admin_change_team_access": false,
		},
	})
	switch {
	case errors.Is(err, ErrExists), errors.Is(err, ErrInvalid):
		return fmt.Errorf("%w: org %s: %v", ErrExists, name, err)
	case err != nil:
		return fmt.Errorf("forge: create org %s: %w", name, err)
	}
	var got struct {
		Visibility string `json:"visibility"`
	}
	if _, err := c.get(ctx, "/orgs/"+url.PathEscape(name), nil, &got); err != nil {
		return fmt.Errorf("forge: confirm org %s: %w", name, err)
	}
	if got.Visibility != "private" {
		_ = c.deleteOrg(context.WithoutCancel(ctx), name)
		return fmt.Errorf("forge: org %s came back %q, not private", name, got.Visibility)
	}
	return nil
}

// deleteOrg removes an org THIS PROCESS just made and did not keep ([Provision],
// [Client.CreateOrg]). Unexported because that is its only use: an org that
// outlived the call which made it is somebody's, and removing one is not a
// capability anything else here needs.
func (c *Client) deleteOrg(ctx context.Context, name string) error {
	if !c.machine {
		return fmt.Errorf("forge: deleting an org is the machine identity's")
	}
	if err := validSegment(name); err != nil {
		return err
	}
	err := c.send(ctx, sendOpts{method: http.MethodDelete, path: "/orgs/" + url.PathEscape(name)})
	if err != nil && !isMissing(err) {
		return fmt.Errorf("forge: delete org %s: %w", name, err)
	}
	return nil
}

// capacity is how many repositories one made workspace may hold. A workspace
// is the platform's to fill on a person's word — every new project and every
// template taken is one — so it is bounded, and the bound is said when it is met
// rather than found as a forge that has slowed for everyone.
const capacity = 100

// ErrFull is a made workspace that already holds [capacity] repositories.
var ErrFull = errors.New("forge: this workspace holds as many repositories as it may")

// count is how many repositories the namespace owner holds, as the forge
// counts them: one page of one, and the total the forge sends beside it, or
// the whole list when it sends none.
func (c *Client) count(ctx context.Context, owner string) (int, error) {
	var one []Repo
	h, err := c.get(ctx, "/orgs/"+url.PathEscape(owner)+"/repos", url.Values{"limit": {"1"}, "page": {"1"}}, &one)
	if err != nil {
		return 0, fmt.Errorf("forge: count %s's repositories: %w", owner, err)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Total-Count"))); err == nil {
		return n, nil
	}
	all, _, err := c.listRepos(ctx, owner)
	if err != nil {
		return 0, fmt.Errorf("forge: count %s's repositories: %w", owner, err)
	}
	return len(all), nil
}

// Create makes owner/repo NEW — private, with Actions off, its default branch
// refusing a run's key — and never adopts: a name that is taken answers
// [ErrExists] and nothing is touched.
//
// A workspace holding [capacity] repositories is [ErrFull]. It is asked before
// the create, so a full workspace costs no clone, and again after it, so two
// creates that both passed the first question cannot leave it over: the one
// that finds it over takes back its own repository and is refused.
//
// source empty starts it with one commit (auto_init: an empty repository clones
// with no HEAD, and the run's first rev-parse fails). source set COPIES it: the
// forge clones that address ANONYMOUSLY, as `git` and not as a mirror, so the
// copy can hold nothing the world could not already read, follows nothing
// afterwards, and is the org's own to push to. It must be https, carry no
// credential, and not name this forge — whose private repositories the
// machine could otherwise read into a tenant's namespace.
//
// It is the machine's, because it is for a namespace the platform made
// ([Workspace]), where nobody has a forge account to act as.
//
// ACTIONS OFF is part of being made, not a setting somebody may later get
// round to: the forge's runners are the estate's, and a tenant's workflow must
// never be one they run. The verdict is a read-back — private, and Actions
// reported off — and a repository that fails any step after the create is
// deleted before the refusal is returned.
func (c *Client) Create(ctx context.Context, owner, repo, description, source string) (Repo, error) {
	if !c.machine {
		return Repo{}, fmt.Errorf("forge: Create is the machine identity's; %q may not create a repository this way", c.actor)
	}
	if err := validSegment(owner); err != nil {
		return Repo{}, err
	}
	if err := validSegment(repo); err != nil {
		return Repo{}, fmt.Errorf("forge: repo: %w", err)
	}
	switch n, err := c.count(ctx, owner); {
	case err != nil:
		return Repo{}, err
	case n >= capacity:
		return Repo{}, fmt.Errorf("%w: %s holds %d", ErrFull, owner, n)
	}
	var err error
	if source == "" {
		err = c.send(ctx, sendOpts{
			method: http.MethodPost,
			path:   "/orgs/" + url.PathEscape(owner) + "/repos",
			body: map[string]any{
				"name": repo, "description": description, "private": true,
				"auto_init": true, "default_branch": "main",
			},
		})
	} else {
		if err := c.copyable(source); err != nil {
			return Repo{}, err
		}
		// The name is asked about FIRST, because the forge does not: it refuses a
		// clone address it will not reach (not allowed, not resolved) with 422
		// before it looks at the name, so a 422 alone cannot say whether the row
		// under this name is this call's — and one that was there already is
		// somebody's work, never taken back below.
		switch _, rerr := c.Repo(ctx, owner, repo); {
		case rerr == nil:
			return Repo{}, fmt.Errorf("%w: %s/%s", ErrExists, owner, repo)
		case !isMissing(rerr):
			return Repo{}, fmt.Errorf("forge: ask whether %s/%s is free: %w", owner, repo, rerr)
		}
		err = c.send(ctx, sendOpts{
			method: http.MethodPost,
			path:   "/repos/migrate",
			body: map[string]any{
				"clone_addr": source, "repo_owner": owner, "repo_name": repo,
				"service": "git", "mirror": false, "private": true, "description": description,
			},
		})
	}
	switch {
	case errors.Is(err, ErrExists):
		return Repo{}, fmt.Errorf("%w: %s/%s", ErrExists, owner, repo)
	case errors.Is(err, ErrInvalid) && source != "":
		// A copy the forge could not finish keeps the row it began with (its
		// uploader does not roll the repository back), so it is taken back here.
		// The name was free a moment ago and only the platform creates in a
		// namespace it made, so a row there now is this call's.
		_ = c.Delete(context.WithoutCancel(ctx), owner, repo)
		return Repo{}, fmt.Errorf("forge: copy %s into %s/%s: %w", source, owner, repo, err)
	case err != nil:
		return Repo{}, fmt.Errorf("forge: create %s/%s: %w", owner, repo, err)
	}
	out, err := c.seal(ctx, owner, repo)
	if err == nil {
		switch n, cerr := c.count(ctx, owner); {
		case cerr != nil:
			err = cerr
		case n > capacity:
			err = fmt.Errorf("%w: %s holds %d", ErrFull, owner, n)
		}
	}
	if err != nil {
		_ = c.Delete(context.WithoutCancel(ctx), owner, repo)
		return Repo{}, err
	}
	return out, nil
}

// seal turns Actions off on a repository [Client.Create] just made, protects its
// default branch, and reads it back to confirm both closures held.
func (c *Client) seal(ctx context.Context, owner, repo string) (Repo, error) {
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	if err := c.send(ctx, sendOpts{method: http.MethodPatch, path: path,
		body: map[string]any{"has_actions": false, "private": true}}); err != nil {
		return Repo{}, fmt.Errorf("forge: turn Actions off on %s/%s: %w", owner, repo, err)
	}
	r, err := c.Repo(ctx, owner, repo)
	if err != nil {
		return Repo{}, fmt.Errorf("forge: confirm %s/%s: %w", owner, repo, err)
	}
	if !r.Private {
		return Repo{}, fmt.Errorf("forge: %s/%s came back public", owner, repo)
	}
	if r.Actions == nil || *r.Actions {
		return Repo{}, fmt.Errorf("forge: %s/%s does not report Actions off", owner, repo)
	}
	if err := c.Protect(ctx, owner, repo, r.Branch); err != nil {
		return Repo{}, err
	}
	return r, nil
}

// Receive makes owner/repo NEW and EMPTY — private, never adopting a taken name —
// and hands it to push, which moves a history that exists elsewhere into it
// (apps/git moves a repository whose history it held into the forge, its one
// home). Only after the push is its default branch set to branch and the
// repository sealed as [Client.Create]'s are: Actions off, the default branch
// protected. Pushed while empty and unprotected because a protected branch
// refuses the force a first push into a born-with-a-commit repository needs. A
// push that fails, and any step after it, takes the repository back.
//
// It is the machine's, for a namespace the platform holds, as Create is.
func (c *Client) Receive(ctx context.Context, owner, repo, description, branch string, push func(context.Context) error) (Repo, error) {
	if !c.machine {
		return Repo{}, fmt.Errorf("forge: Receive is the machine identity's; %q may not create a repository this way", c.actor)
	}
	if err := validSegment(owner); err != nil {
		return Repo{}, err
	}
	if err := validSegment(repo); err != nil {
		return Repo{}, fmt.Errorf("forge: repo: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return Repo{}, fmt.Errorf("forge: empty branch")
	}
	// [capacity] bounds a WORKSPACE the platform made for a tenant. An estate
	// namespace ([Org] answers for it: hanzoai holds some 250) is not one, and
	// bounding it would refuse every move into the namespace that most needs them.
	if _, err := Org(owner); err != nil {
		switch n, err := c.count(ctx, owner); {
		case err != nil:
			return Repo{}, err
		case n >= capacity:
			return Repo{}, fmt.Errorf("%w: %s holds %d", ErrFull, owner, n)
		}
	}
	switch _, rerr := c.Repo(ctx, owner, repo); {
	case rerr == nil:
		return Repo{}, fmt.Errorf("%w: %s/%s", ErrExists, owner, repo)
	case !isMissing(rerr):
		return Repo{}, fmt.Errorf("forge: ask whether %s/%s is free: %w", owner, repo, rerr)
	}
	err := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/orgs/" + url.PathEscape(owner) + "/repos",
		body:   map[string]any{"name": repo, "description": description, "private": true, "auto_init": false},
	})
	switch {
	case errors.Is(err, ErrExists):
		return Repo{}, fmt.Errorf("%w: %s/%s", ErrExists, owner, repo)
	case err != nil:
		return Repo{}, fmt.Errorf("forge: create %s/%s: %w", owner, repo, err)
	}
	// The name was free a moment ago and only the platform creates in a namespace
	// it holds, so the row there now is this call's to take back.
	back := func(err error) (Repo, error) {
		_ = c.Delete(context.WithoutCancel(ctx), owner, repo)
		return Repo{}, err
	}
	if err := push(ctx); err != nil {
		return back(fmt.Errorf("forge: move a history into %s/%s: %w", owner, repo, err))
	}
	if err := c.Default(ctx, owner, repo, branch); err != nil {
		return back(fmt.Errorf("forge: default branch of %s/%s: %w", owner, repo, err))
	}
	r, err := c.seal(ctx, owner, repo)
	if err != nil {
		return back(err)
	}
	return r, nil
}

// Origin is the https origin repositories on this forge are cloned from.
func (c *Client) Origin() string { return strings.TrimSuffix(c.base, API) }

// copyable refuses a source [Client.Create] must not clone: anything but https,
// an address carrying a credential, and this forge itself.
func (c *Client) copyable(source string) error {
	u, err := url.Parse(strings.TrimSpace(source))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("forge: %q is not an https address to copy from", source)
	}
	if u.User != nil {
		return fmt.Errorf("forge: a source to copy carries no credential")
	}
	if strings.EqualFold(u.Hostname(), strings.Split(c.host, ":")[0]) {
		return fmt.Errorf("forge: a copy is made from outside this forge, not from %s", u.Host)
	}
	return nil
}

// Delete removes org/repo, and CONFIRMS that it is gone.
//
// It is the inverse of [Client.Ensure] and exists because Ensure is idempotent
// BY NAME: a repository left behind by whatever it was made for is one the next
// caller of that name adopts, commits and all. Closing it is not the same
// answer — a closed repository is still there to be adopted and re-opened — so
// the caller that owns the name for good asks for this.
//
// A repository that is NOT THERE is success. Absence is the state asked for, and
// a delete that has to be retried must be able to say so by trying again.
//
// The verdict comes from a READ, for the same reason [Client.SetPublic]'s does:
// a 2xx says the forge accepted the request, and the only statement that the
// repository is gone is the forge failing to find it afterwards.
//
// It acts as THIS CLIENT'S identity, machine or sudoed. Forgejo requires repo
// admin to delete, so a sudoed caller gets its own answer rather than the
// platform's — a caller that wants the platform's authority asks for it with
// [Client.Machine], in writing.
func (c *Client) Delete(ctx context.Context, owner, repo string) error {
	if err := validSegment(owner); err != nil {
		return err
	}
	if err := validSegment(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	if err := c.send(ctx, sendOpts{method: http.MethodDelete, path: path}); err != nil && !isMissing(err) {
		return fmt.Errorf("forge: delete %s/%s: %w", owner, repo, err)
	}
	_, err := c.Repo(ctx, owner, repo)
	switch {
	case isMissing(err):
		return nil
	case err != nil:
		return fmt.Errorf("forge: confirm delete %s/%s: %w", owner, repo, err)
	}
	return fmt.Errorf("forge: %s/%s is still there after a delete", owner, repo)
}

// Tip is the commit a branch points at, and whether the branch is there at all.
//
// found is carried separately from the sha because the two absences must not be
// spelled the same way: a branch that is missing and a read that failed both
// have to fail the integrity gate this feeds, and an absent tip arriving as a
// present-but-empty one would file a pull request for a branch nobody can fetch.
//
// It reads as the MACHINE. The question is not "may this person see this branch"
// — it is "did the push this platform dispatched actually land", and answering
// it through the human's visibility would make a permissions change look like a
// failed push.
func (c *Client) Tip(ctx context.Context, owner, repo, branch string) (sha string, found bool, err error) {
	if err := validSegment(owner); err != nil {
		return "", false, err
	}
	if err := validSegment(repo); err != nil {
		return "", false, fmt.Errorf("forge: repo: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return "", false, fmt.Errorf("forge: empty branch")
	}
	// The wire is [Client.branch], which [Client.Resolve] also reads — one
	// spelling of the branch route, and the only difference between the two
	// callers is the POLICY stated here: this one is the machine's, and it reads
	// an absent branch as an answer rather than as a failure.
	id, err := c.Machine().branch(ctx, owner, repo, branch)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", false, nil // a real answer: the branch is not there
	case err != nil:
		return "", false, err
	case id == "":
		return "", false, nil
	}
	return id, true, nil
}

// Sync asks a pull mirror to follow its origin now rather than at its next
// interval. It is the MACHINE's (the forge wants repo admin), and nothing waits
// on it: a mirror is a cache of its origin, and a stale one is slow, never wrong.
func (c *Client) Sync(ctx context.Context, owner, repo string) error {
	if err := validSegment(owner); err != nil {
		return err
	}
	if err := validSegment(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	return c.Machine().send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/mirror-sync",
	})
}
