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
// derived from it: world-readable exactly when the row says listed. The
// derivation runs on every create and every update rather than on a transition,
// because a transition this side failed to notice would leave a private
// project's source readable — see [share].
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
// separator is `_` because neither half can contain one: an IAM org slug and a
// project slug are both [a-z0-9-] (account.slugifyOrg, slugRE). A `-` would not
// have that property — org `a` slug `b-c` and org `a-b` slug `c` both spell
// `a-b-c`, and the second tenant would be publishing, and un-publishing, the
// first tenant's source. That is the same collision the GitHub replica's
// `<org>-<slug>` carries, and it is not carried here.
//
// A name outside that alphabet is REFUSED rather than escaped or coerced. An org
// this seam cannot spell unambiguously gets no repository at all, which is the
// safe half of the failure: nothing is published, so nothing is exposed.
func repoName(org, slug string) (string, bool) {
	if !plain(org) || !plain(slug) {
		return "", false
	}
	return org + "_" + slug, true
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

// share brings a project's repository on the forge into step with its row:
// present, and world-readable exactly when the project is listed.
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
	org, slug := p.Org, p.Slug
	name, ok := repoName(org, slug)
	if !ok {
		// Said once, here, rather than discovered five attempts later: a name this
		// seam cannot spell is a permanent condition, not a failure to retry. The
		// project keeps working and publishes nothing, which is the safe half.
		s.Log.Warn("project cannot be published: its name is not spellable as a repository",
			"org", org, "slug", slug)
		return
	}
	s.State.queue.add(org+"/"+slug, func() {
		settle(s, context.WithoutCancel(ctx), org, slug, name)
	})
}

// tries is how long a reconcile keeps trying before it gives up and says so.
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

// budget bounds ONE attempt. The forge client bounds a single request at 30s and
// an attempt makes three or four, so without this a wedged forge would hold a
// reconcile — and the project's place in the queue — for minutes.
const budget = 45 * time.Second

// settle runs one project's reconcile to completion, retrying while it fails.
//
// A reconcile that never lands is reported at ERROR naming the repository,
// because at that point the deployment holds a repository whose visibility
// disagrees with the row, and the only two things that will fix it are the next
// write to that project and the boot-time [sweep].
func settle(s *cloud.Service[state], ctx context.Context, org, slug, name string) {
	var err error
	for _, wait := range tries {
		if wait > 0 {
			time.Sleep(wait)
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		err = reconcile(s, attempt, org, slug, name)
		cancel()
		if err == nil {
			return
		}
	}
	s.Log.Error("project visibility never reached the forge",
		"org", org, "slug", slug, "repo", community+"/"+name, "err", err)
}

// reconcile applies one project's row to its repository, once.
//
// The row is READ HERE rather than carried in from the write, and that is what
// makes the ordering safe: two writes to one project run one at a time (see
// [queue]) and each reads the row it finds, so the last write always wins and a
// slow first reconcile cannot land after — and undo — a second one.
//
// It acts as the MACHINE. The visibility of a published project is the
// platform's to enforce, not the requesting user's to be asked about: a
// moderation is made by an admin who has no forge account, and an un-share must
// land whether or not the publisher can still write anything on the forge.
// Sudoing as whoever happened to make the write would make an un-share fail for
// exactly the people whose access has been taken away.
func reconcile(s *cloud.Service[state], ctx context.Context, org, slug, name string) error {
	cl, err := s.State.forge.Client(ctx, s.KMS, s.Domain)
	if err != nil {
		return err
	}
	m := cl.Machine()

	p, err := s.State.store.GetProject(ctx, org, slug)
	switch {
	case errors.Is(err, errNotFound):
		// No row means nothing is published, so nothing may be readable. A
		// repository that is not there is already closed — the delete path leaves
		// the source behind on purpose (a reclaimed slug inherits it), and this
		// only makes sure it is not left open.
		if err := m.SetPublic(ctx, community, name, false); err != nil && !errors.Is(err, forge.ErrNotFound) {
			return err
		}
		return nil
	case err != nil:
		return err
	}
	// The repository is ENSURED on every pass, not created on a first event we
	// would have to recognise. It is born private and protected (forge.Ensure), so
	// a project that is created private is never briefly public, and a project that
	// goes public later is a flag flip rather than a migration.
	//
	// Name and description seed it at creation only — forge.Ensure sends them on
	// the create and never on the read — so an author who edits their repository's
	// description keeps their edit. Visibility is ours; their prose is not.
	if _, err := m.Ensure(ctx, community, name, p.Description); err != nil {
		return err
	}
	return m.SetPublic(ctx, community, name, p.listed())
}

// ── ordering ─────────────────────────────────────────────────────────────────

// queue runs one reconcile at a time per project, and collapses the rest.
//
// Two writes to one project must not reach the forge out of order: the second is
// the one that is true, and the first landing after it would set the visibility
// the publisher just changed away from. Running them one at a time is the whole
// of that guarantee, and because each reconcile re-reads the row, a write that
// arrives while one is running does not need a place in a queue — it only needs
// to make sure another run happens after this one. So the depth is one, and a
// burst of writes to one project costs two reconciles rather than a burst of
// them against our own forge.
type queue struct {
	mu   sync.Mutex
	work map[string]slot
}

// slot is what one project's place in the queue means.
type slot int

const (
	idle    slot = iota // no reconcile: not in the map at all
	running             // one is running
	again               // one is running and the row changed under it
)

// add starts a reconcile for one project, or arranges for one to follow the run
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

// sweep closes, once at startup, any repository that is open and must not be.
//
// It is here because the derivation above is best-effort and this process can
// die between a project going private and the forge being told. Every other
// failure in this file costs a repository that is closed when it could be open,
// and the next write fixes it. That one costs a private project whose source
// anyone can read, and nothing fixes it — the publisher went private precisely
// because they have stopped touching the project.
//
// It reads only the rows that MUST be closed (private, or moderated), so in the
// steady state it is one read per private project at boot and no writes at all.
// A repository that is open when its row says it must not be is closed, and said
// out loud, because it was readable for however long this process was away.
func sweep(s *cloud.Service[state], ctx context.Context) {
	rows, err := s.State.store.Unlisted(ctx)
	if err != nil {
		s.Log.Warn("visibility audit: cannot read the unlisted projects", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	cl, err := s.State.forge.Client(ctx, s.KMS, s.Domain)
	if err != nil {
		s.Log.Warn("visibility audit: no forge", "projects", len(rows), "err", err)
		return
	}
	m := cl.Machine()
	closed := 0
	for _, p := range rows {
		name, ok := repoName(p.Org, p.Slug)
		if !ok {
			continue
		}
		got, err := m.Repo(ctx, community, name)
		switch {
		case errors.Is(err, forge.ErrNotFound):
			continue // never published: nothing to close
		case err != nil:
			s.Log.Warn("visibility audit: cannot read a repository", "repo", name, "err", err)
			continue
		case got.Private:
			continue
		}
		s.Log.Error("visibility audit: an unlisted project was world-readable",
			"org", p.Org, "slug", p.Slug, "repo", community+"/"+name)
		if err := m.SetPublic(ctx, community, name, false); err != nil {
			s.Log.Error("visibility audit: could not close it", "repo", name, "err", err)
			continue
		}
		closed++
	}
	if closed > 0 {
		s.Log.Info("visibility audit closed repositories left open", "count", closed)
	}
}
