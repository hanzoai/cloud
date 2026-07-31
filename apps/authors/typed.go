package authors

// typed.go is the /v1/authors + /v1/admin/authors surface as TYPED ops — every one
// of the eleven.
//
// A typed op is ONE registry entry with N projections: the REST route, the OpenAPI
// operation's schema AND prose, the MCP tool an agent calls, the CLI command and
// every generated SDK method all follow from the same declaration. An untyped route
// gets a route and nothing else, which is what this surface was.
//
// Three wire facts had to be carried over deliberately rather than inherited, and
// one schema is honestly under-specified:
//
//   - the CONDITIONAL 200/201 on connect, verify and record-deploy. Each answers 201
//     when it CREATED the row and 200 when it found one — one address, two success
//     codes — which zip cannot declare (WithStatus takes one). cloud.Created sets the
//     status the route has always sent, so the WIRE is exact; the published document
//     keys its response on 200 and the prose says when 201 comes instead. That is a
//     zip gap, tracked as "multi-status responses", not a cloud workaround.
//   - the SuperAdmin gate on the six /v1/admin ops. It reads X-User-IsAdmin, a header
//     only the identity boundary can mint and one principal.OrgFrom does not carry,
//     so those ops reach the REQUEST through cloud.Request rather than the tenant.
//   - the cloud.OK envelope — {"status","msg","data"} — that every admin route
//     answers. It is spelled out per op rather than made generic, because a generic
//     envelope publishes a schema named for its type parameter and no SDK reads that.
//
// The two READS of the money-audit payload (GET /v1/authors, GET /v1/authors/basis,
// and the admin mirror's data) answer a JSON OBJECT whose KEYS DEPEND on the answer:
// an org that has not enrolled gets a short "not enrolled" shape, an enrolled one
// gets the dashboard or the full basis. One address, two shapes, and an op declares
// exactly one Out. Their Out is therefore a NAMED map — `{"type":"object"}`, which
// is TRUE of every response they send and is the only true thing a single schema can
// say about them — and the doc comment states both shapes in full. The stronger fix
// is to split the address, which is an API decision, not a typing one.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.adminList), which is also
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op that takes nothing off the wire — it is addressed
// entirely by the caller's validated principal.
type noInput struct{}

// payload is a JSON OBJECT whose keys depend on the answer. It is the Out of the
// three reads that legitimately send two shapes from one address (see the package
// note): the schema says "an object", which is true of every response they send, and
// the op's doc comment names both shapes. It is a NAMED type because zip keys a
// response on 204 when the Out type has no name, and these answer 200 with a body.
type payload map[string]any

// caller is the request behind a typed op, for the ops that need a fact
// principal.OrgFrom does not carry. Fails closed off the HTTP path, where there is
// no request and therefore no attested caller.
func caller(ctx context.Context) (*zip.Ctx, bool) {
	return cloud.Request(ctx)
}

// requireAdmin admits only a platform SuperAdmin. Platform-ness lives in
// X-User-IsAdmin, a header only the identity boundary can mint and one
// principal.OrgFrom does not carry, so this is the ONE place this package reaches
// for the request rather than the tenant. Fails closed off the HTTP path.
func requireAdmin(ctx context.Context) error {
	c, ok := caller(ctx)
	if !ok || !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	return nil
}

// created marks the response 201 when the op CREATED the row it is answering with.
// These routes have always answered 201-on-create and 200-on-found from one address;
// zip declares one success status per op, so the code is set per request here and the
// prose states it. Tracked as a zip gap (multi-status responses), not a cloud policy.
func created(ctx context.Context, isNew bool) {
	if isNew {
		cloud.Created(ctx)
	}
}

// normPeriod validates the optional period narrowing against the ONE shape the
// accrual latch mints. The value is echoed back into the response AND used as a SQL
// filter, so anything else is refused rather than silently matching nothing.
func normPeriod(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p != "" && !periodShape.MatchString(p) {
		return "", zip.ErrBadRequest("period must be YYYY-MM")
	}
	return p, nil
}

// pendingMsg is the over-payment refusal, stated once so the manual payout and any
// future caller word it identically.
func pendingMsg(pending int64) string {
	return fmt.Sprintf("amount exceeds pending royalty (%d cents available)", pending)
}

// ----- customer surface -----------------------------------------------------

// MyAuthorProgram returns the caller's author-program dashboard: enrolment status,
// linked forge login, verified repositories and owner-wide claims, recorded deploys,
// accrued / pending / paid royalty, and the payout history.
//
// It answers ONE OF TWO SHAPES from this address. An org that has never connected
// gets {"isAuthor": false, "defaultShareBps", "badgeBase"} — an honest "not enrolled"
// rather than a 404, so the console can render the connect form. An enrolled org gets
// the dashboard: isAuthor, id, status, githubLogin, verified, verifyCode, verifyFile,
// verifySnippet, shareBps, badgeBase, repos, orgs, deploys, accruedCents,
// pendingCents, paidCents, payouts and ledger.
//
// For an APPROVED author this read ALSO runs the accrual sweep opportunistically, so
// the dashboard is self-updating. That is why the royalty AUDIT lives at its own
// address: an audit must not move the money it is auditing.
func (o ops) myAuthors(ctx context.Context, _ *noInput) (*payload, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your author program")
	}
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &payload{
			"isAuthor":        false,
			"defaultShareBps": defaultShareBps,
			"badgeBase":       o.s.State.badgeBase,
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}

	// Lazy accrual sweep for MY deploying orgs (bounded, best-effort — a commerce
	// hiccup never fails the page; it simply accrues on the next sweep).
	if a.Status == StatusApproved {
		if _, _, serr := sweepAuthor(o.s, ctx, a); serr != nil {
			o.s.Log.Warn("authors: lazy sweep failed", "author", a.ID, "err", serr)
		}
		if refreshed, rerr := o.s.State.store.GetByID(ctx, a.ID); rerr == nil {
			a = refreshed // pick up any accrual the lazy sweep just latched
		}
	}

	repos, err := o.s.State.store.ListRepos(ctx, a.ID, repoLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list repos: %v", err)
	}
	orgs, err := o.s.State.store.ListOrgs(ctx, a.ID, repoLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list orgs: %v", err)
	}
	deploys, err := o.s.State.store.ListDeploys(ctx, a.ID, deployLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deploys: %v", err)
	}
	payouts, err := o.s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}
	ledger, err := o.s.State.store.ListLedger(ctx, a.ID, "", ledgerLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list ledger: %v", err)
	}
	return &payload{
		"isAuthor":      true,
		"id":            a.ID,
		"status":        a.Status,
		"githubLogin":   a.GithubLogin,
		"verified":      a.VerifiedAt > 0,
		"verifyCode":    a.VerifyCode,
		"verifyFile":    verifyFile,
		"verifySnippet": verifySnippet(a.VerifyCode),
		"shareBps":      a.ShareBps,
		"badgeBase":     o.s.State.badgeBase,
		"repos":         authorRepos(repos, o.s.State.badgeBase),
		"orgs":          orgViews(orgs, o.s.State.badgeBase),
		"deploys":       deployViews(deploys),
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"payouts":       payoutViews(payouts),
		"ledger":        ledger,
	}, nil
}

// periodQuery narrows a royalty-basis read to one accrual period.
type periodQuery struct {
	// Period is the UTC accrual month, YYYY-MM. Empty means every period; any other
	// shape is refused with 400, because the period is echoed back and used as a SQL
	// filter and is only ever accepted in the one form the accrual latch mints.
	Period string `json:"period"`
}

// MyRoyaltyBasis returns the AUDIT TRAIL behind the caller's own royalty: every
// ledger row with the spend it was computed from, the share applied at the time, the
// platform's matching half, whether each row satisfies the formula, and the
// attribution edges that already existed when the row was written.
//
// It answers ONE OF TWO SHAPES. An org that has never connected gets
// {"isAuthor": false, "defaultShareBps"} — never a 404, which would answer "is this
// org an author?" for anyone who asked. An enrolled org gets the basis: isAuthor, id,
// status, asOf, shareBps, platformShareBps, defaultShareBps, shareSource, settlesTo,
// method (the formula, the rate card and the sizing), ledger, reconciliation, window,
// and period when one was requested.
//
// This read NEVER sweeps, and that is the point of it being a separate address from
// the dashboard: an audit must not move the money it is auditing, so calling it N
// times leaves the balances and the ledger byte-identical.
//
// Example: {"period": "2026-07"}
func (o ops) basis(ctx context.Context, in *periodQuery) (*payload, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your royalty basis")
	}
	period, err := normPeriod(in.Period)
	if err != nil {
		return nil, err
	}
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		// Honest, not a 404 — a 404 here would answer "is this org an author?".
		return &payload{"isAuthor": false, "defaultShareBps": defaultShareBps}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}
	out, err := basisOf(o.s, ctx, a, period)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	p := payload(out)
	return &p, nil
}

// connectRequest is the POST /v1/authors/connect body: the forge provider and an
// optional login used only when IAM has no linked account for that provider.
type connectRequest struct {
	// Provider is the forge to enrol with: github (the default) or gitlab.
	Provider string `json:"provider"`
	// GithubLogin is the account to link. Used only when IAM holds no linked
	// account for the provider — a linked account is stronger proof and always wins.
	GithubLogin string `json:"githubLogin"`
	// Login is the provider-neutral alias for GithubLogin, preferred when both are
	// sent.
	Login string `json:"login"`
}

// enrolment is an author's enrolment state and the proof material for the file
// verification method.
type enrolment struct {
	// ID is the author record's server-minted handle, "aut_"-prefixed.
	ID string `json:"id"`
	// Status is connected, approved or suspended. Only an approved author earns.
	Status string `json:"status"`
	// GithubLogin is the linked forge account.
	GithubLogin string `json:"githubLogin"`
	// Verified reports whether any repository or owner claim has been proven yet.
	Verified bool `json:"verified"`
	// VerifyCode is this author's stable proof token — the value a repository's
	// verify file must carry.
	VerifyCode string `json:"verifyCode"`
	// VerifyFile is the repo-root file the file method reads, on the default branch.
	VerifyFile string `json:"verifyFile"`
	// VerifySnippet is that file's exact contents, ready to commit.
	VerifySnippet string `json:"verifySnippet"`
	// ShareBps is this author's royalty share in basis points of the spend their
	// deployed work generates.
	ShareBps int64 `json:"shareBps"`
	// Created reports whether this call enrolled the org (201) or found an existing
	// enrolment (200).
	Created bool `json:"created"`
}

// ConnectAuthor enrols the caller's org in the author program at status "connected"
// and returns its enrolment, including the verify code the file method needs. It is
// IDEMPOTENT: a second call returns the same enrolment rather than a conflict.
//
// The forge login is taken from IAM's LINKED account for the provider when there is
// one — that is identity proof, not a claim — and only otherwise from the login in
// the body, which then has to be proven per repository. Connecting does not admit an
// org to earning: a platform reviewer approves that separately.
//
// Answers 201 when it enrolled the org and 200 when it found an existing enrolment.
//
// Example: {"provider": "github", "login": "octocat"}
func (o ops) connect(ctx context.Context, in *connectRequest) (*enrolment, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to connect GitHub")
	}
	c, ok := caller(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to connect GitHub")
	}
	userSub := strings.TrimSpace(c.User())
	provider := normalizeProvider(in.Provider)

	// Prefer IAM's linked forge identity for the provider (strong proof of the login).
	login := normalizeLogin(firstNonEmpty(in.Login, in.GithubLogin))
	identityVerified := false
	if l, _, linked, lerr := o.s.State.forge.linkedAccount(ctx, provider, org, userSub); lerr != nil {
		o.s.Log.Warn("authors: linked-account lookup failed", "org", org, "provider", provider, "err", lerr)
	} else if linked && l != "" {
		login = normalizeLogin(l)
		identityVerified = true
	}
	if login == "" {
		return nil, zip.ErrBadRequest("login is required (no linked " + provider + " account found)")
	}

	id, err := genID("aut")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	verifyCode, err := genID("avc")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	a, isNew, err := o.s.State.store.Connect(ctx, id, org, login, verifyCode, defaultShareBps, identityVerified, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "connect: %v", err)
	}
	created(ctx, isNew)
	return &enrolment{
		ID: a.ID, Status: a.Status, GithubLogin: a.GithubLogin, Verified: a.VerifiedAt > 0,
		VerifyCode: a.VerifyCode, VerifyFile: verifyFile, VerifySnippet: verifySnippet(a.VerifyCode),
		ShareBps: a.ShareBps, Created: isNew,
	}, nil
}

// verifyRequest is the POST /v1/authors/repos/verify body.
type verifyRequest struct {
	// RepoURL is what to claim: a repository (github.com/owner/name) or a whole
	// OWNER (github.com/owner, no repository segment). gitlab.com is accepted too.
	RepoURL string `json:"repoUrl"`
}

// claim is a proven ownership claim — exactly one of repo or org is present,
// depending on what was claimed.
type claim struct {
	// Repo is the verified repository claim, present when a repository was claimed.
	Repo *authorRepo `json:"repo,omitempty"`
	// Org is the verified owner-wide claim, present when an owner was claimed. It
	// covers every repository the author publishes under that owner.
	Org *orgView `json:"org,omitempty"`
	// Created reports whether this call recorded a new claim (201) or found an
	// existing one (200).
	Created bool `json:"created"`
}

// VerifyAuthorRepo proves that the caller owns a repository — or a whole OWNER — and
// records the claim, which is what makes deploys of that code earn royalty.
//
// Ownership is proven the SAME two ways in both cases, tried in order: an IAM-linked
// forge token with admin or push permission, or a hanzo.json on the default branch
// carrying the author's verify code. Claiming an OWNER proves it against that
// owner's ".github" control repository, and is exactly as strong as a per-repository
// claim — an owner the caller cannot prove is refused with 422, never assumed.
//
// A per-repository claim wins over an owner-wide one, so a specifically-claimed
// repository always earns for its own author. A repository another author has
// already verified is a 409. The org must have connected first.
//
// Answers 201 when it recorded a new claim and 200 when the claim already existed.
//
// Example: {"repoUrl": "github.com/octocat/hello-world"}
func (o ops) verifyRepo(ctx context.Context, in *verifyRequest) (*claim, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to verify a repo")
	}
	c, ok := caller(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to verify a repo")
	}
	target, perr := parseTarget(in.RepoURL)
	if perr != nil {
		return nil, zip.ErrBadRequest("repoUrl must be a GitHub or GitLab repo OR owner — github.com/owner or github.com/owner/name (gitlab.com too)")
	}
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return nil, zip.ErrBadRequest("connect a forge account before verifying a repo")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}
	if target.isOrg() {
		return o.verifyOwner(ctx, c, a, org, target)
	}

	host, owner, name := target.host, target.owner, target.name
	method, verified := proveOwnership(o.s, ctx, a, org, strings.TrimSpace(c.User()), host, owner, name)
	if !verified {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"could not verify ownership of %s — grant the Hanzo %s app OR add %s containing your verify code (%s) to the default branch",
			target.canonical, providerForHost(host), verifyFile, a.VerifyCode)
	}
	repoID, err := genID("arp")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	repo, isNew, err := o.s.State.store.UpsertVerifiedRepo(ctx, repoID, a.ID, target.canonical, method, time.Now().Unix())
	if err != nil {
		if err == errRepoOwned {
			return nil, zip.ErrConflict("that repo is already verified by another author")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "record repo: %v", err)
	}
	emitAudit(o.s, ctx, "author.verify_repo", a, map[string]any{"repoUrl": target.canonical, "method": method})
	created(ctx, isNew)
	v := authorRepoOf(repo, o.s.State.badgeBase)
	return &claim{Repo: &v, Created: isNew}, nil
}

// verifyOwner proves an OWNER-WIDE claim and records it. Ownership of the whole owner
// is proven the SAME two ways as a repo — reusing proveOwnership against the owner's
// canonical control repo "<owner>/.github" (OAuth admin/push on it, OR a hanzo.json
// with the verify code on its default branch). The ownership check is NOT weakened:
// an owner the caller can't prove is 422, exactly like an unprovable repo.
func (o ops) verifyOwner(ctx context.Context, c *zip.Ctx, a Author, org string, t verifyTarget) (*claim, error) {
	method, verified := proveOwnership(o.s, ctx, a, org, strings.TrimSpace(c.User()), t.host, t.owner, orgProofRepo)
	if !verified {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"could not verify ownership of the %s owner %q — grant the Hanzo %s app admin on %s/%s OR add %s carrying your verify code (%s) to its default branch",
			providerForHost(t.host), t.owner, providerForHost(t.host), t.owner, orgProofRepo, verifyFile, a.VerifyCode)
	}
	orgID, err := genID("aog")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	rec, isNew, err := o.s.State.store.UpsertVerifiedOrg(ctx, orgID, a.ID, t.canonical, method, time.Now().Unix())
	if err != nil {
		if err == errOrgOwned {
			return nil, zip.ErrConflict("that owner is already verified by another author")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "record org: %v", err)
	}
	emitAudit(o.s, ctx, "author.verify_org", a, map[string]any{"ownerUrl": t.canonical, "method": method})
	created(ctx, isNew)
	v := orgViewOf(rec, o.s.State.badgeBase)
	return &claim{Org: &v, Created: isNew}, nil
}

// deployRequest is the POST /v1/authors/deploys/record body: the source repository a
// project was built from and the project id. The deploying org is the caller.
type deployRequest struct {
	// RepoURL is the source repository the project was built from. Empty means a
	// hand-built project with nothing to attribute — an honest no-op, not an error.
	RepoURL string `json:"repoUrl"`
	// Project is the deployed project's id. Required.
	Project string `json:"project"`
}

// deployRecord is the outcome of a deploy attribution.
type deployRecord struct {
	// Recorded reports whether the deploy was attributed to an author at all. False
	// is the ordinary answer for a project built from no repository, or from one no
	// author has verified — never an error, so a deploy path can fire this
	// unconditionally.
	Recorded bool `json:"recorded"`
	// Reason says why nothing was attributed. Present only when recorded is false.
	Reason string `json:"reason,omitempty"`
	// Created reports whether this call recorded a new attribution edge (201) or
	// found an existing one (200). Absent when nothing was recorded.
	Created *bool `json:"created,omitempty"`
	// Self reports that the deploying org IS the author's org. Such a deploy is
	// recorded for provenance but excluded from accrual. Absent when nothing was
	// recorded.
	Self *bool `json:"self,omitempty"`
	// DeployID is the attribution edge's handle. Absent when nothing was recorded.
	DeployID string `json:"deployId,omitempty"`
	// CreatedAt is when the edge was first recorded, in unix seconds. Absent when
	// nothing was recorded.
	CreatedAt *int64 `json:"createdAt,omitempty"`
}

// RecordAuthorDeploy records that the caller's org deployed a project built from a
// source repository, which is the edge that makes an author's work earn royalty.
//
// It is deliberately NOT an error for a deploy to attribute to nobody: a project
// built from no repository, or from one no author has verified, answers
// {"recorded": false, "reason"} so a deploy pipeline can fire this on every deploy
// without branching. Attribution resolves per-repository first, then owner-wide, so a
// repository with its own claim always earns for its own author.
//
// A deploy of a Hanzo-maintained template attributes to the platform treasury, and a
// self-deploy (the author's own org deploying its own repository) is recorded for
// provenance but excluded from accrual. The edge is idempotent per
// repository+project+org.
//
// Answers 201 when it recorded a new edge and 200 otherwise.
//
// Example: {"repoUrl": "github.com/octocat/hello-world", "project": "prj_1f…"}
func (o ops) recordDeploy(ctx context.Context, in *deployRequest) (*deployRecord, error) {
	deployingOrg, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to record a deploy")
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		return nil, zip.ErrBadRequest("project is required")
	}
	repoURL := normalizeRepo(in.RepoURL)
	if repoURL == "" {
		// No source repo → nothing to attribute (a hand-built project). Honest no-op.
		return &deployRecord{Recorded: false, Reason: "no source repo"}, nil
	}

	// Hanzo-maintained template (owner ∈ this brand's GitHub orgs, e.g. hanzoai /
	// hanzo-*)? Attribute it to the treasury SYSTEM author so its creator royalty
	// accrues to the Hanzo treasury ("pay ourselves") — idempotent, no human verify
	// step. A repo a real external author already verified is left theirs (first-verify
	// wins, enforced inside ensureMaintainedRepo).
	if isMaintainedRepo(repoURL, o.s.State.maintainerOrg) {
		ensureMaintainedRepo(o.s, ctx, repoURL, time.Now().Unix())
	}

	// Attribution resolves in ONE of two arms — per-repo first, then owner-wide.
	authorID, err := resolveDeployAuthor(o.s, ctx, repoURL)
	if err == errUnknownRepo || err == errRepoNotVerified {
		return &deployRecord{Recorded: false, Reason: "repo is not a verified author repo"}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve repo: %v", err)
	}
	id, err := genID("ade")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	edge, isNew, err := o.s.State.store.RecordDeploy(ctx, id, authorID, repoURL, project, deployingOrg, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record deploy: %v", err)
	}
	author, _ := o.s.State.store.GetByID(ctx, authorID)
	self := author.Org == deployingOrg
	if isNew {
		emitAudit(o.s, ctx, "author.deploy", author, map[string]any{
			"repoUrl": repoURL, "project": project, "deployingOrg": deployingOrg, "self": self,
		})
	}
	created(ctx, isNew)
	at := edge.CreatedAt
	return &deployRecord{
		Recorded: true, Created: &isNew, Self: &self, DeployID: edge.ID, CreatedAt: &at,
	}, nil
}

// ----- admin surface (SuperAdmin, fail-closed) ------------------------------

// adminBook is the whole author program: every author with the org exposed, plus a
// fleet roll-up. Wrapped in the operator console's {"status","msg","data"} envelope,
// which every /v1/admin route answers.
type adminBook struct {
	// Status is "ok" — the operator console's envelope discriminator.
	Status string `json:"status"`
	// Msg is the envelope's message slot, empty on success.
	Msg string `json:"msg"`
	// Data is the book.
	Data adminBookData `json:"data"`
}

type adminBookData struct {
	// Authors are the author records, with each one's repository and deploy counts.
	Authors []adminAuthorView `json:"authors"`
	// Summary is the fleet roll-up: how many authors at each status and the money
	// accrued, pending and paid across all of them.
	Summary authorProgramSummary `json:"summary"`
}

// adminLimit pages an admin listing.
type adminLimit struct {
	// Limit bounds the page. 0 or less means the default of 500; anything above
	// 1000 is clamped to 1000.
	Limit int `json:"limit"`
}

// ListAuthors returns the platform's whole author program — every org's author
// record, not the caller's — with each one's repository and deploy counts and a
// fleet roll-up of the money accrued, pending and paid.
//
// It is a Hanzo platform operation: a caller who is not a SuperAdmin gets 403. It
// exposes the owning org of each author, which no tenant-facing read ever does.
func (o ops) adminList(ctx context.Context, in *adminLimit) (*adminBook, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListAll(ctx, clampAdminLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list authors: %v", err)
	}
	repoCounts, err := o.s.State.store.RepoCountsByAuthor(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count repos: %v", err)
	}
	deployCounts, err := o.s.State.store.DeployCountsByAuthor(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count deploys: %v", err)
	}
	views := make([]adminAuthorView, 0, len(rows))
	sum := authorProgramSummary{}
	for _, a := range rows {
		sum.add(a)
		views = append(views, adminViewOf(a, repoCounts[a.ID], deployCounts[a.ID]))
	}
	return &adminBook{Status: "ok", Data: adminBookData{Authors: views, Summary: sum}}, nil
}

// clampAdminLimit bounds an admin page exactly as adminLimitOf did off the query
// string: absent or non-positive means the default, and the maximum is a ceiling.
func clampAdminLimit(n int) int {
	if n <= 0 {
		return listLimit
	}
	if n > maxAdminLimit {
		return maxAdminLimit
	}
	return n
}

// authorSweepResult is one accrual sweep's outcome, in the operator envelope.
type authorSweepResult struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is the envelope's message slot, empty on success.
	Msg string `json:"msg"`
	// Data is the sweep's counts.
	Data sweepCounts `json:"data"`
}

type sweepCounts struct {
	// Swept is how many (author, deploying org) pairs the sweep examined.
	Swept int `json:"swept"`
	// Accrued is how many new royalty accruals it latched.
	Accrued int `json:"accrued"`
}

// SweepAuthorRoyalty runs the accrual sweep across every approved author: for each of
// their deploying orgs it computes this period's royalty from that org's metered
// spend and latches it at most once per period.
//
// It is an OVERRIDE, not the mechanism: a background scheduler runs the same sweep on
// its own, and every author's dashboard read sweeps their own accruals lazily. This
// is the manual trigger for an operator who needs the numbers now. It is idempotent —
// the per-period latch means running it twice accrues nothing the second time.
//
// A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
func (o ops) adminSweep(ctx context.Context, _ *noInput) (*authorSweepResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	approved, err := o.s.State.store.ListApproved(ctx, sweepLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list approved: %v", err)
	}
	swept, accrued := 0, 0
	for _, a := range approved {
		checked, credited, serr := sweepAuthor(o.s, ctx, a)
		swept += checked
		accrued += credited
		if serr != nil {
			o.s.Log.Warn("authors: sweep author failed", "author", a.ID, "err", serr)
		}
	}
	return &authorSweepResult{Status: "ok", Data: sweepCounts{Swept: swept, Accrued: accrued}}, nil
}

// authorResult is one author record in the operator envelope.
type authorResult struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is the envelope's message slot, empty on success.
	Msg string `json:"msg"`
	// Data carries the author.
	Data authorData `json:"data"`
}

type authorData struct {
	// Author is the author record after the change. Its repository and deploy counts
	// are 0 here — this is the mutated row, not a re-listing.
	Author adminAuthorView `json:"author"`
}

// approveRequest admits one author to earning.
type approveRequest struct {
	// ID is the author to approve, from the path.
	ID string `json:"id"`
	// ShareBps overrides this author's royalty share, in basis points (0–10000).
	// 0 keeps the platform default. A share change never rewrites history: existing
	// ledger rows keep the share that was applied when they were written.
	ShareBps int64 `json:"shareBps"`
}

// ApproveAuthor admits one author to EARNING, optionally on a negotiated royalty
// share. Until this runs, a connected author accrues nothing however many verified
// repositories they have.
//
// A share override applies from here forward only — existing ledger rows keep the
// share that was applied when they were written, because a rate change must never
// rewrite what was already owed.
//
// A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
//
// Example: {"id": "aut_1f…", "shareBps": 2500}
func (o ops) adminApprove(ctx context.Context, in *approveRequest) (*authorResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if in.ShareBps < 0 || in.ShareBps > bpsDenom {
		return nil, zip.ErrBadRequest("shareBps must be 0–10000")
	}
	a, err := o.s.State.store.Approve(ctx, strings.TrimSpace(in.ID), in.ShareBps, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("author not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
	}
	emitAudit(o.s, ctx, "author.approve", a, map[string]any{"shareBps": a.ShareBps})
	return &authorResult{Status: "ok", Data: authorData{Author: adminViewOf(a, 0, 0)}}, nil
}

// authorRef addresses ONE author by its id, which is the path segment.
type authorRef struct {
	// ID is the author record's handle, "aut_"-prefixed.
	ID string `json:"id"`
}

// SuspendAuthor stops one author earning. Their record, verified claims and ledger
// are untouched — suspension halts future accrual, it does not erase what was already
// owed, and it does not delete the evidence behind it.
//
// A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
func (o ops) adminSuspend(ctx context.Context, in *authorRef) (*authorResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	a, err := o.s.State.store.Suspend(ctx, strings.TrimSpace(in.ID), time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("author not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "suspend: %v", err)
	}
	emitAudit(o.s, ctx, "author.suspend", a, nil)
	return &authorResult{Status: "ok", Data: authorData{Author: adminViewOf(a, 0, 0)}}, nil
}

// payoutRequest pays out accrued royalty to one author.
type payoutRequest struct {
	// ID is the author to pay, from the path.
	ID string `json:"id"`
	// AmountCents is how much to pay, in cents. Must be positive and can never
	// exceed the author's pending royalty (accrued minus paid).
	AmountCents int64 `json:"amountCents"`
	// Method is how it settles: "credits" issues a grant into the author's wallet;
	// wire, paypal and the like are record-only. Required.
	Method string `json:"method"`
	// Reference is the operator's external reference for a cash settlement — a wire
	// confirmation, a PayPal transaction id.
	Reference string `json:"reference"`
}

// payoutResult is one recorded payout plus the author's balances after it.
type payoutResult struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is the envelope's message slot, empty on success.
	Msg string `json:"msg"`
	// Data carries the payout and the author.
	Data payoutData `json:"data"`
}

type payoutData struct {
	// Payout is the recorded payout, including where it settled.
	Payout payoutView `json:"payout"`
	// Author is the author record after the payout, with the balances updated.
	Author adminAuthorView `json:"author"`
}

// PayAuthor records a payout of accrued royalty and settles it.
//
// The amount is RESERVED against the author's pending royalty atomically before
// anything is paid, so a payout can never exceed what is owed even under concurrent
// calls. An external author's payout is then BACKED against the platform reserve
// fund — a second, independent guard — and refused with 402 if the reserve cannot
// cover it, with the reservation voided. A "credits" method issues the actual wallet
// grant after both guards; a cash method is record-only. A first-party (treasury)
// author's royalty is realized into Hanzo's own reserve instead of an external
// wallet, and every payout row discloses which of the three it was.
//
// A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
//
// Example: {"id": "aut_1f…", "amountCents": 25000, "method": "credits"}
func (o ops) adminPayout(ctx context.Context, in *payoutRequest) (*payoutResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	if in.AmountCents <= 0 {
		return nil, zip.ErrBadRequest("amountCents must be positive")
	}
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "" {
		return nil, zip.ErrBadRequest("method is required (credits, wire, paypal, …)")
	}
	id := strings.TrimSpace(in.ID)
	a, err := o.s.State.store.GetByID(ctx, id)
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("author not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}
	payout, err := issuePayout(o.s, ctx, a, in.AmountCents, method, strings.TrimSpace(in.Reference))
	if err != nil {
		switch err {
		case errNotFound:
			return nil, zip.ErrNotFound("author not found")
		case errInsufficientPending:
			return nil, zip.ErrBadRequest(pendingMsg(a.PendingCents()))
		default:
			return nil, err // issuePayout returns a ready zip error
		}
	}
	after, _ := o.s.State.store.GetByID(ctx, a.ID)
	emitAudit(o.s, ctx, "author.payout", after, map[string]any{
		"payoutId": payout.ID, "amountCents": payout.AmountCents, "method": payout.Method,
		"reference": payout.Reference, "txn": payout.Txn,
	})
	return &payoutResult{Status: "ok", Data: payoutData{
		Payout: payoutViewOf(payout), Author: adminViewOf(after, 0, 0),
	}}, nil
}

// requireBody replays, at the point in the sequence the raw handler reached it, the
// refusal c.Bind has always answered on the payout route: it takes a JSON body, and a
// request with none — or with a content type this service does not parse — is a 400,
// never a payout of zero.
//
// zip's typed decode is TOLERANT by construction (it skips an empty body and leaves
// the In at its zero value), so a naive conversion would have changed what the route
// ACCEPTS. This calls the SAME c.Bind over an empty target, so it is the same decision
// and the same message rather than a re-implementation free to drift. The approve
// route needs none: its body has always been optional (`_ = c.Bind(&body)`).
func requireBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil // off the HTTP path there is no body to require
	}
	return c.Bind(&struct{}{})
}

// AuthorRoyaltyBasis returns the audit trail behind ONE author's royalty — the same
// payload the author reads at /v1/authors/basis, from the same builder, so support
// sees exactly what the author sees rather than a parallel view free to drift.
//
// The data object carries: id, status, asOf, shareBps, platformShareBps,
// defaultShareBps, shareSource, settlesTo, method (the formula, the rate card and the
// sizing), ledger (every row with its spend, the share applied then, the platform's
// matching half, whether it satisfies the formula and the attribution edges that
// explain it), reconciliation (does the ledger foot to the balance) and window (what
// slice was actually returned) — plus period when one was requested.
//
// A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
//
// Example: {"id": "aut_1f…", "period": "2026-07"}
func (o ops) adminBasis(ctx context.Context, in *basisQuery) (*basisResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	period, err := normPeriod(in.Period)
	if err != nil {
		return nil, err
	}
	a, err := o.s.State.store.GetByID(ctx, strings.TrimSpace(in.ID))
	if err == errNotFound {
		return nil, zip.ErrNotFound("author not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}
	out, err := basisOf(o.s, ctx, a, period)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return &basisResult{Status: "ok", Data: out}, nil
}

// basisQuery addresses one author's royalty basis, optionally narrowed to a period.
type basisQuery struct {
	// ID is the author record's handle, from the path.
	ID string `json:"id"`
	// Period is the UTC accrual month, YYYY-MM. Empty means every period; any other
	// shape is refused with 400.
	Period string `json:"period"`
}

// basisResult is one author's royalty basis in the operator envelope. Its data is a
// JSON object whose keys the op's prose names in full — see AuthorRoyaltyBasis. It is
// a map rather than a struct because the payload carries the deployment's own rate
// card and sizing tables, which are configuration values with no fixed shape.
type basisResult struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is the envelope's message slot, empty on success.
	Msg string `json:"msg"`
	// Data is the royalty basis.
	Data map[string]any `json:"data"`
}
