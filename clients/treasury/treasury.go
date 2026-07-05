// Package treasury mounts the Hanzo Cloud /v1/treasury/* surface: the platform's
// OWN fund/reserve accounting, one layer ABOVE the per-org commerce credit ledger.
// Where commerce tracks what each CUSTOMER holds and spends, treasury tracks the
// PLATFORM's books — a real, backed reserve fund that stands behind the growth-loop
// payouts (referrals, affiliates, OSS authors) so a payout is a debit against funded
// capital, never unbounded minting.
//
// It is the cloud-facing adapter around the store-agnostic double-entry engine in
// clients/treasury/ledger (the seed of the native hanzoai/finance central ledger):
// this file owns HTTP, tenant gating, audit and the KMS-signed L1 anchor; the engine
// owns the accounting. The two never leak into each other — the engine has no idea
// an HTTP request exists, so it lifts to hanzoai/finance untouched.
//
// Surface:
//
//	GET  /v1/treasury                 (org)          reserve health + policy (transparency: the pool backing MY payouts)
//	GET  /v1/admin/treasury           (global-admin) full report + journal + anchor status
//	POST /v1/admin/treasury/policy    (global-admin) set the revenue-share %
//	POST /v1/admin/treasury/sweep     (global-admin) accrue the revenue-share into the fund for a period
//	POST /v1/admin/treasury/seed      (global-admin) inject bootstrap capital into the fund
//	POST /v1/admin/treasury/anchor    (global-admin) anchor the ledger root on Hanzo L1 (Phase 2)
//
// serve.go auto-registers GET /v1/treasury/health.
package treasury

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
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/treasury/ledger"
	"github.com/hanzoai/cloud/clients/treasury/ledger/sqlstore"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Program identifiers for backed payouts — the ONE place the growth loops name
// themselves to the treasury, so a payout sink account id is never re-spelled.
const (
	ProgramReferral  = "referral"
	ProgramAffiliate = "affiliate"
	ProgramAuthor    = "author"
)

const (
	// journalLimit / maxJournalLimit bound the admin journal read.
	journalLimit    = 200
	maxJournalLimit = 1000
)

type svc struct {
	store      *sqlstore.Store
	ledger     *ledger.Ledger
	log        luxlog.Logger
	auditStore *audit.Recorder // best-effort debit/policy audit; nil disables it
	anchor     *anchorer        // Hanzo L1 anchor (Phase 2); nil-safe
}

// mounted is the process singleton the Reserve helper resolves. Set at Mount; nil
// when the subsystem is not linked/enabled, which makes Reserve a passthrough.
var mounted *svc

// Mount wires the treasury surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("treasury.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("treasury.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "treasury")
	if deps.DataDir == "" {
		return fmt.Errorf("treasury.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("treasury.Mount: data dir: %w", err)
	}
	store, err := sqlstore.Open(filepath.Join(deps.DataDir, "treasury.db"))
	if err != nil {
		return fmt.Errorf("treasury.Mount: open store: %w", err)
	}
	s := &svc{
		store:      store,
		ledger:     ledger.New(store),
		log:        log,
		auditStore: deps.Audit,
		anchor:     newAnchorer(deps, log),
	}
	mounted = s

	app.Get("/v1/treasury", s.myTreasury)
	app.Get("/v1/admin/treasury", s.adminReport)
	app.Post("/v1/admin/treasury/policy", s.adminSetPolicy)
	app.Post("/v1/admin/treasury/sweep", s.adminSweep)
	app.Post("/v1/admin/treasury/seed", s.adminSeed)
	app.Post("/v1/admin/treasury/anchor", s.adminAnchor)

	log.Info("treasury mounted", "brand", deps.Brand, "anchor", s.anchor.configured())
	return nil
}

func init() {
	// Order 146 — alongside the admin surface (clients/admin is 146), after the
	// growth loops (referrals 149 etc. are LATER, but ordering is irrelevant: the
	// loops call treasury.Reserve at REQUEST time, long after every Mount ran, so
	// the mounted singleton is always set). Routes are specific (/v1/treasury*,
	// /v1/admin/treasury*) so they bind ahead of the AI /v1/* catch-all (150).
	cloud.RegisterWithShutdown("treasury", 146, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("treasury.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	}, func(context.Context) error { return Shutdown() })
}

// ── the backed-payout seam (the ONE helper the 3 growth loops call) ──────────

// Reserve backs a payout of amountCents (minor units) for `program` against the
// platform reserve fund: it posts the double-entry fund→payout:<program> journal
// entry, idempotently keyed by ref, and reports whether the fund could cover it.
//
//   - backed=true  → posted (or already posted for this ref); the caller MUST now
//     credit the recipient wallet. Fund down, wallet up — reconciled.
//   - backed=false → INSUFFICIENT RESERVE; the caller MUST NOT credit. The payout is
//     honestly pending/blocked until a sweep or seed replenishes the fund.
//
// When the treasury subsystem is NOT mounted (a partial deploy, or a growth-loop
// unit test that does not wire treasury) Reserve is a PASSTHROUGH returning
// backed=true — behaviour identical to before treasury existed, the same
// degrade-gracefully contract the commerce seam uses. In production the subsystem is
// always mounted, so the reserve is enforced. entryID is the journal entry id (empty
// on passthrough), for the caller to record alongside its own payout row.
func Reserve(ctx context.Context, program, ref, memo string, amountCents int64) (backed bool, entryID string, err error) {
	s := mounted
	if s == nil {
		return true, "", nil // unmounted → passthrough (backward-safe)
	}
	entry, backed, created, err := s.ledger.DebitReserve(ctx, program, ref, memo, amountCents, time.Now().Unix())
	if err != nil {
		return false, "", err
	}
	if backed && created {
		s.emitAudit(ctx, "treasury.debit", program, entry.ID, map[string]any{
			"program": program, "ref": ref, "amountCents": amountCents, "entryId": entry.ID,
		})
	}
	return backed, entry.ID, nil
}

// ── customer surface ─────────────────────────────────────────────────────────

// myTreasury answers GET /v1/treasury for any validated caller: the reserve-fund
// health + the revenue-share policy. This is a TRANSPARENCY view — a partner/author
// can see the pool that backs their payouts is solvent — not per-org money (that is
// the customer's commerce balance at /v1/billing/balance). Policy is read-only here;
// only global-admin sets it.
func (s *svc) myTreasury(c *zip.Ctx) error {
	if _, ok := principal.Tenant(c); !ok {
		return zip.ErrForbidden("sign in to view the treasury")
	}
	rep, err := s.ledger.Snapshot(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	return c.JSON(http.StatusOK, rep)
}

// ── admin surface (global-admin, fail-closed) ────────────────────────────────

// adminReport answers GET /v1/admin/treasury — the full fund report, the recent
// journal (double-entry postings), and the Hanzo L1 anchor status. Global-admin only.
func (s *svc) adminReport(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	ctx := c.Context()
	rep, err := s.ledger.Snapshot(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	entries, err := s.ledger.Entries(ctx, journalLimitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "journal: %v", err)
	}
	return adminOK(c, map[string]any{
		"report":  rep,
		"journal": entries,
		"anchor":  s.anchor.status(ctx, s.ledger),
	})
}

// policyRequest is the POST /v1/admin/treasury/policy body.
type policyRequest struct {
	RevenueShareBps int64 `json:"revenueShareBps"`
}

// adminSetPolicy sets the revenue-share basis points (0–10000). Global-admin only.
func (s *svc) adminSetPolicy(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	var body policyRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	pol, err := s.ledger.SetPolicy(c.Context(), body.RevenueShareBps, time.Now().Unix())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	s.emitAudit(c.Context(), "treasury.policy", "", "", map[string]any{"revenueShareBps": pol.RevenueShareBps})
	return adminOK(c, map[string]any{"policy": pol})
}

// sweepRequest is the POST /v1/admin/treasury/sweep body. RevenueCents is the net
// platform revenue MEASURED for the period (the caller — a cron or an operator —
// supplies it from the revenue view; treasury does the accounting, not the metering,
// keeping the concerns orthogonal). Period defaults to the current UTC month.
type sweepRequest struct {
	Period       string `json:"period"`
	RevenueCents int64  `json:"revenueCents"`
}

// adminSweep posts the revenue-share accrual for a period (revenue → fund),
// idempotent per period. Global-admin only.
func (s *svc) adminSweep(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	var body sweepRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	period := strings.TrimSpace(body.Period)
	if period == "" {
		period = time.Now().UTC().Format("2006-01")
	}
	if body.RevenueCents < 0 {
		return zip.ErrBadRequest("revenueCents must be >= 0")
	}
	entry, created, err := s.ledger.Accrue(c.Context(), period, body.RevenueCents, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "sweep: %v", err)
	}
	if created {
		s.emitAudit(c.Context(), "treasury.sweep", "", entry.ID, map[string]any{
			"period": period, "revenueCents": body.RevenueCents, "accruedCents": entry.AmountCents,
		})
	}
	reserve, _ := s.ledger.ReserveCents(c.Context())
	return adminOK(c, map[string]any{
		"period":       period,
		"revenueCents": body.RevenueCents,
		"accruedCents": entry.AmountCents,
		"created":      created,
		"reserveCents": reserve,
	})
}

// seedRequest is the POST /v1/admin/treasury/seed body — a bootstrap capital
// injection into the reserve fund. Ref (optional) is an idempotency key; without one
// each seed is a distinct injection.
type seedRequest struct {
	AmountCents int64  `json:"amountCents"`
	Memo        string `json:"memo"`
	Ref         string `json:"ref"`
}

// adminSeed injects bootstrap capital into the reserve fund so backed payouts can
// begin before the first revenue-share sweep. Global-admin only.
func (s *svc) adminSeed(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	var body seedRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.AmountCents <= 0 {
		return zip.ErrBadRequest("amountCents must be > 0")
	}
	ref := strings.TrimSpace(body.Ref)
	if ref == "" {
		ref = "seed:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	memo := strings.TrimSpace(body.Memo)
	if memo == "" {
		memo = "reserve capital injection"
	}
	entry, created, err := s.ledger.Seed(c.Context(), ref, memo, body.AmountCents, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "seed: %v", err)
	}
	if created {
		s.emitAudit(c.Context(), "treasury.seed", "", entry.ID, map[string]any{
			"amountCents": body.AmountCents, "ref": ref, "entryId": entry.ID,
		})
	}
	reserve, _ := s.ledger.ReserveCents(c.Context())
	return adminOK(c, map[string]any{"entry": entry, "created": created, "reserveCents": reserve})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// adminOK writes the { status:"ok", msg, data } envelope the console's admin
// surface (originGet/originPost via app/admin/aggregate) unwraps — identical to
// clients/admin and clients/referrals. The customer /v1/treasury surface stays bare
// JSON (read via the /cloud proxy + restGet).
func adminOK(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
}

// emitAudit records a treasury money action in cloud's tamper-evident trail.
// Best-effort; a nil store is a no-op. The actor is the treasury engine (a system
// action, not a user).
func (s *svc) emitAudit(ctx context.Context, action, program, resourceID string, after map[string]any) {
	if s.auditStore == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: program, Sub: "treasury"},
		Action:   action,
		Resource: audit.Resource{Type: "treasury", ID: resourceID},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After:    audit.Redact(mustJSON(after)),
	}
	if _, err := s.auditStore.Append(ctx, rec); err != nil {
		s.log.Error("treasury: audit emit failed", "action", action, "err", err)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func journalLimitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return journalLimit
	}
	if n > maxJournalLimit {
		return maxJournalLimit
	}
	return n
}

// Shutdown closes the treasury store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
