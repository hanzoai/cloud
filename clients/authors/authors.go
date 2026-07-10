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
//  4. The ACCRUAL SWEEP (POST /v1/admin/authors/sweep, the cron path; also lazy on
//     the author's own dashboard read) folds over each approved author's DISTINCT
//     deploying orgs (excluding the author's own): royalty = that org's metered spend
//     THIS PERIOD × the author's share (default 5%), accrued at-most-once per
//     (author, deploying_org, period).
//  5. Staff PAY OUT accrued royalty (POST /v1/admin/authors/:id/payout): "credits"
//     issues a commerce grant into the author's wallet; cash methods are record-only.
//     A payout can never exceed pending (accrued − paid), guarded atomically.
//
// Surface:
//
//	GET  /v1/authors                       (org)          my status, login, verified, repos, deploys, accrued/pending/paid, payouts
//	POST /v1/authors/connect               (org)          link GitHub (IAM-linked account or supplied login) + mint verify code
//	POST /v1/authors/repos/verify          (org)          verify repo ownership (oauth admin-check OR hanzo.json file)
//	POST /v1/authors/deploys/record        (org=deployer) record a deploy of a verified author repo (provenance → royalty)
//	GET  /v1/admin/authors                  (global-admin) every author + a summary
//	POST /v1/admin/authors/sweep            (global-admin) accrue royalty for every deploying org this period
//	POST /v1/admin/authors/:id/approve      (global-admin) admit to earning (+ optional share override)
//	POST /v1/admin/authors/:id/suspend      (global-admin) suspend
//	POST /v1/admin/authors/:id/payout       (global-admin) record a payout (credits → grant; cash → record-only)
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
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/treasury"
	"github.com/zap-proto/zip"
)

// The author economy — ONE place. Amounts are USD minor units (cents); a credits
// payout lands in the commerce Credit/trial bucket (grant:* → Credit per DepositKind),
// distinct from grant:referral / grant:affiliate / grant:admin only by its tag.
const (
	// defaultShareBps is the royalty a new author earns, in basis points (500 = 5%
	// of a deploying org's metered platform spend). The ONE royalty constant.
	defaultShareBps int64 = 500
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
	// verifyFile is the repo-root file the file-verification method reads; it must
	// contain the author's verify code on the repo's default branch.
	verifyFile = "hanzo.json"
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
)

// state is authors' own data; shared deps live in the embedded cloud.Base.
type state struct {
	store      *Store
	commerce   commerce
	github     github
	badgeBase  string          // https://hanzo.app — the Deploy-on-Hanzo badge/link host
	auditStore *audit.Recorder // best-effort payout/accrual audit; nil disables it
}

var mounted *cloud.Service[state]

// Mount wires the authors surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("authors.Mount: nil zip.App")
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
		store:      store,
		commerce:   newCommerceClient(commerceinproc.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		github:     newGitHubClient(os.Getenv("CLOUD_IAM_HTTP_URL"), os.Getenv("IAM_SERVICE_TOKEN")),
		badgeBase:  badgeBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s

	routes(app, s)

	b.Log.Info("authors mounted", "brand", deps.Brand, "badgeBase", s.State.badgeBase,
		"commerce", s.State.commerce.configured())
	return nil
}

// routes registers the authors surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/authors", cloud.Handle(s, myAuthors))
	app.Post("/v1/authors/connect", cloud.Handle(s, connect))
	app.Post("/v1/authors/repos/verify", cloud.Handle(s, verifyRepo))
	app.Post("/v1/authors/deploys/record", cloud.Handle(s, recordDeploy))
	app.Get("/v1/admin/authors", cloud.Handle(s, adminList))
	app.Post("/v1/admin/authors/sweep", cloud.Handle(s, adminSweep))
	app.Post("/v1/admin/authors/:id/approve", cloud.Handle(s, adminApprove))
	app.Post("/v1/admin/authors/:id/suspend", cloud.Handle(s, adminSuspend))
	app.Post("/v1/admin/authors/:id/payout", cloud.Handle(s, adminPayout))
}

// ── customer surface ─────────────────────────────────────────────────────────

// myAuthors answers GET /v1/authors for the validated caller. If the org has not
// connected it returns an honest "not enrolled" shape so the console shows the
// connect form; otherwise it returns the dashboard (status, login, verified, repos,
// deploys, accrued/pending/paid, payouts). For an APPROVED author it ALSO
// opportunistically runs the accrual sweep, so the dashboard is self-updating.
func myAuthors(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to view your author program")
	}
	ctx := c.Context()

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return c.JSON(http.StatusOK, map[string]any{
			"isAuthor":        false,
			"defaultShareBps": defaultShareBps,
			"badgeBase":       s.State.badgeBase,
		})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
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
		return zip.Errorf(http.StatusInternalServerError, "list repos: %v", err)
	}
	deploys, err := s.State.store.ListDeploys(ctx, a.ID, deployLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list deploys: %v", err)
	}
	payouts, err := s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}

	return c.JSON(http.StatusOK, map[string]any{
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
		"deploys":       deployViews(deploys),
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"payouts":       payoutViews(payouts),
	})
}

// connectRequest is the POST /v1/authors/connect body: an optional GitHub login used
// only when IAM has no linked GitHub account for the caller.
type connectRequest struct {
	GithubLogin string `json:"githubLogin"`
}

// connect enrolls the validated caller's org as an author at status=connected,
// idempotently. It links a GitHub login — from IAM's linked account (identity
// verified) when present, else the supplied login (verified later per-repo) — and
// mints a stable verify code for the file method.
func connect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to connect GitHub")
	}
	var body connectRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	ctx := c.Context()
	userSub := strings.TrimSpace(c.User())

	// Prefer IAM's linked GitHub identity (strong proof of the login).
	login := normalizeLogin(body.GithubLogin)
	identityVerified := false
	if l, _, linked, lerr := s.State.github.linkedAccount(ctx, org, userSub); lerr != nil {
		s.Log.Warn("authors: linked-account lookup failed", "org", org, "err", lerr)
	} else if linked && l != "" {
		login = normalizeLogin(l)
		identityVerified = true
	}
	if login == "" {
		return zip.ErrBadRequest("githubLogin is required (no linked GitHub account found)")
	}

	id, err := genID("aut")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	verifyCode, err := genID("avc")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	a, created, err := s.State.store.Connect(ctx, id, org, login, verifyCode, defaultShareBps, identityVerified, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "connect: %v", err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, map[string]any{
		"id":            a.ID,
		"status":        a.Status,
		"githubLogin":   a.GithubLogin,
		"verified":      a.VerifiedAt > 0,
		"verifyCode":    a.VerifyCode,
		"verifyFile":    verifyFile,
		"verifySnippet": verifySnippet(a.VerifyCode),
		"shareBps":      a.ShareBps,
		"created":       created,
	})
}

// verifyRepoRequest is the POST /v1/authors/repos/verify body.
type verifyRepoRequest struct {
	RepoURL string `json:"repoUrl"`
}

// verifyRepo verifies the caller owns a repo and records it as a verified author repo.
// Ownership is proven by an IAM-linked GitHub token (admin/push permission) OR a
// hanzo.json on the default branch carrying the author's verify code. The author must
// have connected first.
func verifyRepo(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to verify a repo")
	}
	var body verifyRepoRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	repoURL, perr := parseRepoInput(body.RepoURL)
	if perr != nil {
		return zip.ErrBadRequest("repoUrl must be a GitHub repo (github.com/owner/name)")
	}
	ctx := c.Context()

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return zip.ErrBadRequest("connect GitHub before verifying a repo")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}

	owner, name, _ := splitOwnerRepo(repoURL)
	method, verified := proveOwnership(s, ctx, a, org, strings.TrimSpace(c.User()), owner, name)
	if !verified {
		return zip.Errorf(http.StatusUnprocessableEntity,
			"could not verify ownership of %s — grant the Hanzo GitHub app OR add %s containing your verify code (%s) to the default branch",
			repoURL, verifyFile, a.VerifyCode)
	}

	repoID, err := genID("arp")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	repo, created, err := s.State.store.UpsertVerifiedRepo(ctx, repoID, a.ID, repoURL, method, time.Now().Unix())
	if err != nil {
		if err == errRepoOwned {
			return zip.ErrConflict("that repo is already verified by another author")
		}
		return zip.Errorf(http.StatusInternalServerError, "record repo: %v", err)
	}
	emitAudit(s, ctx, "author.verify_repo", a, map[string]any{"repoUrl": repoURL, "method": method})
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, map[string]any{
		"repo":    repoViewOf(repo, s.State.badgeBase),
		"created": created,
	})
}

// proveOwnership tries the two verification methods in order (oauth, then file) and
// returns the method that succeeded + whether it did.
func proveOwnership(s *cloud.Service[state], ctx context.Context, a Author, org, userSub, owner, name string) (method string, verified bool) {
	// Method 1: IAM-linked GitHub token → admin/push permission on the repo.
	if login, token, linked, lerr := s.State.github.linkedAccount(ctx, org, userSub); lerr == nil && linked && token != "" {
		if admin, aerr := s.State.github.repoAdmin(ctx, token, owner, name); aerr == nil && admin {
			return MethodOAuth, true
		} else if aerr != nil {
			s.Log.Warn("authors: repoAdmin check failed", "repo", owner+"/"+name, "err", aerr)
		}
		_ = login
	}
	// Method 2: hanzo.json on the default branch containing the verify code.
	for _, branch := range verifyBranches {
		file, ferr := s.State.github.fetchFile(ctx, owner, name, branch, verifyFile)
		if ferr != nil {
			s.Log.Warn("authors: fetch verify file failed", "repo", owner+"/"+name, "branch", branch, "err", ferr)
			continue
		}
		if len(file) > 0 && fileProvesCode(file, a.VerifyCode) {
			return MethodFile, true
		}
	}
	return "", false
}

// recordDeployRequest is the POST /v1/authors/deploys/record body: the sourceRepo a
// project was built from and the project id. The deploying org is the caller.
type recordDeployRequest struct {
	RepoURL string `json:"repoUrl"`
	Project string `json:"project"`
}

// recordDeploy records a deploy-attribution edge for the validated DEPLOYING org.
// When repoUrl matches a VERIFIED author repo, the edge is recorded (idempotent per
// repo+project+org) and becomes eligible for royalty. A deploy of a repo that isn't a
// verified author repo is NOT an error — it returns recorded:false so the deploy path
// can fire this unconditionally. A self-deploy is recorded (provenance) but excluded
// from accrual by the sweep.
func recordDeploy(s *cloud.Service[state], c *zip.Ctx) error {
	deployingOrg, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to record a deploy")
	}
	var body recordDeployRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	project := strings.TrimSpace(body.Project)
	if project == "" {
		return zip.ErrBadRequest("project is required")
	}
	repoURL := normalizeRepo(body.RepoURL)
	if repoURL == "" {
		// No source repo → nothing to attribute (a hand-built project). Honest no-op.
		return c.JSON(http.StatusOK, map[string]any{"recorded": false, "reason": "no source repo"})
	}
	ctx := c.Context()

	repo, err := s.State.store.VerifiedRepoForURL(ctx, repoURL)
	if err == errUnknownRepo || err == errRepoNotVerified {
		return c.JSON(http.StatusOK, map[string]any{"recorded": false, "reason": "repo is not a verified author repo"})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "resolve repo: %v", err)
	}

	id, err := genID("ade")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	edge, created, err := s.State.store.RecordDeploy(ctx, id, repo.AuthorID, repoURL, project, deployingOrg, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "record deploy: %v", err)
	}
	author, _ := s.State.store.GetByID(ctx, repo.AuthorID)
	self := author.Org == deployingOrg
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		emitAudit(s, ctx, "author.deploy", author, map[string]any{
			"repoUrl": repoURL, "project": project, "deployingOrg": deployingOrg, "self": self,
		})
	}
	return c.JSON(status, map[string]any{
		"recorded":  true,
		"created":   created,
		"self":      self,
		"deployId":  edge.ID,
		"createdAt": edge.CreatedAt,
	})
}

// ── admin surface (global-admin, fail-closed) ────────────────────────────────

// adminList answers GET /v1/admin/authors — every author (org exposed) + a fleet
// summary. Global-admin only.
func adminList(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	ctx := c.Context()
	rows, err := s.State.store.ListAll(ctx, adminLimitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list authors: %v", err)
	}
	repoCounts, err := s.State.store.RepoCountsByAuthor(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count repos: %v", err)
	}
	deployCounts, err := s.State.store.DeployCountsByAuthor(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count deploys: %v", err)
	}
	views := make([]adminAuthorView, 0, len(rows))
	sum := adminSummary{}
	for _, a := range rows {
		sum.add(a)
		views = append(views, adminViewOf(a, repoCounts[a.ID], deployCounts[a.ID]))
	}
	return adminOK(c, map[string]any{"authors": views, "summary": sum})
}

// adminApprove answers POST /v1/admin/authors/:id/approve — admit to earning. Body
// may carry a {shareBps} override. Global-admin only.
func adminApprove(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	var body struct {
		ShareBps int64 `json:"shareBps"`
	}
	_ = c.Bind(&body) // body is optional
	if body.ShareBps < 0 || body.ShareBps > bpsDenom {
		return zip.ErrBadRequest("shareBps must be 0–10000")
	}
	ctx := c.Context()
	a, err := s.State.store.Approve(ctx, id, body.ShareBps, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("author not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
	}
	emitAudit(s, ctx, "author.approve", a, map[string]any{"shareBps": a.ShareBps})
	return adminOK(c, map[string]any{"author": adminViewOf(a, 0, 0)})
}

// adminSuspend answers POST /v1/admin/authors/:id/suspend. Global-admin only.
func adminSuspend(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	ctx := c.Context()
	a, err := s.State.store.Suspend(ctx, id, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("author not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "suspend: %v", err)
	}
	emitAudit(s, ctx, "author.suspend", a, nil)
	return adminOK(c, map[string]any{"author": adminViewOf(a, 0, 0)})
}

// payoutRequest is the POST /v1/admin/authors/:id/payout body.
type payoutRequest struct {
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference"`
}

// adminPayout records a payout of accrued royalty. A "credits" method issues a
// commerce grant into the author's wallet; a cash method is record-only. The amount
// can never exceed pending (accrued − paid), reserved atomically before any grant.
// Global-admin only.
func adminPayout(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	var body payoutRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.AmountCents <= 0 {
		return zip.ErrBadRequest("amountCents must be positive")
	}
	method := strings.ToLower(strings.TrimSpace(body.Method))
	if method == "" {
		return zip.ErrBadRequest("method is required (credits, wire, paypal, …)")
	}
	ctx := c.Context()

	a, err := s.State.store.GetByID(ctx, id)
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("author not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}

	payoutID, err := genID("apo")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// Reserve against pending FIRST (atomic guard) — a payout can never exceed owed.
	payout, err := s.State.store.RecordPayout(ctx, payoutID, a.ID, body.AmountCents, method, strings.TrimSpace(body.Reference), time.Now().Unix())
	if err != nil {
		switch err {
		case errNotFound:
			return zip.ErrNotFound("author not found")
		case errInsufficientPending:
			return zip.ErrBadRequest(fmt.Sprintf("amount exceeds pending royalty (%d cents available)", a.PendingCents()))
		default:
			return zip.Errorf(http.StatusInternalServerError, "record payout: %v", err)
		}
	}

	// BACK the payout against the platform reserve fund (double-entry
	// fund→payout:author, idempotent by payout id). This is the SECOND guard: a payout
	// must not exceed EITHER the author's pending royalty (above) OR the funded reserve
	// (here). Not backed → VOID the pending reservation (restore it) and refuse
	// honestly — the platform has not reserved capital for this payout.
	backed, _, berr := treasury.Reserve(ctx, treasury.ProgramAuthor, "payout:"+payoutID,
		fmt.Sprintf("OSS author royalty payout (%s)", a.GithubLogin), body.AmountCents)
	if berr != nil || !backed {
		if verr := s.State.store.VoidPayout(ctx, payoutID, a.ID, body.AmountCents); verr != nil {
			s.Log.Error("authors: void after unbacked payout failed", "payout", payoutID, "err", verr)
		}
		if berr != nil {
			return zip.Errorf(http.StatusInternalServerError, "reserve payout: %v", berr)
		}
		reserve, _ := treasury.ReserveCents(ctx)
		return zip.Errorf(http.StatusPaymentRequired,
			"treasury reserve insufficient to back this payout (%d cents available); replenish via /v1/admin/treasury/sweep or seed", reserve)
	}

	// A credits payout issues the actual grant AFTER both reservations. The
	// reservations are the safety authority (at-most-pending AND at-most-reserve); a
	// grant failure is logged loud (never silent) so an operator reconciles from the
	// payout row + audit.
	if method == methodCredits {
		txn, gerr := s.State.commerce.deposit(ctx, a.Org, orgSubject(a.Org), body.AmountCents, grantCurrency,
			fmt.Sprintf("OSS author royalty payout (%s)", a.GithubLogin), grantTag)
		if gerr != nil {
			s.Log.Error("authors: credits payout grant failed (reserved against pending; not retried)",
				"author", a.ID, "payout", payoutID, "err", gerr)
		} else if serr := s.State.store.SetPayoutTxn(ctx, payoutID, txn); serr != nil {
			s.Log.Error("authors: record payout txn failed", "payout", payoutID, "err", serr)
		}
		payout.Txn = txn
	}

	after, _ := s.State.store.GetByID(ctx, a.ID)
	emitAudit(s, ctx, "author.payout", after, map[string]any{
		"payoutId": payout.ID, "amountCents": payout.AmountCents, "method": payout.Method,
		"reference": payout.Reference, "txn": payout.Txn,
	})
	return adminOK(c, map[string]any{"payout": payoutViewOf(payout), "author": adminViewOf(after, 0, 0)})
}

// adminSweep answers POST /v1/admin/authors/sweep — the periodic accrual path. It
// folds over every approved author's DISTINCT deploying orgs and accrues this
// period's royalty, at-most-once per period. Global-admin only.
func adminSweep(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	ctx := c.Context()
	approved, err := s.State.store.ListApproved(ctx, sweepLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list approved: %v", err)
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
	return adminOK(c, map[string]any{"swept": swept, "accrued": accrued})
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
		earning := spend * a.ShareBps / bpsDenom
		if earning <= 0 {
			continue // no spend to accrue yet this period
		}
		accrualID, gerr := genID("aca")
		if gerr != nil {
			continue
		}
		won, lerr := s.State.store.LatchAccrual(ctx, accrualID, a.ID, dorg, period, spend, earning, now)
		if lerr != nil {
			s.Log.Warn("authors: accrual latch failed", "author", a.ID, "deployingOrg", dorg, "err", lerr)
			continue
		}
		if won {
			created++
			emitAudit(s, ctx, "author.accrue", a, map[string]any{
				"deployingOrg": dorg, "period": period,
				"spendCents": spend, "earningCents": earning,
			})
		}
	}
	return checked, created, nil
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

// adminAuthorView is one row in the global-admin directory (org exposed).
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

// repoView is one row of an author's verified/claimed repos, with the ready-to-paste
// Deploy-on-Hanzo markdown snippet.
type repoView struct {
	RepoURL       string `json:"repoUrl"`
	Verified      bool   `json:"verified"`
	Method        string `json:"method,omitempty"`
	BadgeMarkdown string `json:"badgeMarkdown"`
	VerifiedAt    int64  `json:"verifiedAt"`
	CreatedAt     int64  `json:"createdAt"`
}

func repoViewOf(r AuthorRepo, badgeBase string) repoView {
	return repoView{
		RepoURL: r.RepoURL, Verified: r.Verified, Method: r.Method,
		BadgeMarkdown: badgeMarkdown(badgeBase, r.RepoURL),
		VerifiedAt:    r.VerifiedAt, CreatedAt: r.CreatedAt,
	}
}

func repoViews(rs []AuthorRepo, badgeBase string) []repoView {
	out := make([]repoView, 0, len(rs))
	for _, r := range rs {
		out = append(out, repoViewOf(r, badgeBase))
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
	CreatedAt   int64  `json:"createdAt"`
}

func payoutViewOf(p Payout) payoutView {
	return payoutView{ID: p.ID, AmountCents: p.AmountCents, Method: p.Method, Reference: p.Reference, Txn: p.Txn, CreatedAt: p.CreatedAt}
}

func payoutViews(ps []Payout) []payoutView {
	out := make([]payoutView, 0, len(ps))
	for _, p := range ps {
		out = append(out, payoutViewOf(p))
	}
	return out
}

// adminSummary is the fleet tally for the admin directory.
type adminSummary struct {
	Total        int   `json:"total"`
	Connected    int   `json:"connected"`
	Approved     int   `json:"approved"`
	Suspended    int   `json:"suspended"`
	AccruedCents int64 `json:"accruedCents"`
	PendingCents int64 `json:"pendingCents"`
	PaidCents    int64 `json:"paidCents"`
}

func (s *adminSummary) add(a Author) {
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

// adminOK writes the { status:"ok", msg, data } envelope the console's admin surface
// (originGet/originPost via app/admin/aggregate) unwraps — identical to
// clients/admin's ok() and clients/affiliates' adminOK. The customer /v1/authors
// surface stays bare JSON (read via the /cloud proxy + restGet).
func adminOK(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
}

// orgSubject is the billing subject commerce keys an org's wallet on — the bare org
// slug, exactly like clients/affiliates.orgSubject. Kept as a named function so the
// "subject == org" contract lives in one place.
func orgSubject(org string) string { return org }

// periodKey is the accrual period bucket — the UTC year-month (YYYY-MM). Commerce's
// usage rollup is month-to-date, so one accrual per deploying org per month is the
// at-most-once unit.
func periodKey(t time.Time) string { return t.UTC().Format("2006-01") }

func adminLimitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
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

// Shutdown closes the authors store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
