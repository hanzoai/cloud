// Package authors is a royalty for open-source work: your repo runs, you get paid.
//
// An author links GitHub, proves they own a repo, and earns a royalty on the
// metered spend of every org that deploys a project built from it — accrued per
// period, with a full audit trail behind the number. Accrual records what is OWED;
// paying it is a human act.
//
// It is the CREATOR member of the three programs built on the same shape;
// apps/referrals is the one-time bonus and apps/affiliates the partner commission.
// All three share the commerce ledger path (a credits payout is a grant, tag
// grant:author).
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
//  4. The ACCRUAL SWEEP (POST /v1/admin/authors/sweep, SuperAdmin) folds over each
//     approved author's DISTINCT deploying orgs (excluding the author's own): royalty =
//     that org's metered spend THIS PERIOD × the author's share (20%), accrued
//     at-most-once per (author, deploying_org, period). Accrual is TRACKING — it records
//     what we owe and issues nothing.
//  5. A PAYOUT (POST /v1/admin/authors/:id/payout, SuperAdmin) RECORDS a disbursement
//     against pending royalty, for every method. It moves no money: a human settles the
//     recorded payout out of band. A payout can never exceed pending (accrued − paid),
//     guarded atomically.
//
// HANZO FORKS. A repo whose owner is a brand org (owner ∈ {hanzoai, hanzo-*}) is
// auto-attributed on first deploy to the treasury SYSTEM author (org = the brand slug),
// so Hanzo earns 20% on its OWN templates when other orgs deploy them — recorded, not
// settled.
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
//	POST /v1/admin/authors/:id/payout       (SuperAdmin) RECORD a payout (record-only; a human settles it)
//	GET  /v1/admin/authors/:id/basis        (SuperAdmin) the SAME basis payload the author reads (support mirror)
//
// serve.go auto-registers GET /v1/authors/health.
package authors

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/mint"
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

// Mount wires the authors surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("authors.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("authors.Mount: empty DataDir")
	}
	// A typed op is a route PLUS a registry entry, and the registry lives on the App.
	// A router that cannot reach it would serve every route with no schema, no prose,
	// no MCP tool and no SDK method — so the mount FAILS rather than quietly
	// publishing a surface no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("authors.Mount: router is not a zip app, so the typed ops have no registry")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("authors.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "authors")
	s := &cloud.Service[state]{Base: b, State: state{
		store:         store,
		commerce:      newCommerceClient(),
		forge:         newGitHubClient(os.Getenv("CLOUD_IAM_HTTP_URL"), os.Getenv("IAM_SERVICE_TOKEN")),
		badgeBase:     badgeBase(deps),
		maintainerOrg: maintainerOrgFor(deps),
		auditStore:    deps.Audit,
	}}
	mounted = s

	routes(app, zapp, s)

	b.Log.Info("authors mounted", "brand", deps.Brand, "badgeBase", s.State.badgeBase,
		"maintainerOrg", s.State.maintainerOrg)
	return nil
}

// routes registers the authors surface. Every route is a TYPED op (typed.go) — one
// registry entry carrying the schema, the prose, an MCP tool, a CLI command and an
// SDK method.
//
// The two subtrees are declared on their OWN groups, because they are two surfaces:
// /v1/authors is the tenant's, /v1/admin/authors is the platform's. A typed op reads
// the validated org parked on the context by cloud.Bridge — never an In field, which
// is caller-supplied and would be a cross-tenant read the caller asserted for itself.
// The composer owns that install, once at its root; both groups here are bare path
// prefixes.
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}

	g := app.Group("/v1/authors")
	// The root of the tenant surface, declared on the App with its whole path rather
	// than on the group with an empty leaf: joining "/v1/authors" with "" yields
	// "/v1/authors/", a DIFFERENT path from the one it has always served.
	zip.Get(zapp, "/v1/authors", o.myAuthors)
	zip.Get(g, "/basis", o.basis)
	zip.Post(g, "/connect", o.connect)
	zip.Post(g, "/repos/verify", o.verifyRepo)
	zip.Post(g, "/deploys/record", o.recordDeploy)

	ga := app.Group("/v1/admin/authors")
	zip.Get(zapp, "/v1/admin/authors", o.adminList)
	zip.Post(ga, "/sweep", o.adminSweep)
	zip.Post(ga, "/:id/approve", o.adminApprove)
	zip.Post(ga, "/:id/suspend", o.adminSuspend)
	zip.Post(ga, "/:id/payout", o.adminPayout)
	zip.Get(ga, "/:id/basis", o.adminBasis)
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

// issuePayout RECORDS a payout of accrued royalty. It is record-only for EVERY method:
// it reserves amountCents against the author's pending royalty atomically (RecordPayout's
// WHERE guard makes it impossible to exceed accrued−paid, even concurrently) and writes
// the row. It moves NO money — a human settles the recorded payout out of band, which is
// the only way value leaves. Errors are the raw store sentinels (errNotFound /
// errInsufficientPending) or a ready zip error; the caller maps them.
func issuePayout(s *cloud.Service[state], ctx context.Context, a Author, amountCents int64, method, reference string) (Payout, error) {
	// Reserve against pending FIRST (atomic guard) — a payout can never exceed owed.
	payout, err := s.State.store.RecordPayout(ctx, mint.ID("apo"), a.ID, amountCents, method, reference,
		settlementOf(s, a, method), time.Now().Unix())
	if err != nil {
		return Payout{}, err // errNotFound | errInsufficientPending | internal
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
		spend, serr := s.State.commerce.spendCents(ctx, dorg)
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
	won, lerr := s.State.store.LatchAccrual(ctx, mint.ID("aca"), mint.ID("alg"), a.ID, deployingOrg, period, a.ShareBps, spend, earning, now)
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
// at-most-once per (author, org, period). Returns the number of NEW royalty
// accruals latched.
//
// It returned a bare int and no error, so "nothing was owed" and "authors is not
// in this process" were the same value: 0. authors ships as its own binary and
// the walk runs in affiliates, so the second case is the one that happens — the
// royalty leg of every sweep has been latching nothing, reporting it as a
// completed accrual of zero, with no error anywhere to see. On a money path a
// silent zero is the one outcome that must not be expressible, so absence is now
// ErrNoPeer and the caller decides what to do about it.
//
// A lookup failure is likewise returned rather than logged-and-zeroed: a sweep
// that could not read the authors is not a sweep that found none.
func AccrueForOrg(ctx context.Context, deployingOrg string, spend int64, period string, now int64) (int, error) {
	s := mounted
	if s == nil || s.State.store == nil {
		return 0, fmt.Errorf("%w: authors (this process does not own the royalty store)", cloud.ErrNoPeer)
	}
	if spend <= 0 {
		return 0, nil // nothing metered this period is a real answer
	}
	authors, err := s.State.store.AuthorsDeployedBy(ctx, deployingOrg, sweepLimit)
	if err != nil {
		return 0, fmt.Errorf("authors: deployed-by lookup for %q: %w", deployingOrg, err)
	}
	created := 0
	for _, a := range authors {
		if accrueOne(s, ctx, a, deployingOrg, spend, period, now) {
			created++
		}
	}
	return created, nil
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
	sys, err := s.State.store.EnsureSystemAuthor(ctx, mint.ID("aut"), s.State.maintainerOrg, s.State.maintainerOrg+"-maintainers", defaultShareBps, now)
	if err != nil {
		s.Log.Warn("authors: ensure maintainer author failed", "org", s.State.maintainerOrg, "err", err)
		return
	}
	if _, _, verr := s.State.store.UpsertVerifiedRepo(ctx, mint.ID("arp"), sys.ID, repoURL, MethodMaintainer, now); verr != nil {
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
	maps.Copy(after, extra)
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

// adminAuthorView is one row in the SuperAdmin directory (org exposed).
type adminAuthorView struct {
	// ID is the author record's server-minted handle, "aut_"-prefixed. It is the id
	// the approve, suspend, payout and admin-basis routes address.
	ID string `json:"id"`
	// Org is the tenant org that owns this author record — UNIQUE, one author per
	// org. It is exposed HERE and nowhere else (Author.Org is json:"-" on the tenant
	// surface), and it is the org excluded from this author's own accrual: deploying
	// your own repo earns you nothing.
	Org string `json:"org"`
	// GithubLogin is the linked forge account, lowercased. It comes from IAM's
	// linked account when the connect had one — which is also what sets verified —
	// and otherwise from the login the caller declared. The treasury author carries
	// "<brand>-maintainers".
	GithubLogin string `json:"githubLogin"`
	// Status is connected, approved or suspended. Only an approved author accrues;
	// a connected one may verify repos and collect deploy edges but earns nothing
	// until a reviewer admits it.
	Status string `json:"status"`
	// Verified is IDENTITY proof of the login, NOT proof of any repository: true
	// when the connect took the login from IAM's linked forge account (and for the
	// seeded treasury author), false when the caller merely declared it. A false
	// here still earns — repository ownership is proven separately, per claim.
	Verified bool `json:"verified"`
	// ShareBps is the royalty rate accrual applies, in basis points of a deploying
	// org's metered spend for the period: 2000 (the platform default) is 20%, 10000
	// would be the entire spend. The platform keeps 10000 − shareBps. Changing it
	// never rewrites history — each ledger row keeps the rate it was written with.
	ShareBps int64 `json:"shareBps"`
	// RepoCount is how many of this author's repository claims are VERIFIED, counted
	// for this response in one GROUP BY over the whole table rather than a query per
	// row. The single-author replies from approve, suspend and payout report 0: they
	// carry the mutated row, not a re-listing.
	RepoCount int `json:"repoCount"`
	// DeployCount is how many attribution edges point at this author — one per
	// (repository, project, deploying org), so re-deploying the same project adds
	// none. It includes self-deploys, which are recorded for provenance and excluded
	// from accrual, so it measures reach, not the earning set.
	DeployCount int `json:"deployCount"`
	// AccruedCents is lifetime royalty accrued, in integer USD cents: the sum of
	// every latched accrual (spend × shareBps / 10000). It only ever rises — a
	// payout is recorded against paidCents and never reduces this.
	AccruedCents int64 `json:"accruedCents"`
	// PendingCents is what a payout may still draw against — accrued − paid, floored
	// at zero. It is derived for each response, never stored, and it is the exact
	// figure the atomic payout guard refuses to exceed.
	PendingCents int64 `json:"pendingCents"`
	// PaidCents is lifetime royalty RECORDED as paid, in integer USD cents. It rises
	// the moment a payout reserves against pending — recording, not settling; a human
	// moves the money out of band — and falls back only when a payout is voided.
	PaidCents int64 `json:"paidCents"`
	// CreatedAt is unix seconds at the FIRST connect. Re-connecting re-links the
	// login and leaves this alone, so it dates the enrolment, not the latest link.
	CreatedAt int64 `json:"createdAt"`
	// ApprovedAt is unix seconds of the first approval, and 0 means never approved —
	// which is also "has never been able to accrue". Re-approving to renegotiate the
	// share leaves it at the original date.
	ApprovedAt int64 `json:"approvedAt"`
	// SuspendedAt is unix seconds of the most recent suspension. 0 means the author
	// is not suspended: either never was, or was and has since been approved again,
	// which clears this back to 0.
	SuspendedAt int64 `json:"suspendedAt"`
}

func adminViewOf(a Author, repos, deploys int) adminAuthorView {
	return adminAuthorView{
		ID: a.ID, Org: a.Org, GithubLogin: a.GithubLogin, Status: a.Status, Verified: a.VerifiedAt > 0,
		ShareBps: a.ShareBps, RepoCount: repos, DeployCount: deploys, AccruedCents: a.AccruedCents,
		PendingCents: a.PendingCents(), PaidCents: a.PaidCents,
		CreatedAt: a.CreatedAt, ApprovedAt: a.ApprovedAt, SuspendedAt: a.SuspendedAt,
	}
}

// authorRepo is one row of an author's verified/claimed repos, with the ready-to-paste
// Deploy-on-Hanzo markdown snippet.
type authorRepo struct {
	// RepoURL is the claim key in canonical form — lowercased "host/owner/name",
	// no scheme, no .git, host ∈ {github.com, gitlab.com}. A deploy's source repo is
	// normalized through the same function before attribution, so the two sides can
	// never miss on a cosmetic difference. UNIQUE across every author: first proven
	// claim wins.
	RepoURL string `json:"repoUrl"`
	// Verified reports that ownership was proven. Only a proven claim is ever
	// written, so it is true on every row this surface returns; the deploy path
	// re-reads it regardless, because an unverified claim attributes nothing.
	Verified bool `json:"verified"`
	// Method is HOW ownership was proven: "oauth" — an IAM-linked forge token showed
	// admin or push on the repository; "file" — a hanzo.json on the default branch
	// carried this author's verify code; or "maintainer" — the repository sits in a
	// first-party namespace, where ownership is intrinsic and the treasury author
	// holds it with no proof step. Omitted on a row written before the method was
	// recorded.
	Method string `json:"method,omitempty"`
	// BadgeMarkdown is the ready-to-paste README snippet, DERIVED for each response
	// from this deployment's badge host and never stored: a "Deploy on Hanzo" image
	// linking to the one-click import of this repository. Re-hosting the builder
	// changes every badge without touching a row.
	BadgeMarkdown string `json:"badgeMarkdown"`
	// VerifiedAt is unix seconds of the most recent successful proof. Re-verifying
	// refreshes it, and the method beside it, in place.
	VerifiedAt int64 `json:"verifiedAt"`
	// CreatedAt is unix seconds when the claim was first recorded. It equals
	// verifiedAt on the first proof and then stays put while verifiedAt moves, so the
	// pair reads as "claimed since / last proven".
	CreatedAt int64 `json:"createdAt"`
}

func authorRepoOf(r AuthorRepo, badgeBase string) authorRepo {
	return authorRepo{
		RepoURL: r.RepoURL, Verified: r.Verified, Method: r.Method,
		BadgeMarkdown: badgeMarkdown(badgeBase, r.RepoURL),
		VerifiedAt:    r.VerifiedAt, CreatedAt: r.CreatedAt,
	}
}

func authorRepos(rs []AuthorRepo, badgeBase string) []authorRepo {
	out := make([]authorRepo, 0, len(rs))
	for _, r := range rs {
		out = append(out, authorRepoOf(r, badgeBase))
	}
	return out
}

// orgView is one row of an author's verified OWNER-WIDE claims: the owner url + a
// ready-to-paste badge deep-linking that owner's Hanzo template import.
type orgView struct {
	// OwnerURL is the claim key in canonical form — lowercased "host/owner" with NO
	// repository segment, host ∈ {github.com, gitlab.com}. It covers every repository
	// under that owner, so code with no claim of its own still earns; a per-repository
	// claim outranks it. UNIQUE across every author: first proven claim wins.
	OwnerURL string `json:"ownerUrl"`
	// Verified reports that ownership of the WHOLE owner was proven — against that
	// owner's ".github" control repository, which is exactly as strong as a
	// per-repository claim. Only a proven claim is written, so every row returned
	// here is true.
	Verified bool `json:"verified"`
	// Method is HOW the owner was proven, always against its ".github" control
	// repository: "oauth" — an IAM-linked forge token showed admin or push on it; or
	// "file" — a hanzo.json on its default branch carried this author's verify code.
	// The "maintainer" shortcut is a per-repository attribution and never appears
	// here. Omitted on a row written before the method was recorded.
	Method string `json:"method,omitempty"`
	// BadgeMarkdown is the ready-to-paste README snippet, DERIVED for each response
	// from this deployment's badge host and never stored — here it deep-links the
	// OWNER's template import rather than one repository's.
	BadgeMarkdown string `json:"badgeMarkdown"`
	// VerifiedAt is unix seconds of the most recent successful proof of the owner;
	// re-verifying refreshes it, and the method beside it, in place.
	VerifiedAt int64 `json:"verifiedAt"`
	// CreatedAt is unix seconds when the owner claim was first recorded — equal to
	// verifiedAt on the first proof, then fixed while verifiedAt moves.
	CreatedAt int64 `json:"createdAt"`
}

func orgViewOf(o AuthorOrg, badgeBase string) orgView {
	return orgView{
		OwnerURL: o.OwnerURL, Verified: o.Verified, Method: o.Method,
		BadgeMarkdown: badgeMarkdown(badgeBase, o.OwnerURL),
		VerifiedAt:    o.VerifiedAt, CreatedAt: o.CreatedAt,
	}
}

func orgViews(claims []AuthorOrg, badgeBase string) []orgView {
	out := make([]orgView, 0, len(claims))
	for _, o := range claims {
		out = append(out, orgViewOf(o, badgeBase))
	}
	return out
}

// deployView is one row of an author's deploy events.
type deployView struct {
	RepoURL      string `json:"repoUrl"`
	Project      string `json:"project"`
	DeployingOrg string `json:"deployingOrg"`
	CreatedAt    int64  `json:"createdAt"`
}

func deployViews(es []DeployEvent) []deployView {
	out := make([]deployView, 0, len(es))
	for _, e := range es {
		out = append(out, deployView{RepoURL: e.RepoURL, Project: e.Project, DeployingOrg: e.DeployingOrg, CreatedAt: e.CreatedAt})
	}
	return out
}

// payoutView is one row of an author's payout history.
type payoutView struct {
	// ID is the payout row's server-minted handle, "apo_"-prefixed. A caller never
	// supplies it; it is what an operator quotes when reconciling a settlement.
	ID string `json:"id"`
	// AmountCents is the amount RESERVED against pending royalty, in integer USD
	// cents, always positive. The reservation is atomic and can never exceed
	// accrued − paid, so this is owed money moved out of pending — not money moved.
	AmountCents int64 `json:"amountCents"`
	// Method is how the operator says this settles, lowercased as recorded.
	// "credits" is the one method that means the author's own wallet; anything else
	// — wire, paypal, check — is a cash disbursement a human performs. Recording it
	// pays nobody either way.
	Method string `json:"method"`
	// Reference is the operator's external handle for the settlement: a wire
	// confirmation, a PayPal transaction id. Absent when none was given.
	Reference string `json:"reference,omitempty"`
	// Txn is the commerce ledger transaction id of a SETTLED credits payout, and it
	// is absent on every payout this service records. Recording moves no money, and
	// authors asks the money plane exactly one question — what has this org spent? —
	// with no write to answer it with, so there is no receipt to carry. It fills in
	// only when a settlement stamps its transaction back onto the row.
	Txn string `json:"txn,omitempty"`
	// Settlement discloses treasury-vs-wallet-vs-cash on every payout, to the author
	// and to the admin mirror alike — the disclosure that keeps a first-party
	// settlement legible as internal accounting.
	Settlement string `json:"settlement,omitempty"`
	// CreatedAt is unix seconds when the payout was RECORDED — the moment the amount
	// left pending, not the moment a human moved the money.
	CreatedAt int64 `json:"createdAt"`
}

func payoutViewOf(p Payout) payoutView {
	return payoutView{ID: p.ID, AmountCents: p.AmountCents, Method: p.Method, Reference: p.Reference,
		Txn: p.Txn, Settlement: p.Settlement, CreatedAt: p.CreatedAt}
}

func payoutViews(ps []Payout) []payoutView {
	out := make([]payoutView, 0, len(ps))
	for _, p := range ps {
		out = append(out, payoutViewOf(p))
	}
	return out
}

// authorProgramSummary is the fleet tally for the admin directory.
// authorProgramSummary is the fleet roll-up of the author program. The name is
// product-qualified because the fleet's schema namespace is FLAT and apps/referrals
// already publishes an "adminSummary" of its own.
type authorProgramSummary struct {
	// Total is how many author records this response actually carried. The roll-up
	// is folded over the SAME page as authors — newest first, bounded by limit
	// (default 500, ceiling 1000) — so on a program larger than the page it
	// summarizes that page, not the fleet.
	Total int `json:"total"`
	// Connected is how many of those are enrolled but not yet admitted to earning.
	Connected int `json:"connected"`
	// Approved is how many are admitted and accruing.
	Approved int `json:"approved"`
	// Suspended is how many have been stopped from accruing further. An author holds
	// exactly one status, so the three buckets never overlap and connected +
	// approved + suspended = total.
	Suspended int `json:"suspended"`
	// AccruedCents is the page's lifetime royalty accrued, in integer USD cents.
	AccruedCents int64 `json:"accruedCents"`
	// PendingCents is what the platform still owes across the page, in integer USD
	// cents — the sum of each author's own accrued − paid, each floored at zero.
	PendingCents int64 `json:"pendingCents"`
	// PaidCents is what has been RECORDED as paid across the page, in integer USD
	// cents. Recorded, not settled: the money leaves in a human's hands.
	PaidCents int64 `json:"paidCents"`
}

func (s *authorProgramSummary) add(a Author) {
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

// periodKey is the accrual period bucket — the UTC year-month (YYYY-MM). Commerce's
// usage rollup is month-to-date, so one accrual per deploying org per month is the
// at-most-once unit.
func periodKey(t time.Time) string { return t.UTC().Format("2006-01") }

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

// Shutdown closes the authors store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
