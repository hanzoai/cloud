package projects

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// visibility.go — who can SEE a project, and who decides.
//
// There is exactly one axis, and it belongs to the publisher: public or
// private. Public is the default and is ungated, because a community nobody can
// enter without approval is a directory, and a directory does not grow. Private
// is the paid feature.
//
// That is the inverse of the arrangement this replaced, which gated the way IN
// (an admin-only `official` badge) and left the way out free. Gating entry
// suppresses exactly the thing the platform wants more of, and it failed on its
// own terms: the badge was unreachable by the script that published the
// platform's own catalogue, so 74 Hanzo apps were filed as somebody else's.
//
// AUTHORSHIP IS NOT STORED. Who made a project is its org — the account that
// pays for it — which the tenancy boundary already enforces and no request can
// forge. A tenant cannot publish into `hanzo` because it cannot fund `hanzo`.
// So there is nothing left for a badge to say that the org column does not
// already say, unforgeably.
//
// MODERATION IS SUBTRACTIVE. Project.Hidden is set from admin.hanzo.ai only and
// only ever removes. It is safe to be the one admin-gated field precisely
// because it cannot be used to promote anything, and because it leaves the
// publisher's own Visibility untouched — lifting a moderation restores exactly
// what they asked for, with no second write to get wrong.
//
// # The source follows the row
//
// The project row is the source of truth and the repository on the forge is
// derived from it: present and world-readable exactly when the row says listed,
// closed when it does not, and GONE when the row is. The derivation runs on
// every create, update and delete rather than on a transition, because a
// transition this side failed to notice would leave a private project's source
// readable — see [share].
//
// The delete is part of the derivation and not an afterthought. A repository
// left behind by a deleted project is readable with no row left to say it must
// not be, and the next tenant of that slug INHERITS it: forge.Ensure is
// idempotent by name, so a create finds the old repository, adopts its commits
// and publishes them. Hence [forget].
//
// # The GitHub replica is not here
//
// A published project also has a real repository at github.com/hanzo-community,
// so its author has a link to hand out, and its visibility is kept in step
// there. That replica is a MIRROR — the outbound push of one repository's
// contents to another host — which is what the sync surface does for every other
// mirrored repository and where the one outbound target list lives. Whether it
// stays driven from here or becomes a push mirror the forge runs itself is that
// surface's call.
//
// What matters here is that nothing below depends on it. The flip is a write to
// the forge and a read back from the forge, so a replica that is absent, stale
// or failing can neither manufacture a success this seam did not have nor stop a
// project from being closed.

const (
	// Public is the default: the project appears in the community
	// catalogue and its source is readable at the forge.
	Public = "public"
	// Private hides a project from the catalogue at the publisher's own
	// request. Paid: see resolve.
	Private = "private"

	// privateKind is the metering unit for keeping a project private. It shares
	// the ONE cloud.ResourceMeter every other paid surface uses (hosting, agents,
	// functions, s3), so "must have a paid account" is the existing funded-org
	// gate rather than a second notion of entitlement that could disagree with
	// billing. Fee 0 (operator-configured) makes private free and un-gated.
	privateKind = "private"
)

// resolve resolves the visibility a create/update request asks for, and
// enforces the ONE rule: public is free, private requires a funded org.
//
// An empty request means public — a caller that says nothing gets the default
// the platform wants, not an error. An unfunded org asking for private is
// REFUSED (402), never silently published as public: quietly making somebody's
// private project public is the one failure mode here that cannot be undone.
func resolve(s *cloud.Service[state], c *zip.Ctx, want string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "", Public:
		return Public, nil
	case Private:
		fee := cloud.ResourceFeeCents(deployFeeEnvPrefix, privateKind)
		project, validated := principal.ValidatedProject(c)
		if err := s.State.bill.Gate(c.Context(), principal.Ledger(c), project, validated, privateKind, fee); err != nil {
			return "", err
		}
		return Private, nil
	default:
		return "", zip.Errorf(http.StatusBadRequest,
			"visibility must be %q or %q", Public, Private)
	}
}

// listed reports whether a project belongs in the public community catalogue:
// the publisher chose public AND moderation has not removed it. Both halves are
// plain values on the row, so this is the whole rule and there is nowhere else
// for a second copy of it to drift.
func (p Project) listed() bool {
	return p.Visibility == Public && !p.Hidden
}

// ── where a published project lives ──────────────────────────────────────────

// community is the forge namespace a published project's source lives in.
//
// It is NOT the estate's own namespace, and that is the whole tenancy argument.
// forge.Owner maps this deployment's tenant to `hanzoai`, which holds the
// estate's 250 real repositories — cloud, iam, kms — and the one thing this seam
// does is set a repository's visibility from a tenant-chosen slug. Published
// there, a project named `cloud` would address `hanzoai/cloud`, and naming your
// project after ours would open our source or close it. A namespace that holds
// nothing but published projects has nothing to take.
//
// It is the same org the GitHub replica publishes to, deliberately: one name for
// the community on both hosts, so a replica is a copy rather than a translation.
const community = "hanzo-community"

// repoName is the repository one project publishes to, and false for a project
// that cannot have one.
//
// The tenant is part of the NAME because the namespace is flat, and the
// separator is `_` because NEITHER HALF MAY CONTAIN ONE: [plain] is the whole
// alphabet either half is allowed and `_` is not in it. A `-` would not have
// that property — org `a` slug `b-c` and org `a-b` slug `c` both spell `a-b-c`,
// and the second tenant would be publishing, and un-publishing, the first
// tenant's source. That is the same collision the GitHub replica's `<org>-<slug>`
// carries, and it is not carried here.
//
// So the name is INVERTIBLE, which is what lets the audit read a repository back
// to the one project that can have minted it — see [parts].
//
// A name outside that alphabet is REFUSED rather than escaped or coerced, and
// the org arrives here VERBATIM as the validated IAM owner (principal.Org): an
// owner spelled with a capital, a dot or an underscore is a project with NO
// repository at all. That is the safe half of the failure — nothing is
// published, so nothing is exposed — and it is said out loud where it happens
// (see [enqueue]) rather than discovered as a missing repository.
func repoName(org, slug string) (string, bool) {
	if !plain(org) || !plain(slug) {
		return "", false
	}
	return org + "_" + slug, true
}

// parts is the inverse of [repoName]: the project a repository name belongs to,
// and false for a name this seam cannot have minted.
//
// The audit needs the direction that starts at the forge (see [sweep]), and it
// only has one because the separator is not in either half's alphabet: the cut
// is unambiguous, so a name either reads back as exactly one (org, slug) or was
// not published from a row here at all.
func parts(name string) (org, slug string, ok bool) {
	org, slug, ok = strings.Cut(name, "_")
	if !ok || !plain(org) || !plain(slug) {
		return "", "", false
	}
	return org, slug, true
}

// plain reports whether s is a bare lowercase identifier — the alphabet both an
// org slug and a project slug are minted in.
func plain(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// ── the derivation ───────────────────────────────────────────────────────────

// want is what a project's repository must BE. It has three values because the
// row has three states: a project that is listed, one that is not, and one that
// is not there at all.
//
// It is the whole vocabulary of this seam. Every path below reads a row into one
// of these ([wanted]) and applies it ([apply]); the paths differ only in which
// of the three they are allowed to reach.
type want int

const (
	shut want = iota // closed: readable only by someone on the repository
	open             // world-readable
	gone             // not there at all
)

// wanted reads one project's row and says what its repository must be.
//
// The row is READ HERE rather than carried in from the write, and that is what
// makes the ordering safe: two writes to one project run one at a time (see
// [queue]) and each reads the row it finds, so the last write always wins and a
// slow first reconcile cannot land after — and undo — a second one. The audit
// reads it for the same reason: a project that goes public while the audit is
// walking must not be closed by a decision taken before it did.
func wanted(s *cloud.Service[state], ctx context.Context, org, slug string) (want, Project, error) {
	p, err := s.State.store.GetProject(ctx, org, slug)
	switch {
	case errors.Is(err, errNotFound):
		return gone, Project{}, nil
	case err != nil:
		return shut, Project{}, err
	case p.listed():
		return open, p, nil
	}
	return shut, p, nil
}

// apply moves one repository to w, AS THE MACHINE.
//
// It acts as the machine because the visibility of a published project is the
// platform's to enforce, not the requesting user's to be asked about: a
// moderation is made by an admin who has no forge account, and an un-share must
// land whether or not the publisher can still write anything on the forge.
// Sudoing as whoever happened to make the write would make an un-share fail for
// exactly the people whose access has been taken away.
func apply(ctx context.Context, m *forge.Client, name string, w want, p Project) error {
	switch w {
	case gone:
		// A project's source does not outlive the project, and DELETING it rather
		// than closing it is what makes a reclaimed slug safe: forge.Ensure is
		// idempotent BY NAME, so a repository left behind is one the next project of
		// that name adopts — commits and all — and then publishes.
		//
		// CLOSED FIRST and then deleted, because the two cover different failures: a
		// forge that refuses the delete has still been told to close it, and open is
		// the half that leaks.
		if _, err := retract(ctx, m, name); err != nil {
			return err
		}
		return m.Delete(ctx, community, name)

	case open:
		// The repository is ENSURED, not created on a first event we would have to
		// recognise. It is born private and protected (forge.Ensure), so a project
		// that is created private is never briefly public, and a project that goes
		// public later is a flag flip rather than a migration.
		//
		// Name and description seed it at creation only — forge.Ensure sends them on
		// the create and never on the read — so an author who edits their
		// repository's description keeps their edit. Visibility is ours; their prose
		// is not.
		if _, err := m.Ensure(ctx, community, name, p.Description); err != nil {
			return err
		}
		return m.SetPublic(ctx, community, name, true)
	}
	// shut. The close goes FIRST and alone, gated on nothing — see [retract].
	there, err := retract(ctx, m, name)
	if err != nil || there {
		return err
	}
	// It is not there at all, which is already closed. A private project still
	// gets its source, born closed, so choosing private is not choosing a project
	// with nowhere to push.
	_, err = m.Ensure(ctx, community, name, p.Description)
	return err
}

// retract closes one repository, and reports whether it was there at all.
//
// It is the retraction spelled ONCE — the direction that leaks when it is
// missed, and the one both the derivation and the audit make.
//
// It is gated on NOTHING. Closing needs neither the repository to exist (one
// that is not there is already closed) nor this machine to be able to write its
// contents, so it does not go through forge.Ensure — which refuses an ARCHIVED
// repository, and would therefore make an archived repository the one kind this
// seam could never close.
func retract(ctx context.Context, m *forge.Client, name string) (bool, error) {
	err := m.SetPublic(ctx, community, name, false)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, forge.ErrNotFound):
		return false, nil
	}
	return false, err
}

// machine is the forge, as the DEPLOYMENT itself. Resolved per attempt rather
// than held, because the credential rotates (forge.Source) and a reconcile that
// captured one would keep replaying a revoked token.
func machine(s *cloud.Service[state], ctx context.Context) (*forge.Client, error) {
	cl, err := s.State.forge.Client(ctx, s.KMS, s.Domain)
	if err != nil {
		return nil, err
	}
	return cl.Machine(), nil
}

// ── the three steps ──────────────────────────────────────────────────────────

// step is one attempt at bringing one project's repository into line: the shape
// [queue] runs one at a time and [settle] retries. There are three, and they
// differ only in which of the three [want]s they may reach.
type step func(s *cloud.Service[state], ctx context.Context, org, slug, name string) error

// reconcile applies one project's row to its repository, once. It is the write
// path's step and the only one that may OPEN anything.
func reconcile(s *cloud.Service[state], ctx context.Context, org, slug, name string) error {
	m, err := machine(s, ctx)
	if err != nil {
		return err
	}
	w, p, err := wanted(s, ctx, org, slug)
	if err != nil {
		return err
	}
	return apply(ctx, m, name, w, p)
}

// retire is the delete path's step: the project is gone, so its repository is.
//
// The deletion is UNCONDITIONAL rather than derived from the absent row, because
// a slug is free to reclaim the moment the row is gone — a retirement that read
// the row first would find the NEXT project's row and adopt the very repository
// it was sent to destroy. The row is read AFTER, so whoever holds the slug now
// gets a fresh repository, which is also what makes this safe to run twice.
func retire(s *cloud.Service[state], ctx context.Context, org, slug, name string) error {
	m, err := machine(s, ctx)
	if err != nil {
		return err
	}
	if err := apply(ctx, m, name, gone, Project{}); err != nil {
		return err
	}
	w, p, err := wanted(s, ctx, org, slug)
	if err != nil || w == gone {
		return err
	}
	return apply(ctx, m, name, w, p)
}

// vet is the audit's step, and it may only ever CLOSE.
//
// The row is re-read here, under the same queue a write goes through, so a
// project that went public while the audit was walking is left alone rather than
// closed from a snapshot taken before it did. And an absent row is CLOSED rather
// than retired: the audit holds an absence, not a deletion — a store that is
// empty, half-restored, or newly pointed at an old forge must be able to cost a
// namespace of closed repositories, never a namespace of deleted ones.
func vet(s *cloud.Service[state], ctx context.Context, org, slug, name string) error {
	m, err := machine(s, ctx)
	if err != nil {
		return err
	}
	w, _, err := wanted(s, ctx, org, slug)
	if err != nil {
		return err
	}
	if w == open {
		return nil // a live row permits it: not the audit's business
	}
	_, err = retract(ctx, m, name)
	return err
}

// ── running a step ───────────────────────────────────────────────────────────

// share brings a project's repository into step with its row, behind the write.
//
// It is FIRED ON EVERY CREATE AND UPDATE rather than on a transition, because
// the transition that gets missed is the one nobody noticed happening, and a
// missed one leaves a private project's source readable. The work is idempotent
// twice over — the repository is ensured rather than created, and the visibility
// is set rather than toggled — so a redundant firing costs a read.
//
// It does not run on the caller's request. The row is already committed and is
// what anyone reads; the forge is downstream of it, costs several round trips,
// and must not be able to fail, slow or cancel a project write. That last one is
// not a nicety: the request's context dies when the browser tab does, and a
// close that inherited it would be abandoned halfway by a publisher who clicked
// "private" and then closed the tab — which is precisely the state that must
// never be left behind.
func share(s *cloud.Service[state], ctx context.Context, p Project) {
	enqueue(s, ctx, p.Org, p.Slug, reconcile)
}

// forget takes a deleted project's source off the forge, BEFORE the delete
// answers.
//
// Inline, unlike every other step here, and for a reason the write path does not
// have: the slug is free to reclaim the moment this returns. A retirement still
// in flight when the next tenant creates the same slug is a repository that
// tenant's own reconcile ADOPTS, so the ordering has to be real rather than
// probable. It is one round trip on a path that already purges S3 and the edge
// inline, and it takes this project's place in the [queue] so it cannot
// interleave with a write that is already reconciling.
//
// It cannot FAIL the delete. The row is already gone, and refusing to delete a
// project because the forge is away would leave one nobody can address; so a
// failure is retried behind the answer, on a context the closing tab cannot
// cancel, and past that it is caught by [sweep] — which finds an open repository
// no row permits.
func forget(s *cloud.Service[state], ctx context.Context, p Project) {
	name, ok := repoName(p.Org, p.Slug)
	if !ok {
		return // it was never published: see repoName
	}
	ctx = context.WithoutCancel(ctx)
	var err error
	run := func() {
		one, cancel := context.WithTimeout(ctx, hurry)
		err = retire(s, one, p.Org, p.Slug, name)
		cancel()
	}
	held, more := s.State.queue.hold(p.Org+"/"+p.Slug, run)
	if !held {
		// Something else holds this project's place, and the retirement does NOT
		// wait for it. That run would carry the deletion — it re-reads the row and
		// finds none — but only until the slug is reclaimed, and then it finds the
		// NEW row and adopts the very repository it was meant to destroy. Running
		// beside it costs at worst a repository that run recreates EMPTY and
		// unlisted, which the audit closes; yielding costs the deleted project's
		// commits, republished under somebody else's project.
		run()
	}
	if err == nil && !more {
		return // done, before this answered
	}
	if err != nil {
		s.Log.Warn("a deleted project's source may still be on the forge; retrying",
			"org", p.Org, "slug", p.Slug, "repo", community+"/"+name, "err", err)
	}
	enqueue(s, ctx, p.Org, p.Slug, retire)
}

// enqueue runs one step for one project: one at a time per project, retried, and
// on a context the caller's request cannot cancel.
func enqueue(s *cloud.Service[state], ctx context.Context, org, slug string, do step) {
	name, ok := repoName(org, slug)
	if !ok {
		// Said once, here, rather than discovered five attempts later: a name this
		// seam cannot spell is a permanent condition, not a failure to retry. The
		// project keeps working and publishes nothing, which is the safe half.
		s.Log.Warn("project cannot be published: its name is not spellable as a repository",
			"org", org, "slug", slug)
		return
	}
	ctx = context.WithoutCancel(ctx)
	s.State.queue.add(org+"/"+slug, func() { settle(s, ctx, org, slug, name, do) })
}

// tries is how long a step keeps trying before it gives up and says so.
//
// It exists for the closing direction. Opening late is invisible; closing late
// is a leak, so a forge that is redeploying, rate-limiting or briefly unreachable
// must not be able to turn one refused request into a repository that stays open
// until somebody edits the project again — which, for a project its author just
// made private, may be never.
//
// Each attempt re-reads the row, so a retry applies whatever is true when it
// runs and never resurrects the state it started with.
var tries = []time.Duration{0, 2 * time.Second, 8 * time.Second, 30 * time.Second, 90 * time.Second}

const (
	// budget bounds ONE attempt. The forge client bounds a single request at 30s
	// and an attempt makes three or four, so without this a wedged forge would
	// hold a step — and the project's place in the queue — for minutes.
	budget = 45 * time.Second
	// hurry bounds the one attempt the delete path makes INLINE, and is shorter
	// because somebody is waiting for the answer: a slow forge must cost the
	// retirement its ordering guarantee, not the delete its response. It does not
	// cost the retirement itself — what runs out of time here is handed to the
	// retry behind the answer, and to the audit behind that.
	hurry = 10 * time.Second
)

// settle runs one project's step to completion, retrying while it fails.
//
// A step that never lands is reported at ERROR naming the repository, because at
// that point the deployment holds a repository whose visibility disagrees with
// the row, and the only two things that will fix it are the next write to that
// project and [sweep].
func settle(s *cloud.Service[state], ctx context.Context, org, slug, name string, do step) {
	var err error
	for _, wait := range tries {
		if wait > 0 {
			time.Sleep(wait)
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		err = do(s, attempt, org, slug, name)
		cancel()
		if err == nil {
			return
		}
	}
	s.Log.Error("a project's source never reached the forge",
		"org", org, "slug", slug, "repo", community+"/"+name, "err", err)
}

// ── ordering ─────────────────────────────────────────────────────────────────

// queue runs one step at a time per project, and collapses the rest.
//
// Two writes to one project must not reach the forge out of order: the second is
// the one that is true, and the first landing after it would set the visibility
// the publisher just changed away from. Running them one at a time is the whole
// of that guarantee, and because each step re-reads the row, a write that
// arrives while one is running does not need a place in a queue — it only needs
// to make sure another run happens after this one. So the depth is one, and a
// burst of writes to one project costs two runs rather than a burst of them
// against our own forge.
//
// Collapsing is safe because every step CONVERGES: a run that was queued as a
// reconcile and re-run as a retirement, or the other way round, reads the row it
// finds and reaches the same place as the run it replaced.
type queue struct {
	mu   sync.Mutex
	work map[string]slot
}

// slot is what one project's place in the queue means.
type slot int

const (
	idle    slot = iota // no run: not in the map at all
	running             // one is running
	again               // one is running and the row changed under it
)

// add starts a run for one project, or arranges for one to follow the run
// already in flight.
func (q *queue) add(key string, run func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.work == nil {
		q.work = map[string]slot{}
	}
	if q.work[key] != idle {
		// Something is already running for this project. It may already have read
		// the row this write changed, so a further run has to happen — but only one,
		// because that one will read whatever is current when it starts.
		q.work[key] = again
		return
	}
	q.work[key] = running
	go q.drain(key, run)
}

// hold takes this project's place in the queue and runs `run` HERE, reporting
// whether it could — and, when it could, whether a write landed under it that
// still wants a run of its own.
//
// It is [queue.add] for the one caller that cannot be behind its own work: the
// delete path frees the slug as it answers, so its retirement has to have
// HAPPENED by then rather than be scheduled (see [forget]). Everything the queue
// is for still holds — the run is exclusive for that project — and what it
// deliberately does not do is start the follow-up itself: `run` belongs to the
// caller's goroutine and nothing here may still be executing it after this
// returns. The caller queues the follow-up, as its own work.
func (q *queue) hold(key string, run func()) (held, more bool) {
	q.mu.Lock()
	if q.work == nil {
		q.work = map[string]slot{}
	}
	if q.work[key] != idle {
		q.mu.Unlock()
		return false, false
	}
	q.work[key] = running
	q.mu.Unlock()

	run()

	q.mu.Lock()
	defer q.mu.Unlock()
	more = q.work[key] == again
	delete(q.work, key)
	return true, more
}

// drain runs until nothing more is wanted for this project.
func (q *queue) drain(key string, run func()) {
	for {
		run()
		q.mu.Lock()
		if q.work[key] == again {
			q.work[key] = running
			q.mu.Unlock()
			continue
		}
		delete(q.work, key)
		q.mu.Unlock()
		return
	}
}

// ── the audit ────────────────────────────────────────────────────────────────

// sweep closes every repository in the community namespace that no live project
// permits to be open.
//
// It is driven FROM THE FORGE and not from the rows, and that inversion is the
// whole of what it can find. Asked the other way round — for each row that must
// be closed, is its repository open? — a DELETED project is structurally
// invisible: there is no row to ask about, so the one repository nobody will
// ever write again is the one nothing checks. Asked this way the question is
// "who says this may be open?", and an absent row is not an answer.
//
// It can only ever CLOSE. Every repository it acts on goes through [vet], which
// re-reads the row and refuses to touch one a live row permits — so an audit
// running while a publisher goes public cannot undo them, and a store that is
// empty or half-restored costs closed repositories rather than deleted ones.
//
// One list of the namespace, and in the steady state no writes at all: a
// repository that is already closed is skipped without a second thought, and an
// open one costs one local row read to find it is a public project.
func sweep(s *cloud.Service[state], ctx context.Context) error {
	m, err := machine(s, ctx)
	if err != nil {
		return err
	}
	// The forge's answer NOW, not through its read cache: a list minutes old
	// would have the audit act on a repository that has since changed, and skip
	// one that has since been opened.
	repos, err := m.Inventory(ctx, community)
	if err != nil {
		return err
	}
	readable := 0
	for _, r := range repos {
		if r.Private {
			continue // already closed: the steady state, and it costs nothing
		}
		readable++
		org, slug, ok := parts(r.Name)
		if !ok {
			// A name this seam cannot have minted, so no row can be permitting it:
			// [repoName] is the only way a repository reaches this namespace.
			s.Log.Error("visibility audit: an open repository nothing here published",
				"repo", community+"/"+r.Name)
			if _, err := retract(ctx, m, r.Name); err != nil {
				s.Log.Error("visibility audit: could not close it", "repo", community+"/"+r.Name, "err", err)
			}
			continue
		}
		// The same read [vet] makes again under the queue, and it is here only to
		// keep the steady state free: every public project is an open repository,
		// and queueing a check for each of them would be a burst of runs to learn
		// what one local read already says. The read that DECIDES is vet's.
		w, _, err := wanted(s, ctx, org, slug)
		if err != nil {
			s.Log.Warn("visibility audit: cannot read a project", "org", org, "slug", slug, "err", err)
			continue
		}
		if w == open {
			continue
		}
		s.Log.Error("visibility audit: an open repository no live project permits",
			"org", org, "slug", slug, "repo", community+"/"+r.Name)
		enqueue(s, ctx, org, slug, vet)
	}
	s.Log.Info("visibility audit", "repos", len(repos), "readable", readable)
	return nil
}

const (
	// period is how often the audit runs once it has landed.
	//
	// It repeats rather than running once at boot because boot is the LEAST
	// reliable moment this process has — the forge may not be reachable yet and
	// the credential may not be readable yet — and this is the only thing that
	// recovers a close nothing else noticed missing. A pod that stays up for a
	// month would otherwise have audited its repositories exactly once, in its
	// worst minute.
	period = time.Hour
	// pause is the first wait after an audit that could not run at all. It doubles
	// up to [period], so a forge that is away at boot costs a handful of log lines
	// rather than a spin.
	pause = 5 * time.Second
)

// audit keeps [sweep] running: it retries until it lands once, and then repeats.
func audit(s *cloud.Service[state], ctx context.Context) {
	if s.KMS == nil {
		// No credential can ever be read here, so there is nothing to retry toward.
		// Said once: a permanent condition is not a failure.
		s.Log.Info("no KMS: the visibility audit is not running")
		return
	}
	wait := pause
	for {
		if err := sweep(s, ctx); err != nil {
			s.Log.Warn("visibility audit did not run; retrying", "in", wait, "err", err)
		} else {
			wait = period
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < period {
			if wait *= 2; wait > period {
				wait = period
			}
		}
	}
}
