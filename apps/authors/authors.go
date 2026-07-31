// Package authors mounts the Hanzo Cloud /v1/authors/* OSS-author surface: a
// native-Go, per-org program on Base/SQLite that pays open-source AUTHORS a royalty
// on the metered platform spend of the orgs who DEPLOY their projects on Hanzo. It
// sits next to clients/referrals (a one-time credit for both sides) and
// clients/affiliates (an ongoing partner commission on referred customers) as the
// THIRD growth loop — the CREATOR one — and mirrors their structure exactly: one
// SQLite store, server-side tenant isolation, one Mount, HIP-0106, and the SAME
// commerce ledger path (a credits payout is a grant, tag grant:author).
//
// The loop, end to end:
//
//  1. An author CONNECTS GitHub (POST /v1/authors/connect): we link the caller's org
//     to a GitHub login — from IAM's linked GitHub account when available (identity
//     verified), else a login the caller supplies (verified per-repo below). We mint
//     a stable per-author VERIFY CODE for the file method.
//  2. The author VERIFIES a repo (POST /v1/authors/repos/verify): ownership is proven
//     either by an IAM-linked GitHub token showing ADMIN/PUSH permission on the repo
//     (OAuth method), or by a hanzo.json on the repo's default branch carrying the
//     author's verify code (file method — proves default-branch control). A verified
//     repo can now earn.
//  3. When a published project whose sourceRepo matches a VERIFIED author repo is
//     DEPLOYED by ANY org, the deploy path records it (POST /v1/authors/deploys/
//     record): deploying_org↔repo↔project, idempotent per (repo, project, org).
//     hanzo.app persists sourceRepo on the published project so the deploy is
//     attributable.
//  4. The ACCRUAL SWEEP (the scheduler's automatic loop; POST /v1/admin/authors/sweep
//     as an operator override; also lazy on the author's own dashboard read) folds
//     over each approved author's DISTINCT deploying orgs (excluding the author's own):
//     royalty = that org's metered spend THIS PERIOD × the author's share (20%),
//     accrued at-most-once per (author, deploying_org, period).
//  5. The AUTO-PAYOUT (same scheduler pass, right after accrual; POST
//     /v1/admin/authors/:id/payout as an operator override) settles each author's
//     pending royalty with NO human in the loop: "credits" issues a commerce grant into
//     an external author's wallet; a Hanzo-MAINTAINED template's royalty is realized
//     into the Hanzo treasury reserve instead ("pay ourselves"). A payout can never
//     exceed pending (accrued − paid), guarded atomically — idempotent, never
//     double-pays.
//
// HANZO FORKS. A repo whose owner is a brand org (owner ∈ {hanzoai, hanzo-*}) is
// auto-attributed on first deploy to the treasury SYSTEM author (org = the brand slug),
// so Hanzo earns 20% on its OWN templates when other orgs deploy them, credited to the
// treasury reserve via the shared treasury client — no external wallet, no new ledger.
//
// Surface:
//
//	GET  /v1/authors                       (org)          my status, login, verified, repos, deploys, accrued/pending/paid, payouts
//	GET  /v1/authors/basis                 (org)          why my number is my number: share, cost model, immutable rows, reconciliation
//	POST /v1/authors/connect               (org)          link GitHub (IAM-linked account or supplied login) + mint verify code
//	POST /v1/authors/repos/verify          (org)          verify repo ownership (oauth admin-check OR hanzo.json file)
//	POST /v1/authors/deploys/record        (org=deployer) record a deploy of a verified author repo (provenance → royalty)
//	GET  /v1/admin/authors                  (SuperAdmin) every author + a summary
//	POST /v1/admin/authors/sweep            (SuperAdmin) accrue royalty for every deploying org this period
//	POST /v1/admin/authors/:id/approve      (SuperAdmin) admit to earning (+ optional share override)
//	POST /v1/admin/authors/:id/suspend      (SuperAdmin) suspend
//	POST /v1/admin/authors/:id/payout       (SuperAdmin) record a payout (credits → grant; cash → record-only)
//	GET  /v1/admin/authors/:id/basis        (SuperAdmin) the SAME basis payload the author reads (support mirror)
//
// serve.go auto-registers GET /v1/authors/health.
package authors

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/treasury"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// The author economy — ONE place. Amounts are USD minor units (cents); a credits
// payout lands in the commerce Credit/trial bucket (grant:* → Credit per DepositKind),
// distinct from grant:referral / grant:affiliate / grant:admin only by its tag.
const (
	// defaultShareBps is the internal default royalty rate a new author earns, in basis
	// points (2000 = 20% of a deploying org's metered platform spend), overridable
	// per-author at approval. It is the ONE royalty constant the accrual math reads;
	// any public-facing rate is presented by the frontend, not promised here. Was
	// 2500/25%; a stale flow comment said 5% — both reconciled to this ONE default.
	defaultShareBps int64 = 2000
	// bpsDenom converts basis points to a fraction (spend × shareBps / 10000).
	bpsDenom int64 = 10000
	// grantCurrency is the ledger currency for a credits payout.
	grantCurrency = "usd"
	// grantTag classifies a credits payout as a non-cash Credit in commerce's
	// DepositKind (grant:* → Credit), distinct from the other loops' tags.
	grantTag = "grant:author"
	// methodCredits is the ONE payout method that issues a commerce grant; every other
	// method (wire/paypal/check/…) is a record-only cash disbursement.
	methodCredits = "credits"
	// The settlement kinds — WHERE a payout's money actually landed, recorded on the
	// payout row (see Payout.Settlement). settlementTreasury is INTERNAL ACCOUNTING:
	// a first-party author (our own maintained templates and the seeded example
	// creators) settles into our own reserve fund, and saying so on the row is what
	// keeps it from ever being read as an independent creator's earnings.
	settlementTreasury = "treasury"
	settlementWallet   = "wallet"
	settlementCash     = "cash"
	// verifyFile is the repo-root file the file-verification method reads; it must
	// contain the author's verify code on the repo's default branch.
	verifyFile = "hanzo.json"
	// orgProofRepo is the owner-wide control artifact an ORG claim is proven against.
	// GitHub/GitLab expose a special "<owner>/.github" repository for owner-level
	// config, so proving ownership of it — the SAME OAuth admin/push check OR a
	// hanzo.json with the verify code on its default branch — proves control of the
	// whole owner. No new proof primitive, exactly as strong as a per-repo claim.
	orgProofRepo = ".github"
)

// verifyBranches are the default-branch names the file method probes, in order.
var verifyBranches = []string{"main", "master"}

const (
	// sweepLimit bounds one accrual sweep so an unbounded set can't wedge a request.
	sweepLimit = 500
	// listLimit / maxAdminLimit bound read responses; repo/deploy/payout limits bound
	// per-author history.
	listLimit     = 500
	maxAdminLimit = 1000
	repoLimit     = 200
	deployLimit   = 200
	payoutLimit   = 100
	ledgerLimit   = 200
)

// state is authors' own data; shared deps live in the embedded cloud.Base.
type state struct {
	store         *Store
	commerce      commerce
	forge         forge
	badgeBase     string          // https://hanzo.app — the Deploy-on-Hanzo badge/link host
	maintainerOrg string          // the first-party org (hanzo/lux/zoo) whose maintained templates earn INTO the treasury ("pay ourselves")
	auditStore    *audit.Recorder // best-effort payout/accrual audit; nil disables it
}

var mounted *cloud.Service[state]

// stopScheduler stops the background accrual+auto-payout loop. Set by Mount, called
// by Shutdown; the default no-op keeps an unmounted/partial deploy safe.
var stopScheduler = func() {}

// Mount wires the authors surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("authors.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("authors.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("authors.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("authors.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "authors.db"))
	if err != nil {
		return fmt.Errorf("authors.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "authors")
	s := &cloud.Service[state]{Base: b, State: state{
		store:         store,
		commerce:      newCommerceClient(transport.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		forge:         newGitHubClient(os.Getenv("CLOUD_IAM_HTTP_URL"), os.Getenv("IAM_SERVICE_TOKEN")),
		badgeBase:     badgeBase(deps),
		maintainerOrg: maintainerOrgFor(deps),
		auditStore:    deps.Audit,
	}}
	mounted = s

	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("authors.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	routes(zapp, s)

	// Drive the DEFAULT automatic money loop: a periodic goroutine that accrues every
	// approved author's royalty AND auto-pays their pending balance, no human in the
	// loop (the manual sweep/payout admin endpoints remain as overrides). Single-writer:
	// authors mounts only on the writer pod, so exactly one scheduler runs.
	stopScheduler = startScheduler(s)

	b.Log.Info("authors mounted", "brand", deps.Brand, "badgeBase", s.State.badgeBase,
		"maintainerOrg", s.State.maintainerOrg, "commerce", s.State.commerce.configured())
	return nil
}

// routes registers the authors surface. EVERY route is a typed op: the registry
// entry zip.<Verb> makes is the ONE thing OpenAPI, MCP and the CLI project from,
// and it takes the ABSOLUTE path because the registry keys on it. The static
// /sweep literal binds before the /:id/* param routes (distinct segment counts).
func routes(zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	zip.Get(zapp, "/v1/authors", o.myAuthors, zip.WithOperationID("authorMe"))
	zip.Get(zapp, "/v1/authors/basis", o.basis, zip.WithOperationID("authorBasis"))
	zip.Post(zapp, "/v1/authors/connect", o.connect, zip.WithOperationID("authorConnect"))
	zip.Post(zapp, "/v1/authors/repos/verify", o.verifyRepo, zip.WithOperationID("authorVerifyRepo"))
	zip.Post(zapp, "/v1/authors/deploys/record", o.recordDeploy, zip.WithOperationID("authorRecordDeploy"))
	zip.Get(zapp, "/v1/admin/authors", o.adminList, zip.WithOperationID("adminAuthors"))
	zip.Post(zapp, "/v1/admin/authors/sweep", o.adminSweep, zip.WithOperationID("adminAuthorSweep"))
	zip.Post(zapp, "/v1/admin/authors/:id/approve", o.adminApprove, zip.WithOperationID("adminAuthorApprove"))
	zip.Post(zapp, "/v1/admin/authors/:id/suspend", o.adminSuspend, zip.WithOperationID("adminAuthorSuspend"))
	zip.Post(zapp, "/v1/admin/authors/:id/payout", o.adminPayout, zip.WithOperationID("adminAuthorPayout"))
	zip.Get(zapp, "/v1/admin/authors/:id/basis", o.adminBasis, zip.WithOperationID("adminAuthorBasis"))
}

// ops binds the service to the typed handlers: a TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// admit is the SuperAdmin gate for every /v1/admin/authors op, fail-closed. It reads
// the platform-sudo claim off the request the bridge carried in, so an op with no
// request (the CLI's local invoke) refuses rather than inventing an identity.
func admit(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	return nil
}

// ── customer surface ─────────────────────────────────────────────────────────

// myAuthors reads the caller org's author dashboard. It answers status, login,
// verified repos and owner claims, deploy edges, accrued/pending/paid royalty and
// payout history.
//
// An org that has not connected gets an honest {isAuthor:false} shape so the console
// can show the connect form — not a 404, which would answer "is this org an author?".
// For an APPROVED author it ALSO opportunistically runs the accrual sweep, so the
// dashboard is self-updating; a commerce hiccup never fails the page.
//
// The response is the open dashboard document, not a fixed record: the enrolled and
// not-enrolled answers carry different keys, so no single struct states it truthfully.
//
// Response: {"isAuthor":true,"id":"aut_9f2a","status":"approved","githubLogin":"octocat","verified":true,"shareBps":2000,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"repos":[],"orgs":[],"deploys":[],"payouts":[],"ledger":[]}
func (o ops) myAuthors(ctx context.Context, _ *struct{}) (*map[string]any, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your author program")
	}

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &map[string]any{
			"isAuthor":        false,
			"defaultShareBps": defaultShareBps,
			"badgeBase":       s.State.badgeBase,
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}

	// Lazy accrual sweep for MY deploying orgs (bounded, best-effort — a commerce
	// hiccup never fails the page; it simply accrues on the next sweep).
	if a.Status == StatusApproved {
		if _, _, serr := sweepAuthor(s, ctx, a); serr != nil {
			s.Log.Warn("authors: lazy sweep failed", "author", a.ID, "err", serr)
		}
		if refreshed, rerr := s.State.store.GetByID(ctx, a.ID); rerr == nil {
			a = refreshed // pick up any accrual the lazy sweep just latched
		}
	}

	repos, err := s.State.store.ListRepos(ctx, a.ID, repoLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list repos: %v", err)
	}
	orgs, err := s.State.store.ListOrgs(ctx, a.ID, repoLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list orgs: %v", err)
	}
	deploys, err := s.State.store.ListDeploys(ctx, a.ID, deployLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deploys: %v", err)
	}
	payouts, err := s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}
	ledger, err := s.State.store.ListLedger(ctx, a.ID, "", ledgerLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list ledger: %v", err)
	}

	return &map[string]any{
		"isAuthor":      true,
		"id":            a.ID,
		"status":        a.Status,
		"githubLogin":   a.GithubLogin,
		"verified":      a.VerifiedAt > 0,
		"verifyCode":    a.VerifyCode,
		"verifyFile":    verifyFile,
		"verifySnippet": verifySnippet(a.VerifyCode),
		"shareBps":      a.ShareBps,
		"badgeBase":     s.State.badgeBase,
		"repos":         repoViews(repos, s.State.badgeBase),
		"orgs":          orgViews(orgs, s.State.badgeBase),
		"deploys":       deployViews(deploys),
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"payouts":       payoutViews(payouts),
		"ledger":        ledger,
	}, nil
}

// AuthorConnect is the POST /v1/authors/connect body.
type AuthorConnect struct {
	// Provider is the forge to link: "github" or "gitlab". Empty means github.
	Provider string `json:"provider"`
	// GithubLogin is the login to link when IAM holds no linked account for the
	// provider. Ignored when one is linked — that identity is stronger proof.
	GithubLogin string `json:"githubLogin"`
	// Login is the provider-neutral alias for GithubLogin, and wins when both are
	// sent.
	Login string `json:"login"`
}

// AuthorIdentity is the POST /v1/authors/connect answer: the author's linked forge
// identity and the material for the file-verification method.
type AuthorIdentity struct {
	// ID is the author id, stable across repeat connects.
	ID string `json:"id"`
	// Status is the author's lifecycle state — "connected" on first link.
	Status string `json:"status"`
	// GithubLogin is the linked forge login, lower-cased.
	GithubLogin string `json:"githubLogin"`
	// Verified reports an IAM-linked forge identity (strong proof) rather than a
	// login the caller merely typed.
	Verified bool `json:"verified"`
	// VerifyCode is this author's stable code for the file method.
	VerifyCode string `json:"verifyCode"`
	// VerifyFile is the file name to place on the default branch: "hanzo.json".
	VerifyFile string `json:"verifyFile"`
	// VerifySnippet is the ready-to-paste hanzo.json body carrying the code.
	VerifySnippet string `json:"verifySnippet"`
	// ShareBps is the author's royalty share in basis points.
	ShareBps int64 `json:"shareBps"`
	// Created is true when THIS call enrolled the org; false on a repeat connect.
	Created bool `json:"created"`
}

// connect enrolls the caller's org as an author. It answers the verify material an
// author places on their default branch, and is idempotent — a repeat connect returns
// the existing author with created:false.
//
// The login comes from IAM's linked forge account when there is one (identity
// verified), else from the supplied login, which is verified later per repo. The
// response is 201 on the call that enrolled the org and 200 on a repeat.
//
// Example: {"provider":"github","login":"octocat"}
// Response: {"id":"aut_9f2a","status":"connected","githubLogin":"octocat","verified":true,"verifyCode":"avc_1d7f","verifyFile":"hanzo.json","verifySnippet":"{\n  \"hanzoAuthorCode\": \"avc_1d7f\"\n}","shareBps":2000,"created":true}
func (o ops) connect(ctx context.Context, body *AuthorConnect) (*AuthorIdentity, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to connect GitHub")
	}
	var userSub string
	if c, ok := cloud.Request(ctx); ok {
		userSub = strings.TrimSpace(c.User())
	}
	provider := normalizeProvider(body.Provider)

	// Prefer IAM's linked forge identity for the provider (strong proof of the login).
	login := normalizeLogin(firstNonEmpty(body.Login, body.GithubLogin))
	identityVerified := false
	if l, _, linked, lerr := s.State.forge.linkedAccount(ctx, provider, org, userSub); lerr != nil {
		s.Log.Warn("authors: linked-account lookup failed", "org", org, "provider", provider, "err", lerr)
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
	a, created, err := s.State.store.Connect(ctx, id, org, login, verifyCode, defaultShareBps, identityVerified, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "connect: %v", err)
	}
	if created {
		cloud.Created(ctx)
	}
	return &AuthorIdentity{
		ID:            a.ID,
		Status:        a.Status,
		GithubLogin:   a.GithubLogin,
		Verified:      a.VerifiedAt > 0,
		VerifyCode:    a.VerifyCode,
		VerifyFile:    verifyFile,
		VerifySnippet: verifySnippet(a.VerifyCode),
		ShareBps:      a.ShareBps,
		Created:       created,
	}, nil
}

// AuthorVerify is the POST /v1/authors/repos/verify body.
type AuthorVerify struct {
	// RepoURL is a forge repo (github.com/owner/name) or a whole owner
	// (github.com/owner). gitlab.com is accepted in both forms. Required.
	RepoURL string `json:"repoUrl" validate:"required"`
}

// AuthorClaim is the POST /v1/authors/repos/verify answer. Exactly one of repo or
// owner is present — the one the url addressed.
type AuthorClaim struct {
	// Repo is the per-repo claim, present when the url named a repo.
	Repo *AuthorRepoView `json:"repo,omitempty"`
	// Owner is the owner-wide claim covering every repo under that owner, present
	// when the url named an owner. It is serialized as "org".
	Owner *AuthorOwnerView `json:"org,omitempty"`
	// Created is true when THIS call recorded the claim; false when it already
	// existed.
	Created bool `json:"created"`
}

// verifyRepo proves the caller owns a repo or a whole owner. Both are proven the same
// two ways, and both record a claim.
//
// A repo url records a per-repo claim; an owner url records an owner-wide claim
// covering every repo under that owner. Ownership is proven the SAME two ways in both
// cases: an IAM-linked forge token with admin/push permission, or a hanzo.json
// carrying the author's verify code on the default branch (for an owner, of its
// .github control repo). Unprovable ownership is 422, a claim another author already
// holds is 409, and the caller must have connected first. 201 on the call that
// recorded the claim, 200 on a repeat.
//
// Example: {"repoUrl":"github.com/octocat/hello-world"}
// Response: {"repo":{"repoUrl":"github.com/octocat/hello-world","verified":true,"method":"oauth","badgeMarkdown":"[![Deploy on Hanzo](https://hanzo.ai/deploy-badge.svg)](https://hanzo.ai/new?template=https://github.com/octocat/hello-world)","verifiedAt":1780000000,"createdAt":1780000000},"created":true}
func (o ops) verifyRepo(ctx context.Context, body *AuthorVerify) (*AuthorClaim, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to verify a repo")
	}
	target, perr := parseTarget(body.RepoURL)
	if perr != nil {
		return nil, zip.ErrBadRequest("repoUrl must be a GitHub or GitLab repo OR owner — github.com/owner or github.com/owner/name (gitlab.com too)")
	}
	var userSub string
	if c, ok := cloud.Request(ctx); ok {
		userSub = strings.TrimSpace(c.User())
	}

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return nil, zip.ErrBadRequest("connect a forge account before verifying a repo")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}

	if target.isOrg() {
		return verifyOrg(s, ctx, userSub, a, org, target)
	}

	host, owner, name := target.host, target.owner, target.name
	method, verified := proveOwnership(s, ctx, a, org, userSub, host, owner, name)
	if !verified {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"could not verify ownership of %s — grant the Hanzo %s app OR add %s containing your verify code (%s) to the default branch",
			target.canonical, providerForHost(host), verifyFile, a.VerifyCode)
	}

	repoID, err := genID("arp")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	repo, created, err := s.State.store.UpsertVerifiedRepo(ctx, repoID, a.ID, target.canonical, method, time.Now().Unix())
	if err != nil {
		if err == errRepoOwned {
			return nil, zip.ErrConflict("that repo is already verified by another author")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "record repo: %v", err)
	}
	emitAudit(s, ctx, "author.verify_repo", a, map[string]any{"repoUrl": target.canonical, "method": method})
	if created {
		cloud.Created(ctx)
	}
	view := repoViewOf(repo, s.State.badgeBase)
	return &AuthorClaim{Repo: &view, Created: created}, nil
}

// verifyOrg proves an OWNER-WIDE claim and records it. Ownership of the whole owner is
// proven the SAME two ways as a repo — reusing proveOwnership against the owner's
// canonical control repo "<owner>/.github" (OAuth admin/push on it, OR a hanzo.json
// with the verify code on its default branch). The ownership check is NOT weakened: an
// owner the caller can't prove is 422, exactly like an unprovable repo. A verified org
// claim covers every repo the author publishes under that owner.
func verifyOrg(s *cloud.Service[state], ctx context.Context, userSub string, a Author, org string, t verifyTarget) (*AuthorClaim, error) {
	method, verified := proveOwnership(s, ctx, a, org, userSub, t.host, t.owner, orgProofRepo)
	if !verified {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"could not verify ownership of the %s owner %q — grant the Hanzo %s app admin on %s/%s OR add %s carrying your verify code (%s) to its default branch",
			providerForHost(t.host), t.owner, providerForHost(t.host), t.owner, orgProofRepo, verifyFile, a.VerifyCode)
	}

	orgID, err := genID("aog")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	claim, created, err := s.State.store.UpsertVerifiedOrg(ctx, orgID, a.ID, t.canonical, method, time.Now().Unix())
	if err != nil {
		if err == errOrgOwned {
			return nil, zip.ErrConflict("that owner is already verified by another author")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "record org: %v", err)
	}
	emitAudit(s, ctx, "author.verify_org", a, map[string]any{"ownerUrl": t.canonical, "method": method})
	if created {
		cloud.Created(ctx)
	}
	view := orgViewOf(claim, s.State.badgeBase)
	return &AuthorClaim{Owner: &view, Created: created}, nil
}

// proveOwnership tries the two verification methods in order (oauth, then file) on the
// repo's forge (host → provider) and returns the method that succeeded + whether it did.
func proveOwnership(s *cloud.Service[state], ctx context.Context, a Author, org, userSub, host, owner, name string) (method string, verified bool) {
	provider := providerForHost(host)
	// Method 1: IAM-linked forge token → admin/push permission on the repo.
	if login, token, linked, lerr := s.State.forge.linkedAccount(ctx, provider, org, userSub); lerr == nil && linked && token != "" {
		if admin, aerr := s.State.forge.repoAdmin(ctx, host, token, owner, name); aerr == nil && admin {
			return MethodOAuth, true
		} else if aerr != nil {
			s.Log.Warn("authors: repoAdmin check failed", "repo", host+"/"+owner+"/"+name, "err", aerr)
		}
		_ = login
	}
	// Method 2: hanzo.json on the default branch containing the verify code.
	for _, branch := range verifyBranches {
		file, ferr := s.State.forge.fetchFile(ctx, host, owner, name, branch, verifyFile)
		if ferr != nil {
			s.Log.Warn("authors: fetch verify file failed", "repo", host+"/"+owner+"/"+name, "branch", branch, "err", ferr)
			continue
		}
		if len(file) > 0 && fileProvesCode(file, a.VerifyCode) {
			return MethodFile, true
		}
	}
	return "", false
}

// normalizeProvider trims + lowercases a provider name and defaults empty → github.
func normalizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == ProviderGitLab {
		return ProviderGitLab
	}
	return ProviderGitHub
}

// firstNonEmpty returns the first non-empty trimmed string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// AuthorDeploy is the POST /v1/authors/deploys/record body: the source repo a project
// was built from and the project id. The deploying org is the caller's, never a field.
type AuthorDeploy struct {
	// RepoURL is the forge repo the project was sourced from. Empty means a
	// hand-built project with nothing to attribute.
	RepoURL string `json:"repoUrl"`
	// Project is the deployed project's id. Required.
	Project string `json:"project" validate:"required"`
}

// AuthorDeployRecorded is the POST /v1/authors/deploys/record answer. When Recorded
// is false only Reason is set; when it is true the edge fields are.
type AuthorDeployRecorded struct {
	// Recorded reports whether an attribution edge exists for this deploy. False is
	// an honest no-op, not an error, so a deploy path can fire this unconditionally.
	Recorded bool `json:"recorded"`
	// Reason says why nothing was attributed, on a recorded:false answer only.
	Reason string `json:"reason,omitempty"`
	// Created is true when THIS call wrote the edge, false when it already existed.
	// Present only on a recorded:true answer.
	Created *bool `json:"created,omitempty"`
	// Self reports a deploy of the author's own repo by the author's own org —
	// recorded for provenance, excluded from accrual by the sweep. Present only on a
	// recorded:true answer.
	Self *bool `json:"self,omitempty"`
	// DeployID is the attribution edge's id, on a recorded:true answer only.
	DeployID string `json:"deployId,omitempty"`
	// CreatedAt is unix seconds when the edge was first written, on a recorded:true
	// answer only.
	CreatedAt *int64 `json:"createdAt,omitempty"`
}

// recordDeploy records a deploy-attribution edge for the caller's DEPLOYING org. It
// is what makes a deploy earn its author royalty.
//
// When repoUrl names a VERIFIED author repo — or a repo under a verified owner — the
// edge is recorded (idempotent per repo+project+org) and becomes eligible for royalty.
// A repo that is not a verified author repo is NOT an error: it answers recorded:false
// so the deploy path can fire this unconditionally. A self-deploy is recorded for
// provenance but excluded from accrual. 201 on the call that wrote the edge.
//
// Example: {"repoUrl":"github.com/octocat/hello-world","project":"prj_4c1e"}
// Response: {"recorded":true,"created":true,"self":false,"deployId":"ade_1d7f","createdAt":1780000000}
func (o ops) recordDeploy(ctx context.Context, body *AuthorDeploy) (*AuthorDeployRecorded, error) {
	s := o.s
	deployingOrg, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to record a deploy")
	}
	project := strings.TrimSpace(body.Project)
	if project == "" {
		return nil, zip.ErrBadRequest("project is required")
	}
	repoURL := normalizeRepo(body.RepoURL)
	if repoURL == "" {
		// No source repo → nothing to attribute (a hand-built project). Honest no-op.
		return &AuthorDeployRecorded{Recorded: false, Reason: "no source repo"}, nil
	}

	// Hanzo-maintained template (owner ∈ this brand's GitHub orgs, e.g. hanzoai /
	// hanzo-*)? Attribute it to the treasury SYSTEM author so its creator royalty
	// accrues to the Hanzo treasury ("pay ourselves") — idempotent, no human verify
	// step. A repo a real external author already verified is left theirs (first-verify
	// wins, enforced inside ensureMaintainedRepo). This makes Hanzo earn 20% on its own
	// templates when other orgs deploy them.
	if isMaintainedRepo(repoURL, s.State.maintainerOrg) {
		ensureMaintainedRepo(s, ctx, repoURL, time.Now().Unix())
	}

	// Attribution resolves in ONE of two arms — per-repo first, then owner-wide: a repo
	// with its OWN verified claim earns for that author; otherwise a repo whose OWNER is
	// a verified org claim earns for the org's author (so every repo under a verified
	// owner earns without a per-repo verify). Neither → an honest recorded:false no-op.
	authorID, err := resolveDeployAuthor(s, ctx, repoURL)
	if err == errUnknownRepo || err == errRepoNotVerified {
		return &AuthorDeployRecorded{Recorded: false, Reason: "repo is not a verified author repo"}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve repo: %v", err)
	}

	id, err := genID("ade")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	edge, created, err := s.State.store.RecordDeploy(ctx, id, authorID, repoURL, project, deployingOrg, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record deploy: %v", err)
	}
	author, _ := s.State.store.GetByID(ctx, authorID)
	self := author.Org == deployingOrg
	if created {
		cloud.Created(ctx)
		emitAudit(s, ctx, "author.deploy", author, map[string]any{
			"repoUrl": repoURL, "project": project, "deployingOrg": deployingOrg, "self": self,
		})
	}
	return &AuthorDeployRecorded{
		Recorded:  true,
		Created:   &created,
		Self:      &self,
		DeployID:  edge.ID,
		CreatedAt: &edge.CreatedAt,
	}, nil
}

// resolveDeployAuthor returns the author a deployed repo earns for, trying the two
// attribution arms in order: (1) a per-repo verified claim, then (2) an owner-wide
// verified org claim covering the repo. Per-repo wins, so a specifically-claimed repo
// always attributes to its own author. Returns errUnknownRepo / errRepoNotVerified when
// neither arm matches (the deploy is an honest no-op).
func resolveDeployAuthor(s *cloud.Service[state], ctx context.Context, repoURL string) (string, error) {
	repo, err := s.State.store.VerifiedRepoForURL(ctx, repoURL)
	if err == nil {
		return repo.AuthorID, nil
	}
	if err != errUnknownRepo && err != errRepoNotVerified {
		return "", err
	}
	// No per-repo claim — fall back to an owner-wide org claim covering this repo.
	claim, oerr := s.State.store.VerifiedOrgForURL(ctx, repoURL)
	if oerr != nil {
		return "", oerr
	}
	return claim.AuthorID, nil
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// AuthorPage bounds an admin listing.
type AuthorPage struct {
	// Limit caps the rows returned; absent or non-positive means 500, and nothing
	// above 1000 is honoured.
	Limit int `json:"limit"`
}

// AuthorDirectory is the GET /v1/admin/authors envelope.
type AuthorDirectory struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data is the directory itself.
	Data AuthorDirectoryData `json:"data"`
}

// AuthorDirectoryData is every author plus the fleet tally.
type AuthorDirectoryData struct {
	// Authors is one row per author, org exposed. Never null.
	Authors []AdminAuthorView `json:"authors"`
	// Summary tallies the rows returned.
	Summary AuthorSummary `json:"summary"`
}

// AuthorOne is the envelope for the admin ops that answer with one author.
type AuthorOne struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data carries the author as it now stands.
	Data AuthorOneData `json:"data"`
}

// AuthorOneData carries the author an approve or suspend left behind.
type AuthorOneData struct {
	// Author is the author after the change.
	Author AdminAuthorView `json:"author"`
}

// adminList reads every author with its repo and deploy counts, plus a fleet tally of
// accrued, pending and paid royalty. SuperAdmin only.
//
// Response: {"status":"ok","msg":"","data":{"authors":[{"id":"aut_9f2a","org":"acme","githubLogin":"octocat","status":"approved","verified":true,"shareBps":2000,"repoCount":3,"deployCount":12,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}],"summary":{"total":1,"connected":0,"approved":1,"suspended":0,"accruedCents":1250,"pendingCents":1250,"paidCents":0}}}
func (o ops) adminList(ctx context.Context, in *AuthorPage) (*AuthorDirectory, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	rows, err := s.State.store.ListAll(ctx, adminLimitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list authors: %v", err)
	}
	repoCounts, err := s.State.store.RepoCountsByAuthor(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count repos: %v", err)
	}
	deployCounts, err := s.State.store.DeployCountsByAuthor(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count deploys: %v", err)
	}
	views := make([]AdminAuthorView, 0, len(rows))
	sum := AuthorSummary{}
	for _, a := range rows {
		sum.add(a)
		views = append(views, adminViewOf(a, repoCounts[a.ID], deployCounts[a.ID]))
	}
	return &AuthorDirectory{Status: "ok", Data: AuthorDirectoryData{Authors: views, Summary: sum}}, nil
}

// AuthorApprove is the POST /v1/admin/authors/:id/approve input.
type AuthorApprove struct {
	// ID is the author id from the path.
	ID string `json:"id"`
	// ShareBps overrides the author's royalty share, in basis points (0–10000). 0
	// keeps the current share.
	ShareBps int64 `json:"shareBps"`
}

// adminApprove admits an author to earning, optionally overriding their royalty
// share. SuperAdmin only.
//
// Example: {"id":"aut_9f2a","shareBps":2500}
// Response: {"status":"ok","msg":"","data":{"author":{"id":"aut_9f2a","org":"acme","githubLogin":"octocat","status":"approved","verified":true,"shareBps":2500,"repoCount":0,"deployCount":0,"accruedCents":0,"pendingCents":0,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}}}
func (o ops) adminApprove(ctx context.Context, in *AuthorApprove) (*AuthorOne, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	if in.ShareBps < 0 || in.ShareBps > bpsDenom {
		return nil, zip.ErrBadRequest("shareBps must be 0–10000")
	}
	a, err := s.State.store.Approve(ctx, strings.TrimSpace(in.ID), in.ShareBps, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("author not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
	}
	emitAudit(s, ctx, "author.approve", a, map[string]any{"shareBps": a.ShareBps})
	return &AuthorOne{Status: "ok", Data: AuthorOneData{Author: adminViewOf(a, 0, 0)}}, nil
}

// AuthorRef addresses one author by the id in the path.
type AuthorRef struct {
	// ID is the author id from the path, as returned by the admin directory.
	ID string `json:"id"`
}

// adminSuspend stops an author earning, leaving accrued royalty payable. SuperAdmin
// only.
//
// Example: {"id":"aut_9f2a"}
// Response: {"status":"ok","msg":"","data":{"author":{"id":"aut_9f2a","org":"acme","githubLogin":"octocat","status":"suspended","verified":true,"shareBps":2000,"repoCount":0,"deployCount":0,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":1780000200}}}
func (o ops) adminSuspend(ctx context.Context, in *AuthorRef) (*AuthorOne, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	a, err := s.State.store.Suspend(ctx, strings.TrimSpace(in.ID), time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("author not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "suspend: %v", err)
	}
	emitAudit(s, ctx, "author.suspend", a, nil)
	return &AuthorOne{Status: "ok", Data: AuthorOneData{Author: adminViewOf(a, 0, 0)}}, nil
}

// AuthorPayoutRequest is the POST /v1/admin/authors/:id/payout input.
type AuthorPayoutRequest struct {
	// ID is the author id from the path.
	ID string `json:"id"`
	// AmountCents is the payout, in USD minor units. Must be positive and can never
	// exceed the author's pending royalty.
	AmountCents int64 `json:"amountCents" validate:"required"`
	// Method is "credits" (issues a commerce grant into the author's wallet) or a
	// cash method such as wire or paypal (record-only). Required.
	Method string `json:"method" validate:"required"`
	// Reference is the operator's own note or external transfer id.
	Reference string `json:"reference"`
}

// AuthorPayoutOut is the POST /v1/admin/authors/:id/payout envelope.
type AuthorPayoutOut struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data carries the payout row and the author after it.
	Data AuthorPayoutData `json:"data"`
}

// AuthorPayoutData is the settled payout plus the author's new balances.
type AuthorPayoutData struct {
	// Payout is the recorded disbursement.
	Payout AuthorPayoutView `json:"payout"`
	// Author is the author after the payout reserved against pending.
	Author AdminAuthorView `json:"author"`
}

// adminPayout records a payout of accrued royalty and settles it. Both guards run
// before any money moves.
//
// A "credits" method issues a commerce grant into the author's wallet; a cash method
// is record-only. The amount can never exceed pending (accrued − paid), reserved
// atomically before any grant, and an external payout must additionally be backed by
// the treasury reserve — an unbacked one is refused with 402 and the reservation
// voided. SuperAdmin only.
//
// Example: {"id":"aut_9f2a","amountCents":1250,"method":"credits","reference":"Q3 royalty"}
// Response: {"status":"ok","msg":"","data":{"payout":{"id":"apo_1d7f","amountCents":1250,"method":"credits","reference":"Q3 royalty","txn":"txn_44","settlement":"wallet","createdAt":1780000000},"author":{"id":"aut_9f2a","org":"acme","githubLogin":"octocat","status":"approved","verified":true,"shareBps":2000,"repoCount":0,"deployCount":0,"accruedCents":1250,"pendingCents":0,"paidCents":1250,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}}}
func (o ops) adminPayout(ctx context.Context, in *AuthorPayoutRequest) (*AuthorPayoutOut, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	if in.AmountCents <= 0 {
		return nil, zip.ErrBadRequest("amountCents must be positive")
	}
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "" {
		return nil, zip.ErrBadRequest("method is required (credits, wire, paypal, …)")
	}

	a, err := s.State.store.GetByID(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("author not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}

	payout, err := issuePayout(s, ctx, a, in.AmountCents, method, strings.TrimSpace(in.Reference))
	if err != nil {
		switch err {
		case errNotFound:
			return nil, zip.ErrNotFound("author not found")
		case errInsufficientPending:
			return nil, zip.ErrBadRequest(fmt.Sprintf("amount exceeds pending royalty (%d cents available)", a.PendingCents()))
		default:
			return nil, err // issuePayout returns a ready zip error (reserve/PaymentRequired/rng)
		}
	}

	after, _ := s.State.store.GetByID(ctx, a.ID)
	emitAudit(s, ctx, "author.payout", after, map[string]any{
		"payoutId": payout.ID, "amountCents": payout.AmountCents, "method": payout.Method,
		"reference": payout.Reference, "txn": payout.Txn,
	})
	return &AuthorPayoutOut{Status: "ok", Data: AuthorPayoutData{
		Payout: payoutViewOf(payout),
		Author: adminViewOf(after, 0, 0),
	}}, nil
}

// issuePayout is the ONE payout path — the manual admin endpoint AND the automatic
// scheduler share it, so a payout settles identically however it is triggered. It
// RESERVES amountCents against the author's pending royalty ATOMICALLY (RecordPayout's
// WHERE guard makes it impossible to exceed accrued−paid, even concurrently), then
// settles:
//
//   - a TREASURY (system) author → CREDIT the platform reserve fund ("pay ourselves",
//     treasury.Credit, idempotent by payout id); NO external wallet, NO reserve-backing.
//   - an external author, method=credits → BACK the payout against the reserve
//     (treasury.Reserve; unbacked → void the reservation + PaymentRequired) then issue
//     the commerce grant into the author's wallet.
//   - an external author, cash method (wire/paypal/…) → record-only after backing.
//
// The reservation(s) are the money authority (at-most-pending AND, for external, at-
// most-reserve). A settlement error AFTER a successful reservation is logged LOUD, not
// retried — an operator reconciles from the payout row + audit; this never double-pays
// and never leaks reserve. Errors are the raw store sentinels (errNotFound /
// errInsufficientPending) or a ready zip error; the caller maps them.
func issuePayout(s *cloud.Service[state], ctx context.Context, a Author, amountCents int64, method, reference string) (Payout, error) {
	payoutID, err := genID("apo")
	if err != nil {
		return Payout{}, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// Reserve against pending FIRST (atomic guard) — a payout can never exceed owed.
	payout, err := s.State.store.RecordPayout(ctx, payoutID, a.ID, amountCents, method, reference,
		settlementOf(s, a, method), time.Now().Unix())
	if err != nil {
		return Payout{}, err // errNotFound | errInsufficientPending | internal
	}

	// Pay ourselves: a Hanzo-maintained template's royalty is realized INTO the treasury
	// reserve fund, not paid to an external wallet. Idempotent by payout id; a failure is
	// logged loud (the pending reservation stands, reconciled from audit) — symmetric
	// with the external credits path. No reserve-backing debit: we are crediting.
	if isTreasuryAuthor(s, a) {
		credited, entryID, cerr := treasury.Credit(ctx, treasury.ProgramAuthor, "payout:"+payoutID,
			fmt.Sprintf("OSS author royalty → treasury (%s)", a.GithubLogin), amountCents)
		if cerr != nil || !credited {
			s.Log.Error("authors: treasury credit failed (reserved against pending; not retried)",
				"author", a.ID, "payout", payoutID, "err", cerr)
		} else if entryID != "" {
			if serr := s.State.store.SetPayoutTxn(ctx, payoutID, entryID); serr != nil {
				s.Log.Error("authors: record treasury payout txn failed", "payout", payoutID, "err", serr)
			}
			payout.Txn = entryID
		}
		return payout, nil
	}

	// External author: BACK the payout against the platform reserve fund (double-entry
	// fund→payout:author, idempotent by payout id). SECOND guard: a payout must not
	// exceed EITHER the author's pending royalty (above) OR the funded reserve (here).
	// Not backed → VOID the pending reservation (restore it) and refuse honestly.
	backed, _, berr := treasury.Reserve(ctx, treasury.ProgramAuthor, "payout:"+payoutID,
		fmt.Sprintf("OSS author royalty payout (%s)", a.GithubLogin), amountCents)
	if berr != nil || !backed {
		if verr := s.State.store.VoidPayout(ctx, payoutID, a.ID, amountCents); verr != nil {
			s.Log.Error("authors: void after unbacked payout failed", "payout", payoutID, "err", verr)
		}
		if berr != nil {
			return Payout{}, zip.Errorf(http.StatusInternalServerError, "reserve payout: %v", berr)
		}
		reserve, _ := treasury.ReserveCents(ctx)
		return Payout{}, zip.Errorf(http.StatusPaymentRequired,
			"treasury reserve insufficient to back this payout (%d cents available); replenish via /v1/admin/treasury/sweep or seed", reserve)
	}

	// A credits payout issues the actual grant AFTER both reservations. A grant failure
	// is logged loud (never silent) so an operator reconciles from the payout row + audit.
	if method == methodCredits {
		txn, gerr := s.State.commerce.deposit(ctx, a.Org, orgSubject(a.Org), amountCents, grantCurrency,
			fmt.Sprintf("OSS author royalty payout (%s)", a.GithubLogin), grantTag)
		if gerr != nil {
			s.Log.Error("authors: credits payout grant failed (reserved against pending; not retried)",
				"author", a.ID, "payout", payoutID, "err", gerr)
		} else if serr := s.State.store.SetPayoutTxn(ctx, payoutID, txn); serr != nil {
			s.Log.Error("authors: record payout txn failed", "payout", payoutID, "err", serr)
		}
		payout.Txn = txn
	}
	return payout, nil
}

// isTreasuryAuthor reports whether a is the treasury SYSTEM author — the "pay
// ourselves" identity whose org is this deployment's maintainer org. Its royalty
// credits the reserve fund instead of an external wallet.
func isTreasuryAuthor(s *cloud.Service[state], a Author) bool {
	return s.State.maintainerOrg != "" && a.Org == s.State.maintainerOrg
}

// settlementOf names where this payout's money will land — the SAME branch
// issuePayout then takes, decided once and stored on the row so the books are read
// from a captured fact rather than re-derived from a maintainerOrg that may be
// reconfigured later.
func settlementOf(s *cloud.Service[state], a Author, method string) string {
	switch {
	case isTreasuryAuthor(s, a):
		return settlementTreasury
	case method == methodCredits:
		return settlementWallet
	default:
		return settlementCash
	}
}

// AuthorSweepOut is the POST /v1/admin/authors/sweep envelope.
type AuthorSweepOut struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data is what the fold did.
	Data AuthorSweepData `json:"data"`
}

// AuthorSweepData counts the work one sweep pass did.
type AuthorSweepData struct {
	// Swept is how many (author, deploying org) pairs were checked.
	Swept int `json:"swept"`
	// Accrued is how many royalty ledger rows THIS pass latched.
	Accrued int `json:"accrued"`
}

// adminSweep accrues this period's royalty for every approved author. It is the
// operator's override of the scheduler's automatic pass.
//
// The fold walks each approved author's DISTINCT deploying orgs and latches royalty
// at-most-once per (author, org, period), so running it twice accrues nothing the
// second time. A per-author failure is logged and skipped, never fatal. SuperAdmin
// only.
//
// Response: {"status":"ok","msg":"","data":{"swept":12,"accrued":3}}
func (o ops) adminSweep(ctx context.Context, _ *struct{}) (*AuthorSweepOut, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	approved, err := s.State.store.ListApproved(ctx, sweepLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list approved: %v", err)
	}
	swept, accrued := 0, 0
	for _, a := range approved {
		checked, credited, serr := sweepAuthor(s, ctx, a)
		swept += checked
		accrued += credited
		if serr != nil {
			s.Log.Warn("authors: sweep author failed", "author", a.ID, "err", serr)
		}
	}
	return &AuthorSweepOut{Status: "ok", Data: AuthorSweepData{Swept: swept, Accrued: accrued}}, nil
}

// ── accrual core (the ONE royalty path, shared by sweep + lazy read) ───────────

// sweepAuthor folds over one author's DISTINCT deploying orgs (excluding the author's
// own org) and accrues this period's royalty for each (spend × share), latched
// at-most-once per period. Returns (orgs checked, accruals created). A per-org
// commerce error is skipped (accrued next sweep) rather than failing the whole fold.
func sweepAuthor(s *cloud.Service[state], ctx context.Context, a Author) (checked, created int, err error) {
	orgs, err := s.State.store.DistinctDeployingOrgs(ctx, a.ID, a.Org, sweepLimit)
	if err != nil {
		return 0, 0, err
	}
	period := periodKey(time.Now())
	now := time.Now().Unix()
	for _, dorg := range orgs {
		checked++
		spend, serr := s.State.commerce.spendCents(ctx, dorg, orgSubject(dorg))
		if serr != nil {
			s.Log.Warn("authors: spend read failed", "author", a.ID, "deployingOrg", dorg, "err", serr)
			continue
		}
		if accrueOne(s, ctx, a, dorg, spend, period, now) {
			created++
		}
	}
	return checked, created, nil
}

// accrueOne latches ONE author's royalty for a deploying org's already-read spend
// this period (earning = spend × share), appending the immutable ledger row, at-most-
// once per (author, org, period). Returns true when THIS call created the accrual.
// It is the ONE royalty step shared by the standalone author sweep, the lazy dashboard
// read, and the unified affiliate-walk fold (AccrueForOrg).
func accrueOne(s *cloud.Service[state], ctx context.Context, a Author, deployingOrg string, spend int64, period string, now int64) bool {
	earning := spend * a.ShareBps / bpsDenom
	if earning <= 0 {
		return false // no spend to accrue yet this period
	}
	accrualID, err := genID("aca")
	if err != nil {
		return false
	}
	ledgerID, err := genID("alg")
	if err != nil {
		return false
	}
	won, lerr := s.State.store.LatchAccrual(ctx, accrualID, ledgerID, a.ID, deployingOrg, period, a.ShareBps, spend, earning, now)
	if lerr != nil {
		s.Log.Warn("authors: accrual latch failed", "author", a.ID, "deployingOrg", deployingOrg, "err", lerr)
		return false
	}
	if won {
		emitAudit(s, ctx, "author.accrue", a, map[string]any{
			"deployingOrg": deployingOrg, "period": period,
			"spendCents": spend, "earningCents": earning, "shareBps": a.ShareBps,
		})
	}
	return won
}

// AccrueForOrg is the seam the unified affiliate accrual walk calls once per source
// org (with the spend it already read): it accrues royalty to EVERY approved author
// whose verified repo that org deployed (excluding the author's own org), latched
// at-most-once per (author, org, period). It resolves the mounted authors singleton;
// when authors is NOT mounted (a partial deploy, or an affiliates unit test that does
// not wire authors) it is a no-op returning 0 — the same degrade-gracefully contract
// treasury.Reserve uses. Returns the number of NEW royalty accruals latched.
func AccrueForOrg(ctx context.Context, deployingOrg string, spend int64, period string, now int64) int {
	s := mounted
	if s == nil || s.State.store == nil || spend <= 0 {
		return 0
	}
	authors, err := s.State.store.AuthorsDeployedBy(ctx, deployingOrg, sweepLimit)
	if err != nil {
		s.Log.Warn("authors: AccrueForOrg lookup failed", "deployingOrg", deployingOrg, "err", err)
		return 0
	}
	created := 0
	for _, a := range authors {
		if accrueOne(s, ctx, a, deployingOrg, spend, period, now) {
			created++
		}
	}
	return created
}

// ── automatic money loop (accrue + auto-payout, no human) ──────────────────────

// sweepAndPayout is the automatic creator-payout loop the scheduler drives: for every
// approved author it ACCRUES this period's royalty, then AUTO-PAYS the author's full
// pending balance. Both halves are idempotent — accrual latches at-most-once per
// (author, deploying_org, period); payout reserves against pending atomically and
// never exceeds accrued−paid — so running it on a schedule (or twice) never
// double-accrues or double-pays. This closes the loop: accrual AND payout run with no
// human. Returns (authors swept, accruals created, payouts issued).
func sweepAndPayout(s *cloud.Service[state]) (swept, accrued, paid int) {
	ctx := context.Background()
	approved, err := s.State.store.ListApproved(ctx, sweepLimit)
	if err != nil {
		s.Log.Error("authors: auto loop list approved failed", "err", err)
		return 0, 0, 0
	}
	for _, a := range approved {
		checked, credited, serr := sweepAuthor(s, ctx, a)
		swept += checked
		accrued += credited
		if serr != nil {
			s.Log.Warn("authors: auto sweep author failed", "author", a.ID, "err", serr)
		}
		if autoPayoutAuthor(s, ctx, a.ID) {
			paid++
		}
	}
	if accrued > 0 || paid > 0 {
		s.Log.Info("authors: auto accrual+payout", "swept", swept, "accrued", accrued, "paid", paid)
	}
	return swept, accrued, paid
}

// autoPayoutAuthor issues a credits payout of an author's FULL pending royalty
// (external → their wallet; the treasury system author → the reserve fund),
// idempotently. It re-reads the author for the freshest pending (the sweep just
// latched); RecordPayout's atomic pending guard makes a zero-pending call a no-op
// (errInsufficientPending → skip), so a repeat tick after the balance is drained pays
// NOTHING. Returns true when a payout was issued this call.
func autoPayoutAuthor(s *cloud.Service[state], ctx context.Context, id string) bool {
	a, err := s.State.store.GetByID(ctx, id)
	if err != nil {
		return false
	}
	pending := a.PendingCents()
	if pending <= 0 {
		return false
	}
	payout, err := issuePayout(s, ctx, a, pending, methodCredits, "auto:"+periodKey(time.Now()))
	if err != nil {
		if err != errInsufficientPending { // a drained-in-race author is not worth a loud log
			s.Log.Warn("authors: auto payout failed", "author", id, "pending", pending, "err", err)
		}
		return false
	}
	after, _ := s.State.store.GetByID(ctx, id)
	emitAudit(s, ctx, "author.payout", after, map[string]any{
		"payoutId": payout.ID, "amountCents": payout.AmountCents, "method": payout.Method,
		"reference": payout.Reference, "txn": payout.Txn, "auto": true,
	})
	return true
}

// ── Hanzo-fork attribution → treasury ("pay ourselves") ────────────────────────

// maintainerOrgFor resolves the first-party org whose maintained OSS templates earn
// INTO the treasury (the "pay ourselves" author). AUTHOR_MAINTAINER_ORG overrides;
// else the brand slug (hanzo/lux/zoo). White-label by brand so a Lux/Zoo deployment
// pays ITS OWN treasury, never Hanzo's.
func maintainerOrgFor(deps cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("AUTHOR_MAINTAINER_ORG")); v != "" {
		return strings.ToLower(v)
	}
	switch strings.ToLower(strings.TrimSpace(deps.Brand)) {
	case "lux":
		return "lux"
	case "zoo":
		return "zoo"
	default:
		return "hanzo"
	}
}

// canonicalForgeOrg maps a brand slug to its canonical GitHub org (hanzo → hanzoai,
// lux → luxfi, zoo → zooai) — the owner under which the brand publishes its OSS.
func canonicalForgeOrg(maintainerOrg string) string {
	switch maintainerOrg {
	case "hanzo":
		return "hanzoai"
	case "lux":
		return "luxfi"
	case "zoo":
		return "zooai"
	default:
		return maintainerOrg
	}
}

// isMaintainedRepo reports whether a canonical repo url (host/owner/name) is owned by
// this deployment's first-party org — owner == the canonical forge org (hanzoai),
// owner == the brand slug (hanzo), or owner has the "<brand>-" prefix (hanzo-*). Those
// repos are Hanzo's OWN forks/blueprints, so their creator share is Hanzo's.
func isMaintainedRepo(repoURL, maintainerOrg string) bool {
	if maintainerOrg == "" {
		return false
	}
	_, owner, _, ok := splitRepo(repoURL)
	if !ok {
		return false
	}
	owner = strings.ToLower(owner)
	return owner == canonicalForgeOrg(maintainerOrg) ||
		owner == maintainerOrg ||
		strings.HasPrefix(owner, maintainerOrg+"-")
}

// ensureMaintainedRepo attributes a Hanzo-maintained repo to the treasury SYSTEM
// author, idempotently: it seeds the system author (approved, share=defaultShareBps)
// and auto-verifies the repo under it (method=maintainer — ownership is intrinsic to
// the namespace, no OAuth/file proof). A repo a REAL external author already verified
// is left theirs (first-verify wins → errRepoOwned, respected). Best-effort: a failure
// logs and the deploy simply records no attribution this time.
func ensureMaintainedRepo(s *cloud.Service[state], ctx context.Context, repoURL string, now int64) {
	sysID, err := genID("aut")
	if err != nil {
		return
	}
	sys, err := s.State.store.EnsureSystemAuthor(ctx, sysID, s.State.maintainerOrg, s.State.maintainerOrg+"-maintainers", defaultShareBps, now)
	if err != nil {
		s.Log.Warn("authors: ensure maintainer author failed", "org", s.State.maintainerOrg, "err", err)
		return
	}
	repoID, err := genID("arp")
	if err != nil {
		return
	}
	if _, _, verr := s.State.store.UpsertVerifiedRepo(ctx, repoID, sys.ID, repoURL, MethodMaintainer, now); verr != nil {
		if verr == errRepoOwned {
			return // a real author already verified it — respect first-verify-wins
		}
		s.Log.Warn("authors: auto-verify maintained repo failed", "repo", repoURL, "err", verr)
	}
}

// ── audit ─────────────────────────────────────────────────────────────────────

// emitAudit records an author money/lifecycle action in cloud's tamper-evident
// trail. Best-effort; a nil store is a no-op.
func emitAudit(s *cloud.Service[state], ctx context.Context, action string, a Author, extra map[string]any) {
	if s.State.auditStore == nil {
		return
	}
	after := map[string]any{"authorId": a.ID, "org": a.Org, "githubLogin": a.GithubLogin, "status": a.Status}
	for k, v := range extra {
		after[k] = v
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: a.Org, Sub: "authors"},
		Action:   action,
		Resource: audit.Resource{Type: "author", ID: a.ID},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After:    audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.auditStore.Append(ctx, rec); err != nil {
		s.Log.Error("authors: audit emit failed", "author", a.ID, "action", action, "err", err)
	}
}

// ── view models + helpers ─────────────────────────────────────────────────────

// AdminAuthorView is one row in the SuperAdmin directory (org exposed).
type AdminAuthorView struct {
	// ID is the author id.
	ID string `json:"id"`
	// Org is the author's own org, exposed here and nowhere on the customer surface.
	Org string `json:"org"`
	// GithubLogin is the linked forge login.
	GithubLogin string `json:"githubLogin"`
	// Status is connected, approved or suspended.
	Status string `json:"status"`
	// Verified reports an IAM-linked forge identity rather than file-only proof.
	Verified bool `json:"verified"`
	// ShareBps is the author's royalty share in basis points.
	ShareBps int64 `json:"shareBps"`
	// RepoCount is how many repos this author has claimed. 0 on the single-author
	// answers, which do not count.
	RepoCount int `json:"repoCount"`
	// DeployCount is how many attribution edges point at this author. 0 on the
	// single-author answers, which do not count.
	DeployCount int `json:"deployCount"`
	// AccruedCents is lifetime royalty earned.
	AccruedCents int64 `json:"accruedCents"`
	// PendingCents is accrued minus paid, never negative.
	PendingCents int64 `json:"pendingCents"`
	// PaidCents is lifetime royalty paid out.
	PaidCents int64 `json:"paidCents"`
	// CreatedAt is unix seconds when the org connected.
	CreatedAt int64 `json:"createdAt"`
	// ApprovedAt is unix seconds when the author was admitted to earning; 0 if never.
	ApprovedAt int64 `json:"approvedAt"`
	// SuspendedAt is unix seconds when the author was suspended; 0 if never.
	SuspendedAt int64 `json:"suspendedAt"`
}

func adminViewOf(a Author, repos, deploys int) AdminAuthorView {
	return AdminAuthorView{
		ID: a.ID, Org: a.Org, GithubLogin: a.GithubLogin, Status: a.Status, Verified: a.VerifiedAt > 0,
		ShareBps: a.ShareBps, RepoCount: repos, DeployCount: deploys, AccruedCents: a.AccruedCents,
		PendingCents: a.PendingCents(), PaidCents: a.PaidCents,
		CreatedAt: a.CreatedAt, ApprovedAt: a.ApprovedAt, SuspendedAt: a.SuspendedAt,
	}
}

// AuthorRepoView is one row of an author's verified/claimed repos, with the ready-to-paste
// Deploy-on-Hanzo markdown snippet.
type AuthorRepoView struct {
	// RepoURL is the canonical "host/owner/name" form.
	RepoURL string `json:"repoUrl"`
	// Verified reports that ownership was proven.
	Verified bool `json:"verified"`
	// Method is how it was proven: "oauth" (admin/push on the repo) or "file"
	// (hanzo.json carrying the verify code).
	Method string `json:"method,omitempty"`
	// BadgeMarkdown is the ready-to-paste "Deploy on Hanzo" README snippet.
	BadgeMarkdown string `json:"badgeMarkdown"`
	// VerifiedAt is unix seconds when ownership was proven.
	VerifiedAt int64 `json:"verifiedAt"`
	// CreatedAt is unix seconds when the claim was first recorded.
	CreatedAt int64 `json:"createdAt"`
}

func repoViewOf(r AuthorRepo, badgeBase string) AuthorRepoView {
	return AuthorRepoView{
		RepoURL: r.RepoURL, Verified: r.Verified, Method: r.Method,
		BadgeMarkdown: badgeMarkdown(badgeBase, r.RepoURL),
		VerifiedAt:    r.VerifiedAt, CreatedAt: r.CreatedAt,
	}
}

func repoViews(rs []AuthorRepo, badgeBase string) []AuthorRepoView {
	out := make([]AuthorRepoView, 0, len(rs))
	for _, r := range rs {
		out = append(out, repoViewOf(r, badgeBase))
	}
	return out
}

// AuthorOwnerView is one row of an author's verified OWNER-WIDE claims: the owner url + a
// ready-to-paste badge deep-linking that owner's Hanzo template import.
type AuthorOwnerView struct {
	// OwnerURL is the canonical "host/owner" form, unique across authors.
	OwnerURL string `json:"ownerUrl"`
	// Verified reports that ownership of the owner was proven.
	Verified bool `json:"verified"`
	// Method is how it was proven: "oauth" or "file", against the owner's .github
	// control repo.
	Method string `json:"method,omitempty"`
	// BadgeMarkdown is the ready-to-paste badge deep-linking this owner's template
	// import.
	BadgeMarkdown string `json:"badgeMarkdown"`
	// VerifiedAt is unix seconds when ownership was proven.
	VerifiedAt int64 `json:"verifiedAt"`
	// CreatedAt is unix seconds when the claim was first recorded.
	CreatedAt int64 `json:"createdAt"`
}

func orgViewOf(o AuthorOrg, badgeBase string) AuthorOwnerView {
	return AuthorOwnerView{
		OwnerURL: o.OwnerURL, Verified: o.Verified, Method: o.Method,
		BadgeMarkdown: badgeMarkdown(badgeBase, o.OwnerURL),
		VerifiedAt:    o.VerifiedAt, CreatedAt: o.CreatedAt,
	}
}

func orgViews(claims []AuthorOrg, badgeBase string) []AuthorOwnerView {
	out := make([]AuthorOwnerView, 0, len(claims))
	for _, o := range claims {
		out = append(out, orgViewOf(o, badgeBase))
	}
	return out
}

// AuthorDeployView is one row of an author's deploy events.
type AuthorDeployView struct {
	// RepoURL is the source repo the project was built from.
	RepoURL string `json:"repoUrl"`
	// Project is the deployed project's id.
	Project string `json:"project"`
	// DeployingOrg is the org that deployed it — the org whose spend the royalty is
	// taken from.
	DeployingOrg string `json:"deployingOrg"`
	// CreatedAt is unix seconds when the edge was recorded.
	CreatedAt int64 `json:"createdAt"`
}

func deployViews(es []DeployEvent) []AuthorDeployView {
	out := make([]AuthorDeployView, 0, len(es))
	for _, e := range es {
		out = append(out, AuthorDeployView{RepoURL: e.RepoURL, Project: e.Project, DeployingOrg: e.DeployingOrg, CreatedAt: e.CreatedAt})
	}
	return out
}

// AuthorPayoutView is one row of an author's payout history.
type AuthorPayoutView struct {
	// ID is the payout id.
	ID string `json:"id"`
	// AmountCents is the disbursement, in USD minor units.
	AmountCents int64 `json:"amountCents"`
	// Method is "credits" or a cash method such as wire or paypal.
	Method string `json:"method"`
	// Reference is the operator's note or external transfer id.
	Reference string `json:"reference,omitempty"`
	// Txn is the commerce grant or treasury entry id, when one was issued.
	Txn string `json:"txn,omitempty"`
	// Settlement discloses treasury-vs-wallet-vs-cash on every payout, to the author
	// and to the admin mirror alike — the disclosure that keeps a first-party
	// settlement legible as internal accounting.
	Settlement string `json:"settlement,omitempty"`
	// CreatedAt is unix seconds when the payout was reserved.
	CreatedAt int64 `json:"createdAt"`
}

func payoutViewOf(p Payout) AuthorPayoutView {
	return AuthorPayoutView{ID: p.ID, AmountCents: p.AmountCents, Method: p.Method, Reference: p.Reference,
		Txn: p.Txn, Settlement: p.Settlement, CreatedAt: p.CreatedAt}
}

func payoutViews(ps []Payout) []AuthorPayoutView {
	out := make([]AuthorPayoutView, 0, len(ps))
	for _, p := range ps {
		out = append(out, payoutViewOf(p))
	}
	return out
}

// AuthorSummary is the fleet tally for the admin directory.
type AuthorSummary struct {
	// Total is how many authors the listing returned.
	Total int `json:"total"`
	// Connected, Approved and Suspended count them by status.
	Connected int `json:"connected"`
	Approved  int `json:"approved"`
	Suspended int `json:"suspended"`
	// AccruedCents, PendingCents and PaidCents sum the rows returned.
	AccruedCents int64 `json:"accruedCents"`
	PendingCents int64 `json:"pendingCents"`
	PaidCents    int64 `json:"paidCents"`
}

func (s *AuthorSummary) add(a Author) {
	s.Total++
	switch a.Status {
	case StatusConnected:
		s.Connected++
	case StatusApproved:
		s.Approved++
	case StatusSuspended:
		s.Suspended++
	}
	s.AccruedCents += a.AccruedCents
	s.PendingCents += a.PendingCents()
	s.PaidCents += a.PaidCents
}

// badgeMarkdown builds the ready-to-paste "Deploy on Hanzo" README snippet for a repo
// — the button image links to the one-click template import.
func badgeMarkdown(badgeBase, repoURL string) string {
	return fmt.Sprintf("[![Deploy on Hanzo](%s/deploy-badge.svg)](%s/new?template=https://%s)",
		badgeBase, badgeBase, repoURL)
}

// verifySnippet is the hanzo.json body an author places on their default branch for
// the file-verification method.
func verifySnippet(code string) string {
	return fmt.Sprintf("{\n  \"hanzoAuthorCode\": %q\n}", code)
}

// fileProvesCode reports whether a fetched verify file proves the author's code. It
// parses hanzo.json and checks hanzoAuthorCode, and also accepts a bare substring
// match so a code embedded anywhere in the file still verifies (robust to formatting).
func fileProvesCode(file []byte, code string) bool {
	if code == "" {
		return false
	}
	var doc struct {
		HanzoAuthorCode string `json:"hanzoAuthorCode"`
	}
	if err := json.Unmarshal(file, &doc); err == nil && doc.HanzoAuthorCode == code {
		return true
	}
	return strings.Contains(string(file), code)
}

// normalizeLogin trims + lowercases a GitHub login (GitHub logins are
// case-insensitive; store one canonical form).
func normalizeLogin(login string) string { return strings.ToLower(strings.TrimSpace(login)) }

// orgSubject is the billing subject commerce keys an org's wallet on — the bare org
// slug, exactly like clients/affiliates.orgSubject. Kept as a named function so the
// "subject == org" contract lives in one place.
func orgSubject(org string) string { return org }

// periodKey is the accrual period bucket — the UTC year-month (YYYY-MM). Commerce's
// usage rollup is month-to-date, so one accrual per deploying org per month is the
// at-most-once unit.
func periodKey(t time.Time) string { return t.UTC().Format("2006-01") }

// adminLimitOf bounds an admin listing: absent or non-positive means listLimit, and
// nothing above maxAdminLimit is honoured.
func adminLimitOf(n int) int {
	if n <= 0 {
		return listLimit
	}
	if n > maxAdminLimit {
		return maxAdminLimit
	}
	return n
}

// mustJSON marshals v for the audit After payload, returning an empty object on the
// (unexpected) marshal error rather than crashing a money action.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// badgeBase resolves the Deploy-on-Hanzo badge/link host. AUTHOR_BADGE_BASE wins;
// else the brand's builder host. White-label by brand so a Lux/Zoo deployment mints
// its OWN badge, never hanzo.app.
func badgeBase(deps cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("AUTHOR_BADGE_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	switch strings.ToLower(strings.TrimSpace(deps.Brand)) {
	case "lux":
		return "https://lux.build"
	case "zoo":
		return "https://zoo.build"
	default:
		return "https://hanzo.app"
	}
}

// Shutdown stops the accrual+auto-payout scheduler (draining any in-flight sweep) and
// closes the authors store, in that order — so the store is never closed out from
// under a running payout. Idempotent.
func Shutdown() error {
	stopScheduler()
	stopScheduler = func() {}
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
