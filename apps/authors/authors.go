// Package authors is a royalty for open-source work: your repo runs, you get paid.
//
// An author links GitHub, proves they own a repo, and earns a royalty on the
// metered spend of every org that deploys a project built from it — accrued per
// period and auto-paid, with a full audit trail behind the number.
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
	// A typed op is a route PLUS a registry entry, and the registry lives on the App.
	// A router that cannot reach it would serve every route with no schema, no prose,
	// no MCP tool and no SDK method — so the mount FAILS rather than quietly
	// publishing a surface no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("authors.Mount: router is not a zip app, so the typed ops have no registry")
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

	routes(app, zapp, s)

	// Drive the DEFAULT automatic money loop: a periodic goroutine that accrues every
	// approved author's royalty AND auto-pays their pending balance, no human in the
	// loop (the manual sweep/payout admin endpoints remain as overrides). Single-writer:
	// authors mounts only on the writer pod, so exactly one scheduler runs.
	stopScheduler = startScheduler(s)

	b.Log.Info("authors mounted", "brand", deps.Brand, "badgeBase", s.State.badgeBase,
		"maintainerOrg", s.State.maintainerOrg, "commerce", s.State.commerce.configured())
	return nil
}

// routes registers the authors surface. Every route is a TYPED op (typed.go) — one
// registry entry carrying the schema, the prose, an MCP tool, a CLI command and an
// SDK method.
//
// The two subtrees are declared on their OWN groups, because they are two surfaces:
// /v1/authors is the tenant's, /v1/admin/authors is the platform's. Both get
// cloud.Bridge, which is what parks the validated org a typed op reads — never an In
// field, which is caller-supplied and would be a cross-tenant read the caller
// asserted for itself. fiber runs middleware in registration order, so each Use
// precedes its own leaves; nesting under Serve's own Bridge is harmless.
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}

	g := app.Group("/v1/authors")
	g.Use(cloud.Bridge())
	// The root of the tenant surface, declared on the App with its whole path rather
	// than on the group with an empty leaf: joining "/v1/authors" with "" yields
	// "/v1/authors/", a DIFFERENT path from the one it has always served.
	zip.Get(zapp, "/v1/authors", o.myAuthors)
	zip.Get(g, "/basis", o.basis)
	zip.Post(g, "/connect", o.connect)
	zip.Post(g, "/repos/verify", o.verifyRepo)
	zip.Post(g, "/deploys/record", o.recordDeploy)

	ga := app.Group("/v1/admin/authors")
	ga.Use(cloud.Bridge())
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

// firstNonEmpty returns the first non-empty trimmed string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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

// adminAuthorView is one row in the SuperAdmin directory (org exposed).
type adminAuthorView struct {
	ID           string `json:"id"`
	Org          string `json:"org"`
	GithubLogin  string `json:"githubLogin"`
	Status       string `json:"status"`
	Verified     bool   `json:"verified"`
	ShareBps     int64  `json:"shareBps"`
	RepoCount    int    `json:"repoCount"`
	DeployCount  int    `json:"deployCount"`
	AccruedCents int64  `json:"accruedCents"`
	PendingCents int64  `json:"pendingCents"`
	PaidCents    int64  `json:"paidCents"`
	CreatedAt    int64  `json:"createdAt"`
	ApprovedAt   int64  `json:"approvedAt"`
	SuspendedAt  int64  `json:"suspendedAt"`
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
	RepoURL       string `json:"repoUrl"`
	Verified      bool   `json:"verified"`
	Method        string `json:"method,omitempty"`
	BadgeMarkdown string `json:"badgeMarkdown"`
	VerifiedAt    int64  `json:"verifiedAt"`
	CreatedAt     int64  `json:"createdAt"`
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
	OwnerURL      string `json:"ownerUrl"`
	Verified      bool   `json:"verified"`
	Method        string `json:"method,omitempty"`
	BadgeMarkdown string `json:"badgeMarkdown"`
	VerifiedAt    int64  `json:"verifiedAt"`
	CreatedAt     int64  `json:"createdAt"`
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
	ID          string `json:"id"`
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference,omitempty"`
	Txn         string `json:"txn,omitempty"`
	// Settlement discloses treasury-vs-wallet-vs-cash on every payout, to the author
	// and to the admin mirror alike — the disclosure that keeps a first-party
	// settlement legible as internal accounting.
	Settlement string `json:"settlement,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
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
	Total        int   `json:"total"`
	Connected    int   `json:"connected"`
	Approved     int   `json:"approved"`
	Suspended    int   `json:"suspended"`
	AccruedCents int64 `json:"accruedCents"`
	PendingCents int64 `json:"pendingCents"`
	PaidCents    int64 `json:"paidCents"`
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

// orgSubject is the billing subject commerce keys an org's wallet on — the bare org
// slug, exactly like clients/affiliates.orgSubject. Kept as a named function so the
// "subject == org" contract lives in one place.
func orgSubject(org string) string { return org }

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
