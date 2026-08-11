package tracker

// source.go binds the tracker to the forge.
//
// The forge (git.hanzo.ai beside api.hanzo.ai) is the SINGLE source of truth for
// this estate's work items. An issue is filed, labelled, assigned and closed
// there, so that is where the tracker reads it from — not from a copy. There is
// deliberately no mirror, no cache table and no write-through: a second store at
// this prefix would be a second answer to "what is the state of this work", and
// the two would drift the first time anyone touched the forge directly, which is
// every day.
//
// # What maps to what
//
// The forge already has every noun the board needs, so nothing is invented:
//
//	tracker project   a forge REPOSITORY (its name is the key)
//	tracker issue     a forge ISSUE (its per-repo number is the number)
//	board column      a forge LABEL drawn from the closed `statuses` set
//	priority          a forge LABEL drawn from the closed `priorities` set
//	milestone         a forge MILESTONE, rolled up across the org's repos
//
// Reading the column off a LABEL is what makes the board and the forge the same
// object seen twice: moving a card is a relabel, and an engineer who relabels in
// the forge web UI has moved the card. A status column in a table here could not
// have that property.
//
// # Tenancy, and the two independent controls
//
// The org is resolved from the VALIDATED principal (principal.OrgFrom) and never
// from a path, a query or a body — a tenant key read from caller-supplied data is
// a cross-tenant read the caller asserted for itself. That is control one, and it
// is the same rule typed.go states for In fields.
//
// Control two is the forge's own ACL: every call is made with Sudo as the
// requesting user (forge.Client.As), which DROPS PRIVILEGE to that user. So the
// deployment's machine token cannot read an org the user could not read anyway,
// and a bug in control one cannot leak a private repo on its own. The two are
// independent, and neither is trusted to be sufficient — which matters here
// because this forge really does host private orgs whose issues must not cross.
//
// The actor is X-User-Name, the IAM username, which the identity boundary strips
// on ingress and re-mints only from validated claims (middleware_identity.go). A
// caller cannot choose who it acts as.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// tokenRef is the KMS coordinate of the forge machine credential.
//
// KMS is the one home for a secret: not an env file (which reaches a git history
// and a pod spec), not a browser-held PAT (which puts a forge credential in
// reach of any script on the page), and not a per-user OAuth grant this process
// would have to custody and rotate N times.
//
// Same shape as apps/platform's pinTokenRef, and a DIFFERENT secret on purpose:
// that one may push to universe, this one reads issues. One credential per
// capability means a compromise of the tracker cannot deploy, and revoking the
// tracker's token does not stop releases.
const tokenRef = "orgs/hanzo/deploy/FORGE_TRACKER_TOKEN@prod"

// forgeSource holds the deployment's forge client and the credential behind it.
//
// The client is resolved LAZILY rather than at Mount: KMS need not be reachable
// at process start, a token rotates while the process lives, and a tracker that
// refused to mount because KMS was slow would take the whole binary down with
// it. It is cached because the alternative is a KMS read per board load.
type forgeSource struct {
	mu     sync.Mutex
	client *forge.Client
	host   string
	fresh  time.Time
}

// ttl bounds how long a resolved credential is reused. A rotated token is
// therefore live within this window without a restart, and a revoked one stops
// working. Short enough to make rotation real, long enough that a board load is
// not a KMS read.
const ttl = 5 * time.Minute

// resolve returns a forge client authenticated with the deployment's machine
// credential, reading it from KMS when the cached one is absent or stale.
//
// Fail closed at every step: no KMS client, a KMS that cannot answer, or an
// empty secret each return an ERROR and never a client. The alternative — an
// anonymous client — would quietly serve only public repos and read as "your
// board is empty" rather than "this deployment is misconfigured".
//
// The error names the REF, never the value. A ref is a path and is safe to log.
func (f *forgeSource) resolve(ctx context.Context, s *cloud.Service[state]) (*forge.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client != nil && time.Since(f.fresh) < ttl {
		return f.client, nil
	}
	if s.KMS == nil {
		return nil, fmt.Errorf("no KMS client mounted: cannot read %s", tokenRef)
	}
	b, err := s.KMS.GetSecret(ctx, tokenRef)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tokenRef, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return nil, fmt.Errorf("%s is empty", tokenRef)
	}
	// The forge is the sibling of this deployment's own API host, derived through
	// the ONE derivation of it. A literal "git.hanzo.ai" here is what makes a
	// white-labelled deployment read another brand's forge.
	//
	// CLOUD_FORGE_HOST overrides it for the deployment whose forge genuinely is
	// not that sibling — a developer box, or a migration running against a staging
	// forge. It is an override and not the source: unset, which is every
	// production deployment, the host is derived and cannot drift per brand.
	host := f.host
	if host == "" {
		host = strings.TrimSpace(os.Getenv("CLOUD_FORGE_HOST"))
	}
	if host == "" {
		host = brand.Sibling(s.Domain, forge.Name)
	}
	c, err := forge.New(host, token)
	if err != nil {
		return nil, err
	}
	// Carry the warm repository list across the rotation. Without this the
	// credential's 5-minute lifetime would silently become the read cache's,
	// and one board load every five minutes would pay the full cold-path wait
	// for no reason anyone reading either constant could see.
	c.Reuse(f.client)
	f.client, f.fresh = c, time.Now()
	return c, nil
}

// invalidate drops a cached credential the forge has just rejected, so the next
// request re-reads KMS instead of replaying a revoked token for the rest of the
// TTL.
func (f *forgeSource) invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.client = nil
}

// forgeOwners maps an IAM org to the org that owns its work ON THE FORGE.
//
// The two names are not the same fact, and this deployment is the proof. The IAM
// tenant is `hanzo`; its work lives under `hanzoai`, which is the name the estate
// writes wherever a namespace is written down — github.com/hanzoai,
// ghcr.io/hanzoai, git.hanzo.ai/hanzoai. Measured on git.hanzo.ai:
//
//	forge org `hanzo`     64 repos, 0 issues, and hanzo/cloud is 404
//	forge org `hanzoai`   250 repos, the actual work, hanzoai/cloud is 200
//
// A NEAR-EMPTY NAMESAKE also exists, which is why mapping by name did not fail
// loudly: the forge answered 200 with an empty list, and an empty board reads as
// "you have no work" rather than as "we asked the wrong org". That is the whole
// hazard — a wrong answer that looks like a healthy one.
//
// A declared table rather than a branch inside the resolver: the mapping is a
// VALUE, so it can be read, tested and added to without touching the code that
// applies it. Identity by default, so a tenant whose two names already agree
// needs no entry.
var forgeOwners = map[string]string{"hanzo": "hanzoai"}

// forgeOwner is the forge org for a VALIDATED IAM org.
//
// It is applied to the principal's own org and never to anything a caller sent:
// this decides WHICH ORG is asked about, and a caller-supplied value here would
// be a tenant selecting its own tenancy.
//
// It does not touch WHO the forge answers as. That remains the Sudo actor, so
// the forge's own ACL still decides what comes back — which means a wrong entry
// in this table can show a user an empty board, but cannot show them anything
// they are not entitled to see. The two controls stay independent.
func forgeOwner(org string) string {
	if o, ok := forgeOwners[strings.ToLower(strings.TrimSpace(org))]; ok {
		return o
	}
	return org
}

// budget bounds a forge-backed request end to end.
//
// It had to EXIST, which is the half that was missing and the reason a board
// could hang forever. The forge client's own 30s timeout bounds ONE request,
// and a read here makes many — up to maxPages of them for a list, plus one per
// repository for a rollup — so with no deadline over the whole operation a slow
// forge is an unbounded wait. The browser gets no response and no error, and
// renders its loading skeleton indefinitely: the failure never becomes visible
// to anyone, which is the worst shape a failure can take.
//
// It also has to be GENEROUS ENOUGH THAT THE COLD PATH SUCCEEDS, which is the
// non-obvious half. The cache behind these reads is filled by a request that
// COMPLETES, so a budget under the cold-path cost would abort the very requests
// that would have warmed it, and the surface would be permanently slow instead
// of slow once. Measured on git.hanzo.ai: ~22s worst case for the repository
// list, plus ~4s of milestone fan-out behind it.
//
// A var only so a test can shorten it: asserting that a wedged forge becomes a
// 504 rather than a hang is the regression test for this whole file, and at the
// production value that test would take half a minute to make its point.
var budget = 30 * time.Second

// onForge runs a forge-backed operation under a deadline, with a client scoped
// to the validated tenant and actor.
//
// It is the ONE place the budget is applied, and the closure is what makes that
// unforgettable: the bounded context SHADOWS the request's inside fn, so a call
// site cannot reach the unbounded one even by accident. Every op below is
// written in terms of this for the same reason every forge call is written in
// terms of do() — a control that each call site has to remember is a control
// that one of them will eventually be written without.
func onForge[T any](o ops, ctx context.Context, fn func(context.Context, *forge.Client, string) (T, error)) (T, error) {
	var zero T
	// Before scopeForge, not after: the credential read it makes is itself a
	// call that can be slow, and a deadline that started after it would leave
	// the one step nobody bounded.
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cl, org, err := o.scopeForge(ctx)
	if err != nil {
		return zero, err
	}
	return fn(ctx, cl, org)
}

// scopeForge resolves the two facts every forge-backed read needs: the validated
// ORG (which tenant's work this is) and the ACTOR (whose eyes the forge should
// answer through), returning a client already scoped to both.
//
// Both come from the validated principal and neither can be supplied by the
// caller. A request with no validated org, or with no IAM username to act as,
// gets 403 — never a client carrying the bare machine identity.
func (o ops) scopeForge(ctx context.Context) (*forge.Client, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		// Off the HTTP path (a CLI LocalInvoke) there is no attested tenant and no
		// attested actor, so there is nothing to scope by. Same 403 as an
		// unauthenticated request.
		return nil, "", principal.RefusedFrom(ctx)
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, "", principal.RefusedFrom(ctx)
	}
	// THE BRAND GATE. One cloud binary serves every brand's API host and its
	// validator trusts EVERY white-label issuer (auth_identity.go, trustedIssuers
	// unions BrandIssuers) — so a lux.id- or zoo.ngo-issued token validates here,
	// on the hanzo deployment, and arrives with a perfectly good org and username.
	//
	// But the forge is resolved from the DEPLOYMENT's own domain (brand.Sibling),
	// not from the principal's. Without this check a token from another brand's
	// IAM would be Sudo'd against git.hanzo.ai as whoever happens to hold that
	// login THERE — a different person entirely — and the forge would answer with
	// that person's private issues. The org and the actor are both attested, and
	// both attested by the WRONG AUTHORITY for this forge; two sound controls
	// compose into a cross-brand private-repo read because neither asks who
	// vouched.
	//
	// So the vouching brand must be this deployment's own. ok==false means there
	// is no second fact to compare — an hk-/sk- key minted by this deployment's
	// own IAM, which is by construction this brand — and is allowed, exactly as
	// apps/tenant reads the same pair. Normalised on both sides so a case
	// difference cannot decide a tenancy question.
	if vouched, ok := principal.BrandFrom(ctx); ok &&
		!strings.EqualFold(strings.TrimSpace(vouched), strings.TrimSpace(o.s.Brand)) {
		o.s.Log.Warn("tracker: refusing a principal vouched by another brand",
			"vouched", vouched, "deployment", o.s.Brand, "org", org)
		return nil, "", zip.ErrForbidden("this deployment's forge does not serve that brand's principals")
	}
	actor := actorOf(c)
	if actor == "" {
		return nil, "", zip.ErrForbidden("no forge identity for this principal")
	}
	cl, err := o.s.State.forge.resolve(ctx, o.s)
	if err != nil {
		o.s.Log.Error("forge credential unavailable", "err", err)
		return nil, "", zip.Errorf(http.StatusServiceUnavailable, "forge unavailable")
	}
	// The ORG is translated here, at the one place the validated tenant becomes a
	// forge coordinate, so no call site can ask the forge about an IAM name.
	return cl.As(actor), forgeOwner(org), nil
}

// actorOf is the IAM username the forge should act as.
//
// X-User-Name is the `name` half of <owner>/<name>, stamped by the identity
// boundary from VALIDATED claims only — it is in authorityHeaders, so a client's
// own copy is stripped on ingress and cannot survive. It is therefore safe to
// hand to Sudo: a caller cannot name someone else.
//
// It falls back to X-User-Id only when the username is absent, which is the same
// order resolveCaller uses — the gateway path historically minted the name into
// X-User-Id while the in-binary direct-Bearer path stamps the UUID subject.
func actorOf(c *zip.Ctx) string {
	if n := strings.TrimSpace(c.Header(authz.HeaderUserName)); n != "" {
		return n
	}
	return strings.TrimSpace(c.User())
}

// answer renders a forge error onto the wire.
//
// An unknown actor is 403 and says so: the user has no forge identity, which is
// a fact about them and is fixable, unlike an empty board which is indistinguishable
// from "no work". A rejected credential invalidates the cache and reads 503 —
// the deployment is broken, not the request.
func (o ops) answer(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// The budget ran out. 504 rather than 502: the forge did not refuse us
		// and did not answer wrongly, it did not answer IN TIME, which is a
		// different fact and the only one of the three a caller can usefully
		// retry. It must reach the wire as an error and never as an empty list —
		// a board rendering "no work" because the forge was slow is exactly the
		// silent failure this path exists to remove.
		o.s.Log.Error("forge read exceeded its budget", "budget", budget)
		return zip.Errorf(http.StatusGatewayTimeout, "the forge did not answer within %s", budget)
	case errors.Is(err, context.Canceled):
		// The CALLER went away — a closed tab, a navigation. Nothing is broken
		// and nobody is listening, so this is not an error to raise the alarm
		// with; logging it as one would bury the real failures above in noise.
		o.s.Log.Debug("forge read abandoned by the caller")
		return zip.Errorf(http.StatusGatewayTimeout, "request abandoned")
	case errors.Is(err, forge.ErrUnknownActor):
		return zip.ErrForbidden("no forge identity for this principal")
	case errors.Is(err, forge.ErrNoActor), errors.Is(err, forge.ErrNoToken):
		o.s.Log.Error("forge call made unscoped or uncredentialed", "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "forge unavailable")
	default:
		if strings.Contains(err.Error(), "credential rejected") {
			o.s.State.forge.invalidate()
			o.s.Log.Error("forge rejected the machine credential", "ref", tokenRef)
			return zip.Errorf(http.StatusServiceUnavailable, "forge unavailable")
		}
		o.s.Log.Error("forge read failed", "err", err)
		return zip.Errorf(http.StatusBadGateway, "forge read failed")
	}
}

// ── the projections ──────────────────────────────────────────────────────────
//
// A forge row rendered as the shape this surface already publishes, so the
// shipped SPA keeps working against a different source of truth.

// repoProject renders a forge repository as a tracker project. The repo NAME is
// the key: it is already unique within the org and already the thing every URL,
// clone and issue reference addresses, so deriving a separate short key would
// invent a second name for one object.
func repoProject(org string, r forge.Repo) trackerProject {
	return trackerProject{
		ID:   r.FullName,
		Org:  org,
		Key:  r.Name,
		Name: r.Name,
	}
}

// forgeIssue renders a forge issue as a tracker issue.
//
// Status and priority are LIFTED OUT of the label set rather than sitting beside
// it: a label that means "in_progress" is the column, so leaving it in the
// generic label list would render it twice — once as the card's column and once
// as a chip on the card.
func forgeIssue(i forge.Issue) issueView {
	repo := ""
	if i.Repository != nil {
		repo = i.Repository.Name
	}
	status, priority, rest := classify(i.Labels)
	// A closed issue is done regardless of its labels: the forge's own state is
	// the stronger fact, and a card sitting in "todo" after being closed on the
	// forge is precisely the drift this design removes.
	if strings.EqualFold(i.State, "closed") {
		status = "done"
	}
	kind := "issue"
	if i.PullRequest != nil {
		kind = "pr"
	}
	assignee := ""
	if len(i.Assignees) > 0 {
		assignee = i.Assignees[0].Login
	}
	v := issueView{
		ID:         fmt.Sprintf("%d", i.ID),
		Identifier: fmt.Sprintf("%s#%d", repo, i.Number),
		ProjectKey: repo,
		Number:     int(i.Number),
		Kind:       kind,
		// Every row on this surface now originates on the forge. `git` is the
		// contract's word for that origin (contract.go), and it is a fact rather
		// than a default.
		Source:      "git",
		Repo:        repo,
		Title:       i.Title,
		Description: i.Body,
		Status:      status,
		Priority:    priority,
		Assignee:    assignee,
		Labels:      rest,
		CreatedAt:   unix(i.Created),
		UpdatedAt:   unix(i.Updated),
	}
	if i.Milestone != nil && i.Milestone.Due != "" {
		v.DueAt = unix(i.Milestone.Due)
	}
	return v
}

// classify splits a forge label set into the board column, the priority, and
// everything else. The first label matching each closed set wins; a repo that
// carries two status labels is mis-labelled on the forge, and picking the first
// deterministically is better than refusing to render the card.
func classify(labels []forge.Label) (status, priority string, rest []string) {
	status, priority = "", ""
	rest = []string{}
	for _, l := range labels {
		n := strings.ToLower(strings.TrimSpace(l.Name))
		// A forge label is conventionally "status/in progress" or "in_progress";
		// normalise the separator so both spell the same column.
		n = strings.ReplaceAll(strings.TrimPrefix(n, "status/"), " ", "_")
		switch {
		case status == "" && statuses[n]:
			status = n
		case priority == "" && priorities[n]:
			priority = n
		default:
			rest = append(rest, l.Name)
		}
	}
	if status == "" {
		status = "backlog"
	}
	if priority == "" {
		priority = "none"
	}
	return status, priority, rest
}

// unix converts a forge RFC3339 timestamp to unix seconds, 0 when absent or
// unparseable — the wire shape publishes 0 as "unset" and a rendering failure
// must not fail the read.
func unix(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// milestoneView is one milestone in the org rollup. It carries the repo it came
// from because the rollup spans repos and a title alone does not identify one.
type milestoneView struct {
	ID     int64  `json:"id"`
	Repo   string `json:"repo"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Open   int    `json:"open"`
	Closed int    `json:"closed"`
	DueAt  int64  `json:"dueAt,omitempty"`
}

func forgeMilestone(m forge.Milestone) milestoneView {
	return milestoneView{
		ID: m.ID, Repo: m.Repo, Title: m.Title, State: m.State,
		Open: m.Open, Closed: m.Closed, DueAt: unix(m.Due),
	}
}

// ── the reads ────────────────────────────────────────────────────────────────

// ListProjects returns the boards of your org — one per repository on the
// deployment's forge that you can see. The key is the repository name, and it is
// what addresses the board's issues.
//
// Archived repositories are omitted: they are not live work. The set is the
// FORGE's answer for your own account, so two people in one org can legitimately
// see different boards.
func (o ops) forgeProjects(ctx context.Context, _ *noInput) (*projectList, error) {
	return onForge(o, ctx, func(ctx context.Context, cl *forge.Client, owner string) (*projectList, error) {
		// The board list is assembled from ISSUES, not from the org's repository
		// inventory. Both can answer "which boards are there", but on this forge
		// they do not cost remotely the same: the inventory charges per repository
		// returned — ~21s for ONE of the five pages of a 250-repo org — while
		// issues-search answers the whole org in ~1.5s, and it is the call the
		// board pages already make. Putting the inventory on this path is what
		// made the board hang.
		rows, err := cl.Issues(ctx, owner, forge.IssueFilter{State: "all"})
		if err != nil {
			return nil, o.answer(err)
		}
		seen := map[string]forge.Repo{}
		for _, r := range rows {
			if r.Repository == nil || r.Repository.Name == "" {
				continue
			}
			full := r.Repository.FullName
			if full == "" {
				full = owner + "/" + r.Repository.Name
			}
			seen[strings.ToLower(r.Repository.Name)] = forge.Repo{Name: r.Repository.Name, FullName: full}
		}
		// A repository with no work on it yet is still a board you can file
		// against, and only the inventory knows about it. So the inventory is read
		// WARM-ONLY: present, it completes the list; absent, it fills behind this
		// request and the next load has it. Waiting for it would put the 21s back
		// to add boards that are, by definition, empty.
		if repos, ok := cl.ReposWarm(ctx, owner); ok {
			for _, r := range repos {
				if r.Archived {
					continue
				}
				if _, dup := seen[strings.ToLower(r.Name)]; !dup {
					seen[strings.ToLower(r.Name)] = r
				}
			}
		}
		out := make(projectList, 0, len(seen))
		for _, r := range seen {
			out = append(out, repoProject(owner, r))
		}
		// Sorted, because the list is assembled from a map and two identical reads
		// must not answer in two different orders.
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return &out, nil
	})
}

// GetProject returns one board of your org by its key — the repository name.
// 404 when your org has no repository under that key, or when your own forge
// account cannot see it.
func (o ops) forgeProject(ctx context.Context, in *projectRef) (*trackerProject, error) {
	return onForge(o, ctx, func(ctx context.Context, cl *forge.Client, owner string) (*trackerProject, error) {
		// ONE repository, read directly. Scanning the org's inventory to find a
		// board whose name we already have is what made this page cost twenty
		// seconds on a large org; the forge will simply hand it over for ~1s.
		r, err := cl.Repo(ctx, owner, in.Key)
		if err != nil {
			// The forge answers 404 for "no such repository" and for "your account
			// cannot see it" alike, and for a board addressed BY NAME those are one
			// answer — which also declines to tell a caller that a private board is
			// there.
			if errors.Is(err, forge.ErrUnknownActor) {
				return nil, zip.ErrNotFound("no such project")
			}
			return nil, o.answer(err)
		}
		if r.Archived {
			return nil, zip.ErrNotFound("no such project")
		}
		v := repoProject(owner, r)
		return &v, nil
	})
}

// ListIssues returns one board's issues — the work items of that repository on
// the forge, with their column, priority, assignee and labels.
//
// The column is a LABEL on the forge, so the board and the forge web UI are the
// same object seen twice: relabelling in either moves the card in both. A closed
// issue reads as done whatever its labels say.
func (o ops) forgeIssues(ctx context.Context, in *issueQuery) (*issueList, error) {
	return onForge(o, ctx, func(ctx context.Context, cl *forge.Client, org string) (*issueList, error) {
		if in.Status != "" && !statuses[in.Status] {
			return nil, zip.ErrBadRequest("unknown status")
		}
		if in.Kind != "" && !kinds[in.Kind] {
			return nil, zip.ErrBadRequest("unknown kind")
		}
		f := forge.IssueFilter{State: "all"}
		switch in.Kind {
		case "pr":
			f.Type = "pulls"
		case "issue":
			f.Type = "issues"
		}
		rows, err := cl.Issues(ctx, org, f)
		if err != nil {
			return nil, o.answer(err)
		}
		out := make(issueList, 0, len(rows))
		for _, r := range rows {
			v := forgeIssue(r)
			// The board is addressed by repository, and issues-search spans the org, so
			// the repo IS the project filter. Compared case-insensitively for the same
			// reason getProject is.
			if !strings.EqualFold(v.Repo, in.Key) {
				continue
			}
			if in.Status != "" && v.Status != in.Status {
				continue
			}
			if in.Scheduled && v.DueAt == 0 && v.StartAt == 0 {
				continue
			}
			out = append(out, v)
		}
		return &out, nil
	})
}

// ListMilestones returns every milestone across your org's repositories, each
// stamped with the repository it belongs to.
//
// The forge scopes milestones to a repository and publishes no org-level list,
// so this is a server-side fan-out over the repositories you can see. It runs
// here rather than in the browser because a client-side fan-out would need the
// forge reachable from the page and a credential held there.
func (o ops) forgeMilestones(ctx context.Context, _ *noInput) (*milestoneList, error) {
	return onForge(o, ctx, func(ctx context.Context, cl *forge.Client, org string) (*milestoneList, error) {
		ms, err := cl.Milestones(ctx, org)
		if err != nil {
			return nil, o.answer(err)
		}
		out := make(milestoneList, 0, len(ms))
		for _, m := range ms {
			out = append(out, forgeMilestone(m))
		}
		return &out, nil
	})
}

// milestoneList is the org's milestone rollup. Empty is an empty JSON array,
// never null.
type milestoneList []milestoneView

// ── the writes ───────────────────────────────────────────────────────────────
//
// Each one is made on the forge under the caller's own Sudo actor, so the forge
// records the HUMAN as the author and as the mover of every card. A shared bot
// identity would make the audit trail say "the tracker did it", which is not an
// answer to who did it.

// newIssue opens a work item on a board.
type newIssue struct {
	// Key is the board — the repository name, from the path.
	Key string `json:"key"`
	// Title is required.
	Title string `json:"title"`
	// Description becomes the issue body.
	Description string `json:"description"`
	// Status is the board column to open into: backlog, todo, in_progress, done
	// or canceled. Empty opens into backlog.
	Status string `json:"status"`
	// Priority is one of none, urgent, high, medium or low.
	Priority string `json:"priority"`
}

// CreateIssue opens a work item on the board — an issue on that repository on
// the deployment's forge, filed as YOU.
//
// The column and priority are written as LABELS, which is what makes the card
// and the forge issue the same object: someone relabelling in the forge web UI
// has moved your card.
func (o ops) forgeCreateIssue(ctx context.Context, in *newIssue) (*issueView, error) {
	return onForge(o, ctx, func(ctx context.Context, cl *forge.Client, org string) (*issueView, error) {
		if strings.TrimSpace(in.Title) == "" {
			return nil, zip.ErrBadRequest("title required")
		}
		labels, err := columnLabels(in.Status, in.Priority)
		if err != nil {
			return nil, err
		}
		got, err := cl.CreateIssue(ctx, org, in.Key, forge.NewIssue{
			Title: in.Title, Body: in.Description, Labels: labels,
		})
		if err != nil {
			return nil, o.answer(err)
		}
		v := forgeIssue(got)
		// issues-search stamps the repository on every row; the create response does
		// not, because the repo was the address. Fill it so the card knows its board.
		if v.Repo == "" {
			v.Repo, v.ProjectKey = in.Key, in.Key
			v.Identifier = fmt.Sprintf("%s#%d", in.Key, got.Number)
		}
		return &v, nil
	})
}

// issueEdit changes a work item. Absent fields are left alone.
type issueEdit struct {
	// Key is the board — the repository name, from the path.
	Key string `json:"key"`
	// Num is the issue number on that repository, from the path.
	Num int64 `json:"num"`
	// Title renames the work item.
	Title string `json:"title"`
	// Description rewrites the body.
	Description string `json:"description"`
	// Status moves the card to another column.
	Status string `json:"status"`
	// Priority re-prioritises it.
	Priority string `json:"priority"`
}

// UpdateIssue edits a work item — rename it, rewrite it, move it to another
// column, or re-prioritise it. Absent fields are left alone.
//
// MOVING A CARD IS A RELABEL. The column lives in the forge's label set, so the
// move replaces that set rather than writing a status column here that a
// forge-side change could contradict. Moving to `done` also CLOSES the issue on
// the forge, because a done card and an open issue are a contradiction.
func (o ops) forgePatchIssue(ctx context.Context, in *issueEdit) (*issueView, error) {
	return onForge(o, ctx, func(ctx context.Context, cl *forge.Client, org string) (*issueView, error) {
		if in.Num <= 0 {
			return nil, zip.ErrBadRequest("bad issue number")
		}
		var patch forge.IssuePatch
		if in.Title != "" {
			patch.Title = &in.Title
		}
		if in.Description != "" {
			patch.Body = &in.Description
		}
		if in.Status != "" {
			if !statuses[in.Status] {
				return nil, zip.ErrBadRequest("unknown status")
			}
			state := "open"
			if in.Status == "done" || in.Status == "canceled" {
				state = "closed"
			}
			patch.State = &state
		}
		if patch.Title != nil || patch.Body != nil || patch.State != nil {
			if err := cl.PatchIssue(ctx, org, in.Key, in.Num, patch); err != nil {
				return nil, o.answer(err)
			}
		}
		// The relabel is a SEPARATE call because the forge models the label set as its
		// own sub-resource, and replacing it is the one unambiguous "move".
		if in.Status != "" || in.Priority != "" {
			labels, err := columnLabels(in.Status, in.Priority)
			if err != nil {
				return nil, err
			}
			if err := cl.SetLabels(ctx, org, in.Key, in.Num, labels); err != nil {
				return nil, o.answer(err)
			}
		}
		// Answer with the row as the FORGE now holds it, not with the patch echoed
		// back: the forge is the source of truth, and a response assembled from the
		// request would be this surface asserting a state it has not confirmed.
		rows, err := cl.Issues(ctx, org, forge.IssueFilter{State: "all"})
		if err != nil {
			return nil, o.answer(err)
		}
		for _, r := range rows {
			v := forgeIssue(r)
			if strings.EqualFold(v.Repo, in.Key) && int64(v.Number) == in.Num {
				return &v, nil
			}
		}
		return nil, zip.ErrNotFound("no such issue")
	})
}

// columnLabels renders a board column and a priority as the forge label set that
// represents them. Validated against the SAME closed sets the board renders from
// (statuses, priorities), so a column can never be written that cannot be read
// back.
func columnLabels(status, priority string) ([]string, error) {
	out := []string{}
	if status != "" {
		if !statuses[status] {
			return nil, zip.ErrBadRequest("unknown status")
		}
		out = append(out, status)
	}
	if priority != "" && priority != "none" {
		if !priorities[priority] {
			return nil, zip.ErrBadRequest("unknown priority")
		}
		out = append(out, priority)
	}
	return out, nil
}

// projectLifecycle refuses to create, rename or delete a board.
//
// A board IS a repository on the forge. Its lifecycle is a forge operation with
// forge permissions, and offering a second door onto it here would mean this
// surface's guard, not the forge's, decided who may make and destroy
// repositories — a weaker guard on the same object.
//
// 405 and not 404: the route exists and the answer is "not this service's job",
// which is a different fact from "no such thing", and the message names where
// the job IS done.
func projectLifecycle(c *zip.Ctx) error {
	return c.JSON(http.StatusMethodNotAllowed, map[string]any{
		"error": map[string]any{
			"code": "forge_owns_repositories",
			"message": "a board is a repository on the forge — create, rename and delete it there; " +
				"this surface reads and moves the work items on it",
		},
	})
}
