package todo

// source.go binds the todo to the forge.
//
// The forge (git.hanzo.ai beside api.hanzo.ai) is the SINGLE source of truth for
// this estate's work items. An issue is filed, labelled, assigned and closed
// there, so that is where the todo reads it from — not from a copy. There is
// deliberately no mirror, no cache table and no write-through: a second store at
// this prefix would be a second answer to "what is the state of this work", and
// the two would drift the first time anyone touched the forge directly, which is
// every day.
//
// # What maps to what
//
// The forge already has every noun the board needs, so nothing is invented:
//
//	todo project   a forge REPOSITORY (its name is the key)
//	todo issue     a forge ISSUE (its per-repo number is the number)
//	board column   a forge LABEL drawn from the closed `statuses` set
//	priority       a forge LABEL drawn from the closed `priorities` set
//	deadline       a forge MILESTONE's due date, read onto the issue it dates
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
// capability means a compromise of the todo cannot deploy, and revoking the
// todo's token does not stop releases.
const tokenRef = "orgs/hanzo/deploy/FORGE_TRACKER_TOKEN@prod"

// forgeSource holds the deployment's forge client and the credential behind it.
//
// The client is resolved LAZILY rather than at Use:  KMS need not be reachable
// at process start, a token rotates while the process lives, and a todo that
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
		return nil, fmt.Errorf("no KMS client in use: cannot read %s", tokenRef)
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

// forgeOwner is the forge org for a VALIDATED IAM org, from forge's own CLOSED
// table. It lived here as a second copy of that table until the coding path
// needed the same translation; two copies of "which namespace is this tenant's"
// is two chances to disagree, and the copy that fell back to the org's own name
// made an unmapped IAM org address the estate's repositories directly.
//
// An org with no forge namespace is REFUSED, which reads to a caller as 403
// rather than as an empty board — an org that has no forge is a fact about the
// deployment, not about the user's work.
func forgeOwner(org string) (string, error) { return forge.Owner(org) }

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
// list.
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
func onForge[T any](o ops, ctx context.Context, act bool, fn func(context.Context, *forge.Client, string) (T, error)) (T, error) {
	var zero T
	// The anti-CSRF control, ahead of everything — before a credential is read,
	// before the deadline starts, and before the forge hears anything. It is
	// asked HERE rather than on the route group because the group's middleware
	// reaches the route's handler and not the op, and MCP calls the op (todo.go
	// says what that costs). act is what makes the question answerable at all:
	// over MCP every operation is one POST, so the method cannot say which of
	// these change a board.
	if err := csrf(ctx, act); err != nil {
		return zero, err
	}
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
// caller. A request with no validated org, or with no PROVED forge identity,
// gets 403 — never a client carrying the bare machine identity.
func (o ops) scopeForge(ctx context.Context) (*forge.Client, string, error) {
	if _, ok := cloud.Request(ctx); !ok {
		// Off the HTTP path (a CLI LocalInvoke) there is no attested tenant and no
		// attested actor, so there is nothing to scope by. Same 403 as an
		// unauthenticated request.
		return nil, "", principal.RefusedFrom(ctx)
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
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
	// is no second fact to compare — an sk- key minted by this deployment's
	// own IAM, which is by construction this brand — and is allowed, exactly as
	// apps/tenant reads the same pair. Normalised on both sides so a case
	// difference cannot decide a tenancy question.
	if vouched, ok := principal.BrandFrom(ctx); ok &&
		!strings.EqualFold(strings.TrimSpace(vouched), strings.TrimSpace(o.s.Brand)) {
		o.s.Log.Warn("todo: refusing a principal vouched by another brand",
			"vouched", vouched, "deployment", o.s.Brand, "org", org)
		return nil, "", zip.ErrForbidden("this deployment's forge does not serve that brand's principals")
	}
	cl, err := o.s.State.forge.resolve(ctx, o.s)
	if err != nil {
		o.s.Log.Error("forge credential unavailable", "err", err)
		return nil, "", zip.Errorf(http.StatusServiceUnavailable, "forge unavailable")
	}
	// WHO the forge answers as, resolved and PROVED — see forge.Client.Caller.
	//
	// This used to be the IAM username, handed straight to Sudo. The two are
	// different namespaces: a forge login is the local part of a confirmed
	// address, and an IAM username is separately chosen. On a shared signup org
	// they collide by choice — a stranger picking the username `z` sudoed as the
	// staff member whose address is z@…, and this surface WRITES: it opens issues
	// and moves cards as whoever it acts for.
	//
	// The same resolver the coding path uses, deliberately: one question, one
	// answer, and no second implementation to drift.
	actor, aerr := cl.Caller(ctx)
	if aerr != nil {
		// A FORGE THAT DID NOT ANSWER IN TIME IS NOT A MISSING IDENTITY. The
		// resolution reads the forge, so a wedged one fails here first — and
		// reporting that as 403 would send an operator looking for a permissions
		// problem that does not exist. The deadline keeps its own answer (504),
		// which is what o.answer already says about every other read.
		if errors.Is(aerr, context.DeadlineExceeded) || errors.Is(aerr, context.Canceled) {
			return nil, "", o.answer(aerr)
		}
		o.s.Log.Warn("todo: no proved forge identity for this principal", "err", aerr)
		return nil, "", zip.ErrForbidden("no forge identity for this principal")
	}
	// The ORG is translated here, at the one place the validated tenant becomes a
	// forge coordinate, so no call site can ask the forge about an IAM name.
	owner, oerr := forgeOwner(org)
	if oerr != nil {
		return nil, "", zip.ErrForbidden("this org has no namespace on the forge")
	}
	return cl.As(actor), owner, nil
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
	case errors.Is(err, forge.ErrBadName):
		// The CALLER's own key, refused before a request was built. 400, because
		// nothing upstream was asked and nothing upstream is wrong — this used to
		// answer "502 forge read failed", which is the same shape of lie the
		// milestone rollup told when it declined to make a read it could have made.
		return zip.ErrBadRequest("that is not a valid board key")
	case errors.Is(err, forge.ErrUnknownActor):
		return zip.ErrForbidden("no forge identity for this principal")
	case errors.Is(err, forge.ErrNoActor), errors.Is(err, forge.ErrNoToken):
		o.s.Log.Error("forge call made unscoped or uncredentialed", "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "forge unavailable")
	case errors.Is(err, forge.ErrRefused):
		// The forge refused the ACTOR, not us. 403 to the caller, and nothing is
		// invalidated — this used to be folded in with a rejected credential and
		// cost every tenant the cached client.
		return zip.ErrForbidden("the forge refused this account")
	default:
		if errors.Is(err, forge.ErrCredentialRejected) {
			o.s.State.forge.invalidate()
			// CARRY THE ERROR. The forge answers 401 and 403 to different problems
			// and the client folds both into this one string, so the status code in
			// it is the only thing that separates "the token is revoked" — re-mint
			// and write the ref — from "the token is fine and its account may not
			// Sudo", which no rotation ever fixes. Dropping it left an operator with
			// a message that named a secret and could not say what was wrong with
			// it. The error carries a path and a code, never the credential.
			o.s.Log.Error("forge rejected the machine credential", "ref", tokenRef, "err", err)
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

// repoProject renders a forge repository as a todo project. The repo NAME is
// the key: it is already unique within the org and already the thing every URL,
// clone and issue reference addresses, so deriving a separate short key would
// invent a second name for one object.
func repoProject(org string, r forge.Repo) todoProject {
	return todoProject{
		ID:   r.FullName,
		Org:  org,
		Key:  r.Name,
		Name: r.Name,
	}
}

// ident is the human handle for a work item: the board it is on, then its number
// on that board. ONE spelling, used by both projections below — a board whose
// forge rows read `cli#1` and whose index rows read `OPS-3` is two products in
// one list, and a person cannot tell that the difference is about where we
// happen to store the row rather than about the work.
func ident(key string, number int) string { return fmt.Sprintf("%s#%d", key, number) }

// indexProject renders a board the INDEX holds as the same view a repository
// renders as. A board is one kind of thing — an org-scoped, key-addressed set of
// work items — and which source fills it is a fact about the row, not about the
// board.
func indexProject(p Project) todoProject {
	return todoProject{
		ID: p.ID, Org: p.Org, Key: p.Key, Name: p.Name,
		Description: p.Description, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// indexIssue renders an index row as the same view a forge issue renders as, so
// a caller cannot tell which source answered from the shape of the answer — the
// property todo.go already states for the search, now holding for the board.
func indexIssue(key string, i Issue) issueView {
	labels := []string{}
	for _, l := range strings.Split(i.Labels, ",") {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	return issueView{
		ID: i.ID, Identifier: ident(key, i.Number), ProjectKey: key, Number: i.Number,
		Kind: i.Kind, Source: i.Source, Repo: i.Repo, ExtRef: i.ExtRef,
		Title: i.Title, Description: i.Description, Status: i.Status,
		Priority: i.Priority, Assignee: i.Assignee, Labels: labels,
		StartAt: i.StartAt, DueAt: i.DueAt,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
}

// index opens the caller's work-item index — the org's default project store,
// which is where every source that is not the forge lands (github_sink.go, the
// plane upsert, an agent filing its own work). It returns the store and the
// validated IAM org, which is the tenant key every query below filters on.
//
// The IAM org, NOT forgeOwner(org): the index is keyed by the tenant IAM named,
// while the forge is asked about the org that tenant's work lives under. Passing
// the forge spelling here would open a different file — one nothing writes.
func (o ops) index(ctx context.Context) (*Store, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
	}
	st, err := storeFor(o.s, org, principal.DefaultProject)
	if err != nil {
		return nil, "", err
	}
	return st, org, nil
}

// boardKeys maps the index's project ids to the KEY that addresses each board.
//
// The wire carries the key and never the id, because an id is not an address:
// GET /projects/prj_b4b4bd4c… answers "no such project", and that is exactly what
// the search used to hand back for every row it found. A result you cannot open
// is a listing, not a tool — todo.go says a search must return rows a caller
// can act on, and returning the id quietly broke that for the only issues the
// index holds.
func boardKeys(ctx context.Context, st *Store, org string) (map[string]string, error) {
	ps, err := st.ListProjects(ctx, org)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(ps))
	for _, p := range ps {
		m[p.ID] = p.Key
	}
	return m, nil
}

// forgeIssue renders a forge issue as a todo issue.
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
		Identifier: ident(repo, int(i.Number)),
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
	// THE SCHEDULE. A forge issue's deadline is its milestone's due date — the
	// forge has no per-issue one — and the interval it occupies runs from when it
	// was opened to when it is due.
	//
	// StartAt was left at 0 here, and that made the timeline structurally
	// incapable of drawing a bar: spanOf reads a due date with no start as a
	// POINT, so every scheduled row on every board rendered as a milestone
	// diamond and the gantt was a column of dots. Filling it from Created is not
	// an invented field — it is the one instant the forge actually knows the work
	// began to exist, and "open since -> due" is what the bar means.
	if i.Milestone != nil && i.Milestone.Due != "" {
		v.DueAt = unix(i.Milestone.Due)
		v.StartAt = v.CreatedAt
		// A row created after its own deadline has no interval to draw. Leave the
		// start unset so it reads as the point it is, rather than as a bar running
		// backwards — barOf would clamp it to a sliver at the wrong end.
		if v.StartAt >= v.DueAt {
			v.StartAt = 0
		}
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

// ── the reads ────────────────────────────────────────────────────────────────

// ListProjects returns the boards of your org — the places your work actually
// is. The key addresses the board's issues.
//
// A BOARD IS A PLACE WORK IS, not an object somebody provisioned. So the list is
// assembled from the work itself: the repositories your org has filed issues on,
// plus the boards the index holds. A repository with nothing on it is not in the
// list and is still perfectly addressable — GET /projects/<name> reads it and a
// create files into it — so nothing is lost by leaving it out.
//
// Measured, which is why: reading the forge's whole repository inventory put 745
// boards here, of which all but a handful were vendored forks and mirrors
// (.github, .profile, DOMPurify, BoatAttack) that will never carry this org's
// work. A list that long is not a list — the estate's real roadmap was in it
// somewhere and no one could see it.
//
// The forge half is the FORGE's answer for your own account, so two people in
// one org can legitimately see different boards.
func (o ops) forgeProjects(ctx context.Context, _ *cloud.Unit) (*projectList, error) {
	return onForge(o, ctx, reads, func(ctx context.Context, cl *forge.Client, owner string) (*projectList, error) {
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
		out := make(projectList, 0, len(seen)+8)
		for _, r := range seen {
			out = append(out, repoProject(owner, r))
		}
		// AND THE BOARDS THE INDEX HOLDS. A board filled by an agent, a mirror or
		// the helpdesk is the same kind of thing as a board filled by a repository
		// — an org-scoped, key-addressed set of work items — so it belongs in the
		// same list rather than behind a second endpoint. Measured before this
		// existed: the org's real roadmap was 15 rows under two project ids that
		// this list did not contain, so the board UI could not address them at all
		// and `GET /projects/<that id>` answered 404.
		st, org, err := o.index(ctx)
		if err != nil {
			return nil, err
		}
		boards, err := st.ListProjects(ctx, org)
		if err != nil {
			return nil, err
		}
		for _, b := range boards {
			if _, dup := seen[strings.ToLower(b.Key)]; !dup {
				out = append(out, indexProject(b))
			}
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
func (o ops) forgeProject(ctx context.Context, in *projectRef) (*todoProject, error) {
	return onForge(o, ctx, reads, func(ctx context.Context, cl *forge.Client, owner string) (*todoProject, error) {
		// ONE repository, read directly. Scanning the org's inventory to find a
		// board whose name we already have is what made this page cost twenty
		// seconds on a large org; the forge will simply hand it over for ~1s.
		r, err := cl.Repo(ctx, owner, in.Key)
		if err == nil && !r.Archived {
			v := repoProject(owner, r)
			return &v, nil
		}
		// Not a repository, or not one you can see — so try the INDEX, which holds
		// the other boards this org files against. Same URL, because a board is one
		// kind of thing however it came to exist; the alternative is a caller that
		// has to know which store a key lives in before it can address it, which is
		// exactly the split this surface no longer has.
		if st, iamOrg, ierr := o.index(ctx); ierr == nil {
			if p, perr := st.GetProject(ctx, iamOrg, strings.ToUpper(in.Key)); perr == nil {
				v := indexProject(p)
				return &v, nil
			}
		}
		// The forge answers 404 for "no such repository" and for "your account
		// cannot see it" alike, and for a board addressed BY NAME those are one
		// answer — which also declines to tell a caller that a private board is
		// there. A forge that FAILED, rather than declined, is still reported as
		// the failure it was.
		if err != nil && !errors.Is(err, forge.ErrUnknownActor) {
			return nil, o.answer(err)
		}
		return nil, zip.ErrNotFound("no such project")
	})
}

// ListIssues returns a board's issues — work items with their column, priority,
// assignee, labels and schedule.
//
// WHICH board is a filter, not an address. Bound to a repository (the key from
// the path) it is that project's board; left unbound it is the org's whole
// board; narrowed by label it is a board smaller than any repository — which is
// the only way an app that lives as a directory inside a shared repository can
// have one. Every combination is the same rows through the same projection, so
// no two boards can disagree about what a column means.
//
// The column is a LABEL on the forge, so the board and the forge web UI are the
// same object seen twice: relabelling in either moves the card in both. A closed
// issue reads as done whatever its labels say.
func (o ops) forgeIssues(ctx context.Context, in *issueQuery) (*issueList, error) {
	return onForge(o, ctx, reads, func(ctx context.Context, cl *forge.Client, org string) (*issueList, error) {
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
		// TWO SOURCES, ONE BOARD. The forge answers for the work filed on its
		// repositories; the index answers for everything else that files against
		// this estate — an agent's own roadmap, a mirrored GitHub issue, a
		// helpdesk escalation. Reading only the first is what made the product
		// show three bot pull requests while the actual roadmap, fifteen rows
		// assigned to named agents, was unreachable from every board in the UI.
		//
		// They are unioned HERE, before the filters, rather than behind two
		// endpoints a caller has to know to ask twice: Source is already the field
		// that says where a row came from (contract.go), so where it is stored is
		// not a second question anyone should have to ask. One list, one filter
		// pass, and no way for the two halves to disagree about what a column is.
		st, iamOrg, err := o.index(ctx)
		if err != nil {
			return nil, err
		}
		key, err := boardKeys(ctx, st, iamOrg)
		if err != nil {
			return nil, err
		}
		indexed, err := st.ListIssues(ctx, iamOrg, "", IssueFilter{})
		if err != nil {
			return nil, err
		}
		all := make([]issueView, 0, len(rows)+len(indexed))
		for _, r := range rows {
			all = append(all, forgeIssue(r))
		}
		for _, r := range indexed {
			all = append(all, indexIssue(key[r.ProjectID], r))
		}

		out := make(issueList, 0, len(all))
		seen := make(map[string]bool, len(all))
		for _, v := range all {
			// One card per handle. The two sources address different keyspaces —
			// repository names, and the index's uppercase board keys — so a
			// collision takes a repository named exactly like a board. Rare, and a
			// card drawn twice is a worse answer than the one dropped here.
			h := strings.ToLower(v.Identifier)
			if seen[h] {
				continue
			}
			seen[h] = true
			// WHICH board is a filter, and it filters on the BOARD KEY. For a forge
			// row that key is the repository, which is why this used to read the
			// repo field and still worked; for an index row the two are different
			// facts and only the key addresses the board.
			//
			// AN EMPTY KEY KEEPS EVERYTHING. The fan-out above is already org-wide and
			// every row not on the requested board was being discarded here; leaving
			// the filter unbound is therefore the global board, at no extra cost and
			// with no second endpoint to drift from this one.
			if in.Key != "" && !strings.EqualFold(v.ProjectKey, in.Key) {
				continue
			}
			if in.Repo != "" && !strings.EqualFold(v.Repo, in.Repo) {
				continue
			}
			if in.Label != "" && !hasLabel(v.Labels, in.Label) {
				continue
			}
			if in.Status != "" && v.Status != in.Status {
				continue
			}
			// Kind and Source are narrowed at the forge for the forge's half (f.Type
			// above) and nowhere at all for the index's, so both are applied here.
			// `source` was declared on this query, documented, and silently ignored
			// while every row on the surface carried the same value — a filter that
			// cannot change the answer reads as a filter that agrees with you.
			if in.Kind != "" && v.Kind != in.Kind {
				continue
			}
			if in.Source != "" && v.Source != in.Source {
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

// GetIssue returns ONE work item in full — its description included.
//
// The list reads answer a board, and a board is a summary: the description is
// where the actual content of a work item lives — what an issue asks for, what
// an epic's acceptance criteria are — and no read on this surface returned it.
// The address is the one PATCH already accepts, so an item you can move is now
// an item you can read.
//
// It reads the forge directly rather than filtering the org fan-out, then falls
// back to the index for a board the forge has never heard of — the same order,
// and the same reason, as GetProject: a row is one kind of thing however it came
// to exist, so a caller does not have to know which store it is in to fetch it.
func (o ops) forgeIssue(ctx context.Context, in *issueRef) (*issueView, error) {
	return onForge(o, ctx, reads, func(ctx context.Context, cl *forge.Client, org string) (*issueView, error) {
		if in.Num <= 0 {
			return nil, zip.ErrBadRequest("bad issue number")
		}
		r, err := cl.Issue(ctx, org, in.Key, in.Num)
		if err == nil {
			v := forgeIssue(r)
			return &v, nil
		}
		// THE INDEX IS ORG-SCOPED, AND ONLY ORG-SCOPED. The forge half above drops
		// privilege to the caller through Sudo, so a repository they cannot see is
		// 404; this half filters on the validated IAM org and nothing finer. The
		// two ACLs are deliberately different — the index holds work the forge
		// never had (an agent's roadmap, a helpdesk escalation) and an org's
		// members are meant to see their org's work — but it does mean a row
		// MIRRORED here from a repository the caller cannot read on the forge is
		// readable through this address. The list and the search have always
		// unioned it the same way; this is the same rule, not a new one.
		if st, iamOrg, ierr := o.index(ctx); ierr == nil {
			key, kerr := boardKeys(ctx, st, iamOrg)
			rows, rerr := st.ListIssues(ctx, iamOrg, "", IssueFilter{})
			if kerr == nil && rerr == nil {
				for _, row := range rows {
					if int64(row.Number) != in.Num || !strings.EqualFold(key[row.ProjectID], in.Key) {
						continue
					}
					v := indexIssue(key[row.ProjectID], row)
					return &v, nil
				}
			}
		}
		// A forge that FAILED, rather than declined, is reported as the failure it
		// was; "no such issue" and "not yours to see" are one answer, which is what
		// declines to tell a caller that a private row is there.
		if !errors.Is(err, forge.ErrUnknownActor) {
			return nil, o.answer(err)
		}
		return nil, zip.ErrNotFound("no such issue")
	})
}

// hasLabel reports whether a row carries the label, case-insensitively — the
// same comparison every other name on this surface uses, because a board that
// answers differently for `app/Meet` and `app/meet` is two boards.
func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// ── the writes ───────────────────────────────────────────────────────────────
//
// Each one is made on the forge under the caller's own Sudo actor, so the forge
// records the HUMAN as the author and as the mover of every card. A shared bot
// identity would make the audit trail say "the todo did it", which is not an
// answer to who did it.

// newIssue opens a work item on a board.
type newIssue struct {
	// Key is the board — the repository name, from the path.
	Key string `json:"key"`
	// Title is the one line the card is read by on the board. Blank or whitespace
	// is refused — an untitled card cannot be told apart from any other.
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
	return onForge(o, ctx, changes, func(ctx context.Context, cl *forge.Client, org string) (*issueView, error) {
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
			v.Identifier = ident(in.Key, int(got.Number))
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
	// Assignee hands the work to somebody — a person or an agent, by the name
	// they are known by on the forge. "" TAKES IT OFF whoever holds it, which is
	// why this is a pointer: absent leaves the holder alone.
	//
	// It is the other half of `claim`, which that handler already named: a claim
	// takes work for the CALLER and refuses to name anyone else, because giving
	// work away is a different act with different authority. This is that act,
	// and until it existed a board could only be worked by whoever clicked
	// first — an agent could never be given anything.
	Assignee *string `json:"assignee"`
}

// UpdateIssue edits a work item — rename it, rewrite it, move it to another
// column, or re-prioritise it. Absent fields are left alone.
//
// MOVING A CARD IS A RELABEL. The column lives in the forge's label set, so the
// move replaces that set rather than writing a status column here that a
// forge-side change could contradict. Moving to `done` also CLOSES the issue on
// the forge, because a done card and an open issue are a contradiction.
func (o ops) forgePatchIssue(ctx context.Context, in *issueEdit) (*issueView, error) {
	return onForge(o, ctx, changes, func(ctx context.Context, cl *forge.Client, org string) (*issueView, error) {
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
			// settled (room.go) is the ONE predicate for "this status ends the
			// work", so the forge issue this closes and the open count a channel
			// header shows cannot disagree about which columns are finished.
			state := "open"
			if settled(in.Status) {
				state = "closed"
			}
			patch.State = &state
		}
		if in.Assignee != nil {
			// A SET OF ONE, or none. The forge models assignment as a set; this
			// surface offers a single holder, so the set it sends has one member
			// or is empty.
			held := []string{}
			if who := strings.TrimSpace(*in.Assignee); who != "" {
				held = append(held, who)
			}
			patch.Assignees = &held
		}
		if patch.Title != nil || patch.Body != nil || patch.State != nil || patch.Assignees != nil {
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
		//
		// One read, at the address just written. It used to be the org-wide search,
		// which fans out over every repository the actor can see to keep the single
		// row it already knows the address of — so moving one card cost the whole
		// board twice.
		//
		// THE READ-BACK IS A SECOND FAILURE POINT ON A PATH THAT HAS ALREADY
		// WRITTEN, so what it says must be about the ROW and never about the
		// caller. Under Sudo the forge answers 404 for "not there" and for "not
		// yours" alike, and both arrive as ErrUnknownActor — which o.answer renders
		// as "no forge identity for this principal", a claim about the caller's
		// IDENTITY that sends whoever reads it to IAM to fix a missing issue.
		//
		// It still reports an ERROR when the read-back fails, and that is a choice
		// rather than an oversight: every write above is IDEMPOTENT — a title, a
		// body, a state and a label set, each set to a value, not incremented — so
		// a caller that retries on this error re-applies exactly the same row and
		// nothing is duplicated. The alternative is answering 200 with a row this
		// surface never read back, which is the one thing the forge-is-the-truth
		// rule above forbids.
		r, err := cl.Issue(ctx, org, in.Key, in.Num)
		if err != nil {
			if errors.Is(err, forge.ErrUnknownActor) {
				return nil, zip.ErrNotFound("no such issue")
			}
			return nil, o.answer(err)
		}
		v := forgeIssue(r)
		return &v, nil
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
// forge permissions, and offering a second endpoint onto it here would mean this
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
